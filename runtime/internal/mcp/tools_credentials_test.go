package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/adapter/storage"
	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// inMemStorage is an in-memory storage.Adapter for tests. Implements
// the full Adapter surface but only Read / Write / Delete / Exists do
// real work — the rest return ErrUnsupported.
type inMemStorage struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newInMemStorage() *inMemStorage { return &inMemStorage{files: map[string][]byte{}} }

func (s *inMemStorage) Init(_ context.Context, _ map[string]any) error { return nil }
func (s *inMemStorage) Close(_ context.Context) error                  { return nil }
func (s *inMemStorage) HealthCheck(_ context.Context) error            { return nil }

func (s *inMemStorage) Read(_ context.Context, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[path]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

func (s *inMemStorage) Write(_ context.Context, path string, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = append([]byte(nil), content...)
	return nil
}

func (s *inMemStorage) Delete(_ context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, path)
	return nil
}

func (s *inMemStorage) Exists(_ context.Context, path string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[path]
	return ok, nil
}

func (s *inMemStorage) ReadStream(_ context.Context, _ string, _, _ int64) (io.ReadCloser, error) {
	return nil, storage.ErrUnsupported
}
func (s *inMemStorage) WriteStream(_ context.Context, _ string, _ io.Reader) error {
	return storage.ErrUnsupported
}
func (s *inMemStorage) Metadata(_ context.Context, _ string) (storage.ObjectMetadata, error) {
	return storage.ObjectMetadata{}, storage.ErrUnsupported
}
func (s *inMemStorage) List(_ context.Context, _ string) (<-chan storage.ListEntry, error) {
	return nil, storage.ErrUnsupported
}
func (s *inMemStorage) SignedURL(_ context.Context, _ string, _ time.Duration, _ storage.Op) (string, error) {
	return "", storage.ErrUnsupported
}

// --- CapsuleCredentialStore unit tests ----------------------------

func TestCapsuleCredentialStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewCapsuleCredentialStore(newInMemStorage())

	// Get before Set returns found=false, no error.
	_, found, err := store.Get(ctx, "calendar-ingestor", "refresh_token")
	if err != nil {
		t.Fatalf("Get before Set: err=%v", err)
	}
	if found {
		t.Error("expected found=false before any Set")
	}

	if err := store.Set(ctx, "calendar-ingestor", "refresh_token", "1//0gxxx", nil); err != nil {
		t.Fatalf("Set: %v", err)
	}
	entry, found, err := store.Get(ctx, "calendar-ingestor", "refresh_token")
	if err != nil || !found {
		t.Fatalf("Get after Set: err=%v found=%v", err, found)
	}
	if entry.Value != "1//0gxxx" {
		t.Errorf("value: %q", entry.Value)
	}

	// Overwrite.
	if err := store.Set(ctx, "calendar-ingestor", "refresh_token", "1//newer", nil); err != nil {
		t.Fatalf("Set 2: %v", err)
	}
	entry, _, _ = store.Get(ctx, "calendar-ingestor", "refresh_token")
	if entry.Value != "1//newer" {
		t.Errorf("after overwrite value: %q", entry.Value)
	}

	// Delete.
	if err := store.Delete(ctx, "calendar-ingestor", "refresh_token"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, found, _ = store.Get(ctx, "calendar-ingestor", "refresh_token")
	if found {
		t.Error("expected found=false after Delete")
	}
}

func TestCapsuleCredentialStore_IsolationBetweenCapsules(t *testing.T) {
	ctx := context.Background()
	store := NewCapsuleCredentialStore(newInMemStorage())

	if err := store.Set(ctx, "calendar-ingestor", "token", "calendar-value", nil); err != nil {
		t.Fatalf("Set calendar: %v", err)
	}
	if err := store.Set(ctx, "slack-ingestor", "token", "slack-value", nil); err != nil {
		t.Fatalf("Set slack: %v", err)
	}

	cal, _, _ := store.Get(ctx, "calendar-ingestor", "token")
	slk, _, _ := store.Get(ctx, "slack-ingestor", "token")
	if cal.Value != "calendar-value" || slk.Value != "slack-value" {
		t.Errorf("isolation broken: cal=%q slk=%q", cal.Value, slk.Value)
	}

	// Deleting one capsule's key leaves the other intact.
	if err := store.Delete(ctx, "calendar-ingestor", "token"); err != nil {
		t.Fatalf("Delete calendar: %v", err)
	}
	_, calFound, _ := store.Get(ctx, "calendar-ingestor", "token")
	slk2, slkFound, _ := store.Get(ctx, "slack-ingestor", "token")
	if calFound {
		t.Error("calendar should be gone")
	}
	if !slkFound || slk2.Value != "slack-value" {
		t.Errorf("slack collateral damage: found=%v value=%q", slkFound, slk2.Value)
	}
}

func TestCapsuleCredentialStore_Expiry(t *testing.T) {
	ctx := context.Background()
	store := NewCapsuleCredentialStore(newInMemStorage())

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	if err := store.Set(ctx, "cap", "expired", "v1", &past); err != nil {
		t.Fatalf("Set expired: %v", err)
	}
	if err := store.Set(ctx, "cap", "fresh", "v2", &future); err != nil {
		t.Fatalf("Set fresh: %v", err)
	}

	_, found, _ := store.Get(ctx, "cap", "expired")
	if found {
		t.Error("expired key should return found=false")
	}
	e, found, _ := store.Get(ctx, "cap", "fresh")
	if !found || e.Value != "v2" || e.ExpiresAt == nil {
		t.Errorf("fresh: found=%v value=%q expires=%v", found, e.Value, e.ExpiresAt)
	}
}

func TestCapsuleCredentialStore_DeleteAll(t *testing.T) {
	ctx := context.Background()
	store := NewCapsuleCredentialStore(newInMemStorage())

	_ = store.Set(ctx, "cap", "a", "1", nil)
	_ = store.Set(ctx, "cap", "b", "2", nil)
	if err := store.DeleteAll(ctx, "cap"); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	_, foundA, _ := store.Get(ctx, "cap", "a")
	_, foundB, _ := store.Get(ctx, "cap", "b")
	if foundA || foundB {
		t.Errorf("after DeleteAll: foundA=%v foundB=%v", foundA, foundB)
	}
	// Idempotent.
	if err := store.DeleteAll(ctx, "cap"); err != nil {
		t.Errorf("DeleteAll on empty: %v", err)
	}
}

func TestCapsuleCredentialStore_RejectsEmptyName(t *testing.T) {
	ctx := context.Background()
	store := NewCapsuleCredentialStore(newInMemStorage())
	if err := store.Set(ctx, "", "k", "v", nil); !errors.Is(err, ErrEmptyCapsuleName) {
		t.Errorf("Set empty name: err=%v want ErrEmptyCapsuleName", err)
	}
}

func TestCapsuleCredentialStore_RejectsBadKey(t *testing.T) {
	ctx := context.Background()
	store := NewCapsuleCredentialStore(newInMemStorage())
	cases := []string{
		"", "key with space", "key/slash", "key@host", "naïve",
	}
	for _, k := range cases {
		err := store.Set(ctx, "cap", k, "v", nil)
		if err == nil {
			t.Errorf("Set with bad key %q: no error", k)
		}
	}
}

// --- Tool handler tests -------------------------------------------

func newCredentialsHandlerFixture(t *testing.T) (
	*CapsuleCredentialStore, audit.Writer, func(),
) {
	t.Helper()
	// Reuse the test fixture's audit writer so we can inspect entries.
	f := newTestHandler(t)
	store := NewCapsuleCredentialStore(newInMemStorage())
	return store, f.audit, func() { /* t.Cleanup already wired in newTestHandler */ }
}

func capsulePrincipal(name string) permission.Principal {
	return permission.Principal{Kind: permission.PrincipalCapsule, ID: name}
}

func clientPrincipal(name string) permission.Principal {
	return permission.Principal{Kind: permission.PrincipalClient, ID: name}
}

func TestCredentialsSet_RejectsNonCapsulePrincipal(t *testing.T) {
	store, aud, cleanup := newCredentialsHandlerFixture(t)
	defer cleanup()
	tool := NewCredentialsSetTool(store, aud)

	in := ToolInput{
		Principal: clientPrincipal("vibez"),
		Args:      json.RawMessage(`{"key":"x","value":"y"}`),
	}
	_, err := tool.Handler(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for client principal")
	}
	if !strings.Contains(err.Error(), "restricted to capsule principals") {
		t.Errorf("error message: %v", err)
	}
}

func TestCredentialsGet_HandlerRoundTrip(t *testing.T) {
	store, aud, cleanup := newCredentialsHandlerFixture(t)
	defer cleanup()
	setTool := NewCredentialsSetTool(store, aud)
	getTool := NewCredentialsGetTool(store, aud)

	ctx := context.Background()
	princ := capsulePrincipal("calendar-ingestor")

	// Set.
	_, err := setTool.Handler(ctx, ToolInput{
		Principal: princ,
		Args:      json.RawMessage(`{"key":"refresh_token","value":"1//0gxxx"}`),
	})
	if err != nil {
		t.Fatalf("set handler: %v", err)
	}

	// Get.
	res, err := getTool.Handler(ctx, ToolInput{
		Principal: princ,
		Args:      json.RawMessage(`{"key":"refresh_token"}`),
	})
	if err != nil {
		t.Fatalf("get handler: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(res.Content))
	}
	var got credentialsGetResult
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("decode get result: %v", err)
	}
	if !got.Found || got.Value != "1//0gxxx" {
		t.Errorf("get: %+v", got)
	}
}

func TestCredentialsGet_MissingKey(t *testing.T) {
	store, aud, cleanup := newCredentialsHandlerFixture(t)
	defer cleanup()
	tool := NewCredentialsGetTool(store, aud)

	res, err := tool.Handler(context.Background(), ToolInput{
		Principal: capsulePrincipal("cap"),
		Args:      json.RawMessage(`{"key":"nope"}`),
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var got credentialsGetResult
	_ = json.Unmarshal([]byte(res.Content[0].Text), &got)
	if got.Found {
		t.Error("expected found=false")
	}
	if got.Value != "" {
		t.Errorf("expected empty value, got %q", got.Value)
	}
}

func TestCredentialsDelete_Idempotent(t *testing.T) {
	store, aud, cleanup := newCredentialsHandlerFixture(t)
	defer cleanup()
	tool := NewCredentialsDeleteTool(store, aud)

	// Delete on never-set key.
	_, err := tool.Handler(context.Background(), ToolInput{
		Principal: capsulePrincipal("cap"),
		Args:      json.RawMessage(`{"key":"never-set"}`),
	})
	if err != nil {
		t.Errorf("delete missing: %v", err)
	}
}

func TestCredentialsSet_PersistsAcrossInstances(t *testing.T) {
	// Confirms the persistence layer is the storage adapter, not the
	// CapsuleCredentialStore instance — a runtime restart with the
	// same data dir picks up the same credentials.
	mem := newInMemStorage()
	storeA := NewCapsuleCredentialStore(mem)
	storeB := NewCapsuleCredentialStore(mem)

	ctx := context.Background()
	if err := storeA.Set(ctx, "cap", "k", "v", nil); err != nil {
		t.Fatal(err)
	}
	entry, found, err := storeB.Get(ctx, "cap", "k")
	if err != nil || !found || entry.Value != "v" {
		t.Errorf("cross-instance read: found=%v entry=%v err=%v", found, entry, err)
	}
}

func TestCredentialsSet_ExpiresAt(t *testing.T) {
	store, aud, cleanup := newCredentialsHandlerFixture(t)
	defer cleanup()
	setTool := NewCredentialsSetTool(store, aud)
	getTool := NewCredentialsGetTool(store, aud)

	ctx := context.Background()
	princ := capsulePrincipal("cap")
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	args, _ := json.Marshal(map[string]any{
		"key": "tok", "value": "v", "expires_at": future,
	})
	if _, err := setTool.Handler(ctx, ToolInput{Principal: princ, Args: args}); err != nil {
		t.Fatalf("set: %v", err)
	}
	res, _ := getTool.Handler(ctx, ToolInput{
		Principal: princ,
		Args:      json.RawMessage(`{"key":"tok"}`),
	})
	var got credentialsGetResult
	_ = json.Unmarshal([]byte(res.Content[0].Text), &got)
	if !got.Found || got.Value != "v" || got.ExpiresAt == "" {
		t.Errorf("expires_at round-trip: %+v", got)
	}
}

func TestCredentialsSet_RejectsMalformedExpiresAt(t *testing.T) {
	store, aud, cleanup := newCredentialsHandlerFixture(t)
	defer cleanup()
	tool := NewCredentialsSetTool(store, aud)

	args := json.RawMessage(`{"key":"k","value":"v","expires_at":"not-a-date"}`)
	_, err := tool.Handler(context.Background(), ToolInput{
		Principal: capsulePrincipal("cap"),
		Args:      args,
	})
	if err == nil {
		t.Fatal("expected error for malformed expires_at")
	}
}
