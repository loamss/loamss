package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/loamss/loamss/runtime/internal/oauth"
)

// OAuth console endpoints — same localhost-only contract as the
// other /console/* surfaces. See server.go for the route map.
//
//   GET    /console/oauth/clients              — list registered clients (secret redacted)
//   POST   /console/oauth/clients/{provider}   — set a client_id for a provider
//   DELETE /console/oauth/clients/{provider}   — remove the registration
//   GET    /console/oauth/providers            — list well-known providers
//   POST   /console/oauth/begin                — kick off a browser flow
//   GET    /console/oauth/status?capsule=...   — check whether the capsule has tokens
//
// "Begin" + "Status" cooperate: the dashboard POSTs begin to open
// the browser, then polls status to learn when tokens are stored.

// --- /console/oauth/clients ------------------------------------------------

type oauthClientSetRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

type oauthClientResponse struct {
	Provider     string `json:"provider"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

func (s *Server) handleOAuthClientsList(w http.ResponseWriter, r *http.Request) {
	if s.oauthClients == nil {
		writeJSONError(w, "oauth client store is not enabled", http.StatusServiceUnavailable)
		return
	}
	rows, err := s.oauthClients.List(r.Context())
	if err != nil {
		writeJSONError(w, "listing oauth clients: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]oauthClientResponse, 0, len(rows))
	for _, c := range rows {
		out = append(out, oauthClientResponse{
			Provider:     c.Provider,
			ClientID:     c.ClientID,
			ClientSecret: c.ClientSecret, // already redacted to "(set)" by ClientStore.List
			CreatedAt:    c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt:    c.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": out})
}

func (s *Server) handleOAuthClientsSet(w http.ResponseWriter, r *http.Request) {
	if s.oauthClients == nil {
		writeJSONError(w, "oauth client store is not enabled", http.StatusServiceUnavailable)
		return
	}
	provider := r.PathValue("provider")
	if provider == "" {
		writeJSONError(w, "provider required in URL", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil {
		writeJSONError(w, "request body too large or unreachable", http.StatusBadRequest)
		return
	}
	var req oauthClientSetRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ClientID) == "" {
		writeJSONError(w, "client_id is required", http.StatusBadRequest)
		return
	}
	if err := s.oauthClients.Set(r.Context(), oauth.ClientCredential{
		Provider:     provider,
		ClientID:     req.ClientID,
		ClientSecret: req.ClientSecret,
	}); err != nil {
		writeJSONError(w, "storing oauth client: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": provider})
}

func (s *Server) handleOAuthClientsDelete(w http.ResponseWriter, r *http.Request) {
	if s.oauthClients == nil {
		writeJSONError(w, "oauth client store is not enabled", http.StatusServiceUnavailable)
		return
	}
	provider := r.PathValue("provider")
	if provider == "" {
		writeJSONError(w, "provider required in URL", http.StatusBadRequest)
		return
	}
	if err := s.oauthClients.Delete(r.Context(), provider); err != nil {
		writeJSONError(w, "deleting oauth client: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "provider": provider})
}

// --- /console/oauth/providers ----------------------------------------------

type oauthProviderInfo struct {
	Name string `json:"name"`
}

func (s *Server) handleOAuthProvidersList(w http.ResponseWriter, _ *http.Request) {
	names := oauth.WellKnownNames()
	out := make([]oauthProviderInfo, 0, len(names))
	for _, n := range names {
		out = append(out, oauthProviderInfo{Name: n})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// --- /console/oauth/begin --------------------------------------------------

type oauthBeginRequest struct {
	Capsule string `json:"capsule"`
}

type oauthBeginResponse struct {
	OK          bool   `json:"ok"`
	FlowID      string `json:"flow_id"`
	AuthURL     string `json:"auth_url"`
	RedirectURI string `json:"redirect_uri"`
}

func (s *Server) handleOAuthBegin(w http.ResponseWriter, r *http.Request) {
	if s.oauthBeginner == nil {
		writeJSONError(w, "oauth flow orchestrator is not enabled", http.StatusServiceUnavailable)
		return
	}
	// Capsule can come from query string (convenient for curl) or
	// JSON body (convenient for the dashboard).
	capsule := r.URL.Query().Get("capsule")
	if capsule == "" {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
		if err != nil {
			writeJSONError(w, "request body too large or unreachable", http.StatusBadRequest)
			return
		}
		if len(body) > 0 {
			var req oauthBeginRequest
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSONError(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
			capsule = req.Capsule
		}
	}
	if capsule == "" {
		writeJSONError(w, "capsule name required (?capsule= or {capsule: ...})", http.StatusBadRequest)
		return
	}

	br, err := s.oauthBeginner.BeginAuthFlow(r.Context(), capsule)
	if err != nil {
		// Surface client-not-registered as 400 so the dashboard can
		// route the user to the OAuth-client setup screen rather than
		// rendering a generic failure.
		if errors.Is(err, oauth.ErrClientNotFound) ||
			strings.Contains(err.Error(), "no client credentials registered") {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSONError(w, "begin oauth flow: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, oauthBeginResponse{
		OK:          true,
		FlowID:      br.FlowID,
		AuthURL:     br.AuthURL,
		RedirectURI: br.RedirectURI,
	})
}

// --- /console/oauth/status -------------------------------------------------

// The dashboard polls this to render "Connected" / "Needs auth"
// next to each ingestor capsule. We don't track the orchestrator's
// in-flight flows here (those are process-local + transient); the
// durable source of truth is "does the capsule have an OAuth
// refresh_token in its credentials blob?" — answered by the OAuth
// bridge through a CapsuleAuthStateProbe interface (wired in cli/).

// CapsuleAuthStateProbe is the optional interface OAuthBeginner
// can implement to also answer "is this capsule connected?" for
// the dashboard's status polling. Wired by cli/start.go's
// daemonOAuthBridge; tests can leave it unimplemented (probe
// returns false → dashboard renders "Needs auth").
type CapsuleAuthStateProbe interface {
	CapsuleHasOAuthToken(ctx context.Context, capsuleName string) (bool, error)
}

type oauthStatusResponse struct {
	Capsule   string `json:"capsule"`
	Connected bool   `json:"connected"`
}

func (s *Server) handleOAuthStatus(w http.ResponseWriter, r *http.Request) {
	capsule := r.URL.Query().Get("capsule")
	if capsule == "" {
		writeJSONError(w, "capsule query param required", http.StatusBadRequest)
		return
	}
	// Without a wired probe we can still answer truthfully: not
	// connected. That keeps the dashboard from breaking when the
	// bridge isn't wired (test builds, headless mode).
	connected := false
	if probe, ok := s.oauthBeginner.(CapsuleAuthStateProbe); ok {
		got, err := probe.CapsuleHasOAuthToken(r.Context(), capsule)
		if err == nil {
			connected = got
		}
	}
	writeJSON(w, http.StatusOK, oauthStatusResponse{Capsule: capsule, Connected: connected})
}
