package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/loamss/loamss/runtime/internal/database"
)

// Store is the SQLite-backed persistence for the memory layer's
// derived state: entities, threads, and the mappings that join them
// to memory entries.
//
// Lives in runtime.db alongside the permission, capsule, and source
// stores. Each owns its own tables (memory_layer_*) and migration
// history (memory_layer_schema_migrations).
//
// Cross-process safety: SQLite's write lock serializes writes from
// concurrent CLI invocations + the daemon. Within a single process,
// the Store's mu serializes inserts so monotonic ULIDs stay
// monotonic.
type Store struct {
	db     *database.DB       // wraps *sql.DB; rebinds ? → $N for postgres
	dbMeta *database.Database // owning handle when ownsDB; borrowed when not
	ownsDB bool
	path   string

	mu sync.Mutex

	ulidMu  sync.Mutex
	ulidEnt *ulid.MonotonicEntropy
}

// OpenStore opens the memory layer's store at a filesystem path.
// Convenience wrapper around OpenStoreWith for the single-subsystem
// case; callers sharing one runtime.db across multiple subsystems
// (start.go pattern) should use OpenStoreWith.
func OpenStore(ctx context.Context, path string) (*Store, error) {
	db, err := database.OpenSQLite(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("memory layer: %w", err)
	}
	s, err := OpenStoreWith(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s.ownsDB = true
	return s, nil
}

// OpenStoreWith creates a memory layer Store on top of an already-open
// Database. The caller retains ownership of the Database.
func OpenStoreWith(ctx context.Context, db *database.Database) (*Store, error) {
	if db == nil || db.Conn() == nil {
		return nil, errors.New("memory layer: OpenStoreWith requires a non-nil Database")
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

// Close releases the database handle.
// Close releases the database handle if this Store opened it. Stores
// constructed via OpenStoreWith do not own the database; Close is a
// no-op for the connection in that case.
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

// --- schema -----------------------------------------------------------

var migrationsSQLite = []string{
	// 1: initial entities + threads + mapping tables.
	`
CREATE TABLE IF NOT EXISTS memory_layer_entities (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    canonical     TEXT NOT NULL,
    namespace     TEXT NOT NULL,
    aliases_json  TEXT NOT NULL,
    first_seen    TEXT NOT NULL,
    last_seen     TEXT NOT NULL,
    entry_count   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_mle_kind ON memory_layer_entities(kind);
CREATE INDEX IF NOT EXISTS idx_mle_ns ON memory_layer_entities(namespace);
CREATE INDEX IF NOT EXISTS idx_mle_canon ON memory_layer_entities(canonical);

CREATE TABLE IF NOT EXISTS memory_layer_aliases (
    alias        TEXT NOT NULL,
    alias_kind   TEXT NOT NULL,
    namespace    TEXT NOT NULL,
    entity_id    TEXT NOT NULL,
    PRIMARY KEY (alias, alias_kind, namespace),
    FOREIGN KEY (entity_id) REFERENCES memory_layer_entities(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_mla_entity ON memory_layer_aliases(entity_id);

CREATE TABLE IF NOT EXISTS memory_layer_threads (
    id           TEXT PRIMARY KEY,
    namespace    TEXT NOT NULL,
    external_id  TEXT NOT NULL,
    subject      TEXT,
    first_seen   TEXT NOT NULL,
    last_seen    TEXT NOT NULL,
    entry_count  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (namespace, external_id)
);
CREATE INDEX IF NOT EXISTS idx_mlt_ns ON memory_layer_threads(namespace);

CREATE TABLE IF NOT EXISTS memory_layer_entity_entries (
    entity_id    TEXT NOT NULL,
    namespace    TEXT NOT NULL,
    entry_id     TEXT NOT NULL,
    role         TEXT NOT NULL,
    entry_date   TEXT,
    PRIMARY KEY (entity_id, namespace, entry_id, role),
    FOREIGN KEY (entity_id) REFERENCES memory_layer_entities(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_mlee_entry ON memory_layer_entity_entries(namespace, entry_id);

CREATE TABLE IF NOT EXISTS memory_layer_thread_entries (
    thread_id    TEXT NOT NULL,
    namespace    TEXT NOT NULL,
    entry_id     TEXT NOT NULL,
    entry_date   TEXT,
    PRIMARY KEY (thread_id, namespace, entry_id),
    FOREIGN KEY (thread_id) REFERENCES memory_layer_threads(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_mlte_entry ON memory_layer_thread_entries(namespace, entry_id);
`,
}

// migrationsPostgres uses TEXT for timestamps; see migrationsPostgres
// in permission/store.go for the rationale.
var migrationsPostgres = []string{
	// 1: initial entities + threads + mapping tables.
	`
CREATE TABLE IF NOT EXISTS memory_layer_entities (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL,
    canonical     TEXT NOT NULL,
    namespace     TEXT NOT NULL,
    aliases_json  TEXT NOT NULL,
    first_seen    TEXT NOT NULL,
    last_seen     TEXT NOT NULL,
    entry_count   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_mle_kind ON memory_layer_entities(kind);
CREATE INDEX IF NOT EXISTS idx_mle_ns ON memory_layer_entities(namespace);
CREATE INDEX IF NOT EXISTS idx_mle_canon ON memory_layer_entities(canonical);

CREATE TABLE IF NOT EXISTS memory_layer_aliases (
    alias        TEXT NOT NULL,
    alias_kind   TEXT NOT NULL,
    namespace    TEXT NOT NULL,
    entity_id    TEXT NOT NULL,
    PRIMARY KEY (alias, alias_kind, namespace),
    FOREIGN KEY (entity_id) REFERENCES memory_layer_entities(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_mla_entity ON memory_layer_aliases(entity_id);

CREATE TABLE IF NOT EXISTS memory_layer_threads (
    id           TEXT PRIMARY KEY,
    namespace    TEXT NOT NULL,
    external_id  TEXT NOT NULL,
    subject      TEXT,
    first_seen   TEXT NOT NULL,
    last_seen    TEXT NOT NULL,
    entry_count  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (namespace, external_id)
);
CREATE INDEX IF NOT EXISTS idx_mlt_ns ON memory_layer_threads(namespace);

CREATE TABLE IF NOT EXISTS memory_layer_entity_entries (
    entity_id    TEXT NOT NULL,
    namespace    TEXT NOT NULL,
    entry_id     TEXT NOT NULL,
    role         TEXT NOT NULL,
    entry_date   TEXT,
    PRIMARY KEY (entity_id, namespace, entry_id, role),
    FOREIGN KEY (entity_id) REFERENCES memory_layer_entities(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_mlee_entry ON memory_layer_entity_entries(namespace, entry_id);

CREATE TABLE IF NOT EXISTS memory_layer_thread_entries (
    thread_id    TEXT NOT NULL,
    namespace    TEXT NOT NULL,
    entry_id     TEXT NOT NULL,
    entry_date   TEXT,
    PRIMARY KEY (thread_id, namespace, entry_id),
    FOREIGN KEY (thread_id) REFERENCES memory_layer_threads(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_mlte_entry ON memory_layer_thread_entries(namespace, entry_id);
`,
}

func migrationsFor(driver database.Driver) []string {
	if driver == database.DriverPostgres {
		return migrationsPostgres
	}
	return migrationsSQLite
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS memory_layer_schema_migrations (
            version    INTEGER PRIMARY KEY,
            applied_at TEXT NOT NULL
        )`); err != nil {
		return fmt.Errorf("memory layer: creating schema_migrations: %w", err)
	}
	var current int
	row := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM memory_layer_schema_migrations`)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("memory layer: reading migration version: %w", err)
	}
	for i, sqlText := range migrationsFor(s.db.Driver()) {
		v := i + 1
		if v <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("memory layer: begin migration tx: %w", err)
		}
		if _, err := tx.ExecContext(ctx, sqlText); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("memory layer: applying migration %d: %w", v, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_layer_schema_migrations (version, applied_at) VALUES (?, ?)`,
			v, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("memory layer: recording migration %d: %w", v, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("memory layer: commit migration %d: %w", v, err)
		}
	}
	return nil
}

// --- ID generation ----------------------------------------------------

func (s *Store) entityID() string {
	s.ulidMu.Lock()
	defer s.ulidMu.Unlock()
	u := ulid.MustNew(ulid.Timestamp(time.Now().UTC()), s.ulidEnt)
	return "ent_" + u.String()
}

func (s *Store) threadID() string {
	s.ulidMu.Lock()
	defer s.ulidMu.Unlock()
	u := ulid.MustNew(ulid.Timestamp(time.Now().UTC()), s.ulidEnt)
	return "thr_" + u.String()
}

// --- entities ---------------------------------------------------------

// UpsertEntity inserts a new entity or updates an existing one.
// Returns the resulting entity (id assigned by the store if absent).
//
// Matching strategy: an entity is identified by (namespace, primary
// alias) where the primary alias is the first email-kind alias.
// Callers pass aliases in priority order — the first alias listed
// is used for lookup.
func (s *Store) UpsertEntity(ctx context.Context, e Entity) (*Entity, error) {
	if e.Namespace == "" {
		return nil, errors.New("memory layer: Namespace required")
	}
	if e.Kind == "" {
		return nil, errors.New("memory layer: Kind required")
	}
	if len(e.Aliases) == 0 {
		return nil, errors.New("memory layer: at least one Alias required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsertEntityLocked(ctx, e)
}

func (s *Store) upsertEntityLocked(ctx context.Context, e Entity) (*Entity, error) {
	// 1. Try to find an existing entity via any of the supplied aliases.
	var existingID string
	for _, a := range e.Aliases {
		row := s.db.QueryRowContext(ctx,
			`SELECT entity_id FROM memory_layer_aliases
                 WHERE alias = ? AND alias_kind = ? AND namespace = ?`,
			a.Value, string(a.Kind), e.Namespace)
		var id string
		if err := row.Scan(&id); err == nil {
			existingID = id
			break
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("memory layer: alias lookup: %w", err)
		}
	}

	now := time.Now().UTC()
	if e.FirstSeen.IsZero() {
		e.FirstSeen = now
	}
	if e.LastSeen.IsZero() {
		e.LastSeen = now
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("memory layer: begin entity tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if existingID == "" {
		if e.ID == "" {
			e.ID = s.entityID()
		}
		aliasesJSON, _ := json.Marshal(e.Aliases)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO memory_layer_entities (
                id, kind, canonical, namespace, aliases_json,
                first_seen, last_seen, entry_count
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, string(e.Kind), e.Canonical, e.Namespace,
			string(aliasesJSON),
			e.FirstSeen.Format(time.RFC3339Nano),
			e.LastSeen.Format(time.RFC3339Nano),
			e.EntryCount,
		); err != nil {
			return nil, fmt.Errorf("memory layer: insert entity: %w", err)
		}
	} else {
		// Merge the new aliases / canonical / time range into the
		// existing row. The canonical name updates if the incoming
		// one is non-empty and the stored one was a fallback (an
		// email local-part — heuristic: no space).
		var existing Entity
		if err := s.scanEntityLocked(tx, existingID, &existing); err != nil {
			return nil, err
		}
		merged := mergeEntity(existing, e)
		aliasesJSON, _ := json.Marshal(merged.Aliases)
		if _, err := tx.ExecContext(ctx, `
            UPDATE memory_layer_entities
                SET kind = ?, canonical = ?, aliases_json = ?,
                    first_seen = ?, last_seen = ?
                WHERE id = ?`,
			string(merged.Kind), merged.Canonical, string(aliasesJSON),
			merged.FirstSeen.Format(time.RFC3339Nano),
			merged.LastSeen.Format(time.RFC3339Nano),
			merged.ID,
		); err != nil {
			return nil, fmt.Errorf("memory layer: update entity: %w", err)
		}
		e = merged
	}

	// 2. Idempotent-insert each alias.
	for _, a := range e.Aliases {
		if _, err := tx.ExecContext(ctx, `
            INSERT OR IGNORE INTO memory_layer_aliases
                (alias, alias_kind, namespace, entity_id)
                VALUES (?, ?, ?, ?)`,
			a.Value, string(a.Kind), e.Namespace, e.ID,
		); err != nil {
			return nil, fmt.Errorf("memory layer: insert alias: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memory layer: commit entity: %w", err)
	}
	return &e, nil
}

// GetEntity returns an entity by id. Returns ErrEntityNotFound if absent.
func (s *Store) GetEntity(ctx context.Context, id string) (*Entity, error) {
	row := s.db.QueryRowContext(ctx, entitySelect+` WHERE id = ?`, id)
	e, err := scanEntity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrEntityNotFound, id)
	}
	return e, err
}

// ListEntities returns entities matching the filter, newest-last-seen
// first.
func (s *Store) ListEntities(ctx context.Context, filter EntityFilter) ([]Entity, error) {
	limit := clampLimit(filter.Limit)
	where := []string{}
	args := []any{}
	if filter.Namespace != "" {
		where = append(where, "namespace = ?")
		args = append(args, filter.Namespace)
	}
	if filter.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, string(filter.Kind))
	}
	if filter.Alias != "" {
		where = append(where,
			"id IN (SELECT entity_id FROM memory_layer_aliases WHERE alias = ?)")
		args = append(args, filter.Alias)
	}
	q := entitySelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY last_seen DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory layer: list entities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Entity
	for rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// --- threads ----------------------------------------------------------

// UpsertThread inserts a new thread or updates an existing one. Looks
// up by (Namespace, ExternalID).
func (s *Store) UpsertThread(ctx context.Context, t Thread) (*Thread, error) {
	if t.Namespace == "" || t.ExternalID == "" {
		return nil, errors.New("memory layer: Namespace + ExternalID required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if t.FirstSeen.IsZero() {
		t.FirstSeen = now
	}
	if t.LastSeen.IsZero() {
		t.LastSeen = now
	}

	// Lookup first.
	row := s.db.QueryRowContext(ctx,
		threadSelect+` WHERE namespace = ? AND external_id = ?`,
		t.Namespace, t.ExternalID)
	existing, err := scanThread(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if t.ID == "" {
			t.ID = s.threadID()
		}
		if _, err := s.db.ExecContext(ctx, `
            INSERT INTO memory_layer_threads
                (id, namespace, external_id, subject, first_seen, last_seen, entry_count)
                VALUES (?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Namespace, t.ExternalID, t.Subject,
			t.FirstSeen.Format(time.RFC3339Nano),
			t.LastSeen.Format(time.RFC3339Nano),
			t.EntryCount,
		); err != nil {
			return nil, fmt.Errorf("memory layer: insert thread: %w", err)
		}
		return &t, nil
	case err != nil:
		return nil, err
	default:
		// Merge.
		if t.Subject == "" {
			t.Subject = existing.Subject
		}
		first := existing.FirstSeen
		if t.FirstSeen.Before(first) || first.IsZero() {
			first = t.FirstSeen
		}
		last := existing.LastSeen
		if t.LastSeen.After(last) {
			last = t.LastSeen
		}
		if _, err := s.db.ExecContext(ctx, `
            UPDATE memory_layer_threads
                SET subject = ?, first_seen = ?, last_seen = ?
                WHERE id = ?`,
			t.Subject,
			first.Format(time.RFC3339Nano),
			last.Format(time.RFC3339Nano),
			existing.ID,
		); err != nil {
			return nil, fmt.Errorf("memory layer: update thread: %w", err)
		}
		existing.Subject = t.Subject
		existing.FirstSeen = first
		existing.LastSeen = last
		return existing, nil
	}
}

// GetThread returns a thread by id.
func (s *Store) GetThread(ctx context.Context, id string) (*Thread, error) {
	row := s.db.QueryRowContext(ctx, threadSelect+` WHERE id = ?`, id)
	t, err := scanThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrThreadNotFound, id)
	}
	return t, err
}

// ListThreads returns threads matching the filter, newest-last-seen first.
func (s *Store) ListThreads(ctx context.Context, filter ThreadFilter) ([]Thread, error) {
	limit := clampLimit(filter.Limit)
	q := threadSelect
	args := []any{}
	if filter.Namespace != "" {
		q += " WHERE namespace = ?"
		args = append(args, filter.Namespace)
	}
	q += " ORDER BY last_seen DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory layer: list threads: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// --- mappings ---------------------------------------------------------

// LinkEntityEntry records that entry (namespace, entry_id) involves
// entity_id in the given role. Idempotent.
func (s *Store) LinkEntityEntry(ctx context.Context, entityID, namespace, entryID string, role EntryRole, entryDate time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var date sql.NullString
	if !entryDate.IsZero() {
		date = sql.NullString{Valid: true, String: entryDate.UTC().Format(time.RFC3339Nano)}
	}
	if _, err := s.db.ExecContext(ctx, `
        INSERT OR IGNORE INTO memory_layer_entity_entries
            (entity_id, namespace, entry_id, role, entry_date)
            VALUES (?, ?, ?, ?, ?)`,
		entityID, namespace, entryID, string(role), date,
	); err != nil {
		return fmt.Errorf("memory layer: link entity entry: %w", err)
	}
	// Recompute entry_count for the entity. Cheap because index on
	// (entity_id) — and we run this only on Upsert, not on query.
	if _, err := s.db.ExecContext(ctx, `
        UPDATE memory_layer_entities
            SET entry_count = (
                SELECT COUNT(DISTINCT entry_id || ':' || namespace)
                FROM memory_layer_entity_entries
                WHERE entity_id = ?
            )
            WHERE id = ?`,
		entityID, entityID,
	); err != nil {
		return fmt.Errorf("memory layer: refresh entity entry_count: %w", err)
	}
	return nil
}

// LinkThreadEntry records that entry (namespace, entry_id) belongs to
// thread_id. Idempotent.
func (s *Store) LinkThreadEntry(ctx context.Context, threadID, namespace, entryID string, entryDate time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var date sql.NullString
	if !entryDate.IsZero() {
		date = sql.NullString{Valid: true, String: entryDate.UTC().Format(time.RFC3339Nano)}
	}
	if _, err := s.db.ExecContext(ctx, `
        INSERT OR IGNORE INTO memory_layer_thread_entries
            (thread_id, namespace, entry_id, entry_date)
            VALUES (?, ?, ?, ?)`,
		threadID, namespace, entryID, date,
	); err != nil {
		return fmt.Errorf("memory layer: link thread entry: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
        UPDATE memory_layer_threads
            SET entry_count = (
                SELECT COUNT(*) FROM memory_layer_thread_entries WHERE thread_id = ?
            )
            WHERE id = ?`,
		threadID, threadID,
	); err != nil {
		return fmt.Errorf("memory layer: refresh thread entry_count: %w", err)
	}
	return nil
}

// UnlinkEntry removes all entity + thread mappings for (namespace,
// entry_id). Used by Delete on the layer.
func (s *Store) UnlinkEntry(ctx context.Context, namespace, entryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Capture affected entity + thread ids first so we can refresh
	// their entry_counts after the delete.
	entityIDs, err := s.queryIDs(ctx,
		`SELECT DISTINCT entity_id FROM memory_layer_entity_entries
           WHERE namespace = ? AND entry_id = ?`,
		namespace, entryID)
	if err != nil {
		return err
	}
	threadIDs, err := s.queryIDs(ctx,
		`SELECT DISTINCT thread_id FROM memory_layer_thread_entries
           WHERE namespace = ? AND entry_id = ?`,
		namespace, entryID)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM memory_layer_entity_entries WHERE namespace = ? AND entry_id = ?`,
		namespace, entryID); err != nil {
		return fmt.Errorf("memory layer: unlink entity entries: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM memory_layer_thread_entries WHERE namespace = ? AND entry_id = ?`,
		namespace, entryID); err != nil {
		return fmt.Errorf("memory layer: unlink thread entries: %w", err)
	}
	for _, id := range entityIDs {
		if _, err := s.db.ExecContext(ctx, `
            UPDATE memory_layer_entities
                SET entry_count = (
                    SELECT COUNT(DISTINCT entry_id || ':' || namespace)
                    FROM memory_layer_entity_entries WHERE entity_id = ?
                ) WHERE id = ?`,
			id, id,
		); err != nil {
			return err
		}
	}
	for _, id := range threadIDs {
		if _, err := s.db.ExecContext(ctx, `
            UPDATE memory_layer_threads
                SET entry_count = (
                    SELECT COUNT(*) FROM memory_layer_thread_entries WHERE thread_id = ?
                ) WHERE id = ?`,
			id, id,
		); err != nil {
			return err
		}
	}
	return nil
}

// EntriesByEntity returns entry refs for an entity, newest-first.
func (s *Store) EntriesByEntity(ctx context.Context, entityID string, limit int) ([]EntryRef, error) {
	limit = clampLimit(limit)
	rows, err := s.db.QueryContext(ctx, `
        SELECT namespace, entry_id, role, entry_date
        FROM memory_layer_entity_entries
        WHERE entity_id = ?
        ORDER BY COALESCE(entry_date, '0') DESC, entry_id DESC
        LIMIT ?`, entityID, limit)
	if err != nil {
		return nil, fmt.Errorf("memory layer: EntriesByEntity: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []EntryRef
	for rows.Next() {
		var (
			ns, id, role string
			date         sql.NullString
		)
		if err := rows.Scan(&ns, &id, &role, &date); err != nil {
			return nil, err
		}
		ref := EntryRef{Namespace: ns, ID: id, Role: EntryRole(role)}
		if date.Valid {
			if t, err := time.Parse(time.RFC3339Nano, date.String); err == nil {
				ref.Date = t
			}
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// EntriesByThread returns entry refs for a thread, oldest-first
// (natural reading order for a conversation).
func (s *Store) EntriesByThread(ctx context.Context, threadID string, limit int) ([]EntryRef, error) {
	limit = clampLimit(limit)
	rows, err := s.db.QueryContext(ctx, `
        SELECT namespace, entry_id, entry_date
        FROM memory_layer_thread_entries
        WHERE thread_id = ?
        ORDER BY COALESCE(entry_date, '0') ASC, entry_id ASC
        LIMIT ?`, threadID, limit)
	if err != nil {
		return nil, fmt.Errorf("memory layer: EntriesByThread: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []EntryRef
	for rows.Next() {
		var (
			ns, id string
			date   sql.NullString
		)
		if err := rows.Scan(&ns, &id, &date); err != nil {
			return nil, err
		}
		ref := EntryRef{Namespace: ns, ID: id}
		if date.Valid {
			if t, err := time.Parse(time.RFC3339Nano, date.String); err == nil {
				ref.Date = t
			}
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// --- scanners ---------------------------------------------------------

const entitySelect = `SELECT id, kind, canonical, namespace, aliases_json,
    first_seen, last_seen, entry_count FROM memory_layer_entities`

const threadSelect = `SELECT id, namespace, external_id, subject,
    first_seen, last_seen, entry_count FROM memory_layer_threads`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEntity(r rowScanner) (*Entity, error) {
	var (
		e           Entity
		kind        string
		aliasesJSON string
		firstStr    string
		lastStr     string
	)
	if err := r.Scan(&e.ID, &kind, &e.Canonical, &e.Namespace,
		&aliasesJSON, &firstStr, &lastStr, &e.EntryCount); err != nil {
		return nil, err
	}
	e.Kind = EntityKind(kind)
	if err := json.Unmarshal([]byte(aliasesJSON), &e.Aliases); err != nil {
		return nil, fmt.Errorf("memory layer: decoding aliases: %w", err)
	}
	if t, err := time.Parse(time.RFC3339Nano, firstStr); err == nil {
		e.FirstSeen = t
	}
	if t, err := time.Parse(time.RFC3339Nano, lastStr); err == nil {
		e.LastSeen = t
	}
	return &e, nil
}

func scanThread(r rowScanner) (*Thread, error) {
	var (
		t        Thread
		subject  sql.NullString
		firstStr string
		lastStr  string
	)
	if err := r.Scan(&t.ID, &t.Namespace, &t.ExternalID, &subject,
		&firstStr, &lastStr, &t.EntryCount); err != nil {
		return nil, err
	}
	if subject.Valid {
		t.Subject = subject.String
	}
	if pt, err := time.Parse(time.RFC3339Nano, firstStr); err == nil {
		t.FirstSeen = pt
	}
	if pt, err := time.Parse(time.RFC3339Nano, lastStr); err == nil {
		t.LastSeen = pt
	}
	return &t, nil
}

// scanEntityLocked reads an entity by id inside a transaction. Used
// by upsertEntityLocked to merge against an existing row without
// taking another connection.
func (s *Store) scanEntityLocked(tx *database.Tx, id string, e *Entity) error {
	row := tx.QueryRowContext(context.Background(),
		entitySelect+` WHERE id = ?`, id)
	got, err := scanEntity(row)
	if err != nil {
		return err
	}
	*e = *got
	return nil
}

func (s *Store) queryIDs(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --- helpers ----------------------------------------------------------

// mergeEntity returns the merge of existing + incoming. Canonical
// updates only when the existing canonical looks like a fallback
// (single token, no space) and the incoming canonical looks like a
// real name (contains a space or a non-ASCII letter).
func mergeEntity(existing, incoming Entity) Entity {
	merged := existing

	// Update canonical when:
	//   - existing canonical is empty, OR
	//   - existing looks like an email local-part (no space), and
	//     incoming looks like a real name (has a space)
	if shouldUpgradeCanonical(existing.Canonical, incoming.Canonical) {
		merged.Canonical = incoming.Canonical
	}

	// Union of aliases. Preserve insertion order: existing first,
	// new ones at the end.
	seen := make(map[string]bool, len(existing.Aliases))
	for _, a := range existing.Aliases {
		seen[a.Value+":"+string(a.Kind)] = true
	}
	for _, a := range incoming.Aliases {
		key := a.Value + ":" + string(a.Kind)
		if !seen[key] {
			merged.Aliases = append(merged.Aliases, a)
			seen[key] = true
		}
	}

	// Time range: take earliest first_seen and latest last_seen.
	if !incoming.FirstSeen.IsZero() &&
		(merged.FirstSeen.IsZero() || incoming.FirstSeen.Before(merged.FirstSeen)) {
		merged.FirstSeen = incoming.FirstSeen
	}
	if !incoming.LastSeen.IsZero() && incoming.LastSeen.After(merged.LastSeen) {
		merged.LastSeen = incoming.LastSeen
	}

	return merged
}

func shouldUpgradeCanonical(existing, incoming string) bool {
	if incoming == "" {
		return false
	}
	if existing == "" {
		return true
	}
	if existing == incoming {
		return false
	}
	// Existing has no space → looks like email local-part.
	// Incoming has space → looks like a real name.
	if !strings.Contains(existing, " ") && strings.Contains(incoming, " ") {
		return true
	}
	return false
}
