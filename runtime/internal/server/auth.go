package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/loamss/loamss/runtime/internal/permission"
)

// contextKey is unexported to prevent collision with other packages
// that stuff values into request context.
type contextKey int

const (
	contextKeyPrincipal contextKey = iota
	contextKeyClient
)

// PrincipalFromContext extracts the authenticated Principal from a
// request context. Returns nil when no principal is attached
// (request did not pass through bearerAuthMiddleware).
func PrincipalFromContext(ctx context.Context) *permission.Principal {
	v, _ := ctx.Value(contextKeyPrincipal).(*permission.Principal)
	return v
}

// ClientFromContext extracts the authenticated Client from a request
// context. Returns nil for unauthenticated requests.
func ClientFromContext(ctx context.Context) *permission.Client {
	v, _ := ctx.Value(contextKeyClient).(*permission.Client)
	return v
}

// bearerAuthMiddleware authenticates the request via the
// `Authorization: Bearer <token>` header against the permission
// engine. On success, the resolved Principal and Client are attached
// to the request context for downstream handlers. On failure, the
// middleware writes a 401 with a JSON error body and short-circuits.
//
// The engine emits its own audit entry (client.auth_failed) on every
// failure path; this middleware does not double-log.
func (s *Server) bearerAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.engine == nil {
			// Programming error: auth middleware mounted without an
			// engine. Fail closed.
			writeAuthError(w, "auth engine not configured", http.StatusInternalServerError)
			return
		}
		token, err := extractBearerToken(r.Header.Get("Authorization"))
		if err != nil {
			writeAuthError(w, "missing or malformed Authorization header", http.StatusUnauthorized)
			return
		}
		principal, client, err := s.engine.AuthenticateClient(r.Context(), token)
		switch {
		case errors.Is(err, permission.ErrInvalidCredential):
			writeAuthError(w, "invalid credential", http.StatusUnauthorized)
			return
		case errors.Is(err, permission.ErrClientRevoked):
			// 401 (not 403) is correct here per RFC 7235: the credential
			// is no longer valid. 403 would imply the credential is
			// valid but lacks authorization for this resource.
			writeAuthError(w, "credential revoked", http.StatusUnauthorized)
			return
		case err != nil:
			s.logger.Error("auth backend error", "err", err)
			writeAuthError(w, "auth backend error", http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), contextKeyPrincipal, principal)
		ctx = context.WithValue(ctx, contextKeyClient, client)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// extractBearerToken returns the token portion of a
// `Bearer <token>` Authorization header. Comparison of the scheme
// is case-insensitive per RFC 7235 §2.1.
func extractBearerToken(header string) (string, error) {
	if header == "" {
		return "", errors.New("missing Authorization header")
	}
	// "Bearer " is 7 chars; reject anything that can't fit.
	if len(header) < 8 {
		return "", errors.New("authorization header too short")
	}
	if !strings.EqualFold(header[:7], "Bearer ") {
		return "", errors.New("authorization scheme must be Bearer")
	}
	token := strings.TrimSpace(header[7:])
	if token == "" {
		return "", errors.New("empty bearer token")
	}
	return token, nil
}
