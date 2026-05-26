package oauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ClientCredential is one user-supplied OAuth client registration.
// Shared across every capsule that targets the same provider — a
// user who installs both a Calendar and a Drive ingestor uses one
// Google client_id for both.
type ClientCredential struct {
	// Provider is the well-known provider name or the custom name
	// declared in a capsule manifest. Unique key for this row.
	Provider string `json:"provider"`

	// ClientID is the per-user OAuth client identifier the user
	// created in the provider's developer console.
	ClientID string `json:"client_id"`

	// ClientSecret is optional. Desktop apps using PKCE don't
	// need one (Google's recommended pattern); web-app clients
	// need it for the token exchange.
	ClientSecret string `json:"client_secret,omitempty"`

	// CreatedAt + UpdatedAt for the dashboard.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ClientStore persists per-user OAuth clients. SQLite-backed,
// shares runtime.db with the permission / capsule / source stores.
type ClientStore struct {
	db *sql.DB

	mu sync.Mutex
}

// OpenClientStore creates or opens the OAuth client store at the
// given runtime.db path. Migrations are idempotent.
func OpenClientStore(ctx context.Context, dbPath string) (*ClientStore, error) {
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("oauth: opening client store: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("oauth: pinging client store: %w", err)
	}
	s := &ClientStore{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *ClientStore) Close() error { return s.db.Close() }

var clientStoreMigrations = []string{
	// 1: oauth_clients table — one row per user-registered OAuth client.
	`
CREATE TABLE IF NOT EXISTS oauth_clients (
    provider       TEXT PRIMARY KEY,
    client_id      TEXT NOT NULL,
    client_secret  TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
`,
}

func (s *ClientStore) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS oauth_schema_migrations (
            version    INTEGER PRIMARY KEY,
            applied_at TEXT NOT NULL
        )`); err != nil {
		return fmt.Errorf("oauth: creating schema_migrations: %w", err)
	}
	var current int
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM oauth_schema_migrations`)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("oauth: reading migration version: %w", err)
	}
	for i, sqlText := range clientStoreMigrations {
		version := i + 1
		if version <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("oauth: begin migration tx: %w", err)
		}
		if _, err := tx.ExecContext(ctx, sqlText); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("oauth: applying migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO oauth_schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("oauth: recording migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("oauth: commit migration %d: %w", version, err)
		}
	}
	return nil
}

// ErrClientNotFound is returned by Get for a provider with no row.
var ErrClientNotFound = errors.New("oauth: no client registered for provider")

// Set inserts or replaces the client registration for a provider.
func (s *ClientStore) Set(ctx context.Context, c ClientCredential) error {
	if c.Provider == "" {
		return errors.New("oauth: ClientCredential.Provider is required")
	}
	if c.ClientID == "" {
		return errors.New("oauth: ClientCredential.ClientID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO oauth_clients (provider, client_id, client_secret, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(provider) DO UPDATE SET
            client_id     = excluded.client_id,
            client_secret = excluded.client_secret,
            updated_at    = excluded.updated_at`,
		c.Provider, c.ClientID, nullString(c.ClientSecret), now, now)
	if err != nil {
		return fmt.Errorf("oauth: storing client %s: %w", c.Provider, err)
	}
	return nil
}

// Get returns the client registration for a provider or ErrClientNotFound.
func (s *ClientStore) Get(ctx context.Context, provider string) (*ClientCredential, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT provider, client_id, client_secret, created_at, updated_at
		   FROM oauth_clients WHERE provider = ?`, provider)
	c := &ClientCredential{}
	var clientSecret sql.NullString
	var createdStr, updatedStr string
	if err := row.Scan(&c.Provider, &c.ClientID, &clientSecret, &createdStr, &updatedStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrClientNotFound, provider)
		}
		return nil, fmt.Errorf("oauth: reading client %s: %w", provider, err)
	}
	if clientSecret.Valid {
		c.ClientSecret = clientSecret.String
	}
	if t, err := time.Parse(time.RFC3339Nano, createdStr); err == nil {
		c.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedStr); err == nil {
		c.UpdatedAt = t
	}
	return c, nil
}

// List returns every registered client (provider + redacted-secret
// status). Used by the dashboard's "providers connected" surface.
// ClientSecret is zeroed out in the returned values so a misuse of
// this method doesn't leak secrets.
func (s *ClientStore) List(ctx context.Context) ([]ClientCredential, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, client_id, client_secret, created_at, updated_at
		   FROM oauth_clients ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("oauth: listing clients: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ClientCredential
	for rows.Next() {
		c := ClientCredential{}
		var clientSecret sql.NullString
		var createdStr, updatedStr string
		if err := rows.Scan(&c.Provider, &c.ClientID, &clientSecret, &createdStr, &updatedStr); err != nil {
			return nil, err
		}
		// Redact: keep only whether a secret was set.
		if clientSecret.Valid && clientSecret.String != "" {
			c.ClientSecret = "(set)"
		}
		if t, err := time.Parse(time.RFC3339Nano, createdStr); err == nil {
			c.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339Nano, updatedStr); err == nil {
			c.UpdatedAt = t
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Delete removes the client registration for a provider. Idempotent.
func (s *ClientStore) Delete(ctx context.Context, provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM oauth_clients WHERE provider = ?`, provider)
	if err != nil {
		return fmt.Errorf("oauth: deleting client %s: %w", provider, err)
	}
	return nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
