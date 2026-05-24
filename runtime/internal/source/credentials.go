package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// storageCredentialStore is the production CredentialStore: each
// source's credentials live at "sources/<source_name>/credentials.json"
// inside the user's storage adapter. The adapter's own encryption (if
// any) provides at-rest protection — fs-encrypted, our v0.1 default,
// uses AES-GCM. Plain-text storage adapters get a config-time warning
// at runtime startup.
//
// This indirection means moving a runtime to another machine and
// pointing the new instance at the same storage adapter recovers
// every source's credentials without re-running OAuth flows — that
// is the walkaway-promise (`loamss export` / re-import) working as
// designed.
type storageCredentialStore struct {
	storage StorageAdapter
	name    string
}

// NewStorageCredentialStore returns a CredentialStore backed by the
// given storage adapter, scoped to the given source name. The
// caller is responsible for not handing the same name to two
// different sources (the source store's UNIQUE constraint on name
// already enforces this for configured sources).
func NewStorageCredentialStore(storage StorageAdapter, sourceName string) CredentialStore {
	return &storageCredentialStore{storage: storage, name: sourceName}
}

// Path returns the storage path used for credentials. Exposed so the
// CLI's `source remove` can audit what it's clearing and tests can
// assert path conventions.
func (c *storageCredentialStore) Path() string {
	return CredentialsPath(c.name)
}

// CredentialsPath returns the storage path a source's credentials
// live at. Single source of truth; do not inline.
func CredentialsPath(sourceName string) string {
	return "sources/" + sourceName + "/credentials.json"
}

func (c *storageCredentialStore) Get(ctx context.Context) (map[string]any, error) {
	exists, err := c.storage.Exists(ctx, c.Path())
	if err != nil {
		return nil, fmt.Errorf("source credentials: exists check: %w", err)
	}
	if !exists {
		return nil, ErrNoCredentials
	}
	raw, err := c.storage.Read(ctx, c.Path())
	if err != nil {
		return nil, fmt.Errorf("source credentials: read: %w", err)
	}
	if len(raw) == 0 {
		return nil, ErrNoCredentials
	}
	var creds map[string]any
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("source credentials: decode: %w", err)
	}
	return creds, nil
}

func (c *storageCredentialStore) Set(ctx context.Context, creds map[string]any) error {
	if creds == nil {
		return errors.New("source credentials: nil creds")
	}
	raw, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("source credentials: encode: %w", err)
	}
	if err := c.storage.Write(ctx, c.Path(), raw); err != nil {
		return fmt.Errorf("source credentials: write: %w", err)
	}
	return nil
}

func (c *storageCredentialStore) Delete(ctx context.Context) error {
	exists, err := c.storage.Exists(ctx, c.Path())
	if err != nil {
		return fmt.Errorf("source credentials: exists check: %w", err)
	}
	if !exists {
		return nil
	}
	if err := c.storage.Delete(ctx, c.Path()); err != nil {
		return fmt.Errorf("source credentials: delete: %w", err)
	}
	return nil
}

// MemoryCredentialStore is an in-memory CredentialStore for tests.
// Safe for concurrent use by one source instance.
type MemoryCredentialStore struct {
	creds map[string]any
}

// NewMemoryCredentialStore returns an empty in-memory credential
// store. Used by tests and by sources that hold creds in volatile
// memory only (rare; not the default).
func NewMemoryCredentialStore() *MemoryCredentialStore {
	return &MemoryCredentialStore{}
}

// Get implements CredentialStore.
func (m *MemoryCredentialStore) Get(_ context.Context) (map[string]any, error) {
	if len(m.creds) == 0 {
		return nil, ErrNoCredentials
	}
	out := make(map[string]any, len(m.creds))
	for k, v := range m.creds {
		out[k] = v
	}
	return out, nil
}

// Set implements CredentialStore.
func (m *MemoryCredentialStore) Set(_ context.Context, creds map[string]any) error {
	if creds == nil {
		return errors.New("source credentials: nil creds")
	}
	m.creds = make(map[string]any, len(creds))
	for k, v := range creds {
		m.creds[k] = v
	}
	return nil
}

// Delete implements CredentialStore.
func (m *MemoryCredentialStore) Delete(_ context.Context) error {
	m.creds = nil
	return nil
}
