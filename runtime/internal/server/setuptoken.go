package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/loamss/loamss/runtime/internal/permission"
)

// SetupTokenGate guards the first-run / re-init surface (`/console/*`
// and `/pair`) on instances reachable beyond localhost. The contract:
//
//   - When the gate is not active (laptop installs in the local
//     profile with no explicit override), it passes every request
//     through unchanged. The historical localhost-only contract holds.
//
//   - When the gate is active and not yet consumed, requests on
//     protected routes must carry either:
//       a) a Bearer setup token matching the active token, or
//       b) a Bearer paired-client credential the engine recognizes.
//     The setup token is single-use — the first successful
//     `/console/init` request flips Consumed to true and persists a
//     sentinel file so subsequent restarts don't re-enable it.
//
//   - When the gate is active and consumed, only (b) is accepted:
//     the operator must use a paired-client credential. Restoring
//     setup-token access requires deleting the sentinel file and
//     restarting (`loamss setup-token reset` lands in a follow-up).
//
// Health (`/healthz`) and version (`/version`) endpoints are never
// gated — Cloud Run / Fly / GKE probes need them unauthenticated.
//
// The gate intentionally does NOT persist the active token itself.
// Persisting an active credential to disk is a footgun (it survives
// instance migration, snapshots, log scrapes). Two acceptable
// origin paths:
//
//   1. LOAMSS_SETUP_TOKEN env var supplied by the operator at deploy
//      time. This persists naturally — same env, same token across
//      restarts.
//   2. Auto-generated at process start. Lasts only the current
//      instance lifetime; if Cloud Run cold-starts before init, the
//      operator sees a fresh token in the new instance's logs.
type SetupTokenGate struct {
	// activeToken is the token to compare incoming Bearer credentials
	// against. Empty string means "no token issued for this process".
	// Set once at construction; never rotated mid-process.
	activeToken string

	// consumed is set to 1 when /console/init has succeeded under
	// this gate. Once consumed, the setup token is no longer accepted
	// — only paired-client credentials work. atomic.Bool semantics
	// because Consume can race with concurrent middleware reads.
	consumed atomic.Bool

	// consumedPath is the sentinel file written when Consume is
	// called. Survives restarts so a re-deploy doesn't undo
	// consumption. Empty string disables persistence (used by tests).
	consumedPath string

	// origin records where the active token came from for diagnostic
	// logging only. Never returned to the wire.
	origin string

	// engine is consulted when the request's Bearer doesn't match the
	// setup token — falls back to paired-client auth. Required when
	// the gate is active.
	engine *permission.Engine
}

// SetupTokenOptions configures the gate. Constructed in start.go from
// the resolved profile + env vars, passed to server.New via Options.
// Nil disables the gate entirely.
type SetupTokenOptions struct {
	// Token is the setup token. When non-empty, the gate is active.
	// When empty, server.New treats SetupToken as nil (off).
	Token string

	// Origin is a short human label for where Token came from
	// ("env LOAMSS_SETUP_TOKEN", "auto-generated", etc.). Logged at
	// startup; never echoed to clients.
	Origin string

	// ConsumedPath is the on-disk sentinel that records prior
	// consumption. When the file exists at construction time, the
	// gate starts in the "consumed" state (setup token rejected,
	// only paired-client auth accepted). Empty disables persistence.
	ConsumedPath string

	// Engine is the permission engine to fall back to for
	// paired-client auth. Required.
	Engine *permission.Engine
}

// NewSetupTokenGate constructs a gate from options. Returns nil when
// opts.Token is empty (gate disabled — laptop path). Errors only on
// programmer mistakes (engine nil, token too short).
func NewSetupTokenGate(opts SetupTokenOptions) (*SetupTokenGate, error) {
	if opts.Token == "" {
		return nil, nil
	}
	if opts.Engine == nil {
		return nil, errors.New("setup token gate requires a permission engine")
	}
	if len(opts.Token) < 16 {
		return nil, fmt.Errorf("setup token must be at least 16 chars (got %d) — generated tokens are 64", len(opts.Token))
	}
	g := &SetupTokenGate{
		activeToken:  opts.Token,
		consumedPath: opts.ConsumedPath,
		origin:       opts.Origin,
		engine:       opts.Engine,
	}
	// Honor a prior consumption marker. Operators who want to re-init
	// must delete this file (the future `loamss setup-token reset`
	// CLI will do it for them) and restart.
	if opts.ConsumedPath != "" {
		if _, err := os.Stat(opts.ConsumedPath); err == nil {
			g.consumed.Store(true)
		}
	}
	return g, nil
}

// GenerateSetupToken returns a fresh 256-bit hex-encoded token.
// Used by start.go when the operator did not supply LOAMSS_SETUP_TOKEN.
func GenerateSetupToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("setup token: reading random bytes: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Active reports whether the gate is currently enforcing checks.
// False either when the gate is nil or when both the token has been
// consumed and no paired-client check applies — callers should use
// IsConsumed for the latter state.
func (g *SetupTokenGate) Active() bool {
	return g != nil && g.activeToken != ""
}

// IsConsumed reports whether the setup token has been spent. After
// consumption only paired-client credentials are accepted on
// protected routes.
func (g *SetupTokenGate) IsConsumed() bool {
	return g != nil && g.consumed.Load()
}

// Origin returns the short label describing where the active token
// came from. Used by start.go's startup log; never exposed via the
// wire surface.
func (g *SetupTokenGate) Origin() string {
	if g == nil {
		return ""
	}
	return g.origin
}

// Consume marks the setup token as spent and writes the persistence
// sentinel. Returns (true, nil) on the call that actually flipped the
// state and (false, nil) on subsequent calls — atomic, so callers can
// rely on exactly one (firstCall=true) result across concurrent
// invocations. Used by /console/init success path to emit the
// audit-trail event exactly once.
//
// Errors writing the sentinel are returned but the in-memory state
// is still flipped — losing the sentinel only means a restart would
// re-open the gate, which is a strictly less-bad failure mode than
// leaving the gate open for the rest of this process lifetime.
func (g *SetupTokenGate) Consume() (firstCall bool, err error) {
	if g == nil {
		return false, nil
	}
	if !g.consumed.CompareAndSwap(false, true) {
		return false, nil
	}
	if g.consumedPath == "" {
		return true, nil
	}
	// Best-effort: ensure parent dir exists. start.go always creates
	// data_dir, but tests with ad-hoc paths may not.
	_ = os.MkdirAll(filepath.Dir(g.consumedPath), 0o700)
	// File contents are not security-relevant — its existence is the
	// signal. A short marker helps humans grepping the data_dir.
	if err := os.WriteFile(g.consumedPath, []byte("consumed\n"), 0o600); err != nil {
		return true, fmt.Errorf("setup token: writing consumed sentinel: %w", err)
	}
	return true, nil
}

// matches reports whether the provided token equals the active token
// under constant-time comparison. False when the gate has been
// consumed (the setup token is no longer accepted).
func (g *SetupTokenGate) matches(token string) bool {
	if g == nil || g.activeToken == "" {
		return false
	}
	if g.consumed.Load() {
		return false
	}
	// Constant-time compare to avoid timing oracles. subtle.ConstantTimeCompare
	// requires equal-length slices, so cheap len() check first.
	if len(token) != len(g.activeToken) {
		// Still spend a comparison against a dummy of equal length so
		// total work isn't observably different. The single allocation
		// per failed auth is acceptable; this path is not hot.
		dummy := make([]byte, len(g.activeToken))
		_ = subtle.ConstantTimeCompare([]byte(token), dummy)
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(g.activeToken)) == 1
}

// Middleware wraps an http.Handler with the gate. When the gate is
// disabled, the handler is returned untouched. Otherwise the wrapper:
//
//   - Returns 200 immediately for the wrapped route only if a valid
//     credential is present
//   - Returns 401 with a structured JSON body on missing/invalid auth
//
// The wrapped handler runs with the principal in context when auth
// succeeded via the paired-client path; setup-token-authenticated
// requests have no principal (the token represents "the operator
// completing first-run" — an unidentified-but-trusted caller).
func (g *SetupTokenGate) Middleware(next http.Handler) http.Handler {
	if !g.Active() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := extractBearerToken(r.Header.Get("Authorization"))
		if err != nil {
			writeAuthError(w, "this instance requires a Bearer credential — supply the setup token (initial deploy) or a paired-client token", http.StatusUnauthorized)
			return
		}

		// Setup token path: accepted only while unconsumed. matches()
		// returns false post-consume, so the request falls through to
		// paired-client auth below.
		if g.matches(token) {
			next.ServeHTTP(w, r)
			return
		}

		// Paired-client path: same logic as bearerAuthMiddleware. We
		// inline it here rather than chaining middlewares because the
		// failure messaging differs (we want to mention setup token
		// as an alternative).
		principal, client, err := g.engine.AuthenticateClient(r.Context(), token)
		switch {
		case errors.Is(err, permission.ErrInvalidCredential):
			writeAuthError(w, "invalid credential (expected setup token or paired-client bearer)", http.StatusUnauthorized)
			return
		case errors.Is(err, permission.ErrClientRevoked):
			writeAuthError(w, "paired-client credential revoked", http.StatusUnauthorized)
			return
		case err != nil:
			writeAuthError(w, "auth backend error", http.StatusInternalServerError)
			return
		}
		// Attach principal so downstream handlers can identify the
		// caller (e.g., for audit context). Mirrors the shape
		// bearerAuthMiddleware produces.
		ctx := r.Context()
		if principal != nil {
			ctx = context.WithValue(ctx, contextKeyPrincipal, principal)
			ctx = context.WithValue(ctx, contextKeyClient, client)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
