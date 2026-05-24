package gmail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// pendingAuthFlow holds the in-flight OAuth handshake state. One
// instance per active authentication; cleared after CompleteAuth.
type pendingAuthFlow struct {
	authURL  string
	state    string
	verifier string

	port int

	srv       *http.Server
	listener  net.Listener
	codeCh    chan loopbackResult
	closeOnce sync.Once
}

type loopbackResult struct {
	code string
	err  error
}

// startLoopback brings up a 127.0.0.1:0 listener, builds the auth URL
// (with PKCE), and returns the populated flow. The listener handles
// exactly one /callback request and then idles for shutdown.
func (g *gmailSource) startLoopback() (*pendingAuthFlow, error) {
	state, err := randomBase64URL(16)
	if err != nil {
		return nil, err
	}
	verifier, err := randomBase64URL(32) // 43 chars; within RFC 7636 [43,128]
	if err != nil {
		return nil, err
	}
	challenge := pkceChallenge(verifier)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen 127.0.0.1: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	flow := &pendingAuthFlow{
		state:    state,
		verifier: verifier,
		port:     port,
		listener: listener,
		codeCh:   make(chan loopbackResult, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", flow.handleCallback)
	flow.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		// Serve returns when listener is closed; ignore that
		// expected error.
		_ = flow.srv.Serve(listener)
	}()

	flow.authURL = buildAuthURL(g.authURL, g.clientID, g.scope, flow.redirectURI(), state, challenge)
	return flow, nil
}

func (f *pendingAuthFlow) redirectURI() string {
	return fmt.Sprintf("http://127.0.0.1:%d/", f.port)
}

// handleCallback is the loopback HTTP handler. Reads ?code= + ?state=
// from the redirect, sends the result down codeCh, and renders a
// "you can close this tab" page.
func (f *pendingAuthFlow) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	gotState := q.Get("state")
	code := q.Get("code")
	errCode := q.Get("error")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if errCode != "" {
		_, _ = fmt.Fprintf(w, callbackErrorHTML, errCode)
		select {
		case f.codeCh <- loopbackResult{err: fmt.Errorf("oauth error: %s", errCode)}:
		default:
		}
		return
	}
	if gotState != f.state {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		select {
		case f.codeCh <- loopbackResult{err: errors.New("state mismatch on callback")}:
		default:
		}
		return
	}
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		select {
		case f.codeCh <- loopbackResult{err: errors.New("missing code in callback")}:
		default:
		}
		return
	}
	_, _ = w.Write([]byte(callbackSuccessHTML))
	select {
	case f.codeCh <- loopbackResult{code: code}:
	default:
	}
}

// waitForCode blocks until the loopback delivers a code, ctx is
// canceled, or timeout elapses.
func (f *pendingAuthFlow) waitForCode(ctx context.Context, timeout time.Duration) (string, error) {
	select {
	case r := <-f.codeCh:
		return r.code, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(timeout):
		return "", errors.New("source:gmail: auth flow timed out waiting for browser callback")
	}
}

// shutdown closes the listener + HTTP server. Safe to call multiple
// times.
func (f *pendingAuthFlow) shutdown() {
	f.closeOnce.Do(func() {
		if f.srv != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = f.srv.Shutdown(shutdownCtx)
		}
		if f.listener != nil {
			_ = f.listener.Close()
		}
	})
}

// --- URL building -----------------------------------------------------

func buildAuthURL(base, clientID, scope, redirectURI, state, codeChallenge string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scope)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	// access_type=offline + prompt=consent ensures Google returns a
	// refresh_token on first consent. Without prompt=consent, a
	// previously-authorized user re-running the flow does NOT get a
	// new refresh token, which leaves Loamss stuck the next time the
	// access token expires.
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	return base + "?" + q.Encode()
}

// --- token exchange + refresh ----------------------------------------

func (g *gmailSource) exchangeCode(ctx context.Context, code, verifier, redirectURI string) (*oauthToken, error) {
	form := url.Values{}
	form.Set("client_id", g.clientID)
	form.Set("client_secret", g.clientSecret)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
	return g.postToken(ctx, form)
}

func (g *gmailSource) refreshToken(ctx context.Context, tok *oauthToken) error {
	if tok.RefreshToken == "" {
		return errors.New("source:gmail: no refresh_token available; user must re-authenticate")
	}
	form := url.Values{}
	form.Set("client_id", g.clientID)
	form.Set("client_secret", g.clientSecret)
	form.Set("refresh_token", tok.RefreshToken)
	form.Set("grant_type", "refresh_token")
	fresh, err := g.postToken(ctx, form)
	if err != nil {
		return err
	}
	tok.AccessToken = fresh.AccessToken
	if fresh.Expiry.After(tok.Expiry) {
		tok.Expiry = fresh.Expiry
	}
	if fresh.RefreshToken != "" {
		// Google may rotate the refresh token; keep whichever's
		// fresh.
		tok.RefreshToken = fresh.RefreshToken
	}
	return g.saveToken(ctx, tok)
}

// tokenExchangeResponse is the wire format of POST /token. We decode
// into this and translate to *oauthToken so the persisted shape stays
// stable even if Google adds fields.
type tokenExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

func (g *gmailSource) postToken(ctx context.Context, form url.Values) (*oauthToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var ter tokenExchangeResponse
	if err := json.Unmarshal(body, &ter); err != nil {
		return nil, fmt.Errorf("decoding token response (status %d): %w; body=%s",
			resp.StatusCode, err, snippet(body))
	}
	if ter.Error != "" {
		return nil, fmt.Errorf("oauth: %s — %s", ter.Error, ter.ErrorDesc)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, snippet(body))
	}
	if ter.AccessToken == "" {
		return nil, errors.New("token endpoint returned empty access_token")
	}
	expiry := time.Now().Add(time.Duration(ter.ExpiresIn) * time.Second)
	return &oauthToken{
		AccessToken:  ter.AccessToken,
		RefreshToken: ter.RefreshToken,
		TokenType:    ter.TokenType,
		Expiry:       expiry,
		Scope:        ter.Scope,
	}, nil
}

func snippet(b []byte) string {
	const snippetMax = 200
	if len(b) > snippetMax {
		return string(b[:snippetMax]) + "..."
	}
	return string(b)
}

// --- PKCE helpers ----------------------------------------------------

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomBase64URL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// --- rendered callback pages -----------------------------------------

const callbackSuccessHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Loamss — Gmail authorized</title>
<style>body{font-family:system-ui,sans-serif;max-width:540px;margin:80px auto;padding:0 24px;color:#111}</style>
</head><body>
<h1>You're authorized</h1>
<p>Loamss has captured the authorization code and is exchanging it for a refresh token now.</p>
<p>You can close this tab and return to your terminal — the CLI will pick up the result on its own.</p>
</body></html>
`

const callbackErrorHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Loamss — Gmail authorization failed</title>
<style>body{font-family:system-ui,sans-serif;max-width:540px;margin:80px auto;padding:0 24px;color:#111}</style>
</head><body>
<h1>Authorization failed</h1>
<p>Google returned an error: <code>%s</code></p>
<p>Switch back to your terminal — the CLI will surface the details.</p>
</body></html>
`
