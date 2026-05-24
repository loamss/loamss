// Package gmail implements the source:gmail connector.
//
// The connector authenticates the user against Google via OAuth 2.0
// authorization-code with PKCE, pulls messages from the user's Gmail
// account via the Gmail v1 REST API, and writes them as:
//
//   - raw RFC822 bytes to the storage adapter at
//     sources/<source_name>/messages/<message_id>.eml
//   - normalized entries (subject, from, snippet, date, labels) to
//     the memory adapter under namespace=<source_name>
//
// Incremental sync uses Gmail's History API; the runtime persists the
// last-seen historyId as the source cursor.
//
// What this connector does NOT do (yet):
//
//   - send mail (gmail.send scope; out of scope for read-only v0.1)
//   - modify labels (gmail.modify scope; not until we have a write path)
//   - parse message bodies into rich structured fields (organizer
//     capsules' job, not a source's)
//   - watch for push notifications via Pub/Sub (deferred)
package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/source"
)

const (
	// SourceID is the registry id this connector registers under.
	SourceID = "source:gmail"

	// DefaultScope is what we ask for if the user doesn't override.
	// gmail.readonly grants list + get + history; no write surface.
	DefaultScope = "https://www.googleapis.com/auth/gmail.readonly"

	// DefaultMaxFullSync caps the first-sync message count so a fresh
	// install doesn't spend hours pulling a decade of mail. The user
	// can raise this in config; the historyId cursor takes over once
	// the first sweep completes.
	DefaultMaxFullSync = 1000

	defaultAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultTokenURL = "https://oauth2.googleapis.com/token"
	defaultAPIBase  = "https://gmail.googleapis.com/gmail/v1"

	// expiryBuffer is how much earlier than the actual expiry we
	// treat the access token as stale. Avoids 401-then-refresh
	// round-trips during long syncs.
	expiryBuffer = 60 * time.Second

	authFlowTimeout = 10 * time.Minute
)

// gmailSource is the source:gmail adapter. One instance per
// configured source (e.g. "gmail-personal").
//
// The struct holds two kinds of state:
//
//   - configuration baked in at Init (OAuth client ids, scope, URLs);
//     immutable after Init returns
//   - per-authentication state used during the OAuth handshake
//     (PKCE verifier, listener, state token); cleared after
//     CompleteAuth returns
type gmailSource struct {
	mu sync.Mutex

	// Wired by Init from source.Deps.
	deps source.Deps

	// Resolved config — immutable after Init.
	clientID     string
	clientSecret string
	scope        string
	query        string
	maxFullSync  int

	// Endpoint URLs. Overridable from config (defaults to Google's
	// production endpoints); tests inject httptest URLs.
	authURL  string
	tokenURL string
	apiBase  string

	// HTTP client (no auto-redirect for the loopback flow).
	httpClient *http.Client

	// pendingAuth holds the in-flight OAuth handshake between
	// BeginAuth and CompleteAuth. Nil when no flow is in progress.
	pendingAuth *pendingAuthFlow
}

// New returns an uninitialized gmail source. Registered via init();
// callers should normally go through source.New("source:gmail").
func New() source.Source { return &gmailSource{} }

func init() {
	source.Register(SourceID, New)
}

// ID implements source.Source.
func (g *gmailSource) ID() string { return SourceID }

// Init implements source.Source.
//
// Required config:
//
//	client_id      — OAuth 2.0 client ID from Google Cloud Console
//	client_secret  — OAuth 2.0 client secret
//
// Optional config:
//
//	scope          — defaults to "https://www.googleapis.com/auth/gmail.readonly"
//	query          — Gmail search query to scope ingestion ("from:newsletters")
//	max_full_sync  — cap on first-sync message count (default 1000)
//	auth_url       — override for tests
//	token_url      — override for tests
//	api_base       — override for tests
func (g *gmailSource) Init(_ context.Context, deps source.Deps) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if deps.Credentials == nil {
		return errors.New("source:gmail: no CredentialStore provided")
	}
	if deps.Storage == nil {
		return errors.New("source:gmail: no Storage adapter provided")
	}

	cid, _ := deps.Config["client_id"].(string)
	csec, _ := deps.Config["client_secret"].(string)
	if cid == "" {
		return errors.New("source:gmail: client_id is required")
	}
	if csec == "" {
		return errors.New("source:gmail: client_secret is required")
	}
	g.clientID = cid
	g.clientSecret = csec

	if v, ok := deps.Config["scope"].(string); ok && v != "" {
		g.scope = v
	} else {
		g.scope = DefaultScope
	}
	if v, ok := deps.Config["query"].(string); ok {
		g.query = v
	}
	switch v := deps.Config["max_full_sync"].(type) {
	case int:
		g.maxFullSync = v
	case int64:
		g.maxFullSync = int(v)
	case float64:
		g.maxFullSync = int(v)
	default:
		g.maxFullSync = DefaultMaxFullSync
	}
	if g.maxFullSync <= 0 {
		g.maxFullSync = DefaultMaxFullSync
	}

	g.authURL = stringOr(deps.Config["auth_url"], defaultAuthURL)
	g.tokenURL = stringOr(deps.Config["token_url"], defaultTokenURL)
	g.apiBase = stringOr(deps.Config["api_base"], defaultAPIBase)

	g.httpClient = &http.Client{
		Timeout: 30 * time.Second,
		// Loopback OAuth flow needs the *user's* browser to follow
		// the redirect to 127.0.0.1; the runtime never follows the
		// redirect itself. Per-request behavior is the default
		// CheckRedirect, which is fine for API calls.
	}

	g.deps = deps
	return nil
}

// AuthStatus implements source.Source.
func (g *gmailSource) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	tok, err := g.loadToken(ctx)
	if errors.Is(err, source.ErrNoCredentials) {
		return source.AuthStatus{Authenticated: false, Reason: "no credentials stored"}, nil
	}
	if err != nil {
		return source.AuthStatus{Authenticated: false, Reason: err.Error()}, nil
	}
	return source.AuthStatus{
		Authenticated: true,
		ExpiresAt:     tok.Expiry,
		NeedsRefresh:  tok.expiresSoon(),
	}, nil
}

// BeginAuth implements source.Source. Starts a loopback listener and
// returns the consent URL; the caller should display the URL and then
// call CompleteAuth, which blocks waiting for the user's browser to
// hit the loopback.
func (g *gmailSource) BeginAuth(_ context.Context) (source.AuthFlow, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.pendingAuth != nil {
		return source.AuthFlow{}, source.ErrAuthInProgress
	}

	flow, err := g.startLoopback()
	if err != nil {
		return source.AuthFlow{}, fmt.Errorf("starting loopback listener: %w", err)
	}
	g.pendingAuth = flow

	return source.AuthFlow{
		Kind: source.AuthFlowBrowser,
		URL:  flow.authURL,
		Instructions: "Loamss will receive the redirect on a local port. " +
			"Approve the consent screen in your browser; this window will return on its own.",
		ExpiresAt: time.Now().Add(authFlowTimeout),
	}, nil
}

// CompleteAuth implements source.Source.
//
// Browser-flow callers pass empty params; CompleteAuth then blocks on
// the loopback listener (until ctx is canceled or the flow times out).
// Code-paste callers pass params["code"] and CompleteAuth exchanges
// the code directly without using the listener (used as a fallback
// when the loopback can't reach the user, e.g. headless server).
func (g *gmailSource) CompleteAuth(ctx context.Context, params map[string]string) error {
	g.mu.Lock()
	flow := g.pendingAuth
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		if g.pendingAuth != nil {
			g.pendingAuth.shutdown()
		}
		g.pendingAuth = nil
		g.mu.Unlock()
	}()

	var (
		code     string
		verifier string
	)

	switch {
	case params["code"] != "":
		code = params["code"]
		if flow != nil {
			verifier = flow.verifier
		}
	case flow != nil:
		var err error
		code, err = flow.waitForCode(ctx, authFlowTimeout)
		if err != nil {
			return err
		}
		verifier = flow.verifier
	default:
		return errors.New("source:gmail: CompleteAuth called with no in-flight flow and no code")
	}

	tok, err := g.exchangeCode(ctx, code, verifier, flow.redirectURI())
	if err != nil {
		return fmt.Errorf("exchanging code: %w", err)
	}
	if err := g.saveToken(ctx, tok); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	return nil
}

// Sync implements source.Source. Pulls messages from Gmail and writes
// them to storage + memory. Uses the cursor (historyId) for
// incremental fetch; empty cursor triggers a first-sync sweep capped
// at max_full_sync.
func (g *gmailSource) Sync(ctx context.Context, cursor []byte) (source.SyncResult, error) {
	started := time.Now().UTC()
	result := source.SyncResult{Started: started}

	cur, err := decodeCursor(cursor)
	if err != nil {
		result.Finished = time.Now().UTC()
		return result, fmt.Errorf("decoding cursor: %w", err)
	}

	api := g.newAPIClient()

	if cur.HistoryID == "" {
		// First sync — list messages, ingest each, capture the
		// largest historyId we see along the way.
		highest, err := g.fullSync(ctx, api, &result)
		if err != nil {
			result.Finished = time.Now().UTC()
			return result, err
		}
		cur.HistoryID = highest
		cur.LastSyncTime = time.Now().UTC().Format(time.RFC3339Nano)
		result.Cursor = mustEncodeCursor(cur)
		result.Finished = time.Now().UTC()
		return result, nil
	}

	// Incremental sync via history.list. Returns added/deleted message
	// ids; we re-fetch added ones and propagate deletions through the
	// storage + memory adapters.
	added, deleted, newHistoryID, err := api.listHistory(ctx, cur.HistoryID)
	if err != nil {
		result.Finished = time.Now().UTC()
		return result, err
	}
	for _, id := range added {
		if err := g.ingestMessage(ctx, api, id, &result); err != nil {
			result.Errors = append(result.Errors, source.SyncError{
				RecordID: id,
				Reason:   err.Error(),
			})
			continue
		}
		result.RecordsUpdated++
	}
	for _, id := range deleted {
		// Best-effort cleanup; missing records are not errors here.
		_ = g.deps.Storage.Delete(ctx, messagePath(g.deps.SourceName, id))
		_ = g.deps.Memory.Delete(ctx, g.deps.SourceName, id)
	}

	cur.HistoryID = newHistoryID
	cur.LastSyncTime = time.Now().UTC().Format(time.RFC3339Nano)
	result.Cursor = mustEncodeCursor(cur)
	result.Finished = time.Now().UTC()
	return result, nil
}

// HealthCheck implements source.Source. Probe is a token-aware ping
// to the profile endpoint; a 200 confirms the token is good and Gmail
// is reachable.
func (g *gmailSource) HealthCheck(ctx context.Context) error {
	tok, err := g.loadToken(ctx)
	if errors.Is(err, source.ErrNoCredentials) {
		return nil // no creds yet is a known state, not unhealthy
	}
	if err != nil {
		return err
	}
	if tok.expired() {
		if err := g.refreshToken(ctx, tok); err != nil {
			return err
		}
	}
	api := g.newAPIClient()
	return api.profilePing(ctx)
}

// Close implements source.Source.
func (g *gmailSource) Close(_ context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pendingAuth != nil {
		g.pendingAuth.shutdown()
		g.pendingAuth = nil
	}
	return nil
}

// --- internals: storage layout ----------------------------------------

func messagePath(sourceName, messageID string) string {
	return "sources/" + sourceName + "/messages/" + messageID + ".eml"
}

// --- internals: token persistence -------------------------------------

type oauthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry"`
	Scope        string    `json:"scope,omitempty"`
}

func (t *oauthToken) expired() bool { return time.Now().After(t.Expiry) }
func (t *oauthToken) expiresSoon() bool {
	return time.Now().Add(expiryBuffer).After(t.Expiry)
}

func (g *gmailSource) loadToken(ctx context.Context) (*oauthToken, error) {
	creds, err := g.deps.Credentials.Get(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(creds)
	if err != nil {
		return nil, fmt.Errorf("re-encoding creds: %w", err)
	}
	var tok oauthToken
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil, fmt.Errorf("decoding token: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, source.ErrNoCredentials
	}
	return &tok, nil
}

func (g *gmailSource) saveToken(ctx context.Context, tok *oauthToken) error {
	raw, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	return g.deps.Credentials.Set(ctx, m)
}

func stringOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}
