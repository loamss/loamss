package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/loamss/loamss/runtime/internal/adapter/storage"
)

// CapsuleCursorStore is the runtime-side backing for the `cursor.*`
// MCP tools. One opaque byte string per capsule installation. The
// runtime persists, the capsule reads/writes — analogous to how
// source.Sync(ctx, cursor) hands a cursor back and forth with an
// in-tree connector.
//
// Cursors are plaintext (not encrypted) since they're opaque to the
// runtime and never carry secrets — they're high-water marks (item
// ids, history tokens, RFC3339 timestamps) the capsule designed.
// Storage path: capsules/<name>/cursor.bin under the configured
// storage adapter.
//
// Concurrency: one sync.Mutex per capsule name within the process.
// The runtime is single-process; cross-process writes against the
// same data dir aren't supported in v0.1.
type CapsuleCursorStore struct {
	storage storage.Adapter

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewCapsuleCursorStore wraps a storage adapter.
func NewCapsuleCursorStore(s storage.Adapter) *CapsuleCursorStore {
	return &CapsuleCursorStore{
		storage: s,
		locks:   make(map[string]*sync.Mutex),
	}
}

// capsuleCursorPath is the storage path a capsule's cursor lives at.
// Single source of truth — do not inline.
func capsuleCursorPath(capsuleName string) string {
	return "capsules/" + capsuleName + "/cursor.bin"
}

// ErrEmptyCursorCapsuleName is returned when a cursor.* call arrives
// without an authenticated capsule principal. Defense in depth —
// the dispatcher's permission check would already have rejected an
// invalid principal.
var ErrEmptyCursorCapsuleName = errors.New("cursor: capsule name is required")

func (s *CapsuleCursorStore) lockFor(capsuleName string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.locks[capsuleName]; ok {
		return m
	}
	m := &sync.Mutex{}
	s.locks[capsuleName] = m
	return m
}

// Set stores the capsule's cursor. An empty value means "no cursor"
// (the next Get returns empty). Cursor bytes are stored verbatim —
// they're opaque to the runtime.
func (s *CapsuleCursorStore) Set(
	ctx context.Context, capsuleName, value string,
) error {
	if capsuleName == "" {
		return ErrEmptyCursorCapsuleName
	}
	lock := s.lockFor(capsuleName)
	lock.Lock()
	defer lock.Unlock()

	path := capsuleCursorPath(capsuleName)
	if value == "" {
		// Delete-on-empty so the next Get is a clean "no cursor",
		// matching the contract documented on the MCP surface.
		exists, err := s.storage.Exists(ctx, path)
		if err != nil {
			return fmt.Errorf("cursor: exists check: %w", err)
		}
		if !exists {
			return nil
		}
		if err := s.storage.Delete(ctx, path); err != nil {
			return fmt.Errorf("cursor: delete-on-empty: %w", err)
		}
		return nil
	}
	if err := s.storage.Write(ctx, path, []byte(value)); err != nil {
		return fmt.Errorf("cursor: write: %w", err)
	}
	return nil
}

// Get returns the capsule's cursor, or "" when none has been set.
// No "found" return — empty string is the universal "no cursor"
// sentinel, matching the contract a fresh in-tree source.Sync gets
// (cursor []byte = nil).
func (s *CapsuleCursorStore) Get(
	ctx context.Context, capsuleName string,
) (string, error) {
	if capsuleName == "" {
		return "", ErrEmptyCursorCapsuleName
	}
	lock := s.lockFor(capsuleName)
	lock.Lock()
	defer lock.Unlock()

	path := capsuleCursorPath(capsuleName)
	exists, err := s.storage.Exists(ctx, path)
	if err != nil {
		return "", fmt.Errorf("cursor: exists check: %w", err)
	}
	if !exists {
		return "", nil
	}
	raw, err := s.storage.Read(ctx, path)
	if err != nil {
		return "", fmt.Errorf("cursor: read: %w", err)
	}
	return string(raw), nil
}

// DeleteAll removes the cursor. Called by `loamss capsule remove`
// so capsule data doesn't outlive the capsule. Idempotent.
func (s *CapsuleCursorStore) DeleteAll(
	ctx context.Context, capsuleName string,
) error {
	if capsuleName == "" {
		return ErrEmptyCursorCapsuleName
	}
	lock := s.lockFor(capsuleName)
	lock.Lock()
	defer lock.Unlock()

	path := capsuleCursorPath(capsuleName)
	exists, err := s.storage.Exists(ctx, path)
	if err != nil {
		return fmt.Errorf("cursor: exists check: %w", err)
	}
	if !exists {
		return nil
	}
	if err := s.storage.Delete(ctx, path); err != nil {
		return fmt.Errorf("cursor: delete-all: %w", err)
	}
	return nil
}
