// Package sourcetest provides test doubles for the source SPI.
// Tests in any package can import this without pulling fakes into
// the runtime binary.
package sourcetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/source"
)

// Fake is a deterministic in-memory source.Source for tests. Behavior:
//
//   - HealthCheck always succeeds unless HealthErr is set
//   - BeginAuth returns AuthFlowCodePaste with URL "fake://auth"
//   - CompleteAuth accepts any non-empty "code" param and persists
//     {"token": "...code..."} via the CredentialStore
//   - Sync increments a counter and returns RecordsAdded=1 each call,
//     packing the call count into a single-byte cursor
//
// All exported fields are knobs tests flip to exercise error paths;
// the observed fields capture what the fake saw.
type Fake struct {
	id string

	mu          sync.Mutex
	initialized bool
	deps        source.Deps

	// Hooks
	InitErr      error
	HealthErr    error
	SyncErr      error
	AuthBeginErr error
	AuthDoneErr  error

	// Observed state
	SyncCalls    int
	LastCursor   []byte
	AuthComplete bool

	// SkipAuth bypasses the AuthRequired check inside Sync. Set
	// true when a test wants to call Sync without going through
	// the auth flow first.
	SkipAuth bool
}

// New constructs a Fake with the given source id.
func New(id string) *Fake { return &Fake{id: id} }

// Factory returns a source.Factory that yields a fresh Fake on
// every call. Useful for `source.Register(id, fake.Factory())`.
func (f *Fake) Factory() source.Factory {
	return func() source.Source { return f }
}

// ID implements source.Source.
func (f *Fake) ID() string { return f.id }

// Init implements source.Source.
func (f *Fake) Init(_ context.Context, deps source.Deps) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.InitErr != nil {
		return f.InitErr
	}
	f.deps = deps
	f.initialized = true
	return nil
}

// AuthStatus implements source.Source.
func (f *Fake) AuthStatus(ctx context.Context) (source.AuthStatus, error) {
	f.mu.Lock()
	creds := f.deps.Credentials
	f.mu.Unlock()
	if creds == nil {
		return source.AuthStatus{Authenticated: false, Reason: "no credential store"}, nil
	}
	_, err := creds.Get(ctx)
	if errors.Is(err, source.ErrNoCredentials) {
		return source.AuthStatus{Authenticated: false, Reason: "no credentials stored"}, nil
	}
	if err != nil {
		return source.AuthStatus{}, err
	}
	return source.AuthStatus{Authenticated: true}, nil
}

// BeginAuth implements source.Source.
func (f *Fake) BeginAuth(_ context.Context) (source.AuthFlow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AuthBeginErr != nil {
		return source.AuthFlow{}, f.AuthBeginErr
	}
	return source.AuthFlow{
		Kind:         source.AuthFlowCodePaste,
		URL:          "fake://auth",
		Instructions: "paste the code back",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}, nil
}

// CompleteAuth implements source.Source.
func (f *Fake) CompleteAuth(ctx context.Context, params map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AuthDoneErr != nil {
		return f.AuthDoneErr
	}
	code := params["code"]
	if code == "" {
		return errors.New("fake source: missing code")
	}
	if f.deps.Credentials == nil {
		return errors.New("fake source: no credential store")
	}
	if err := f.deps.Credentials.Set(ctx, map[string]any{
		"token": "tok-" + code,
	}); err != nil {
		return err
	}
	f.AuthComplete = true
	return nil
}

// Sync implements source.Source.
func (f *Fake) Sync(_ context.Context, cursor []byte) (source.SyncResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.initialized {
		return source.SyncResult{}, errors.New("fake source: not initialized")
	}
	if !f.AuthComplete && !f.SkipAuth {
		return source.SyncResult{}, fmt.Errorf("fake source: %w", source.ErrAuthRequired)
	}
	if f.SyncErr != nil {
		return source.SyncResult{}, f.SyncErr
	}
	f.SyncCalls++
	f.LastCursor = append([]byte(nil), cursor...)
	now := time.Now().UTC()
	return source.SyncResult{
		Cursor:        []byte{byte(f.SyncCalls)},
		RecordsAdded:  1,
		BytesIngested: 4,
		Started:       now,
		Finished:      now,
	}, nil
}

// HealthCheck implements source.Source.
func (f *Fake) HealthCheck(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.HealthErr
}

// Close implements source.Source.
func (f *Fake) Close(_ context.Context) error { return nil }

// SyncCount returns how many times Sync has been called.
func (f *Fake) SyncCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.SyncCalls
}
