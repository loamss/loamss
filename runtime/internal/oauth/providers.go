// Package oauth implements the runtime-owned OAuth 2.0 flow that
// capsule ingestors use to authenticate against external providers.
//
// The runtime IS the OAuth client: it owns the loopback listener,
// holds the PKCE verifier, drives the browser, exchanges the code
// for tokens, persists them, and refreshes transparently. The
// capsule's only OAuth surface is the `oauth.access_token` MCP
// tool, which returns a valid bearer ready to put in an
// Authorization header.
//
// See docs/capsule-ingestor-primitives.md §4 "OAuth callback
// bridge (revised)" for the design.
package oauth

import (
	"errors"
	"fmt"
)

// Provider is the runtime's view of one OAuth 2.0 provider —
// the URLs to talk to + any provider-specific defaults the runtime
// should apply automatically. Well-known providers ship in the
// registry below; non-well-known ones can be expressed inline via
// the capsule manifest's oauth block.
type Provider struct {
	// Name is the identifier used in capsule manifests (e.g. "google").
	Name string

	// AuthorizationEndpoint is the URL the user is sent to in the
	// browser. The runtime appends client_id, redirect_uri, scope,
	// state, code_challenge, and any extra_params.
	AuthorizationEndpoint string

	// TokenEndpoint is the URL the runtime POSTs the auth code to
	// (and later, the refresh token).
	TokenEndpoint string

	// DefaultExtraParams are provider-specific query parameters
	// auto-added to the authorization URL. Capsule manifests can
	// override or extend via their own oauth.extra_params. Common
	// uses:
	//   google: access_type=offline + prompt=consent so the provider
	//           returns a refresh_token on every auth (not just the
	//           first one, which is googles default).
	DefaultExtraParams map[string]string
}

// ErrUnknownProvider is returned by Lookup for a name not in the
// well-known registry. The caller can recover by reading
// endpoints from the manifest's inline declaration.
var ErrUnknownProvider = errors.New("oauth: provider is not in the well-known registry")

// wellKnownProviders is the runtime's built-in OAuth provider
// catalogue. Adding a provider here lets every capsule that targets
// it skip declaring endpoints inline.
//
// All entries use PKCE — desktop OAuth without PKCE is broken by
// design (no confidential client to hold a secret). Loamss treats
// PKCE as always on; capsules can't opt out.
var wellKnownProviders = map[string]Provider{
	"google": {
		Name:                  "google",
		AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenEndpoint:         "https://oauth2.googleapis.com/token",
		// Google withholds the refresh_token unless these are set
		// — once per user, the user has to see the consent dialog
		// again on re-auth to get a fresh refresh_token.
		DefaultExtraParams: map[string]string{
			"access_type":            "offline",
			"prompt":                 "consent",
			"include_granted_scopes": "true",
		},
	},
	"github": {
		Name:                  "github",
		AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
		TokenEndpoint:         "https://github.com/login/oauth/access_token",
		// GitHub's OAuth doesn't have a separate refresh-token
		// concept — tokens live until revoked. No default extras.
		DefaultExtraParams: map[string]string{},
	},
}

// Lookup returns the well-known Provider for the given name. The
// name match is case-sensitive — manifests must use lowercase
// canonical names ("google", not "Google"). For non-well-known
// providers, the caller composes a Provider from the manifest's
// inline endpoint declarations.
func Lookup(name string) (Provider, error) {
	p, ok := wellKnownProviders[name]
	if !ok {
		return Provider{}, fmt.Errorf("%w: %q", ErrUnknownProvider, name)
	}
	return p, nil
}

// WellKnown reports whether the given name is in the registry.
// Used by manifest validation to decide whether inline endpoints
// must be required.
func WellKnown(name string) bool {
	_, ok := wellKnownProviders[name]
	return ok
}

// WellKnownNames returns the sorted list of registered provider
// names. Used by the dashboard's "available providers" surface and
// by the test suite to assert what's shipped.
func WellKnownNames() []string {
	names := make([]string, 0, len(wellKnownProviders))
	for n := range wellKnownProviders {
		names = append(names, n)
	}
	// stdlib sort would do, but the package's only caller is the
	// console — three providers, sort cheap. Keep import surface
	// minimal until there's a real reason to grow it.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	return names
}
