package permission

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/loamss/loamss/runtime/internal/database"
)

// Store is the SQLite-backed persistence for grants, the capability
// registry, and pending approvals. One Store per runtime instance;
// it wraps a single SQLite database file (runtime.db by convention).
//
// All operations are safe for concurrent use; the underlying driver
// pool serializes writes via the busy_timeout pragma. Long
// transactions are avoided; each operation is one short SQL statement
// (or a tight transaction for multi-statement work).
type Store struct {
	db     *database.DB       // wraps *sql.DB; rebinds ? → $N for postgres
	dbMeta *database.Database // owning handle when ownsDB; borrowed when not
	ownsDB bool               // true if Open path opened the underlying *sql.DB
	path   string

	// ulidMu protects ulidEnt. Monotonic ULID generation requires
	// serialized access within a single millisecond.
	ulidMu  sync.Mutex
	ulidEnt *ulid.MonotonicEntropy
}

// Open creates or opens the runtime store at the given filesystem
// path (SQLite). Creates the parent directory if missing. Applies
// schema migrations as needed.
//
// Internally this is a convenience wrapper around OpenWith — opens
// a fresh database.Database via database.OpenSQLite and hands it to
// OpenWith, marking the Store as owner so Close releases the
// handle. Callers that want to share a single Database across
// multiple subsystem stores (the start.go pattern) should use
// OpenWith directly.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := database.OpenSQLite(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("permission: %w", err)
	}
	s, err := OpenWith(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s.ownsDB = true
	return s, nil
}

// OpenWith creates a permission Store on top of an already-open
// Database. The caller retains ownership of the Database; the Store
// will not close it on Close(). Used by start.go to share one
// runtime.db handle across permission, source, capsule, memory_layer,
// and oauth subsystems.
func OpenWith(ctx context.Context, db *database.Database) (*Store, error) {
	if db == nil || db.Conn() == nil {
		return nil, errors.New("permission: OpenWith requires a non-nil Database")
	}
	s := &Store{
		db:      db.Conn(),
		dbMeta:  db,
		path:    db.DSN(),
		ulidEnt: ulid.Monotonic(rand.Reader, 0),
	}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases the database handle if the Store opened it
// (Open path). Stores constructed via OpenWith never own the
// database and Close is a no-op for the database connection;
// the caller is expected to close it via the database.Database
// they passed in.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if s.ownsDB && s.dbMeta != nil {
		return s.dbMeta.Close()
	}
	return nil
}

// Path returns the on-disk database path.
func (s *Store) Path() string { return s.path }

// --- Migrations --------------------------------------------------------

// migrations are applied in order at Open time. Adding a new
// migration is the schema-evolution path; never edit an existing
// migration's SQL after it's been applied in any deployment.
//
// migrationsSQLite + migrationsPostgres are parallel arrays: index N
// in both is "migration N." The migrate() dispatcher picks the right
// one based on driver. When adding a migration, add to both arrays.
//
// Type translation rules used throughout these stores:
//   - SQLite TEXT (ISO-8601 timestamps) → Postgres TIMESTAMPTZ
//   - SQLite INTEGER (used as bool 0/1)  → Postgres BOOLEAN
//   - Everything else                    → TEXT in both
//
// Both dialects accept IF NOT EXISTS on CREATE TABLE/INDEX, so the
// idempotent-migration property holds in either backend.
var migrationsSQLite = []string{
	// 1: initial schema.
	`
CREATE TABLE IF NOT EXISTS capabilities (
    name             TEXT PRIMARY KEY,
    namespace        TEXT NOT NULL,
    direction        TEXT NOT NULL,
    default_approval INTEGER NOT NULL,
    scope_schema     TEXT NOT NULL,
    declared_by      TEXT,
    registered_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_capabilities_namespace ON capabilities(namespace);

CREATE TABLE IF NOT EXISTS grants (
    id                       TEXT PRIMARY KEY,
    principal_kind           TEXT NOT NULL,
    principal_id             TEXT NOT NULL,
    capability               TEXT NOT NULL,
    scope_json               TEXT,
    framing                  TEXT NOT NULL,
    rationale                TEXT,
    user_note                TEXT,
    requires_user_approval   INTEGER NOT NULL,
    issued_at                TEXT NOT NULL,
    expires_at               TEXT,
    revoked_at               TEXT
);
CREATE INDEX IF NOT EXISTS idx_grants_principal  ON grants(principal_kind, principal_id);
CREATE INDEX IF NOT EXISTS idx_grants_capability ON grants(capability);
CREATE INDEX IF NOT EXISTS idx_grants_active     ON grants(principal_kind, principal_id, capability, revoked_at, expires_at);

CREATE TABLE IF NOT EXISTS pending_approvals (
    id                    TEXT PRIMARY KEY,
    grant_id              TEXT NOT NULL,
    principal_kind        TEXT NOT NULL,
    principal_id          TEXT NOT NULL,
    capability            TEXT NOT NULL,
    attempted_scope_json  TEXT,
    rationale             TEXT,
    state                 TEXT NOT NULL,
    requested_at          TEXT NOT NULL,
    decided_at            TEXT,
    decided_by            TEXT,
    decision_note         TEXT
);
CREATE INDEX IF NOT EXISTS idx_approvals_state ON pending_approvals(state);
CREATE INDEX IF NOT EXISTS idx_approvals_principal ON pending_approvals(principal_kind, principal_id);
`,
	// 2: external-client pairing — clients table + single-use codes.
	`
CREATE TABLE IF NOT EXISTS clients (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    credential_hash TEXT NOT NULL,
    metadata_json   TEXT,
    paired_at       TEXT NOT NULL,
    last_seen_at    TEXT,
    revoked_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_clients_revoked ON clients(revoked_at);

CREATE TABLE IF NOT EXISTS pairing_codes (
    code                 TEXT PRIMARY KEY,
    client_name          TEXT NOT NULL,
    created_by           TEXT,
    created_at           TEXT NOT NULL,
    expires_at           TEXT NOT NULL,
    redeemed_at          TEXT,
    redeemed_client_id   TEXT
);
CREATE INDEX IF NOT EXISTS idx_pairing_codes_open ON pairing_codes(redeemed_at, expires_at);
`,
}

// migrationsPostgres mirrors migrationsSQLite with column types that
// the existing application code passes natively without per-call-site
// adaptation:
//
//   - Timestamps: TEXT (the application formats time.RFC3339Nano on
//     write and parses it back on read; portable across both drivers
//     without scanning into time.Time).
//   - Booleans: INTEGER 0/1 (the application uses int conversions on
//     write and `value != 0` on read; portable without coercion).
//
// We're trading "idiomatic Postgres" for "one codebase, two
// backends, zero per-call-site type juggling." Acceptable because
// the runtime never runs SQL-side time-range or boolean-aware
// queries against these tables — filtering happens in Go.
var migrationsPostgres = []string{
	// 1: initial schema.
	`
CREATE TABLE IF NOT EXISTS capabilities (
    name             TEXT PRIMARY KEY,
    namespace        TEXT NOT NULL,
    direction        TEXT NOT NULL,
    default_approval INTEGER NOT NULL,
    scope_schema     TEXT NOT NULL,
    declared_by      TEXT,
    registered_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_capabilities_namespace ON capabilities(namespace);

CREATE TABLE IF NOT EXISTS grants (
    id                       TEXT PRIMARY KEY,
    principal_kind           TEXT NOT NULL,
    principal_id             TEXT NOT NULL,
    capability               TEXT NOT NULL,
    scope_json               TEXT,
    framing                  TEXT NOT NULL,
    rationale                TEXT,
    user_note                TEXT,
    requires_user_approval   INTEGER NOT NULL,
    issued_at                TEXT NOT NULL,
    expires_at               TEXT,
    revoked_at               TEXT
);
CREATE INDEX IF NOT EXISTS idx_grants_principal  ON grants(principal_kind, principal_id);
CREATE INDEX IF NOT EXISTS idx_grants_capability ON grants(capability);
CREATE INDEX IF NOT EXISTS idx_grants_active     ON grants(principal_kind, principal_id, capability, revoked_at, expires_at);

CREATE TABLE IF NOT EXISTS pending_approvals (
    id                    TEXT PRIMARY KEY,
    grant_id              TEXT NOT NULL,
    principal_kind        TEXT NOT NULL,
    principal_id          TEXT NOT NULL,
    capability            TEXT NOT NULL,
    attempted_scope_json  TEXT,
    rationale             TEXT,
    state                 TEXT NOT NULL,
    requested_at          TEXT NOT NULL,
    decided_at            TEXT,
    decided_by            TEXT,
    decision_note         TEXT
);
CREATE INDEX IF NOT EXISTS idx_approvals_state ON pending_approvals(state);
CREATE INDEX IF NOT EXISTS idx_approvals_principal ON pending_approvals(principal_kind, principal_id);
`,
	// 2: external-client pairing — clients table + single-use codes.
	`
CREATE TABLE IF NOT EXISTS clients (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    credential_hash TEXT NOT NULL,
    metadata_json   TEXT,
    paired_at       TEXT NOT NULL,
    last_seen_at    TEXT,
    revoked_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_clients_revoked ON clients(revoked_at);

CREATE TABLE IF NOT EXISTS pairing_codes (
    code                 TEXT PRIMARY KEY,
    client_name          TEXT NOT NULL,
    created_by           TEXT,
    created_at           TEXT NOT NULL,
    expires_at           TEXT NOT NULL,
    redeemed_at          TEXT,
    redeemed_client_id   TEXT
);
CREATE INDEX IF NOT EXISTS idx_pairing_codes_open ON pairing_codes(redeemed_at, expires_at);
`,
}

// migrationsFor returns the migration array for the given driver. The
// two arrays are kept parallel by index — adding a new migration
// means adding to both.
func migrationsFor(driver database.Driver) []string {
	if driver == database.DriverPostgres {
		return migrationsPostgres
	}
	return migrationsSQLite
}

// migrate brings the database schema up to the latest version and
// seeds the canonical capability registry on first run.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS schema_migrations (
            version    INTEGER PRIMARY KEY,
            applied_at TEXT NOT NULL
        )`); err != nil {
		return fmt.Errorf("permission: creating schema_migrations: %w", err)
	}

	var current int
	row := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("permission: reading migration version: %w", err)
	}

	for i, sqlText := range migrationsFor(s.db.Driver()) {
		version := i + 1
		if version <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("permission: begin migration tx: %w", err)
		}
		if _, err := tx.ExecContext(ctx, sqlText); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("permission: applying migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("permission: recording migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("permission: commit migration %d: %w", version, err)
		}
	}

	// Seed the canonical capability registry on first run only.
	// We detect "first run" by an empty capabilities table; later
	// runs find existing rows and skip.
	var existingCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM capabilities`).Scan(&existingCount); err != nil {
		return fmt.Errorf("permission: counting capabilities: %w", err)
	}
	if existingCount == 0 {
		now := time.Now().UTC()
		for _, def := range canonicalCapabilities(now) {
			if err := s.registerLocked(ctx, def); err != nil {
				return fmt.Errorf("permission: seeding canonical capability %s: %w", def.Name, err)
			}
		}
	}
	return nil
}

// --- Capabilities ------------------------------------------------------

// RegisterCapability adds a capability to the registry. Used by the
// capsule installer (when it lands) to add capsule-declared
// capabilities. Re-registering an existing capability with identical
// definition is a no-op; with different definition it errors with
// ErrCapabilityAlreadyRegistered.
//
// Reserved-namespace capabilities are rejected unless declared by the
// runtime itself (DeclaredBy == "").
func (s *Store) RegisterCapability(ctx context.Context, def CapabilityDef) error {
	if def.DeclaredBy != "" && isReservedNamespace(def.Name) {
		return fmt.Errorf("%w: %s", ErrReservedNamespace, def.Name)
	}
	// Existing canonical entry? Compare; if same shape, no-op.
	existing, err := s.GetCapability(ctx, def.Name)
	if err == nil {
		if capDefsEqual(*existing, def) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrCapabilityAlreadyRegistered, def.Name)
	}
	if !errors.Is(err, ErrCapabilityNotFound) {
		return err
	}
	return s.registerLocked(ctx, def)
}

func (s *Store) registerLocked(ctx context.Context, def CapabilityDef) error {
	scopeJSON, err := json.Marshal(def.Scope)
	if err != nil {
		return fmt.Errorf("permission: encoding scope schema: %w", err)
	}
	ns := def.Namespace
	if ns == "" {
		ns = namespaceOf(def.Name)
	}
	if def.RegisteredAt.IsZero() {
		def.RegisteredAt = time.Now().UTC()
	}
	approval := 0
	if def.DefaultApproval {
		approval = 1
	}
	var declared sql.NullString
	if def.DeclaredBy != "" {
		declared = sql.NullString{String: def.DeclaredBy, Valid: true}
	}
	_, err = s.db.ExecContext(ctx, `
        INSERT INTO capabilities (
            name, namespace, direction, default_approval, scope_schema,
            declared_by, registered_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		def.Name, ns, string(def.Direction), approval, string(scopeJSON),
		declared, def.RegisteredAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("permission: inserting capability %s: %w", def.Name, err)
	}
	return nil
}

// GetCapability returns a capability by name, or ErrCapabilityNotFound.
func (s *Store) GetCapability(ctx context.Context, name string) (*CapabilityDef, error) {
	var (
		ns        string
		direction string
		approval  int
		schemaStr string
		declared  sql.NullString
		regAtStr  string
	)
	err := s.db.QueryRowContext(ctx, `
        SELECT namespace, direction, default_approval, scope_schema,
               declared_by, registered_at
        FROM capabilities WHERE name = ?`, name,
	).Scan(&ns, &direction, &approval, &schemaStr, &declared, &regAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrCapabilityNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("permission: reading capability %s: %w", name, err)
	}
	var schema ScopeSchema
	if err := json.Unmarshal([]byte(schemaStr), &schema); err != nil {
		return nil, fmt.Errorf("permission: decoding schema for %s: %w", name, err)
	}
	regAt, err := time.Parse(time.RFC3339Nano, regAtStr)
	if err != nil {
		return nil, fmt.Errorf("permission: parsing registered_at for %s: %w", name, err)
	}
	def := &CapabilityDef{
		Name:            name,
		Namespace:       ns,
		Direction:       Direction(direction),
		DefaultApproval: approval != 0,
		Scope:           schema,
		RegisteredAt:    regAt,
	}
	if declared.Valid {
		def.DeclaredBy = declared.String
	}
	return def, nil
}

// ListCapabilities returns all registered capabilities ordered by name.
func (s *Store) ListCapabilities(ctx context.Context) ([]CapabilityDef, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT name, namespace, direction, default_approval, scope_schema,
               declared_by, registered_at
        FROM capabilities ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("permission: listing capabilities: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []CapabilityDef
	for rows.Next() {
		var (
			name, ns, direction, schemaStr, regAtStr string
			approval                                 int
			declared                                 sql.NullString
		)
		if err := rows.Scan(&name, &ns, &direction, &approval, &schemaStr, &declared, &regAtStr); err != nil {
			return nil, err
		}
		var schema ScopeSchema
		if err := json.Unmarshal([]byte(schemaStr), &schema); err != nil {
			return nil, fmt.Errorf("permission: decoding schema for %s: %w", name, err)
		}
		regAt, _ := time.Parse(time.RFC3339Nano, regAtStr)
		def := CapabilityDef{
			Name:            name,
			Namespace:       ns,
			Direction:       Direction(direction),
			DefaultApproval: approval != 0,
			Scope:           schema,
			RegisteredAt:    regAt,
		}
		if declared.Valid {
			def.DeclaredBy = declared.String
		}
		out = append(out, def)
	}
	return out, rows.Err()
}

func capDefsEqual(a, b CapabilityDef) bool {
	if a.Name != b.Name || a.Namespace != b.Namespace ||
		a.Direction != b.Direction || a.DefaultApproval != b.DefaultApproval ||
		a.DeclaredBy != b.DeclaredBy {
		return false
	}
	return reflect.DeepEqual(a.Scope, b.Scope)
}

// --- Grants ------------------------------------------------------------

// IssueGrant validates and persists a new grant. Caller need not set
// ID or IssuedAt; the store assigns them. RequiresUserApproval cannot
// weaken a capability whose DefaultApproval is true.
func (s *Store) IssueGrant(ctx context.Context, g Grant) (*Grant, error) {
	def, err := s.GetCapability(ctx, g.Capability)
	if err != nil {
		return nil, err
	}
	// Approval-downgrade guard: capability-level default approval
	// cannot be overridden to false by per-grant flag.
	if def.DefaultApproval && !g.RequiresUserApproval {
		// Caller may set true to keep parity; false is rejected.
		g.RequiresUserApproval = true
	}
	if err := validateScope(g.Scope, def.Scope); err != nil {
		return nil, err
	}

	g.ID = s.nextID("grt-")
	g.IssuedAt = time.Now().UTC()
	if g.Framing == "" {
		g.Framing = FramingPrivateRead
	}

	scopeJSON, err := encodeScopeJSON(g.Scope)
	if err != nil {
		return nil, err
	}
	approval := 0
	if g.RequiresUserApproval {
		approval = 1
	}
	var expiresAt sql.NullString
	if g.ExpiresAt != nil {
		expiresAt = sql.NullString{String: g.ExpiresAt.UTC().Format(time.RFC3339Nano), Valid: true}
	}

	if _, err := s.db.ExecContext(ctx, `
        INSERT INTO grants (
            id, principal_kind, principal_id, capability, scope_json,
            framing, rationale, user_note, requires_user_approval,
            issued_at, expires_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, string(g.Principal.Kind), g.Principal.ID, g.Capability, scopeJSON,
		string(g.Framing), nullableString(g.Rationale), nullableString(g.UserNote), approval,
		g.IssuedAt.Format(time.RFC3339Nano), expiresAt,
	); err != nil {
		return nil, fmt.Errorf("permission: inserting grant: %w", err)
	}
	return &g, nil
}

// RevokeGrant marks a grant revoked. Idempotent: revoking an
// already-revoked grant is a no-op (no error). Returns
// ErrGrantNotFound if the id doesn't exist.
func (s *Store) RevokeGrant(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE grants SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("permission: revoking %s: %w", id, err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		// Either grant doesn't exist or was already revoked.
		var exists bool
		err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM grants WHERE id = ?)`, id,
		).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: %s", ErrGrantNotFound, id)
		}
		// Already revoked — idempotent success.
	}
	return nil
}

// GetGrant returns a grant by id (regardless of active/revoked state).
func (s *Store) GetGrant(ctx context.Context, id string) (*Grant, error) {
	row := s.db.QueryRowContext(ctx, grantSelectColumns+` WHERE id = ?`, id)
	g, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrGrantNotFound, id)
	}
	return g, err
}

// ListGrantsByPrincipal returns all grants for a principal,
// active or otherwise. Sorted by issued_at ascending with id as
// the tiebreaker — necessary because RFC3339Nano timestamps can
// tie on fast-issuing platforms (macOS clocks have surfaced ties
// in CI) and SQLite's default tiebreak is undefined. ULIDs within
// a single MonotonicEntropy are guaranteed strictly increasing,
// so id-as-tiebreaker matches insertion order.
func (s *Store) ListGrantsByPrincipal(ctx context.Context, kind PrincipalKind, id string) ([]Grant, error) {
	return s.queryGrants(ctx,
		grantSelectColumns+` WHERE principal_kind = ? AND principal_id = ? ORDER BY issued_at ASC, id ASC`,
		string(kind), id)
}

// ListGrants returns grants matching the filter. Unlike
// ListGrantsByPrincipal, this is the general-purpose query that
// supports any combination of principal / capability / status
// filters — used by `loamss grant list` and the (future) console.
//
// Status defaults to "active" when the filter's Status field is empty.
// Use StatusAll explicitly to include revoked and expired.
func (s *Store) ListGrants(ctx context.Context, f GrantFilter) ([]Grant, error) {
	var (
		conds []string
		args  []any
	)
	if f.PrincipalKind != "" {
		conds = append(conds, "principal_kind = ?")
		args = append(args, string(f.PrincipalKind))
	}
	if f.PrincipalID != "" {
		conds = append(conds, "principal_id = ?")
		args = append(args, f.PrincipalID)
	}
	if f.Capability != "" {
		conds = append(conds, "capability = ?")
		args = append(args, f.Capability)
	}

	status := f.Status
	if status == "" {
		status = StatusActive
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch status {
	case StatusActive:
		conds = append(conds, "revoked_at IS NULL")
		conds = append(conds, "(expires_at IS NULL OR expires_at > ?)")
		args = append(args, now)
	case StatusRevoked:
		conds = append(conds, "revoked_at IS NOT NULL")
	case StatusExpired:
		conds = append(conds, "revoked_at IS NULL")
		conds = append(conds, "expires_at IS NOT NULL")
		conds = append(conds, "expires_at <= ?")
		args = append(args, now)
	case StatusAll:
		// no extra filter
	default:
		return nil, fmt.Errorf("permission: unknown status filter %q", status)
	}

	q := grantSelectColumns
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	// id as tiebreaker — see ListGrantsByPrincipal for the rationale.
	q += " ORDER BY issued_at DESC, id DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 1000
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	return s.queryGrants(ctx, q, args...)
}

// ListActiveGrantsForCheck returns the currently-effective grants
// matching the (principal, capability) tuple. Used by the check
// engine in commit 2.
func (s *Store) ListActiveGrantsForCheck(ctx context.Context, kind PrincipalKind, id, capability string) ([]Grant, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return s.queryGrants(ctx,
		grantSelectColumns+` WHERE principal_kind = ? AND principal_id = ? AND capability = ?
            AND revoked_at IS NULL
            AND (expires_at IS NULL OR expires_at > ?)
            ORDER BY issued_at ASC`,
		string(kind), id, capability, now)
}

const grantSelectColumns = `SELECT id, principal_kind, principal_id, capability, scope_json,
       framing, rationale, user_note, requires_user_approval,
       issued_at, expires_at, revoked_at
       FROM grants`

func (s *Store) queryGrants(ctx context.Context, query string, args ...any) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("permission: querying grants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Grant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanGrant(r rowScanner) (*Grant, error) {
	var (
		g                Grant
		scopeJSON        sql.NullString
		rationale        sql.NullString
		userNote         sql.NullString
		approval         int
		issuedStr        string
		expiresStr       sql.NullString
		revokedStr       sql.NullString
		principalKindStr string
		framingStr       string
	)
	if err := r.Scan(&g.ID, &principalKindStr, &g.Principal.ID, &g.Capability, &scopeJSON,
		&framingStr, &rationale, &userNote, &approval,
		&issuedStr, &expiresStr, &revokedStr); err != nil {
		return nil, err
	}
	g.Principal.Kind = PrincipalKind(principalKindStr)
	g.Framing = Framing(framingStr)
	if scopeJSON.Valid && scopeJSON.String != "" && scopeJSON.String != "null" {
		if err := json.Unmarshal([]byte(scopeJSON.String), &g.Scope); err != nil {
			return nil, fmt.Errorf("permission: decoding grant scope: %w", err)
		}
	}
	if rationale.Valid {
		g.Rationale = rationale.String
	}
	if userNote.Valid {
		g.UserNote = userNote.String
	}
	g.RequiresUserApproval = approval != 0
	if t, err := time.Parse(time.RFC3339Nano, issuedStr); err == nil {
		g.IssuedAt = t
	}
	if expiresStr.Valid {
		t, err := time.Parse(time.RFC3339Nano, expiresStr.String)
		if err == nil {
			g.ExpiresAt = &t
		}
	}
	if revokedStr.Valid {
		t, err := time.Parse(time.RFC3339Nano, revokedStr.String)
		if err == nil {
			g.RevokedAt = &t
		}
	}
	return &g, nil
}

// --- Approvals ---------------------------------------------------------

// EnqueueApproval records a pending approval request. The check
// engine calls this when a Check produces DecisionApprovalRequired.
func (s *Store) EnqueueApproval(ctx context.Context, p PendingApproval) (*PendingApproval, error) {
	p.ID = s.nextID("apr-")
	p.State = ApprovalPending
	p.RequestedAt = time.Now().UTC()

	scopeJSON, err := encodeScopeJSON(p.AttemptedScope)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `
        INSERT INTO pending_approvals (
            id, grant_id, principal_kind, principal_id, capability,
            attempted_scope_json, rationale, state, requested_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.GrantID, string(p.Principal.Kind), p.Principal.ID, p.Capability,
		scopeJSON, nullableString(p.Rationale), string(p.State),
		p.RequestedAt.Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("permission: enqueueing approval: %w", err)
	}
	return &p, nil
}

// GetApproval returns a pending approval by id.
func (s *Store) GetApproval(ctx context.Context, id string) (*PendingApproval, error) {
	row := s.db.QueryRowContext(ctx, approvalSelectColumns+` WHERE id = ?`, id)
	a, err := scanApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrApprovalNotFound, id)
	}
	return a, err
}

// ListPendingApprovals returns pending approvals ordered oldest first.
func (s *Store) ListPendingApprovals(ctx context.Context) ([]PendingApproval, error) {
	rows, err := s.db.QueryContext(ctx,
		approvalSelectColumns+` WHERE state = ? ORDER BY requested_at ASC`,
		string(ApprovalPending))
	if err != nil {
		return nil, fmt.Errorf("permission: listing approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PendingApproval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ResolveApproval moves a pending approval to granted or denied.
// Returns ErrApprovalNotFound if the id doesn't exist;
// ErrApprovalAlreadyResolved if it's already left the pending state.
func (s *Store) ResolveApproval(ctx context.Context, id string, state ApprovalState, decidedBy, note string) error {
	if state != ApprovalGranted && state != ApprovalDenied {
		return fmt.Errorf("permission: invalid resolution state %q", state)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
        UPDATE pending_approvals
        SET state = ?, decided_at = ?, decided_by = ?, decision_note = ?
        WHERE id = ? AND state = ?`,
		string(state), now, decidedBy, nullableString(note),
		id, string(ApprovalPending))
	if err != nil {
		return fmt.Errorf("permission: resolving approval: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		var exists bool
		err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM pending_approvals WHERE id = ?)`, id,
		).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: %s", ErrApprovalNotFound, id)
		}
		return fmt.Errorf("%w: %s", ErrApprovalAlreadyResolved, id)
	}
	return nil
}

const approvalSelectColumns = `SELECT id, grant_id, principal_kind, principal_id, capability,
       attempted_scope_json, rationale, state, requested_at,
       decided_at, decided_by, decision_note
       FROM pending_approvals`

func scanApproval(r rowScanner) (*PendingApproval, error) {
	var (
		a                PendingApproval
		scopeJSON        sql.NullString
		rationale        sql.NullString
		principalKindStr string
		stateStr         string
		requestedStr     string
		decidedStr       sql.NullString
		decidedBy        sql.NullString
		decisionNote     sql.NullString
	)
	if err := r.Scan(&a.ID, &a.GrantID, &principalKindStr, &a.Principal.ID, &a.Capability,
		&scopeJSON, &rationale, &stateStr, &requestedStr,
		&decidedStr, &decidedBy, &decisionNote); err != nil {
		return nil, err
	}
	a.Principal.Kind = PrincipalKind(principalKindStr)
	a.State = ApprovalState(stateStr)
	if scopeJSON.Valid && scopeJSON.String != "" && scopeJSON.String != "null" {
		if err := json.Unmarshal([]byte(scopeJSON.String), &a.AttemptedScope); err != nil {
			return nil, fmt.Errorf("permission: decoding attempted_scope: %w", err)
		}
	}
	if rationale.Valid {
		a.Rationale = rationale.String
	}
	if t, err := time.Parse(time.RFC3339Nano, requestedStr); err == nil {
		a.RequestedAt = t
	}
	if decidedStr.Valid {
		t, err := time.Parse(time.RFC3339Nano, decidedStr.String)
		if err == nil {
			a.DecidedAt = &t
		}
	}
	if decidedBy.Valid {
		a.DecidedBy = decidedBy.String
	}
	if decisionNote.Valid {
		a.DecisionNote = decisionNote.String
	}
	return &a, nil
}

// --- Helpers -----------------------------------------------------------

func (s *Store) nextID(prefix string) string {
	s.ulidMu.Lock()
	defer s.ulidMu.Unlock()
	u := ulid.MustNew(ulid.Timestamp(time.Now().UTC()), s.ulidEnt)
	return prefix + u.String()
}

func nullableString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}

func encodeScopeJSON(scope map[string]any) (sql.NullString, error) {
	if len(scope) == 0 {
		return sql.NullString{}, nil
	}
	data, err := json.Marshal(scope)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("permission: encoding scope: %w", err)
	}
	return sql.NullString{String: string(data), Valid: true}, nil
}

// validateScope checks that every field in `scope` exists in the
// capability's `schema` and that the value's type is compatible with
// the declared primitive. v0.1 does loose validation — the engine in
// commit 2 will surface mismatch errors at check time. This is a
// "no surprise unknown fields" guard, not a deep type check.
func validateScope(scope map[string]any, schema ScopeSchema) error {
	for field := range scope {
		if _, ok := schema[field]; !ok {
			return fmt.Errorf("%w: unknown field %q", ErrScopeViolatesSchema, field)
		}
	}
	return nil
}

// --- Clients -----------------------------------------------------------

// InsertClient persists a Client. Caller is expected to have already
// assigned ID, CredentialHash, and PairedAt. Used internally by
// Engine.RedeemPairingCode (which is the only caller that should
// create new clients in production); exposed on the store for tests
// and future migration tooling.
func (s *Store) InsertClient(ctx context.Context, c Client) error {
	metaJSON, err := encodeMetadataJSON(c.Metadata)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
        INSERT INTO clients (
            id, name, credential_hash, metadata_json, paired_at
        ) VALUES (?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.CredentialHash, metaJSON,
		c.PairedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("permission: inserting client: %w", err)
	}
	return nil
}

// GetClient returns a client by id (regardless of revoked state),
// or ErrClientNotFound.
func (s *Store) GetClient(ctx context.Context, id string) (*Client, error) {
	row := s.db.QueryRowContext(ctx, clientSelectColumns+` WHERE id = ?`, id)
	c, err := scanClient(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrClientNotFound, id)
	}
	return c, err
}

// ListClients returns clients matching the filter, newest first.
// Status defaults to "active" if empty.
func (s *Store) ListClients(ctx context.Context, f ClientFilter) ([]Client, error) {
	var (
		conds []string
		args  []any
	)
	status := f.Status
	if status == "" {
		status = StatusActive
	}
	switch status {
	case StatusActive:
		conds = append(conds, "revoked_at IS NULL")
	case StatusRevoked:
		conds = append(conds, "revoked_at IS NOT NULL")
	case StatusAll:
		// no extra filter
	default:
		return nil, fmt.Errorf("permission: unknown client status filter %q", status)
	}

	q := clientSelectColumns
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY paired_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 1000
	}
	q += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("permission: listing clients: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// RevokeClient marks a client revoked. Idempotent (already-revoked is
// not an error). Does NOT cascade-revoke that client's grants on its
// own — Engine.RevokeClient handles the cascade plus audit; the
// store-level method exists for tests and explicit-use callers.
func (s *Store) RevokeClient(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE clients SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("permission: revoking client %s: %w", id, err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		var exists bool
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM clients WHERE id = ?)`, id,
		).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: %s", ErrClientNotFound, id)
		}
		// Already revoked — idempotent success.
	}
	return nil
}

// TouchClientLastSeen records a successful authentication. Called
// from Engine.AuthenticateClient; idempotent and best-effort (errors
// surface but do not invalidate the authentication).
func (s *Store) TouchClientLastSeen(ctx context.Context, id string, when time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE clients SET last_seen_at = ? WHERE id = ?`,
		when.UTC().Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("permission: touching client %s: %w", id, err)
	}
	return nil
}

const clientSelectColumns = `SELECT id, name, credential_hash, metadata_json,
       paired_at, last_seen_at, revoked_at
       FROM clients`

func scanClient(r rowScanner) (*Client, error) {
	var (
		c           Client
		metaStr     sql.NullString
		pairedStr   string
		lastSeenStr sql.NullString
		revokedStr  sql.NullString
	)
	if err := r.Scan(&c.ID, &c.Name, &c.CredentialHash, &metaStr,
		&pairedStr, &lastSeenStr, &revokedStr); err != nil {
		return nil, err
	}
	if metaStr.Valid && metaStr.String != "" && metaStr.String != "null" {
		if err := json.Unmarshal([]byte(metaStr.String), &c.Metadata); err != nil {
			return nil, fmt.Errorf("permission: decoding client metadata: %w", err)
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, pairedStr); err == nil {
		c.PairedAt = t
	}
	if lastSeenStr.Valid {
		if t, err := time.Parse(time.RFC3339Nano, lastSeenStr.String); err == nil {
			c.LastSeenAt = &t
		}
	}
	if revokedStr.Valid {
		if t, err := time.Parse(time.RFC3339Nano, revokedStr.String); err == nil {
			c.RevokedAt = &t
		}
	}
	return &c, nil
}

func encodeMetadataJSON(m map[string]any) (sql.NullString, error) {
	if len(m) == 0 {
		return sql.NullString{}, nil
	}
	data, err := json.Marshal(m)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("permission: encoding metadata: %w", err)
	}
	return sql.NullString{String: string(data), Valid: true}, nil
}

// --- Pairing codes -----------------------------------------------------

// InsertPairingCode persists a new code. Caller assigns Code,
// CreatedAt, and ExpiresAt. Returns an error if the code collides
// (vanishingly unlikely with a 40-bit code space and TTLs in
// minutes, but handled rather than panicked).
func (s *Store) InsertPairingCode(ctx context.Context, p PairingCode) error {
	if _, err := s.db.ExecContext(ctx, `
        INSERT INTO pairing_codes (
            code, client_name, created_by, created_at, expires_at
        ) VALUES (?, ?, ?, ?, ?)`,
		p.Code, p.ClientName, nullableString(p.CreatedBy),
		p.CreatedAt.UTC().Format(time.RFC3339Nano),
		p.ExpiresAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("permission: inserting pairing code: %w", err)
	}
	return nil
}

// GetPairingCode returns a code's record by its string value. Useful
// for inspection; redemption goes through MarkPairingCodeRedeemed
// which does an atomic check-and-set.
func (s *Store) GetPairingCode(ctx context.Context, code string) (*PairingCode, error) {
	row := s.db.QueryRowContext(ctx, pairingCodeSelectColumns+` WHERE code = ?`, code)
	p, err := scanPairingCode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrPairingCodeNotFound, code)
	}
	return p, err
}

// MarkPairingCodeRedeemed atomically validates and consumes a code.
// On success returns the (now-redeemed) code row. Errors:
// ErrPairingCodeNotFound, ErrPairingCodeExpired,
// ErrPairingCodeAlreadyRedeemed.
//
// The atomic UPDATE ... WHERE redeemed_at IS NULL AND expires_at > ?
// pattern means two concurrent redemption attempts can't both win.
func (s *Store) MarkPairingCodeRedeemed(ctx context.Context, code, clientID string, now time.Time) (*PairingCode, error) {
	nowStr := now.UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `
        UPDATE pairing_codes
        SET redeemed_at = ?, redeemed_client_id = ?
        WHERE code = ? AND redeemed_at IS NULL AND expires_at > ?`,
		nowStr, clientID, code, nowStr,
	)
	if err != nil {
		return nil, fmt.Errorf("permission: redeeming code: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		// Disambiguate: missing / expired / already-redeemed.
		p, err := s.GetPairingCode(ctx, code)
		if err != nil {
			return nil, err
		}
		if p.RedeemedAt != nil {
			return nil, fmt.Errorf("%w: %s", ErrPairingCodeAlreadyRedeemed, code)
		}
		return nil, fmt.Errorf("%w: %s", ErrPairingCodeExpired, code)
	}
	return s.GetPairingCode(ctx, code)
}

const pairingCodeSelectColumns = `SELECT code, client_name, created_by, created_at,
       expires_at, redeemed_at, redeemed_client_id
       FROM pairing_codes`

func scanPairingCode(r rowScanner) (*PairingCode, error) {
	var (
		p             PairingCode
		createdBy     sql.NullString
		createdStr    string
		expiresStr    string
		redeemedStr   sql.NullString
		redeemedIDStr sql.NullString
	)
	if err := r.Scan(&p.Code, &p.ClientName, &createdBy, &createdStr,
		&expiresStr, &redeemedStr, &redeemedIDStr); err != nil {
		return nil, err
	}
	if createdBy.Valid {
		p.CreatedBy = createdBy.String
	}
	if t, err := time.Parse(time.RFC3339Nano, createdStr); err == nil {
		p.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, expiresStr); err == nil {
		p.ExpiresAt = t
	}
	if redeemedStr.Valid {
		if t, err := time.Parse(time.RFC3339Nano, redeemedStr.String); err == nil {
			p.RedeemedAt = &t
		}
	}
	if redeemedIDStr.Valid {
		p.RedeemedClientID = redeemedIDStr.String
	}
	return &p, nil
}
