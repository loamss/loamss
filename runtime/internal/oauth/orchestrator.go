package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Credentials key names used in the per-capsule CapsuleCredentialStore.
// Stable identifiers; capsules should not rely on them.
const (
	credKeyAccessToken  = "oauth.access_token"
	credKeyRefreshToken = "oauth.refresh_token"
	credKeyScope        = "oauth.scope"
	credKeyTokenType    = "oauth.token_type"
)

// Sentinel errors. Callers wrap with %w and test via errors.Is.
var (
	// ErrFlowNotFound is returned when a callback arrives for an
	// unknown flow_id — either an attacker probing the loopback
	// or a stale tab the user finally clicked.
	ErrFlowNotFound = errors.New("oauth: in-flight flow not found")

	// ErrStateMismatch is returned when the callback's state does
	// not match the one the runtime issued. Anti-CSRF check.
	ErrStateMismatch = errors.New("oauth: state mismatch")

	// ErrReauthRequired is returned by AccessToken when the
	// refresh token has been revoked or expired. The caller
	// should surface a re-auth prompt to the user.
	ErrReauthRequired = errors.New("oauth: re-authentication required")

	// ErrNoRefreshToken is returned by AccessToken when the
	// capsule has never completed an OAuth flow + has no stored
	// access token. Same caller treatment as ErrReauthRequired.
	ErrNoRefreshToken = errors.New("oauth: capsule has no stored OAuth credentials")
)

// CredentialStore is the narrow contract Orchestrator needs from
// mcp.CapsuleCredentialStore. Defined locally so the oauth package
// doesn't import mcp (which would create a cycle once mcp's
// oauth.access_token tool wants to call this package).
type CredentialStore interface {
	Set(ctx context.Context, capsuleName, key, value string, expiresAt *time.Time) error
	Get(ctx context.Context, capsuleName, key string) (entry CredentialEntry, found bool, err error)
}

// CredentialEntry mirrors mcp.CapsuleCredentialEntry. Local
// duplicate to keep the import direction clean.
type CredentialEntry struct {
	Value     string
	ExpiresAt *time.Time
}

// ProviderConfig is the resolved configuration for a flow — well-
// known endpoints merged with the manifest's inline declarations
// (manifest wins for the inline path; well-known wins when both
// declare the same field, which the manifest validator should
// already have forbidden).
type ProviderConfig struct {
	// Name is the canonical provider name (e.g. "google").
	Name string

	// AuthorizationEndpoint and TokenEndpoint are the resolved
	// URLs.
	AuthorizationEndpoint string
	TokenEndpoint         string

	// Scopes is the list of OAuth scopes to request.
	Scopes []string

	// ExtraParams are query parameters added to the authorization
	// URL — merged from provider defaults and manifest extras.
	ExtraParams map[string]string

	// ClientID is the per-user OAuth client id (read from
	// ClientStore at flow start).
	ClientID string

	// ClientSecret is optional. PKCE-only desktop flows leave it
	// empty.
	ClientSecret string
}

// Orchestrator drives the OAuth flow. One per runtime daemon.
type Orchestrator struct {
	creds  CredentialStore
	logger *slog.Logger

	// HTTPClient is the client used for token endpoint POSTs.
	// Overridable for tests via NewOrchestratorWithClient.
	httpClient *http.Client

	// BrowserOpener can be overridden in tests to avoid actually
	// launching a browser.
	browserOpener func(url string) error

	mu    sync.Mutex
	flows map[string]*flowState
}

// flowState is the in-memory record of one in-flight authorization.
// Lives only until the callback arrives (or the listener times out).
type flowState struct {
	capsuleName  string
	provider     ProviderConfig
	state        string
	pkceVerifier string
	listener     net.Listener
	server       *http.Server
	completed    chan callbackResult
	createdAt    time.Time
}

// callbackResult is the signal channel value; we don't transmit
// the actual code/error through here (those are handled in
// handleCallback synchronously). The channel is just a "flow has
// concluded" signal for the watchdog.
type callbackResult struct{}

// NewOrchestrator constructs an Orchestrator with a default
// 30-second HTTP timeout for token endpoint POSTs and the system
// browser-open as the launcher.
func NewOrchestrator(creds CredentialStore, logger *slog.Logger) *Orchestrator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Orchestrator{
		creds:         creds,
		logger:        logger,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		browserOpener: openBrowser,
		flows:         make(map[string]*flowState),
	}
}

// SetHTTPClient swaps the HTTP client used for token endpoint
// requests. Used by tests to point at an httptest.Server.
func (o *Orchestrator) SetHTTPClient(c *http.Client) { o.httpClient = c }

// SetBrowserOpener swaps the browser launcher. Used by tests to
// capture the URL instead of opening it.
func (o *Orchestrator) SetBrowserOpener(f func(url string) error) { o.browserOpener = f }

// BeginResult is what Begin returns to the caller (typically the
// /console/oauth/begin HTTP handler).
type BeginResult struct {
	// FlowID is the opaque handle for status polling.
	FlowID string

	// AuthURL is the URL the user is sent to. Begin auto-opens
	// the browser, but the caller may want to surface the URL
	// (e.g. for headless servers where browser-open isn't
	// possible).
	AuthURL string

	// RedirectURI is the ephemeral loopback URL the runtime will
	// receive the callback on. Useful for the user when they're
	// pre-registering the URL in some providers' consoles.
	RedirectURI string
}

// Begin starts an OAuth flow for a capsule. Steps:
//
//  1. Allocate an ephemeral 127.0.0.1 port.
//  2. Generate PKCE verifier+challenge + a CSRF state value.
//  3. Build the authorization URL.
//  4. Spawn the callback listener on a goroutine.
//  5. Open the user's browser.
//  6. Return the flow_id + URL to the caller.
//
// The flow completes asynchronously when the user finishes in the
// browser. Status() reports the current state.
//
// The returned URL contains the user's client_id and the
// scope/state/PKCE-challenge — do not log it verbatim.
func (o *Orchestrator) Begin(
	_ context.Context, capsuleName string, p ProviderConfig,
) (BeginResult, error) {
	if capsuleName == "" {
		return BeginResult{}, errors.New("oauth: Begin requires capsule name")
	}
	if p.AuthorizationEndpoint == "" || p.TokenEndpoint == "" {
		return BeginResult{}, errors.New("oauth: provider config missing endpoints")
	}
	if p.ClientID == "" {
		return BeginResult{}, errors.New("oauth: provider config missing client_id")
	}

	// Allocate the loopback port. Listening on 127.0.0.1:0 gets us
	// a kernel-assigned ephemeral port; we read it back from
	// listener.Addr().
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return BeginResult{}, fmt.Errorf("oauth: opening loopback listener: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	flowID, err := randomID("flw_", 16)
	if err != nil {
		_ = listener.Close()
		return BeginResult{}, err
	}
	state, err := randomID("st_", 16)
	if err != nil {
		_ = listener.Close()
		return BeginResult{}, err
	}
	verifier, challenge, err := generatePKCE()
	if err != nil {
		_ = listener.Close()
		return BeginResult{}, err
	}

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/oauth/callback/%s", port, flowID)
	authURL := buildAuthURL(p, redirectURI, state, challenge)

	fs := &flowState{
		capsuleName:  capsuleName,
		provider:     p,
		state:        state,
		pkceVerifier: verifier,
		listener:     listener,
		completed:    make(chan callbackResult, 1),
		createdAt:    time.Now().UTC(),
	}

	mux := http.NewServeMux()
	// Specific flow path; rejects anything else with 404.
	flowIDCaptured := flowID
	mux.HandleFunc("/oauth/callback/"+flowID, func(w http.ResponseWriter, r *http.Request) {
		o.handleCallback(w, r, fs, flowIDCaptured)
	})
	fs.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	o.mu.Lock()
	o.flows[flowID] = fs
	o.mu.Unlock()

	// Serve the callback listener. The Goroutine ends when the
	// listener closes (Shutdown in handleCallback) OR the
	// flow times out.
	go func() {
		_ = fs.server.Serve(listener)
	}()

	// Watchdog: if the flow doesn't complete within 10 minutes,
	// tear it down. Otherwise listeners pile up.
	go func() {
		select {
		case <-time.After(10 * time.Minute):
			o.tearDown(flowID, fmt.Errorf("oauth: flow expired without callback"))
		case <-fs.completed:
			// handled in handleCallback path
		}
	}()

	if err := o.browserOpener(authURL); err != nil {
		// Browser-open failure isn't fatal — the user may be on a
		// headless setup. They can open the URL manually from the
		// dashboard / API response.
		o.logger.Info("oauth: browser open failed; user must open URL manually",
			"capsule", capsuleName, "err", err)
	}

	o.logger.Info("oauth: flow started",
		"capsule", capsuleName, "provider", p.Name,
		"flow_id", flowID, "port", port)

	return BeginResult{
		FlowID:      flowID,
		AuthURL:     authURL,
		RedirectURI: redirectURI,
	}, nil
}

// Status reports the current state of a flow.
type Status struct {
	State string `json:"state"` // "pending" | "complete" | "error" | "expired" | "unknown"
	Error string `json:"error,omitempty"`
}

// Status returns whether a flow_id is still active, completed
// (tokens stored), or expired/gone. Used by the dashboard's
// polling.
func (o *Orchestrator) Status(flowID string) Status {
	o.mu.Lock()
	_, ok := o.flows[flowID]
	o.mu.Unlock()
	if ok {
		return Status{State: "pending"}
	}
	// Not in flight — either completed (and torn down) or never existed.
	// We don't keep a completion log right now; the per-capsule
	// credential store is the source of truth ("does this capsule have
	// a refresh_token?"). The console layer should consult that.
	return Status{State: "unknown"}
}

// handleCallback is the in-flow HTTP handler. Validates state,
// posts code+verifier to the token endpoint, stores tokens.
func (o *Orchestrator) handleCallback(w http.ResponseWriter, r *http.Request, fs *flowState, flowID string) {
	q := r.URL.Query()
	gotState := q.Get("state")
	code := q.Get("code")
	errCode := q.Get("error")

	if errCode != "" {
		// Provider returned an error (user denied, etc.).
		writeCallbackPage(w, false, "Provider returned error: "+errCode)
		o.completeWithError(fs, fmt.Errorf("provider error: %s", errCode))
		return
	}
	if gotState != fs.state {
		writeCallbackPage(w, false, "State mismatch — request rejected.")
		o.completeWithError(fs, ErrStateMismatch)
		return
	}
	if code == "" {
		writeCallbackPage(w, false, "Missing authorization code.")
		o.completeWithError(fs, errors.New("oauth: callback missing code"))
		return
	}

	tokens, err := o.exchangeCode(r.Context(), fs, code, flowID)
	if err != nil {
		writeCallbackPage(w, false, "Token exchange failed: "+err.Error())
		o.completeWithError(fs, err)
		return
	}

	if err := o.persistTokens(r.Context(), fs.capsuleName, tokens); err != nil {
		writeCallbackPage(w, false, "Token persistence failed: "+err.Error())
		o.completeWithError(fs, err)
		return
	}

	writeCallbackPage(w, true, "")
	o.completeSuccess(fs)
}

// tokenResponse is what the provider's /token endpoint returns. We
// take a permissive view — some fields are optional or omitted by
// some providers.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (o *Orchestrator) exchangeCode(
	ctx context.Context, fs *flowState, code, flowID string,
) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", fs.provider.ClientID)
	form.Set("redirect_uri", fmt.Sprintf(
		"http://127.0.0.1:%d/oauth/callback/%s",
		fs.listener.Addr().(*net.TCPAddr).Port, flowID))
	form.Set("code_verifier", fs.pkceVerifier)
	if fs.provider.ClientSecret != "" {
		form.Set("client_secret", fs.provider.ClientSecret)
	}
	return o.postTokenEndpoint(ctx, fs.provider, form)
}

func (o *Orchestrator) postTokenEndpoint(
	ctx context.Context, p ProviderConfig, form url.Values,
) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: POSTing to %s: %w", p.TokenEndpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oauth: reading token response: %w", err)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		// Some providers (notably GitHub) default to
		// application/x-www-form-urlencoded responses unless
		// Accept: application/json is honored. We sent Accept,
		// but if we got a form-encoded body back try parsing it.
		if formBody, ferr := url.ParseQuery(string(body)); ferr == nil && formBody.Get("access_token") != "" {
			tr.AccessToken = formBody.Get("access_token")
			tr.TokenType = formBody.Get("token_type")
			tr.RefreshToken = formBody.Get("refresh_token")
			tr.Scope = formBody.Get("scope")
		} else {
			return nil, fmt.Errorf("oauth: decoding token response: %w (body: %s)", err, truncate(string(body), 200))
		}
	}

	if resp.StatusCode >= 400 {
		msg := tr.Error
		if tr.ErrorDescription != "" {
			msg += ": " + tr.ErrorDescription
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("oauth: token endpoint error: %s", msg)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("oauth: token endpoint returned no access_token")
	}
	return &tr, nil
}

// persistTokens writes the resulting token set to the capsule's
// credential store via the existing CapsuleCredentialStore. Keys
// use the credKey* constants.
func (o *Orchestrator) persistTokens(
	ctx context.Context, capsuleName string, tr *tokenResponse,
) error {
	var expiresAt *time.Time
	if tr.ExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(tr.ExpiresIn) * time.Second)
		expiresAt = &t
	}
	if err := o.creds.Set(ctx, capsuleName, credKeyAccessToken, tr.AccessToken, expiresAt); err != nil {
		return fmt.Errorf("oauth: storing access_token: %w", err)
	}
	if tr.RefreshToken != "" {
		// Refresh tokens are long-lived; we don't put an expires_at
		// on them. If the provider rotates them, we replace via
		// the next refresh.
		if err := o.creds.Set(ctx, capsuleName, credKeyRefreshToken, tr.RefreshToken, nil); err != nil {
			return fmt.Errorf("oauth: storing refresh_token: %w", err)
		}
	}
	if tr.Scope != "" {
		if err := o.creds.Set(ctx, capsuleName, credKeyScope, tr.Scope, nil); err != nil {
			return fmt.Errorf("oauth: storing scope: %w", err)
		}
	}
	if tr.TokenType != "" {
		if err := o.creds.Set(ctx, capsuleName, credKeyTokenType, tr.TokenType, nil); err != nil {
			return fmt.Errorf("oauth: storing token_type: %w", err)
		}
	}
	return nil
}

// AccessToken returns a valid bearer for the named capsule. Looks
// up the cached access_token, refreshes via the refresh_token if
// the cache is stale or empty, and persists the rotated tokens.
//
// p carries the provider config (endpoints + client_id). The
// caller is the MCP tool handler, which already has the manifest
// + ClientStore lookup in hand.
//
// Returns ErrReauthRequired when the refresh path fails — the
// caller should surface a re-auth prompt to the user via the
// dashboard's approvals queue.
func (o *Orchestrator) AccessToken(
	ctx context.Context, capsuleName string, p ProviderConfig,
) (string, *time.Time, error) {
	// 1. Cache hit?
	if entry, found, err := o.creds.Get(ctx, capsuleName, credKeyAccessToken); err != nil {
		return "", nil, err
	} else if found {
		// Need 60s headroom — if the token expires in <60s, refresh
		// anyway so callers don't hit "token expired" mid-request.
		if entry.ExpiresAt == nil || entry.ExpiresAt.After(time.Now().Add(60*time.Second)) {
			return entry.Value, entry.ExpiresAt, nil
		}
	}

	// 2. Refresh path.
	refreshEntry, hasRefresh, err := o.creds.Get(ctx, capsuleName, credKeyRefreshToken)
	if err != nil {
		return "", nil, err
	}
	if !hasRefresh {
		return "", nil, ErrNoRefreshToken
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshEntry.Value)
	form.Set("client_id", p.ClientID)
	if p.ClientSecret != "" {
		form.Set("client_secret", p.ClientSecret)
	}
	tr, err := o.postTokenEndpoint(ctx, p, form)
	if err != nil {
		// Map invalid_grant / revoked → ErrReauthRequired so the
		// dashboard can surface a re-auth chip.
		if isReauthError(err) {
			return "", nil, fmt.Errorf("%w: %v", ErrReauthRequired, err)
		}
		return "", nil, err
	}

	// Some providers (notably Google) don't return a new
	// refresh_token on every refresh — keep the old one if the
	// response omitted it.
	stashed := *tr
	if stashed.RefreshToken == "" {
		stashed.RefreshToken = refreshEntry.Value
	}
	if err := o.persistTokens(ctx, capsuleName, &stashed); err != nil {
		return "", nil, err
	}

	var expiresAt *time.Time
	if tr.ExpiresIn > 0 {
		t := time.Now().UTC().Add(time.Duration(tr.ExpiresIn) * time.Second)
		expiresAt = &t
	}
	return tr.AccessToken, expiresAt, nil
}

// completeSuccess marks a flow done and shuts the listener down.
func (o *Orchestrator) completeSuccess(fs *flowState) {
	o.mu.Lock()
	for id, candidate := range o.flows {
		if candidate == fs {
			delete(o.flows, id)
			break
		}
	}
	o.mu.Unlock()

	// Drain the watchdog goroutine.
	select {
	case fs.completed <- callbackResult{}:
	default:
	}
	go func() {
		// Give the writeCallbackPage response time to flush
		// before shutting down.
		time.Sleep(500 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = fs.server.Shutdown(ctx)
	}()
}

// completeWithError marks a flow failed.
func (o *Orchestrator) completeWithError(fs *flowState, err error) {
	o.logger.Warn("oauth: flow failed", "capsule", fs.capsuleName, "err", err)
	o.completeSuccess(fs) // same teardown path; the error already wrote the page
}

// tearDown forcefully closes a flow's listener. Used by the
// watchdog when no callback arrived.
func (o *Orchestrator) tearDown(flowID string, reason error) {
	o.mu.Lock()
	fs, ok := o.flows[flowID]
	if ok {
		delete(o.flows, flowID)
	}
	o.mu.Unlock()
	if !ok {
		return
	}
	o.logger.Warn("oauth: flow torn down", "flow_id", flowID, "reason", reason)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_ = fs.server.Shutdown(ctx)
}

// --- helpers ---------------------------------------------------------

func generatePKCE() (verifier, challenge string, err error) {
	// 32 random bytes → 43 chars base64url.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("oauth: generating PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomID(prefix string, byteLen int) (string, error) {
	raw := make([]byte, byteLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("oauth: generating id: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func buildAuthURL(p ProviderConfig, redirectURI, state, challenge string) string {
	u, _ := url.Parse(p.AuthorizationEndpoint)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if len(p.Scopes) > 0 {
		q.Set("scope", strings.Join(p.Scopes, " "))
	}
	for k, v := range p.ExtraParams {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// isReauthError matches token-endpoint failures that mean "the
// user has to re-authenticate." Providers express this with the
// invalid_grant error code (OAuth 2.0 RFC 6749 §5.2).
func isReauthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "invalid_token") ||
		strings.Contains(msg, "token_expired")
}

func writeCallbackPage(w http.ResponseWriter, ok bool, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ok {
		_, _ = fmt.Fprint(w, callbackPageOK)
	} else {
		_, _ = fmt.Fprintf(w, callbackPageErr, errMsg)
	}
}

const callbackPageOK = `<!doctype html>
<html><head><meta charset="utf-8"><title>Connected</title>
<style>body{font-family:system-ui,-apple-system,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#f7f7f5}
.card{background:white;padding:2rem 3rem;border-radius:12px;box-shadow:0 1px 3px rgba(0,0,0,0.1);text-align:center;max-width:24rem}
h1{font-size:1.25rem;margin:0 0 0.5rem;color:#0a0}
p{margin:0;color:#666}</style>
</head><body><div class="card">
<h1>✓ Connected</h1>
<p>You can close this tab and return to Loamss.</p>
</div></body></html>`

const callbackPageErr = `<!doctype html>
<html><head><meta charset="utf-8"><title>Authentication failed</title>
<style>body{font-family:system-ui,-apple-system,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#f7f7f5}
.card{background:white;padding:2rem 3rem;border-radius:12px;box-shadow:0 1px 3px rgba(0,0,0,0.1);text-align:center;max-width:30rem}
h1{font-size:1.25rem;margin:0 0 0.5rem;color:#a00}
p{margin:0;color:#666;word-break:break-word}</style>
</head><body><div class="card">
<h1>Authentication failed</h1>
<p>%s</p>
</div></body></html>`

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
