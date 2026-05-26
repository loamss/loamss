package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/oauth"
)

// daemonOAuthBridge bridges the mcp.OAuthBridge surface (consumed
// by the oauth.access_token tool) to the runtime's actual OAuth
// machinery: the capsule store (for the manifest's provider block),
// the OAuth client store (for the user's per-provider client_id),
// and the orchestrator (for the refresh logic + token cache).
type daemonOAuthBridge struct {
	capsules     *capsule.Store
	clients      *oauth.ClientStore
	orchestrator *oauth.Orchestrator
	creds        oauth.CredentialStore
	logger       *slog.Logger
}

func newDaemonOAuthBridge(
	capsules *capsule.Store,
	clients *oauth.ClientStore,
	orchestrator *oauth.Orchestrator,
	creds oauth.CredentialStore,
	logger *slog.Logger,
) *daemonOAuthBridge {
	return &daemonOAuthBridge{
		capsules:     capsules,
		clients:      clients,
		orchestrator: orchestrator,
		creds:        creds,
		logger:       logger,
	}
}

// AccessTokenFor implements mcp.OAuthBridge.
func (b *daemonOAuthBridge) AccessTokenFor(
	ctx context.Context, capsuleName string,
) (string, *time.Time, error) {
	provider, err := b.resolveProvider(ctx, capsuleName)
	if err != nil {
		return "", nil, err
	}
	return b.orchestrator.AccessToken(ctx, capsuleName, provider)
}

// resolveProvider composes the runtime's view of a capsule's OAuth
// provider config from the manifest + well-known registry +
// per-user client_id store.
func (b *daemonOAuthBridge) resolveProvider(
	ctx context.Context, capsuleName string,
) (oauth.ProviderConfig, error) {
	c, err := b.capsules.Get(ctx, capsuleName)
	if err != nil {
		return oauth.ProviderConfig{}, fmt.Errorf("oauth: capsule %s not installed: %w", capsuleName, err)
	}
	if c.Manifest == nil || c.Manifest.OAuth == nil {
		return oauth.ProviderConfig{}, errors.New("oauth: capsule has no OAuth block in its manifest")
	}
	spec := c.Manifest.OAuth

	// Resolve endpoints: well-known wins; otherwise manifest's inline.
	var authzEndpoint, tokenEndpoint string
	extras := map[string]string{}
	if known, lookupErr := oauth.Lookup(spec.Provider); lookupErr == nil {
		authzEndpoint = known.AuthorizationEndpoint
		tokenEndpoint = known.TokenEndpoint
		for k, v := range known.DefaultExtraParams {
			extras[k] = v
		}
	} else {
		authzEndpoint = spec.AuthorizationEndpoint
		tokenEndpoint = spec.TokenEndpoint
	}
	for k, v := range spec.ExtraParams {
		extras[k] = v // manifest overrides
	}

	// Read the user's per-provider client_id.
	cred, err := b.clients.Get(ctx, spec.Provider)
	if err != nil {
		if errors.Is(err, oauth.ErrClientNotFound) {
			return oauth.ProviderConfig{},
				fmt.Errorf("oauth: no client credentials registered for provider %q "+
					"(POST /console/oauth/clients/%s to set one)",
					spec.Provider, spec.Provider)
		}
		return oauth.ProviderConfig{}, err
	}

	return oauth.ProviderConfig{
		Name:                  spec.Provider,
		AuthorizationEndpoint: authzEndpoint,
		TokenEndpoint:         tokenEndpoint,
		Scopes:                spec.Scopes,
		ExtraParams:           extras,
		ClientID:              cred.ClientID,
		ClientSecret:          cred.ClientSecret,
	}, nil
}

// BeginAuthFlow is exposed for the /console/oauth/begin HTTP
// handler. Resolves the provider for the named capsule and starts
// the flow.
func (b *daemonOAuthBridge) BeginAuthFlow(
	ctx context.Context, capsuleName string,
) (oauth.BeginResult, error) {
	provider, err := b.resolveProvider(ctx, capsuleName)
	if err != nil {
		return oauth.BeginResult{}, err
	}
	return b.orchestrator.Begin(ctx, capsuleName, provider)
}

// CapsuleHasOAuthToken implements server.CapsuleAuthStateProbe.
// Returns true when the capsule has a stored refresh token (the
// durable proof of "this capsule is connected"). The orchestrator's
// in-flight flows are NOT considered — only completed flows count.
func (b *daemonOAuthBridge) CapsuleHasOAuthToken(
	ctx context.Context, capsuleName string,
) (bool, error) {
	_, found, err := b.creds.Get(ctx, capsuleName, oauth.RefreshTokenKey)
	if err != nil {
		return false, err
	}
	return found, nil
}

// credentialStoreAdapter adapts mcp.CapsuleCredentialStore to the
// oauth.CredentialStore interface — same Get/Set surface but with
// the local CredentialEntry shape so the oauth package doesn't
// import mcp.
type credentialStoreAdapter struct {
	inner *mcp.CapsuleCredentialStore
}

func newCredentialStoreAdapter(c *mcp.CapsuleCredentialStore) *credentialStoreAdapter {
	return &credentialStoreAdapter{inner: c}
}

func (a *credentialStoreAdapter) Set(
	ctx context.Context, capsuleName, key, value string, expiresAt *time.Time,
) error {
	return a.inner.Set(ctx, capsuleName, key, value, expiresAt)
}

func (a *credentialStoreAdapter) Get(
	ctx context.Context, capsuleName, key string,
) (oauth.CredentialEntry, bool, error) {
	entry, found, err := a.inner.Get(ctx, capsuleName, key)
	if !found || err != nil {
		return oauth.CredentialEntry{}, false, err
	}
	return oauth.CredentialEntry{
		Value:     entry.Value,
		ExpiresAt: entry.ExpiresAt,
	}, true, nil
}
