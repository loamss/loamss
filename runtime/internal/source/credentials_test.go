package source

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// stubStorageAdapter is a minimal in-memory StorageAdapter for
// CredentialStore tests. Doesn't encrypt — these tests are about
// the credential layer, not the storage layer.
type stubStorageAdapter struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newStubStorage() *stubStorageAdapter {
	return &stubStorageAdapter{files: map[string][]byte{}}
}

func (s *stubStorageAdapter) Write(_ context.Context, path string, content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = append([]byte(nil), content...)
	return nil
}

func (s *stubStorageAdapter) Read(_ context.Context, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[path]
	if !ok {
		return nil, errors.New("stub: not found")
	}
	return append([]byte(nil), b...), nil
}

func (s *stubStorageAdapter) Exists(_ context.Context, path string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[path]
	return ok, nil
}

func (s *stubStorageAdapter) Delete(_ context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, path)
	return nil
}

func TestStorageCredentialStore_RoundTrip(t *testing.T) {
	storage := newStubStorage()
	store := NewStorageCredentialStore(storage, "gmail-personal")
	ctx := context.Background()

	if _, err := store.Get(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("Get before Set: err=%v, want ErrNoCredentials", err)
	}

	in := map[string]any{
		"access_token":  "ya29.AbCdEf",
		"refresh_token": "1//0gxxx",
		"expires_at":    "2026-06-01T12:00:00Z",
	}
	if err := store.Set(ctx, in); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got["access_token"] != "ya29.AbCdEf" {
		t.Errorf("access_token: %v", got["access_token"])
	}
}

func TestStorageCredentialStore_PathConvention(t *testing.T) {
	got := CredentialsPath("gmail-work")
	want := "sources/gmail-work/credentials.json"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestStorageCredentialStore_Delete(t *testing.T) {
	storage := newStubStorage()
	store := NewStorageCredentialStore(storage, "test")
	ctx := context.Background()

	_ = store.Set(ctx, map[string]any{"k": "v"})
	if err := store.Delete(ctx); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("Get after Delete: err=%v", err)
	}
	// Delete is idempotent.
	if err := store.Delete(ctx); err != nil {
		t.Errorf("Delete twice: %v", err)
	}
}

func TestMemoryCredentialStore(t *testing.T) {
	store := NewMemoryCredentialStore()
	ctx := context.Background()

	if _, err := store.Get(ctx); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("Get empty: err=%v", err)
	}
	if err := store.Set(ctx, map[string]any{"token": "abc"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got["token"] != "abc" {
		t.Errorf("token: %v", got["token"])
	}
}
