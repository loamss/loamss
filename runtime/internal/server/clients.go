package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/loamss/loamss/runtime/internal/permission"
)

// Apps (paired-client) management under /console/clients.
//
//   POST   /console/clients/pair       — generate a pairing code
//   DELETE /console/clients/{id}       — revoke a paired client
//
// Pairing flow:
//
//   1. User clicks "Pair an app" in the dashboard.
//   2. POST /console/clients/pair { client_name } returns a code.
//   3. User copies the code into the external client (Claude
//      Desktop, ChatGPT, custom MCP tool).
//   4. The external client POSTs to /pair (the existing endpoint
//      this commit does NOT touch) with the code; the runtime
//      redeems it, issues a bearer token, returns it to the
//      external client.
//   5. The new Client row shows up in /console/state.clients on
//      the dashboard's next poll.
//
// Note the split: this commit only owns *code generation* and
// *client revocation*. Redemption already exists at /pair and
// continues to serve external clients exactly as before.

type pairCodeRequest struct {
	ClientName string `json:"client_name"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type pairCodeResponse struct {
	OK         bool   `json:"ok"`
	Code       string `json:"code"`
	ClientName string `json:"client_name"`
	ExpiresAt  string `json:"expires_at"`
}

func (s *Server) handleClientPair(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeJSONError(w, "pairing is not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		writeJSONError(w, "request body too large or unreachable", http.StatusBadRequest)
		return
	}
	var req pairCodeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.ClientName == "" {
		writeJSONError(w, "client_name is required", http.StatusBadRequest)
		return
	}

	// TTL: caller-supplied positive value wins; otherwise the
	// engine's default (10 minutes today). We cap at 1 hour from
	// the dashboard so a code generated and forgotten can't sit
	// around all day.
	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
		if ttl > time.Hour {
			ttl = time.Hour
		}
	}

	ctx := r.Context()
	p, err := s.engine.CreatePairingCode(ctx, req.ClientName, "console", ttl)
	if err != nil {
		writeJSONError(w, "failed to create pairing code: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.logger.Info("console pair code created",
		"client_name", p.ClientName, "expires_at", p.ExpiresAt)
	writeJSON(w, http.StatusCreated, pairCodeResponse{
		OK:         true,
		Code:       p.Code,
		ClientName: p.ClientName,
		ExpiresAt:  p.ExpiresAt.Format(time.RFC3339Nano),
	})
}

func (s *Server) handleClientRevoke(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeJSONError(w, "client management is not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, "client id required in URL", http.StatusBadRequest)
		return
	}

	// Optional reason — short, persisted on the revocation audit
	// entry. Empty body is fine.
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<10))
	var req struct {
		Reason string `json:"reason,omitempty"`
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &req) // ignore decode errors; reason is optional
	}

	ctx := r.Context()
	err := s.engine.RevokeClient(ctx, id, "console", req.Reason)
	switch {
	case errors.Is(err, permission.ErrClientNotFound):
		writeJSONError(w, "no client with that id", http.StatusNotFound)
		return
	case err != nil:
		s.logger.Warn("console client revoke failed", "err", err, "id", id)
		writeJSONError(w, "revoke failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.logger.Info("console client revoke", "id", id)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"id": id,
	})
}
