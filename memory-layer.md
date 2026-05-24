# Memory Layer Specification v0.1 (draft)

This document defines the **memory layer** — the runtime component that turns raw memory entries (what sources write) into entity-resolved views (what capsules and apps consume). Where the [memory adapter](adapter-interface.md#memory-adapter) is vector-and-metadata storage, the layer is the brain: who's mentioned, what they discussed, how the pieces relate.

> **Status: draft.** The Go SPI and SQLite schema will change before v1.0. Capsule and app authors targeting a pre-v1.0 spec should expect breaking changes.

## What this document is and is not

This spec defines:

- What the layer derives from raw memory entries, and how
- The write path: how entries flow through the layer into the adapter
- The read APIs: entities, threads, and the reverse lookups
- The MCP tool surface paired clients and capsules use
- The CLI surface for inspection
- The trust + audit model
- What's in v0.1 and what's deferred

This spec does **not** define:

- The wire protocols for backends (SQLite, pgvector, etc.) — that's the [memory adapter SPI](adapter-interface.md#memory-adapter)
- How entries get *into* the layer in the first place — that's the [source connector spec](sources.md)
- Embedding generation — that's the [model adapter](adapter-interface.md#model-adapter)
- Permission scope projection — covered in [`permission-model.md`](permission-model.md)

The companion code is `runtime/internal/memory/`. The Go interface (`memory.Layer`) is the authoritative contract; this document is the spec it implements.

## Where the layer sits

```
┌────────────────────────────────────────────────────────────────┐
│  Source connectors (source:gmail, source:calendar, …)         │
│  Write source-shaped entries with provider-specific metadata. │
└─────────────────────────────┬──────────────────────────────────┘
                              │ layer.Upsert(entry)
┌─────────────────────────────▼──────────────────────────────────┐
│  MEMORY LAYER                                                  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ Entity resolver  →  Entity (person, organization)        │  │
│  │   Inputs:  from / to / cc / bcc / mention                │  │
│  │   Aliases: email, name (extensible)                      │  │
│  ├──────────────────────────────────────────────────────────┤  │
│  │ Thread extractor →  Thread (Gmail thread_id today;       │  │
│  │   future: Slack thread_ts, calendar event series…)       │  │
│  ├──────────────────────────────────────────────────────────┤  │
│  │ Mapping tables: which entries involve which entities +   │  │
│  │ threads, with role (from / to / cc / bcc) preserved.     │  │
│  └────┬─────────────────────────────────────────┬───────────┘  │
└───────┼─────────────────────────────────────────┼──────────────┘
        │                                         │
        ▼                                         ▼
┌────────────────────┐                ┌────────────────────────┐
│ Memory adapter     │                │ runtime.db             │
│ (sqlite, pgvector) │                │ memory_layer_* tables  │
│ vectors + metadata │                │ entities / threads /   │
│                    │                │ aliases / mappings     │
└─────────▲──────────┘                └────────────▲───────────┘
          │                                        │
          │ memory.query / memory.show             │ entities.* / threads.*
          │ (semantic search)                      │ (derived views)
          │                                        │
          └────────────────────┬───────────────────┘
                               │ MCP
┌──────────────────────────────▼───────────────────┐
│  Paired clients + capsules                       │
│  Read both surfaces; choose by use case.         │
└──────────────────────────────────────────────────┘
```

Two principles fall out of the picture:

1. **The vector adapter is the source of truth for entries.** Layer tables are a derived index — they can be rebuilt from the adapter. (Today rebuild is on-write only; bulk rebuild is Phase 2.)
2. **Entries and entities have different read paths.** Vector search asks "what's *like* this query?"; entity search asks "who's *been involved in* what?" Both query the same writes; they answer different questions.

## Data model

### Entry

The input shape `layer.Upsert(entry)` accepts. Same fields the source adapter passes through.

```go
type Entry struct {
    Namespace  string             // user's source handle (e.g. "gmail-personal")
    ID         string             // stable external id from the source
    Content    string             // human-readable content
    Metadata   map[string]any     // opaque, but shaped (see below)
    Embeddings []float32          // vector; optional (v0.1 graceful skip)
}
```

The layer reads these metadata fields when present:

| Key | Used for |
| --- | --- |
| `from` | Entity (role = `from`) — RFC822 header |
| `to` | Entity (role = `to`) — RFC822 header |
| `cc` | Entity (role = `cc`) |
| `bcc` | Entity (role = `bcc`) |
| `subject` | Thread label |
| `gmail_thread_id` | Thread external id |
| `internal_date` | Entry date (RFC3339); preferred |
| `date_header` | Fallback when no `internal_date` |

Other fields are stored verbatim by the adapter; the layer ignores them. New resolvers (Slack, Calendar) read additional metadata keys without invalidating these.

### Entity

A thing — person, organization — derived from one or more entries.

```go
type Entity struct {
    ID         string      // "ent_01H..." (ULID)
    Kind       EntityKind  // "person" | "organization"
    Canonical  string      // human-readable label
    Namespace  string      // memory namespace it lives in
    Aliases    []Alias     // every identifier ever seen for it
    FirstSeen  time.Time   // earliest entry date
    LastSeen   time.Time   // most recent entry date
    EntryCount int64       // distinct entries linked to this entity
}
```

**Canonical name semantics:**

- For a person with a display name in any `From:` header, canonical is that name.
- If only the email address is ever seen, canonical falls back to the local-part (`alice@example.com` → `"alice"`).
- Subsequent entries can **upgrade** the canonical: a fallback (no space, looks like a local-part) is replaced when a real name (contains a space) arrives. Real names are not downgraded back to local-parts.

This rule is the entire entity-resolution heuristic in v0.1. It matches the observation that real email always eventually surfaces a name.

### Alias

```go
type Alias struct {
    Value string    // "sarah@example.com" or "Sarah Smith"
    Kind  AliasKind // "email" | "name" | "domain"
}
```

Aliases are how the layer recognizes "this entry's `from`" as "the same person as that entry's `to`." Email is the primary key for v0.1: same lowercased email → same entity. Names accumulate as supplementary aliases.

Email is the primary alias because:

1. It's a globally unique identifier per provider.
2. Names are noisy (people change them, providers truncate them, MIME encodes them).
3. Cross-source merging (Phase 2) will use email as the join key.

### Thread

A conversation grouping derived from source-specific identifiers.

```go
type Thread struct {
    ID         string    // "thr_01H..." (ULID)
    Namespace  string    // memory namespace it lives in
    ExternalID string    // gmail_thread_id today
    Subject    string    // first non-empty subject seen
    FirstSeen  time.Time // earliest entry date in the thread
    LastSeen   time.Time // most recent
    EntryCount int64     // number of entries in the thread
}
```

Threads have stable layer-assigned IDs across re-syncs. The `(Namespace, ExternalID)` pair is unique — re-importing the same data doesn't create a duplicate thread.

### EntryRef

A lightweight pointer returned by reverse lookups (`EntriesByEntity` / `EntriesByThread`). The layer doesn't carry the entry's content + vector through these calls — callers fetch via the memory adapter when they need the full payload.

```go
type EntryRef struct {
    Namespace string
    ID        string
    Role      EntryRole    // populated by EntriesByEntity (from / to / cc / bcc / mention)
    Date      time.Time    // entry's "happened-at" timestamp
    // Subject / Snippet / Metadata are reserved for future use; v0.1 leaves them empty
}
```

`EntryRole`:

```go
const (
    RoleFrom    EntryRole = "from"
    RoleTo      EntryRole = "to"
    RoleCC      EntryRole = "cc"
    RoleBCC     EntryRole = "bcc"
    RoleMention EntryRole = "mention"   // reserved; not emitted in v0.1
)
```

## Storage shape

All layer state lives in `runtime.db` under five tables, prefixed `memory_layer_`. They share the SQLite file with `permission_*`, `capsule_*`, `source_*`, and `audit_*` tables; cross-process safety is provided by SQLite's write lock + WAL.

```sql
-- One row per entity. Aliases inlined as JSON for fast read; the
-- separate aliases table powers lookups by alias value.
CREATE TABLE memory_layer_entities (
    id            TEXT PRIMARY KEY,        -- "ent_01H..."
    kind          TEXT NOT NULL,           -- "person" | "organization"
    canonical     TEXT NOT NULL,
    namespace     TEXT NOT NULL,
    aliases_json  TEXT NOT NULL,
    first_seen    TEXT NOT NULL,
    last_seen     TEXT NOT NULL,
    entry_count   INTEGER NOT NULL DEFAULT 0
);

-- (alias, alias_kind, namespace) → entity_id. Used during Upsert to
-- find existing entities by any of an entry's addresses.
CREATE TABLE memory_layer_aliases (
    alias        TEXT NOT NULL,
    alias_kind   TEXT NOT NULL,
    namespace    TEXT NOT NULL,
    entity_id    TEXT NOT NULL,
    PRIMARY KEY (alias, alias_kind, namespace),
    FOREIGN KEY (entity_id) REFERENCES memory_layer_entities(id) ON DELETE CASCADE
);

-- One row per conversation thread. UNIQUE on (namespace, external_id)
-- so re-syncs don't create duplicates.
CREATE TABLE memory_layer_threads (
    id           TEXT PRIMARY KEY,        -- "thr_01H..."
    namespace    TEXT NOT NULL,
    external_id  TEXT NOT NULL,
    subject      TEXT,
    first_seen   TEXT NOT NULL,
    last_seen    TEXT NOT NULL,
    entry_count  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (namespace, external_id)
);

-- entity ↔ entry mapping, with role + entry date for ordering.
CREATE TABLE memory_layer_entity_entries (
    entity_id    TEXT NOT NULL,
    namespace    TEXT NOT NULL,
    entry_id     TEXT NOT NULL,
    role         TEXT NOT NULL,
    entry_date   TEXT,
    PRIMARY KEY (entity_id, namespace, entry_id, role),
    FOREIGN KEY (entity_id) REFERENCES memory_layer_entities(id) ON DELETE CASCADE
);

-- thread ↔ entry mapping.
CREATE TABLE memory_layer_thread_entries (
    thread_id    TEXT NOT NULL,
    namespace    TEXT NOT NULL,
    entry_id     TEXT NOT NULL,
    entry_date   TEXT,
    PRIMARY KEY (thread_id, namespace, entry_id),
    FOREIGN KEY (thread_id) REFERENCES memory_layer_threads(id) ON DELETE CASCADE
);
```

**Why JSON for aliases in `memory_layer_entities`?** Two reasons: (1) typical entity has 1–3 aliases — a separate `aliases` table is overkill for reading; (2) the dedicated `memory_layer_aliases` table powers lookups by alias value (the path that needs the index).

**Why no `memory_layer_schema_migrations` table mentioned above?** It exists, same shape as every other store's migration table; omitted here for clarity.

## The Layer interface

```go
type Layer interface {
    // Write path — sources call these.
    Upsert(ctx, entry Entry) error
    Delete(ctx, namespace, id string) error

    // Entity queries.
    ListEntities(ctx, filter EntityFilter) ([]Entity, error)
    GetEntity(ctx, id string) (*Entity, error)
    EntriesByEntity(ctx, entityID string, limit int) ([]EntryRef, error)

    // Thread queries.
    ListThreads(ctx, filter ThreadFilter) ([]Thread, error)
    GetThread(ctx, id string) (*Thread, error)
    EntriesByThread(ctx, threadID string, limit int) ([]EntryRef, error)

    Close() error
}
```

### Upsert — what happens

```
layer.Upsert(entry)
  │
  ├─ if entry.Embeddings present:
  │      adapter.Upsert(<namespace>:<entry.id>, vector, metadata)
  │  else:
  │      skip the adapter write (log a debug line)
  │      entry stays searchable by entity / thread but not by vector
  │
  ├─ extract entities from entry.Metadata (from/to/cc/bcc headers)
  │      → for each:
  │          upsert into memory_layer_entities (merging if alias matches)
  │          insert into memory_layer_aliases
  │          insert into memory_layer_entity_entries (with role + date)
  │          refresh entry_count
  │
  └─ extract thread from entry.Metadata (gmail_thread_id today)
         → if present:
             upsert into memory_layer_threads (by namespace + external_id)
             insert into memory_layer_thread_entries
             refresh entry_count
```

**Failure semantics:** the adapter write goes first; if it fails, Upsert returns an error and the layer's tables are not touched (clean failure). If the adapter succeeds but layer derivation fails (e.g., a malformed metadata field), the derivation is logged as a warning and the user-visible Upsert returns success. The entry is still in the adapter and searchable; entity/thread views just won't include it until the entry is re-upserted.

**The no-embedding path:** when `entry.Embeddings` is empty (common in v0.1 when no embedding-capable model adapter is configured), the layer skips the adapter write entirely but still derives entities + threads. Vector search won't find the entry; everything else works. The audit log records nothing layer-specific — the source's `source.sync.completed` entry is the canonical record.

### Delete — what happens

```
layer.Delete(namespace, id)
  │
  ├─ adapter.Delete(<namespace>:<id>)
  │      (idempotent: missing rows are not errors)
  │
  └─ memory_layer_*_entries: drop all rows where (namespace, entry_id) match
         → refresh entry_count on every affected entity + thread
         → entities + threads with entry_count=0 are NOT auto-deleted
           (they may have been linked from other entries we're not aware of;
           orphan cleanup is a Phase 2 maintenance task)
```

## Read APIs — ordering guarantees

These are stable invariants apps can depend on:

| Method | Order |
| --- | --- |
| `ListEntities` | `last_seen DESC, id DESC` — most recently active first |
| `ListThreads` | `last_seen DESC, id DESC` — most recently active first |
| `EntriesByEntity` | `entry_date DESC, entry_id DESC` — newest first (recency mode) |
| `EntriesByThread` | `entry_date ASC, entry_id ASC` — oldest first (reading order) |

**Why differ?** A "recent involvements" UI for an entity wants newest-first (you scrolled there because you're catching up). A thread reads top-to-bottom (you're following a conversation). Each call's ordering matches its natural UI use.

## MCP tool surface

Every memory-layer tool is gated on the `memory.read` capability. Entities and threads are a *derived projection* of permissioned memory entries, not a new permission scope — the user already chose what the client/capsule can read.

| Tool | Purpose |
| --- | --- |
| `entities.list` | List entities; filter by namespace, kind, alias, limit |
| `entities.show` | One entity by id |
| `entities.entries` | Entries an entity is involved in (newest first), with role |
| `threads.list` | List threads; filter by namespace, limit |
| `threads.show` | One thread by id |
| `threads.entries` | Entries in a thread (oldest first; reading order) |

All return JSON content blocks (MCP convention). The wire shapes are the Go types above marshaled to JSON; field names lowercase with underscores (`first_seen`, `entry_count`, etc.).

## CLI surface

```
loamss memory entities list    [--namespace x] [--kind person|organization] [--alias x] [--limit N] [--json]
loamss memory entities show    <entity-id>     [--json]
loamss memory entities entries <entity-id>     [--limit N] [--json]
loamss memory threads  list    [--namespace x] [--limit N] [--json]
loamss memory threads  show    <thread-id>     [--json]
loamss memory threads  entries <thread-id>     [--limit N] [--json]
```

The CLI opens the adapter + layer + store locally — no daemon required. For inspecting state on a running runtime, the same data is available via the MCP tools above.

## Trust + audit

The layer is **first-party runtime code** — its writes and reads happen inside the runtime's trust boundary, like the audit writer or the permission engine. It is not a separate principal; it doesn't authenticate; it doesn't hold its own grants.

The audit story:

- Sources emit `source.sync.completed` audit entries when they call `layer.Upsert(...)` in bulk. The layer itself does not emit per-Upsert entries — that would multiply the audit log by the average entries-per-sync (often hundreds) for no policy gain.
- MCP-level invocations of `entities.list / entities.show / …` go through the runtime's tool dispatcher, which emits `tool.invoked` entries gated on the `memory.read` check. Same machinery that gates `memory.show` and `memory.query`.
- `loamss memory ...` CLI invocations are user-initiated and not audited (they're inspection commands by the user themselves on their own machine). This matches `loamss audit log` — the user reading their own data is not an event.

## v0.1 limits

| Limitation | Why | When |
| --- | --- | --- |
| Single-namespace entity resolution | Cross-source merging needs a join-by-identity story (email is a candidate; phone, handle, etc. are messier). Out of scope for v0.1. | Phase 2 |
| No bulk Rebuild | Requires extending the [memory adapter SPI](adapter-interface.md#memory-adapter) with a Scan/List enumeration method. | Phase 2 — when we add the second memory adapter (pgvector) and need to validate it against the same shape |
| Email-only person identity | Heuristic is good enough for Gmail; insufficient for sources with handles, phone numbers, or wallet addresses. | Phase 2 when those sources arrive |
| No relations between entities | "Sarah works at Acme" not modeled. Adds a relations table + traversal API. | Phase 3 (knowledge-graph work) |
| No episodic summarization | "What happened with Project Alpha last quarter?" requires multi-doc summarization through a model adapter. The substrate is here; the summarizer is a capsule's job. | Reference capsule, not core |
| Orphan entities stay around | Entity with `entry_count=0` doesn't auto-delete. A `loamss memory gc` would clean these up; complexity not worth it before we have field reports of confusion. | Maintenance command, low priority |

## Extending the layer

The plug points for new behavior:

**Add a new entity kind** (e.g., place, project): bump `EntityKind` consts in `types.go`, add the resolver that produces it in `resolver.go`, register a derivation in `layer.go`'s `deriveEntities`. No schema change — `kind` is just a string column.

**Add a new alias kind** (e.g., handle, phone, wallet address): bump `AliasKind` consts in `types.go`, populate it in the appropriate resolver. The `memory_layer_aliases` row supports any (value, kind, namespace) tuple.

**Add a new thread source** (e.g., Slack, calendar event series): write a new extractor that reads source-specific metadata and returns a `ThreadExtraction`. Wire it into `layer.go`'s `deriveThread`. The thread table's `external_id` is opaque to the layer.

**Add a new resolver entirely** (e.g., extracting place names from email signatures): build it as a capsule with `memory.read` + `memory.write` grants. The layer is intentionally a closed first-party surface; richer extraction goes through the capsule sandbox, not into the runtime.

## Related specs

- [`adapter-interface.md`](adapter-interface.md) — the memory adapter SPI the layer sits above
- [`sources.md`](sources.md) — what sources write into the layer
- [`permission-model.md`](permission-model.md) — `memory.read` and scope projection
- [`mcp-surface.md`](mcp-surface.md) — the wire protocol for `entities.*` / `threads.*`
- [`audit-spec.md`](audit-spec.md) — entry shapes for `source.sync.completed` and `tool.invoked`
- [`capsule-spec.md`](capsule-spec.md) — capsule packaging for richer extractors
- [`ARCHITECTURE.md`](ARCHITECTURE.md) — where the layer sits in the broader picture
