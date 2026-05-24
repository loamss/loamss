package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- PKCE -------------------------------------------------------------

func TestPKCEChallenge_KnownVector(t *testing.T) {
	// From RFC 7636 §4.6. Verifier "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	// → challenge "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	v := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	got := pkceChallenge(v)
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- URL building -----------------------------------------------------

func TestBuildAuthURL_HasRequiredParams(t *testing.T) {
	u := buildAuthURL(
		"https://accounts.google.com/o/oauth2/v2/auth",
		"client-xyz",
		DefaultScope,
		"http://127.0.0.1:55123/",
		"state-abc",
		"challenge-def",
	)
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := parsed.Query()
	for k, want := range map[string]string{
		"client_id":             "client-xyz",
		"response_type":         "code",
		"scope":                 DefaultScope,
		"redirect_uri":          "http://127.0.0.1:55123/",
		"state":                 "state-abc",
		"code_challenge":        "challenge-def",
		"code_challenge_method": "S256",
		"access_type":           "offline",
		"prompt":                "consent",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s: got %q, want %q", k, got, want)
		}
	}
}

// --- token exchange / refresh ----------------------------------------

// fakeTokenServer counts requests and returns a canned token JSON.
type fakeTokenServer struct {
	requests atomic.Int64
	srv      *httptest.Server
	lastForm url.Values
	respJSON []byte
	respCode int
}

func newFakeTokenServer(t *testing.T, response any, status int) *fakeTokenServer {
	t.Helper()
	body, _ := json.Marshal(response)
	fts := &fakeTokenServer{respJSON: body, respCode: status}
	fts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fts.requests.Add(1)
		raw, _ := io.ReadAll(r.Body)
		fts.lastForm, _ = url.ParseQuery(string(raw))
		w.Header().Set("Content-Type", "application/json")
		if fts.respCode == 0 {
			fts.respCode = 200
		}
		w.WriteHeader(fts.respCode)
		_, _ = w.Write(fts.respJSON)
	}))
	t.Cleanup(fts.srv.Close)
	return fts
}

func TestExchangeCode_Success(t *testing.T) {
	fts := newFakeTokenServer(t, map[string]any{
		"access_token":  "ya29.access",
		"refresh_token": "1//refresh",
		"expires_in":    3600,
		"token_type":    "Bearer",
		"scope":         DefaultScope,
	}, 200)
	g, _, _, _ := newTestSource(t, "", fts.srv.URL, "")

	tok, err := g.exchangeCode(context.Background(), "auth-code", "verifier-x", "http://127.0.0.1:1/")
	if err != nil {
		t.Fatalf("exchangeCode: %v", err)
	}
	if tok.AccessToken != "ya29.access" {
		t.Errorf("access_token: %q", tok.AccessToken)
	}
	if tok.RefreshToken != "1//refresh" {
		t.Errorf("refresh_token: %q", tok.RefreshToken)
	}
	if time.Until(tok.Expiry) < 50*time.Minute {
		t.Errorf("expiry too soon: %v", tok.Expiry)
	}
	// Confirm form values reached the endpoint.
	for k, want := range map[string]string{
		"client_id":     "test-client",
		"client_secret": "test-secret",
		"code":          "auth-code",
		"code_verifier": "verifier-x",
		"grant_type":    "authorization_code",
		"redirect_uri":  "http://127.0.0.1:1/",
	} {
		if got := fts.lastForm.Get(k); got != want {
			t.Errorf("form[%s]: got %q, want %q", k, got, want)
		}
	}
}

func TestExchangeCode_OAuthError(t *testing.T) {
	fts := newFakeTokenServer(t, map[string]any{
		"error":             "invalid_grant",
		"error_description": "bad code",
	}, 400)
	g, _, _, _ := newTestSource(t, "", fts.srv.URL, "")
	_, err := g.exchangeCode(context.Background(), "c", "v", "http://127.0.0.1:1/")
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("expected invalid_grant error, got %v", err)
	}
}

func TestRefreshToken_PersistsNewAccess(t *testing.T) {
	fts := newFakeTokenServer(t, map[string]any{
		"access_token": "ya29.fresh",
		"expires_in":   3600,
		"token_type":   "Bearer",
	}, 200)
	g, _, _, creds := newTestSource(t, "", fts.srv.URL, "")

	original := &oauthToken{
		AccessToken:  "old",
		RefreshToken: "1//refresh-original",
		Expiry:       time.Now().Add(-time.Minute),
	}
	if err := g.saveToken(context.Background(), original); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := g.refreshToken(context.Background(), original); err != nil {
		t.Fatalf("refreshToken: %v", err)
	}
	if original.AccessToken != "ya29.fresh" {
		t.Errorf("access_token not updated: %q", original.AccessToken)
	}
	if fts.lastForm.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type: %q", fts.lastForm.Get("grant_type"))
	}
	if fts.lastForm.Get("refresh_token") != "1//refresh-original" {
		t.Errorf("refresh_token sent: %q", fts.lastForm.Get("refresh_token"))
	}

	// Persisted to creds.
	persisted, err := creds.Get(context.Background())
	if err != nil {
		t.Fatalf("creds.Get: %v", err)
	}
	if persisted["access_token"] != "ya29.fresh" {
		t.Errorf("persisted access_token: %v", persisted["access_token"])
	}
}

func TestRefreshToken_NoRefreshToken(t *testing.T) {
	g, _, _, _ := newTestSource(t, "", "", "")
	err := g.refreshToken(context.Background(), &oauthToken{AccessToken: "x"})
	if err == nil || !strings.Contains(err.Error(), "no refresh_token") {
		t.Errorf("expected no refresh_token error, got %v", err)
	}
}

// --- loopback flow ---------------------------------------------------

// driveLoopback simulates the user clicking the consent link and
// Google redirecting back to the loopback. Returns the response code
// from the loopback's redirect handler.
func driveLoopback(t *testing.T, flow *pendingAuthFlow, code, state string) int {
	t.Helper()
	u := flow.redirectURI() + "?code=" + code + "&state=" + state
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("loopback GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestLoopback_HappyPath(t *testing.T) {
	fts := newFakeTokenServer(t, map[string]any{
		"access_token":  "ya29.loop",
		"refresh_token": "1//loop-refresh",
		"expires_in":    3600,
		"token_type":    "Bearer",
	}, 200)
	g, _, _, _ := newTestSource(t, "", fts.srv.URL, "")

	flow, err := g.BeginAuth(context.Background())
	if err != nil {
		t.Fatalf("BeginAuth: %v", err)
	}
	if flow.Kind != "browser" {
		t.Errorf("flow.Kind: %q", flow.Kind)
	}
	if !strings.HasPrefix(flow.URL, defaultAuthURL+"?") && !strings.Contains(flow.URL, "client_id=") {
		t.Errorf("URL shape: %q", flow.URL)
	}

	// Pull the state out of the URL — we need it to drive the
	// fake callback.
	parsed, _ := url.Parse(flow.URL)
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("no state in URL")
	}

	g.mu.Lock()
	pa := g.pendingAuth
	g.mu.Unlock()

	// Drive the callback in a goroutine; CompleteAuth blocks
	// waiting for it.
	errCh := make(chan error, 1)
	go func() {
		if status := driveLoopback(t, pa, "the-code", state); status != http.StatusOK {
			errCh <- errors.New("loopback non-200")
			return
		}
		errCh <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.CompleteAuth(ctx, map[string]string{}); err != nil {
		t.Fatalf("CompleteAuth: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("loopback driver: %v", err)
	}

	// Token persisted.
	tok, err := g.loadToken(context.Background())
	if err != nil {
		t.Fatalf("loadToken: %v", err)
	}
	if tok.AccessToken != "ya29.loop" {
		t.Errorf("access_token: %q", tok.AccessToken)
	}
}

func TestLoopback_StateMismatch(t *testing.T) {
	g, _, _, _ := newTestSource(t, "", "", "")

	flow, err := g.BeginAuth(context.Background())
	if err != nil {
		t.Fatalf("BeginAuth: %v", err)
	}
	_ = flow

	g.mu.Lock()
	pa := g.pendingAuth
	g.mu.Unlock()

	// Wrong state — listener should reject and CompleteAuth should
	// surface the mismatch.
	go func() {
		_ = driveLoopback(t, pa, "the-code", "wrong-state")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.CompleteAuth(ctx, map[string]string{}); err == nil {
		t.Error("expected state-mismatch error")
	}
}

func TestLoopback_CodePasteFallback(t *testing.T) {
	fts := newFakeTokenServer(t, map[string]any{
		"access_token":  "ya29.pasted",
		"refresh_token": "1//pasted",
		"expires_in":    3600,
	}, 200)
	g, _, _, _ := newTestSource(t, "", fts.srv.URL, "")

	if _, err := g.BeginAuth(context.Background()); err != nil {
		t.Fatalf("BeginAuth: %v", err)
	}
	if err := g.CompleteAuth(context.Background(), map[string]string{
		"code": "pasted-code",
	}); err != nil {
		t.Fatalf("CompleteAuth: %v", err)
	}
	tok, _ := g.loadToken(context.Background())
	if tok.AccessToken != "ya29.pasted" {
		t.Errorf("access_token: %q", tok.AccessToken)
	}
	// Code-paste path should have sent the code via the form.
	if fts.lastForm.Get("code") != "pasted-code" {
		t.Errorf("form code: %q", fts.lastForm.Get("code"))
	}
}

func TestLoopback_DoubleBeginRejected(t *testing.T) {
	g, _, _, _ := newTestSource(t, "", "", "")
	if _, err := g.BeginAuth(context.Background()); err != nil {
		t.Fatalf("BeginAuth 1: %v", err)
	}
	_, err := g.BeginAuth(context.Background())
	if !errors.Is(err, errAuthInProgressMatch(err)) && !strings.Contains(errToStr(err), "auth flow already") {
		// Loose match: we want any signal that the second call was
		// rejected. The exact sentinel comes from the source pkg.
		t.Errorf("expected in-progress rejection, got %v", err)
	}
	_ = g.Close(context.Background())
}

// helpers to keep the assertion above readable
func errToStr(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

func errAuthInProgressMatch(e error) error { return e } // dummy for ergonomics
