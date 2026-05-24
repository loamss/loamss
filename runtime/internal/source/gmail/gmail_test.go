package gmail

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/source"
)

// stubStorage is a minimal in-memory StorageAdapter for tests.
// Mirrors the credentials_test.go fake in the source package, but
// kept private here to avoid the import cycle.
type stubStorage struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newStubStorage() *stubStorage {
	return &stubStorage{files: map[string][]byte{}}
}

func (s *stubStorage) Write(_ context.Context, path string, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = append([]byte(nil), content...)
	return nil
}

func (s *stubStorage) Read(_ context.Context, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[path]
	if !ok {
		return nil, errors.New("stub: not found")
	}
	return append([]byte(nil), b...), nil
}

func (s *stubStorage) Exists(_ context.Context, path string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[path]
	return ok, nil
}

func (s *stubStorage) Delete(_ context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, path)
	return nil
}

func (s *stubStorage) has(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[path]
	return ok
}

// stubMemory is a minimal in-memory MemoryAdapter for tests.
type stubMemory struct {
	mu      sync.Mutex
	entries map[string]source.MemoryEntry // key: namespace:id
}

func newStubMemory() *stubMemory {
	return &stubMemory{entries: map[string]source.MemoryEntry{}}
}

func (m *stubMemory) Upsert(_ context.Context, e source.MemoryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[e.Namespace+":"+e.ID] = e
	return nil
}

func (m *stubMemory) Delete(_ context.Context, namespace, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, namespace+":"+id)
	return nil
}

func (m *stubMemory) get(namespace, id string) (source.MemoryEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[namespace+":"+id]
	return e, ok
}

// noopLogger satisfies source.Logger without emitting anything.
type noopLogger struct{}

func (noopLogger) Info(string, ...any)  {}
func (noopLogger) Warn(string, ...any)  {}
func (noopLogger) Error(string, ...any) {}
func (noopLogger) Debug(string, ...any) {}

// newTestSource constructs an Init-ed gmailSource with the given
// auth, token, and API base URLs.
func newTestSource(t *testing.T, authURL, tokenURL, apiBase string) (*gmailSource, *stubStorage, *stubMemory, *source.MemoryCredentialStore) {
	t.Helper()
	storage := newStubStorage()
	memory := newStubMemory()
	creds := source.NewMemoryCredentialStore()

	g := &gmailSource{}
	cfg := map[string]any{
		"client_id":     "test-client",
		"client_secret": "test-secret",
		"max_full_sync": 10,
	}
	if authURL != "" {
		cfg["auth_url"] = authURL
	}
	if tokenURL != "" {
		cfg["token_url"] = tokenURL
	}
	if apiBase != "" {
		cfg["api_base"] = apiBase
	}
	if err := g.Init(context.Background(), source.Deps{
		SourceName:  "gmail-test",
		Config:      cfg,
		Storage:     storage,
		Memory:      memory,
		Credentials: creds,
		Logger:      noopLogger{},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return g, storage, memory, creds
}

// --- ID / Init -------------------------------------------------------

func TestGmail_IDIsRegistered(t *testing.T) {
	if SourceID != "source:gmail" {
		t.Errorf("SourceID = %q", SourceID)
	}
	registered := false
	for _, id := range source.Registered() {
		if id == SourceID {
			registered = true
		}
	}
	if !registered {
		t.Error("source:gmail not in registry")
	}
}

func TestGmail_InitRequiresClientID(t *testing.T) {
	g := &gmailSource{}
	err := g.Init(context.Background(), source.Deps{
		SourceName: "x",
		Config: map[string]any{
			"client_secret": "s",
		},
		Storage:     newStubStorage(),
		Credentials: source.NewMemoryCredentialStore(),
	})
	if err == nil || !contains(err.Error(), "client_id") {
		t.Errorf("expected client_id error, got %v", err)
	}
}

func TestGmail_InitRequiresClientSecret(t *testing.T) {
	g := &gmailSource{}
	err := g.Init(context.Background(), source.Deps{
		SourceName: "x",
		Config: map[string]any{
			"client_id": "c",
		},
		Storage:     newStubStorage(),
		Credentials: source.NewMemoryCredentialStore(),
	})
	if err == nil || !contains(err.Error(), "client_secret") {
		t.Errorf("expected client_secret error, got %v", err)
	}
}

func TestGmail_InitAppliesDefaults(t *testing.T) {
	g, _, _, _ := newTestSource(t, "", "", "")
	if g.scope != DefaultScope {
		t.Errorf("scope: %q", g.scope)
	}
	if g.authURL == "" || g.tokenURL == "" || g.apiBase == "" {
		t.Errorf("URLs unset: auth=%q token=%q api=%q", g.authURL, g.tokenURL, g.apiBase)
	}
}

// --- AuthStatus ------------------------------------------------------

func TestGmail_AuthStatus_NoCreds(t *testing.T) {
	g, _, _, _ := newTestSource(t, "", "", "")
	st, err := g.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if st.Authenticated {
		t.Error("expected unauthenticated")
	}
}

func TestGmail_AuthStatus_WithCreds(t *testing.T) {
	g, _, _, creds := newTestSource(t, "", "", "")
	_ = creds.Set(context.Background(), map[string]any{
		"access_token":  "tok",
		"refresh_token": "ref",
		"expiry":        time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	})
	st, err := g.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if !st.Authenticated {
		t.Errorf("expected authenticated, got %+v", st)
	}
}

// --- cursor encoding -------------------------------------------------

func TestCursor_RoundTrip(t *testing.T) {
	in := cursorPayload{HistoryID: "12345", LastSyncTime: "2026-05-24T12:00:00Z"}
	encoded := mustEncodeCursor(in)
	out, err := decodeCursor(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.HistoryID != "12345" || out.LastSyncTime != "2026-05-24T12:00:00Z" {
		t.Errorf("round-trip mismatch: %+v", out)
	}
}

func TestCursor_EmptyDecodesToZero(t *testing.T) {
	out, err := decodeCursor(nil)
	if err != nil {
		t.Fatalf("decode nil: %v", err)
	}
	if out.HistoryID != "" {
		t.Errorf("expected empty cursor, got %+v", out)
	}
}

// --- token persistence ----------------------------------------------

func TestToken_RoundTrip(t *testing.T) {
	g, _, _, _ := newTestSource(t, "", "", "")
	in := &oauthToken{
		AccessToken:  "ya29.abcd",
		RefreshToken: "1//refresh",
		Expiry:       time.Now().UTC().Add(time.Hour).Truncate(time.Second),
		Scope:        "scope",
	}
	if err := g.saveToken(context.Background(), in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := g.loadToken(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out.AccessToken != in.AccessToken {
		t.Errorf("access_token: %q", out.AccessToken)
	}
	if out.RefreshToken != in.RefreshToken {
		t.Errorf("refresh_token: %q", out.RefreshToken)
	}
	if !out.Expiry.Equal(in.Expiry) {
		t.Errorf("expiry: got %v, want %v", out.Expiry, in.Expiry)
	}
}

func TestToken_ExpiresSoonAndExpired(t *testing.T) {
	tok := &oauthToken{Expiry: time.Now().Add(-time.Minute)}
	if !tok.expired() {
		t.Error("should be expired")
	}
	tok = &oauthToken{Expiry: time.Now().Add(10 * time.Second)}
	if !tok.expiresSoon() {
		t.Error("should be expiresSoon (within buffer)")
	}
	tok = &oauthToken{Expiry: time.Now().Add(2 * time.Hour)}
	if tok.expiresSoon() {
		t.Error("should not be expiresSoon")
	}
}

// --- helpers ---------------------------------------------------------

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
