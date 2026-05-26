package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/loamss/loamss/runtime/internal/oauth"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// OAuthBridge is the narrow surface the oauth.access_token tool
// needs from the runtime. Defined as an interface so the mcp
// package doesn't import internal/capsule (the capsule manifest
// is where the provider name comes from); the cli/start.go
// bridge implementation reads the manifest + walks the OAuth
// provider registry + ClientStore.
type OAuthBridge interface {
	// AccessTokenFor returns a valid bearer for the named capsule,
	// refreshing transparently if cached + reading the capsule's
	// own manifest to find the provider. Returns oauth.ErrReauthRequired
	// when the refresh token is revoked/expired.
	AccessTokenFor(ctx context.Context, capsuleName string) (string, *time.Time, error)
}

// NewOAuthAccessTokenTool builds the oauth.access_token MCP tool.
// Only callable by capsule principals.
func NewOAuthAccessTokenTool(bridge OAuthBridge) Tool {
	return Tool{
		Name: "oauth.access_token",
		Description: "Return a valid OAuth 2.0 bearer access token for this capsule's configured provider. " +
			"Refreshes the token transparently when stale; surfaces a re-auth prompt to the user when the refresh token is revoked. " +
			"Only callable by capsule principals.",
		Capability:  "", // auth-only; gated by capsule install + manifest oauth block
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler:     makeOAuthAccessTokenHandler(bridge),
	}
}

// oauthAccessTokenResult is the shape returned in the tool's text
// content block.
type oauthAccessTokenResult struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

func makeOAuthAccessTokenHandler(
	bridge OAuthBridge,
) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		capsuleName, err := requireCapsulePrincipalOAuth(in.Principal)
		if err != nil {
			return ToolResult{}, err
		}
		token, expiresAt, err := bridge.AccessTokenFor(ctx, capsuleName)
		if err != nil {
			// Re-auth required is surfaced as isError so the capsule
			// sees a structured signal (not a transport failure).
			// The bridge is expected to enqueue a PendingApproval
			// the dashboard will surface to the user.
			if errors.Is(err, oauth.ErrReauthRequired) || errors.Is(err, oauth.ErrNoRefreshToken) {
				content, _ := JSONContent(map[string]any{
					"error":   "oauth.reauth_required",
					"message": err.Error(),
				})
				return ToolResult{Content: []Content{content}, IsError: true}, nil
			}
			return ToolResult{}, fmt.Errorf("%w: oauth.access_token: %v", ErrToolBackend, err)
		}
		out := oauthAccessTokenResult{AccessToken: token}
		if expiresAt != nil {
			out.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
		}
		content, err := JSONContent(out)
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{content}}, nil
	}
}

// requireCapsulePrincipalOAuth mirrors the capsule-only guard from
// the credentials.* tools. Kept separate so the error messages
// self-identify in logs.
func requireCapsulePrincipalOAuth(p permission.Principal) (string, error) {
	if p.Kind != permission.PrincipalCapsule {
		return "", fmt.Errorf("oauth.* tools are restricted to capsule principals (caller is %s)", p.Kind)
	}
	if p.ID == "" {
		return "", errors.New("oauth: capsule name is required")
	}
	return p.ID, nil
}
