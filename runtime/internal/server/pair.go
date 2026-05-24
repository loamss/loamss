package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/loamss/loamss/runtime/internal/permission"
)

// pairRequest is the JSON body of POST /pair. Clients send this when
// they're given a one-time code by the user (via `loamss client pair`
// or a QR code). The metadata field is opaque — the runtime carries
// it through to the Client record without inspection.
type pairRequest struct {
	Code     string         `json:"code"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// pairResponse is the JSON body of a successful POST /pair. The token
// is returned exactly once; clients MUST persist it immediately.
//
// EndpointURL is included for symmetry with eventual federated flows
// where the pairing might happen via one URL but the actual MCP
// surface lives at another. In v0.1 it's the same host as /pair.
type pairResponse struct {
	Client      *permission.Client `json:"client"`
	Token       string             `json:"token"`
	EndpointURL string             `json:"endpoint_url"`
}

// errorResponse is the body shape for failure responses across the
// pairing and MCP surfaces. Keeps the message human-readable; the
// code is the HTTP status repeated so machine clients can branch
// without parsing it out of the status line. Specific subsystems
// (JSON-RPC) layer their own error shapes on top.
type errorResponse struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

// handlePair implements POST /pair. Body is JSON; method must be POST.
// On success, returns 200 with pairResponse. On failure, returns
// 4xx with errorResponse and (where applicable) emits a
// `client.pair_failed` audit entry via the engine.
//
// This endpoint is the HTTP twin of `loamss client pair complete`.
// Both call the same Engine.RedeemPairingCode; the CLI form exists
// for local development and human-driven pairing, the HTTP form for
// real MCP clients that pair programmatically.
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeJSONError(w, "pairing engine not configured", http.StatusServiceUnavailable)
		return
	}

	// Bound the read to a small ceiling so a misbehaving client can't
	// stream gigabytes into /pair. 64 KiB is generous; a real
	// pairRequest is ~200 bytes.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeJSONError(w, "request body too large or unreadable", http.StatusBadRequest)
		return
	}

	var req pairRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Code == "" {
		writeJSONError(w, "field 'code' is required", http.StatusBadRequest)
		return
	}

	// Mark the metadata so observers can tell HTTP-originated pairings
	// from CLI-driven ones. We do this after decoding so a malicious
	// client can't override the source label.
	if req.Metadata == nil {
		req.Metadata = map[string]any{}
	}
	req.Metadata["paired_via"] = "http"

	client, token, err := s.engine.RedeemPairingCode(r.Context(), req.Code, req.Metadata)
	switch {
	case errors.Is(err, permission.ErrPairingCodeNotFound):
		writeJSONError(w, "pairing code not found", http.StatusNotFound)
		return
	case errors.Is(err, permission.ErrPairingCodeExpired):
		writeJSONError(w, "pairing code expired", http.StatusGone)
		return
	case errors.Is(err, permission.ErrPairingCodeAlreadyRedeemed):
		writeJSONError(w, "pairing code already redeemed", http.StatusConflict)
		return
	case err != nil:
		s.logger.Error("pair endpoint redeem error", "err", err)
		writeJSONError(w, "redemption failed", http.StatusInternalServerError)
		return
	}

	resp := pairResponse{
		Client:      client,
		Token:       token,
		EndpointURL: "http://" + r.Host + "/mcp",
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeJSON encodes v as a JSON body with status. Caller has not set
// Content-Type; writeJSON does that.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError writes a JSON-shaped error body with the given
// status. Use writeAuthError instead when responding from the auth
// middleware so the WWW-Authenticate header gets set.
func writeJSONError(w http.ResponseWriter, message string, status int) {
	writeJSON(w, status, errorResponse{Error: message, Code: status})
}

// writeAuthError writes a 401-shaped error with the
// `WWW-Authenticate: Bearer` challenge header per RFC 7235.
func writeAuthError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="loamss"`)
	writeJSONError(w, message, status)
}
