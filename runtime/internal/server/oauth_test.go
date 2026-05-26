package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/oauth"
)

// fakeOAuthBeginner is a minimal OAuthBeginner that captures
// inputs and returns canned outputs. Optionally implements
// CapsuleAuthStateProbe so the status endpoint can be exercised.
type fakeOAuthBeginner struct {
	lastCapsule string
	result      oauth.BeginResult
	err         error

	hasToken map[string]bool // capsule name → connected?
}

func (f *fakeOAuthBeginner) BeginAuthFlow(_ context.Context, capsuleName string) (oauth.BeginResult, error) {
	f.lastCapsule = capsuleName
	return f.result, f.err
}

// fakeOAuthBeginnerWithProbe is the variant that implements the
// CapsuleAuthStateProbe interface so the status endpoint returns
// real values instead of the unwired-default false.
type fakeOAuthBeginnerWithProbe struct {
	*fakeOAuthBeginner
}

func (f *fakeOAuthBeginnerWithProbe) CapsuleHasOAuthToken(_ context.Context, capsule string) (bool, error) {
	if f.hasToken == nil {
		return false, nil
	}
	return f.hasToken[capsule], nil
}

func newOAuthFixture(t *testing.T, withBeginner OAuthBeginner) (*httptest.Server, func()) {
	t.Helper()
	dir := t.TempDir()
	store, err := oauth.OpenClientStore(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("OpenClientStore: %v", err)
	}
	srv := New(Options{
		OAuthClients:  store,
		OAuthBeginner: withBeginner,
	})
	ts := httptest.NewServer(srv.httpSrv.Handler)
	return ts, func() {
		ts.Close()
		_ = store.Close()
	}
}

// --- /console/oauth/clients tests -----------------------------------

func TestOAuthClients_SetGetDelete(t *testing.T) {
	ts, cleanup := newOAuthFixture(t, nil)
	defer cleanup()

	// Set.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/console/oauth/clients/google",
		strings.NewReader(`{"client_id":"abc.apps.googleusercontent.com"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set: status %d", resp.StatusCode)
	}

	// List.
	resp, err = http.Get(ts.URL + "/console/oauth/clients") //nolint:gosec,noctx // test
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var list struct {
		Clients []oauthClientResponse `json:"clients"`
	}
	_ = json.Unmarshal(body, &list)
	if len(list.Clients) != 1 || list.Clients[0].Provider != "google" {
		t.Errorf("list: %+v", list.Clients)
	}

	// Delete.
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/console/oauth/clients/google", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("delete: status %d", resp.StatusCode)
	}

	// List after delete.
	resp, _ = http.Get(ts.URL + "/console/oauth/clients") //nolint:gosec,noctx // test
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	_ = json.Unmarshal(body, &list)
	if len(list.Clients) != 0 {
		t.Errorf("expected 0 clients after delete, got %d", len(list.Clients))
	}
}

func TestOAuthClients_SetRequiresClientID(t *testing.T) {
	ts, cleanup := newOAuthFixture(t, nil)
	defer cleanup()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/console/oauth/clients/google",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing client_id, got %d", resp.StatusCode)
	}
}

// --- /console/oauth/providers ----------------------------------------

func TestOAuthProviders_ListsWellKnown(t *testing.T) {
	ts, cleanup := newOAuthFixture(t, nil)
	defer cleanup()
	resp, err := http.Get(ts.URL + "/console/oauth/providers") //nolint:gosec,noctx // test
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var got struct {
		Providers []oauthProviderInfo `json:"providers"`
	}
	_ = json.Unmarshal(body, &got)
	names := map[string]bool{}
	for _, p := range got.Providers {
		names[p.Name] = true
	}
	for _, want := range []string{"google", "github"} {
		if !names[want] {
			t.Errorf("providers missing %q: %+v", want, got.Providers)
		}
	}
}

// --- /console/oauth/begin --------------------------------------------

func TestOAuthBegin_HappyPath(t *testing.T) {
	fake := &fakeOAuthBeginner{
		result: oauth.BeginResult{
			FlowID:      "flw_abc",
			AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth?...",
			RedirectURI: "http://127.0.0.1:12345/oauth/callback/flw_abc",
		},
	}
	ts, cleanup := newOAuthFixture(t, fake)
	defer cleanup()

	resp, err := http.Post(ts.URL+"/console/oauth/begin?capsule=calendar-ingestor", //nolint:gosec,noctx // test
		"application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var got oauthBeginResponse
	_ = json.Unmarshal(body, &got)
	if got.FlowID != "flw_abc" {
		t.Errorf("flow_id: %q", got.FlowID)
	}
	if fake.lastCapsule != "calendar-ingestor" {
		t.Errorf("bridge received capsule=%q", fake.lastCapsule)
	}
}

func TestOAuthBegin_NoCapsule_400(t *testing.T) {
	ts, cleanup := newOAuthFixture(t, &fakeOAuthBeginner{})
	defer cleanup()
	resp, err := http.Post(ts.URL+"/console/oauth/begin", "application/json", strings.NewReader("")) //nolint:gosec,noctx // test
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestOAuthBegin_NoClientCredentialMapsTo400(t *testing.T) {
	fake := &fakeOAuthBeginner{err: errors.New("oauth: no client credentials registered for provider \"google\"")}
	ts, cleanup := newOAuthFixture(t, fake)
	defer cleanup()
	resp, _ := http.Post(ts.URL+"/console/oauth/begin?capsule=x", "application/json", strings.NewReader("")) //nolint:gosec,noctx // test
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing client cred, got %d", resp.StatusCode)
	}
}

// --- /console/oauth/status -------------------------------------------

func TestOAuthStatus_ConnectedWhenProbeReturnsTrue(t *testing.T) {
	fake := &fakeOAuthBeginnerWithProbe{
		fakeOAuthBeginner: &fakeOAuthBeginner{
			hasToken: map[string]bool{"calendar-ingestor": true},
		},
	}
	ts, cleanup := newOAuthFixture(t, fake)
	defer cleanup()

	resp, _ := http.Get(ts.URL + "/console/oauth/status?capsule=calendar-ingestor") //nolint:gosec,noctx // test
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var got oauthStatusResponse
	_ = json.Unmarshal(body, &got)
	if !got.Connected {
		t.Errorf("expected connected=true, got %+v", got)
	}
}

func TestOAuthStatus_DefaultsToDisconnectedWithoutProbe(t *testing.T) {
	ts, cleanup := newOAuthFixture(t, &fakeOAuthBeginner{})
	defer cleanup()
	resp, _ := http.Get(ts.URL + "/console/oauth/status?capsule=ghost") //nolint:gosec,noctx // test
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var got oauthStatusResponse
	_ = json.Unmarshal(body, &got)
	if got.Connected {
		t.Error("expected connected=false when no probe is wired")
	}
}

func TestOAuthStatus_MissingCapsule_400(t *testing.T) {
	ts, cleanup := newOAuthFixture(t, &fakeOAuthBeginner{})
	defer cleanup()
	resp, _ := http.Get(ts.URL + "/console/oauth/status") //nolint:gosec,noctx // test
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// --- unwired surfaces 503 -------------------------------------------

func TestOAuthEndpoints_503WhenUnwired(t *testing.T) {
	srv := New(Options{}) // no oauth deps
	ts := httptest.NewServer(srv.httpSrv.Handler)
	defer ts.Close()

	for _, c := range []struct {
		method, path string
	}{
		{http.MethodGet, "/console/oauth/clients"},
		{http.MethodPost, "/console/oauth/clients/google"},
		{http.MethodDelete, "/console/oauth/clients/google"},
		{http.MethodPost, "/console/oauth/begin?capsule=x"},
	} {
		req, _ := http.NewRequest(c.method, ts.URL+c.path, strings.NewReader(`{"client_id":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s: expected 503, got %d", c.method, c.path, resp.StatusCode)
		}
	}
}
