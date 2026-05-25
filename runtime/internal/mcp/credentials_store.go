package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/adapter/storage"
)

// CapsuleCredentialStore is the runtime-side backing for the
// `credentials.*` MCP tools. Each capsule gets one encrypted blob at
// `capsules/<capsule_name>/credentials.json` under the configured
// storage adapter — the same encryption-at-rest path source connectors
// use today via source.NewStorageCredentialStore (sources/<name>/...).
//
// The blob holds a map keyed by capsule-chosen key names, where each
// entry has a value and an optional expires_at. `get` after expiry
// returns "not found" to the caller; the entry stays in the blob
// until rewritten or deleted (callers see the same "not found" either
// way, so eager cleanup isn't required).
//
// Concurrency: one sync.Mutex per capsule name. The runtime is a
// single process at a time; cross-process writes against the same
// runtime.db data dir aren't supported in v0.1, so a sync.Map of
// mutexes is sufficient.
type CapsuleCredentialStore struct {
	storage storage.Adapter

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-capsule write serialization
}

// NewCapsuleCredentialStore wraps a storage adapter. The store does
// nothing on construction; the per-capsule blobs are lazy.
func NewCapsuleCredentialStore(s storage.Adapter) *CapsuleCredentialStore {
	return &CapsuleCredentialStore{
		storage: s,
		locks:   make(map[string]*sync.Mutex),
	}
}

// CapsuleCredentialEntry is one stored key.
type CapsuleCredentialEntry struct {
	Value     string     `json:"value"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// capsuleCredentialsBlob is the JSON shape persisted per capsule.
type capsuleCredentialsBlob struct {
	Entries map[string]CapsuleCredentialEntry `json:"entries"`
}

// CredentialKeyPattern matches the set of accepted credential keys.
// Mirrors the inputSchema pattern in the MCP tool surface so both
// guards reject the same shapes.
const CredentialKeyPattern = `^[a-zA-Z0-9_.-]+$`

// ErrEmptyCapsuleName is returned when a `credentials.*` call arrives
// without an authenticated capsule principal. Surfaced to callers as
// a regular tool error (the dispatcher's permission check would have
// caught a missing/invalid principal earlier; this guard exists for
// defense in depth so a misconfigured tool can't silently key under
// the empty string).
var ErrEmptyCapsuleName = errors.New("credentials: capsule name is required")

// pathFor returns the storage path for a capsule's credential blob.
// Single source of truth — do not inline.
func capsuleCredentialsPath(capsuleName string) string {
	return "capsules/" + capsuleName + "/credentials.json"
}

func (s *CapsuleCredentialStore) lockFor(capsuleName string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.locks[capsuleName]; ok {
		return m
	}
	m := &sync.Mutex{}
	s.locks[capsuleName] = m
	return m
}

// validateKey enforces the same pattern the MCP input schema does,
// as defense in depth. The dispatcher validates input schemas at the
// wire level but tools must still be safe if a misbehaving client
// bypasses that.
func validateKey(key string) error {
	if key == "" {
		return errors.New("credentials: key is required")
	}
	for _, r := range key {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '.' || r == '-'
		if !ok {
			return fmt.Errorf("credentials: key %q has disallowed character %q (allowed: %s)",
				key, r, strings.TrimPrefix(strings.TrimSuffix(CredentialKeyPattern, "+$"), "^["))
		}
	}
	return nil
}

// readBlob loads the capsule's credential map. Returns an empty
// (initialized) blob if the file is missing.
func (s *CapsuleCredentialStore) readBlob(
	ctx context.Context, capsuleName string,
) (capsuleCredentialsBlob, error) {
	path := capsuleCredentialsPath(capsuleName)
	exists, err := s.storage.Exists(ctx, path)
	if err != nil {
		return capsuleCredentialsBlob{}, fmt.Errorf("credentials: exists check: %w", err)
	}
	if !exists {
		return capsuleCredentialsBlob{Entries: map[string]CapsuleCredentialEntry{}}, nil
	}
	raw, err := s.storage.Read(ctx, path)
	if err != nil {
		return capsuleCredentialsBlob{}, fmt.Errorf("credentials: read: %w", err)
	}
	if len(raw) == 0 {
		return capsuleCredentialsBlob{Entries: map[string]CapsuleCredentialEntry{}}, nil
	}
	var blob capsuleCredentialsBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		return capsuleCredentialsBlob{}, fmt.Errorf("credentials: decode: %w", err)
	}
	if blob.Entries == nil {
		blob.Entries = map[string]CapsuleCredentialEntry{}
	}
	return blob, nil
}

func (s *CapsuleCredentialStore) writeBlob(
	ctx context.Context, capsuleName string, blob capsuleCredentialsBlob,
) error {
	raw, err := json.Marshal(blob)
	if err != nil {
		return fmt.Errorf("credentials: encode: %w", err)
	}
	if err := s.storage.Write(ctx, capsuleCredentialsPath(capsuleName), raw); err != nil {
		return fmt.Errorf("credentials: write: %w", err)
	}
	return nil
}

// Set persists one entry. Overwrites any previous value for the
// same key. expires_at may be nil (no expiry).
func (s *CapsuleCredentialStore) Set(
	ctx context.Context, capsuleName, key, value string, expiresAt *time.Time,
) error {
	if capsuleName == "" {
		return ErrEmptyCapsuleName
	}
	if err := validateKey(key); err != nil {
		return err
	}
	lock := s.lockFor(capsuleName)
	lock.Lock()
	defer lock.Unlock()

	blob, err := s.readBlob(ctx, capsuleName)
	if err != nil {
		return err
	}
	blob.Entries[key] = CapsuleCredentialEntry{Value: value, ExpiresAt: expiresAt}
	return s.writeBlob(ctx, capsuleName, blob)
}

// Get returns the entry if present and unexpired. The `found` return
// is false for "not present" *or* "expired" — callers can't
// distinguish, and shouldn't want to. The `entry` is the raw record
// when present (even if expired, an internal caller can see it; we
// just don't surface it to MCP). The expired-but-present case returns
// found=false so the MCP tool's contract is uniform.
func (s *CapsuleCredentialStore) Get(
	ctx context.Context, capsuleName, key string,
) (entry CapsuleCredentialEntry, found bool, err error) {
	if capsuleName == "" {
		return CapsuleCredentialEntry{}, false, ErrEmptyCapsuleName
	}
	if err := validateKey(key); err != nil {
		return CapsuleCredentialEntry{}, false, err
	}
	lock := s.lockFor(capsuleName)
	lock.Lock()
	defer lock.Unlock()

	blob, err := s.readBlob(ctx, capsuleName)
	if err != nil {
		return CapsuleCredentialEntry{}, false, err
	}
	e, ok := blob.Entries[key]
	if !ok {
		return CapsuleCredentialEntry{}, false, nil
	}
	if e.ExpiresAt != nil && !e.ExpiresAt.After(time.Now()) {
		return CapsuleCredentialEntry{}, false, nil
	}
	return e, true, nil
}

// Delete removes one entry. Idempotent: deleting a missing key
// succeeds with no error.
func (s *CapsuleCredentialStore) Delete(
	ctx context.Context, capsuleName, key string,
) error {
	if capsuleName == "" {
		return ErrEmptyCapsuleName
	}
	if err := validateKey(key); err != nil {
		return err
	}
	lock := s.lockFor(capsuleName)
	lock.Lock()
	defer lock.Unlock()

	blob, err := s.readBlob(ctx, capsuleName)
	if err != nil {
		return err
	}
	if _, ok := blob.Entries[key]; !ok {
		return nil
	}
	delete(blob.Entries, key)
	if len(blob.Entries) == 0 {
		// Drop the now-empty blob so `loamss capsule remove` doesn't
		// see a stale file. Best-effort; failure to delete is logged
		// upstream but not surfaced — the consistency win matters
		// more than the cleanup.
		if err := s.storage.Delete(ctx, capsuleCredentialsPath(capsuleName)); err != nil {
			// If the storage adapter doesn't support delete-when-missing
			// idempotently, fall back to writing an empty blob.
			return s.writeBlob(ctx, capsuleName, blob)
		}
		return nil
	}
	return s.writeBlob(ctx, capsuleName, blob)
}

// DeleteAll clears every entry for a capsule. Called by
// `loamss capsule remove` to ensure credential blobs don't outlive
// the capsule. Idempotent.
func (s *CapsuleCredentialStore) DeleteAll(
	ctx context.Context, capsuleName string,
) error {
	if capsuleName == "" {
		return ErrEmptyCapsuleName
	}
	lock := s.lockFor(capsuleName)
	lock.Lock()
	defer lock.Unlock()

	path := capsuleCredentialsPath(capsuleName)
	exists, err := s.storage.Exists(ctx, path)
	if err != nil {
		return fmt.Errorf("credentials: exists check: %w", err)
	}
	if !exists {
		return nil
	}
	if err := s.storage.Delete(ctx, path); err != nil {
		return fmt.Errorf("credentials: delete-all: %w", err)
	}
	return nil
}
