package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// memCreds is a lock-protected in-memory CredentialStore for tests.
type memCreds struct {
	mu      sync.Mutex
	entries map[string]CredentialEntry // keyed by "<capsule>:<key>"
}

func newMemCreds() *memCreds {
	return &memCreds{entries: map[string]CredentialEntry{}}
}

func (m *memCreds) Set(_ context.Context, capsule, key, value string, expiresAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[capsule+":"+key] = CredentialEntry{Value: value, ExpiresAt: expiresAt}
	return nil
}

func (m *memCreds) Get(_ context.Context, capsule, key string) (CredentialEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[capsule+":"+key]
	if !ok {
		return CredentialEntry{}, false, nil
	}
	return e, true, nil
}

// --- providers ---------------------------------------------------

func TestProviders_GoogleAndGitHubInRegistry(t *testing.T) {
	for _, name := range []string{"google", "github"} {
		p, err := Lookup(name)
		if err != nil {
			t.Errorf("Lookup(%q): %v", name, err)
			continue
		}
		if !strings.HasPrefix(p.AuthorizationEndpoint, "https://") {
			t.Errorf("%s authorization_endpoint not https: %q", name, p.AuthorizationEndpoint)
		}
		if !strings.HasPrefix(p.TokenEndpoint, "https://") {
			t.Errorf("%s token_endpoint not https: %q", name, p.TokenEndpoint)
		}
	}
}

func TestProviders_UnknownReturnsErr(t *testing.T) {
	_, err := Lookup("not-a-thing")
	if !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("expected ErrUnknownProvider, got %v", err)
	}
}

// --- buildAuthURL ------------------------------------------------

func TestBuildAuthURL_IncludesPKCEAndState(t *testing.T) {
	p := ProviderConfig{
		AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
		ClientID:              "client-abc",
		Scopes:                []string{"a", "b"},
		ExtraParams:           map[string]string{"access_type": "offline"},
	}
	out := buildAuthURL(p, "http://127.0.0.1:1234/cb", "state-xyz", "challenge-xyz")
	u, err := url.Parse(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	for k, v := range map[string]string{
		"response_type":         "code",
		"client_id":             "client-abc",
		"redirect_uri":          "http://127.0.0.1:1234/cb",
		"state":                 "state-xyz",
		"code_challenge":        "challenge-xyz",
		"code_challenge_method": "S256",
		"scope":                 "a b",
		"access_type":           "offline",
	} {
		if got := q.Get(k); got != v {
			t.Errorf("%s: got %q, want %q", k, got, v)
		}
	}
}

// --- end-to-end flow ---------------------------------------------

// fakeProvider stands up an httptest.Server that pretends to be an
// OAuth token endpoint. Returns a fresh access+refresh on /token.
func newFakeProvider(t *testing.T, accessToken, refreshToken string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") == "" || r.Form.Get("code_verifier") == "" {
				http.Error(w, "missing code/verifier", http.StatusBadRequest)
				return
			}
		case "refresh_token":
			if r.Form.Get("refresh_token") == "" {
				http.Error(w, "missing refresh_token", http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "bad grant_type", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  accessToken,
			"refresh_token": refreshToken,
			"expires_in":    3600,
			"token_type":    "Bearer",
			"scope":         "calendar.readonly",
		})
	})
	return httptest.NewServer(mux)
}

func TestOrchestrator_Begin_OpensListenerAndCompletesFlow(t *testing.T) {
	fake := newFakeProvider(t, "AT-001", "RT-001")
	defer fake.Close()

	creds := newMemCreds()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := NewOrchestrator(creds, logger)
	// Capture the auth URL instead of opening a browser.
	openedURL := make(chan string, 1)
	orch.SetBrowserOpener(func(u string) error {
		openedURL <- u
		return nil
	})

	provider := ProviderConfig{
		Name:                  "fake",
		AuthorizationEndpoint: fake.URL + "/authorize", // unused; we just need the runtime not to crash here
		TokenEndpoint:         fake.URL + "/token",
		Scopes:                []string{"calendar.readonly"},
		ClientID:              "client-xyz",
	}

	br, err := orch.Begin(context.Background(), "calendar-ingestor", provider)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if br.FlowID == "" || !strings.HasPrefix(br.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("unexpected BeginResult: %+v", br)
	}

	// Read the auth URL the runtime tried to open.
	select {
	case <-openedURL:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("browserOpener never called")
	}

	// Simulate the user completing the flow: GET the redirect URI
	// with a code + matching state.
	parsed, _ := url.Parse(br.AuthURL)
	state := parsed.Query().Get("state")
	callbackURL := br.RedirectURI + "?code=AUTHCODE&state=" + state
	resp, err := http.Get(callbackURL) //nolint:gosec,noctx // test
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status %d: %s", resp.StatusCode, body)
	}

	// Tokens should now be in the credential store under the capsule.
	// The callback handler runs synchronously before the response —
	// no race here.
	entry, found, _ := creds.Get(context.Background(), "calendar-ingestor", credKeyAccessToken)
	if !found || entry.Value != "AT-001" {
		t.Errorf("access_token: found=%v value=%q", found, entry.Value)
	}
	entry, found, _ = creds.Get(context.Background(), "calendar-ingestor", credKeyRefreshToken)
	if !found || entry.Value != "RT-001" {
		t.Errorf("refresh_token: found=%v value=%q", found, entry.Value)
	}
}

func TestOrchestrator_StateMismatchRejectsCallback(t *testing.T) {
	fake := newFakeProvider(t, "AT", "RT")
	defer fake.Close()

	creds := newMemCreds()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := NewOrchestrator(creds, logger)
	orch.SetBrowserOpener(func(string) error { return nil })

	provider := ProviderConfig{
		AuthorizationEndpoint: fake.URL + "/authorize",
		TokenEndpoint:         fake.URL + "/token",
		Scopes:                []string{"a"},
		ClientID:              "client",
	}
	br, _ := orch.Begin(context.Background(), "cap", provider)

	// Hit the callback with a wrong state.
	resp, err := http.Get(br.RedirectURI + "?code=AUTHCODE&state=WRONG") //nolint:gosec,noctx // test
	if err != nil {
		t.Fatalf("callback GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "State mismatch") {
		t.Errorf("expected state-mismatch page, got: %s", body)
	}

	// No tokens stored.
	_, found, _ := creds.Get(context.Background(), "cap", credKeyAccessToken)
	if found {
		t.Error("access_token should NOT be stored after state mismatch")
	}
}

// --- AccessToken ----------------------------------------------------

func TestAccessToken_CachedTokenReturned(t *testing.T) {
	creds := newMemCreds()
	future := time.Now().Add(time.Hour)
	_ = creds.Set(context.Background(), "cap", credKeyAccessToken, "CACHED", &future)
	orch := NewOrchestrator(creds, slog.New(slog.NewTextHandler(io.Discard, nil)))

	token, _, err := orch.AccessToken(context.Background(), "cap", ProviderConfig{
		TokenEndpoint: "http://unused", ClientID: "x",
	})
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if token != "CACHED" {
		t.Errorf("token: %q", token)
	}
}

func TestAccessToken_RefreshesWhenExpired(t *testing.T) {
	fake := newFakeProvider(t, "AT-FRESH", "RT-NEW")
	defer fake.Close()

	creds := newMemCreds()
	past := time.Now().Add(-time.Minute)
	_ = creds.Set(context.Background(), "cap", credKeyAccessToken, "AT-OLD", &past)
	_ = creds.Set(context.Background(), "cap", credKeyRefreshToken, "RT-OLD", nil)

	orch := NewOrchestrator(creds, slog.New(slog.NewTextHandler(io.Discard, nil)))
	token, _, err := orch.AccessToken(context.Background(), "cap", ProviderConfig{
		TokenEndpoint: fake.URL + "/token", ClientID: "x",
	})
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if token != "AT-FRESH" {
		t.Errorf("token: %q", token)
	}
	// Refresh token rotated.
	rt, _, _ := creds.Get(context.Background(), "cap", credKeyRefreshToken)
	if rt.Value != "RT-NEW" {
		t.Errorf("refresh rotated to %q, want RT-NEW", rt.Value)
	}
}

func TestAccessToken_NoRefreshTokenReturnsErr(t *testing.T) {
	creds := newMemCreds()
	orch := NewOrchestrator(creds, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, _, err := orch.AccessToken(context.Background(), "cap", ProviderConfig{
		TokenEndpoint: "http://unused", ClientID: "x",
	})
	if !errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("expected ErrNoRefreshToken, got %v", err)
	}
}

func TestAccessToken_InvalidGrantMapsToReauthRequired(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"invalid_grant","error_description":"token revoked"}`)
	})
	fake := httptest.NewServer(mux)
	defer fake.Close()

	creds := newMemCreds()
	_ = creds.Set(context.Background(), "cap", credKeyRefreshToken, "RT-BAD", nil)
	orch := NewOrchestrator(creds, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, _, err := orch.AccessToken(context.Background(), "cap", ProviderConfig{
		TokenEndpoint: fake.URL + "/token", ClientID: "x",
	})
	if !errors.Is(err, ErrReauthRequired) {
		t.Errorf("expected ErrReauthRequired, got %v", err)
	}
}

// --- ClientStore ----------------------------------------------------

func TestClientStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenClientStore(context.Background(), dir+"/runtime.db")
	if err != nil {
		t.Fatalf("OpenClientStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	in := ClientCredential{Provider: "google", ClientID: "abc.apps.googleusercontent.com"}
	if err := s.Set(context.Background(), in); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get(context.Background(), "google")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ClientID != in.ClientID {
		t.Errorf("client_id: %q", got.ClientID)
	}
	if got.ClientSecret != "" {
		t.Errorf("client_secret should be empty: %q", got.ClientSecret)
	}

	// Overwrite via Set.
	updated := ClientCredential{Provider: "google", ClientID: "new.apps.googleusercontent.com", ClientSecret: "sssh"}
	_ = s.Set(context.Background(), updated)
	got2, _ := s.Get(context.Background(), "google")
	if got2.ClientID != updated.ClientID || got2.ClientSecret != updated.ClientSecret {
		t.Errorf("after overwrite: %+v", got2)
	}
}

func TestClientStore_GetMissing(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenClientStore(context.Background(), dir+"/runtime.db")
	defer func() { _ = s.Close() }()
	_, err := s.Get(context.Background(), "ghost")
	if !errors.Is(err, ErrClientNotFound) {
		t.Errorf("expected ErrClientNotFound, got %v", err)
	}
}

func TestClientStore_ListRedactsSecret(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenClientStore(context.Background(), dir+"/runtime.db")
	defer func() { _ = s.Close() }()

	_ = s.Set(context.Background(), ClientCredential{
		Provider: "google", ClientID: "abc", ClientSecret: "secret",
	})
	rows, err := s.List(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("List: rows=%d err=%v", len(rows), err)
	}
	if rows[0].ClientSecret != "(set)" {
		t.Errorf("client_secret should be redacted, got %q", rows[0].ClientSecret)
	}
}
