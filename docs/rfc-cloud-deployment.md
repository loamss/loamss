# RFC: cloud-deployable Loamss

Status: **draft**
Target release: v0.2.0
Scope: Scope A (single-tenant cloud deployment); Scope B (multi-tenant)
is explicitly out of scope and gets a separate RFC.

## Summary

Externalize the two state-bearing pieces of the runtime (`runtime.db`
and `audit.db`) so a Loamss runtime can run on Cloud Run, GKE, Fly,
Render, Railway, or any container host — not just on a laptop with
local SQLite. Add a `--profile` flag (or `LOAMSS_PROFILE` env var)
that picks a sensible set of defaults for either deployment shape.
Auto-detect common cloud platforms so a plain `loamss start` in a
container Just Works.

One binary. Two deployment shapes. Same code, same audit format, same
permission model.

## Motivation

Today, running Loamss outside of a laptop hits four hard blockers:

1. **`runtime.db` is local SQLite.** It holds paired clients, capability
   grants, source configurations, installed capsules, OAuth client
   records, and the memory layer's derived state. On a Cloud Run-style
   host, the filesystem is ephemeral and not shared across instances —
   so the runtime forgets every pairing and every grant on cold start.
2. **`audit.db` is local SQLite.** The hash-chained audit log is the
   substrate's most important durability invariant — and the file
   evaporates with the container. The chain breaks. We can't tell a
   compliance audience "every read and every write is recorded" if the
   record vanishes on restart.
3. **OAuth callback uses a loopback listener.** The orchestrator opens
   `127.0.0.1:<random>` for the auth code exchange. On a cloud host the
   OAuth provider can't reach a loopback address. Calendar / Gmail
   capsules can't authenticate.
4. **The wizard is `localhost`-trusting by design.** Today's rule:
   bound to 127.0.0.1, so if you can reach it you're authorized. On a
   public URL that becomes "anyone on the internet can complete the
   wizard before you do." Real security gap.

This RFC closes all four.

## Goals

- A single Go binary runs both locally and in a container with no code
  changes — only configuration.
- Local deployments work exactly as they do today. No regressions.
  `loamss start` on a fresh laptop still produces a wizard at
  `http://127.0.0.1:7777/` with zero configuration.
- Cloud deployments work via `docker run loamss:vX` with `DATABASE_URL`
  (and `AUDIT_DATABASE_URL`) set. Cloud Run + GKE specifically tested.
- Audit log is exportable from the cloud deployment to anywhere the
  operator wants (BigQuery, SIEM, S3 archive). It is **not** locked to
  the cloud Postgres.

## Non-goals

- **Multi-tenancy.** Single deployment serves a single principal-set.
  No `tenant_id` on rows. (That's Scope B, a separate RFC.)
- **Horizontal autoscaling.** Cloud Run with `max-instances > 1` is
  *not* supported in this RFC. The Postgres backend removes the
  filesystem split-brain, but the capsule host and OAuth orchestrator
  still assume a single runtime instance. Multi-instance is a
  follow-up.
- **Cloud SQL IAM auth.** Plain Postgres DSN only in this round.
  Reusing the IAM auth code we wrote for `memory:pgvector` Cloud SQL
  mode is a follow-up RFC.
- **Migrating existing data from local SQLite to cloud Postgres.**
  Out of scope. Existing users keep their local install; new cloud
  deployments start fresh. A migration tool is future work.

## Design overview

### Profiles

Loamss gets a `runtime.profile` config key with two values:

| Profile | Default for                                        |
| ------- | -------------------------------------------------- |
| `local` | Laptop install, `localhost` binding, SQLite files  |
| `cloud` | Container install, public binding, Postgres        |

The profile sets defaults. Every individual key can still be overridden
in config — the profile is sugar, not a lock.

#### `local` defaults (what runs today)

```yaml
runtime:
  profile: local
  listen_addr: 127.0.0.1:7777
  database:
    adapter: sqlite
    path: ~/.loamss/runtime.db
  audit:
    adapter: sqlite
    path: ~/.loamss/audit.db
  oauth:
    callback_mode: loopback
  console:
    wizard_gate: none           # localhost binding is the gate
```

#### `cloud` defaults

```yaml
runtime:
  profile: cloud
  listen_addr: 0.0.0.0:${PORT:-7777}
  database:
    adapter: postgres
    dsn: ${DATABASE_URL}        # required, no default
  audit:
    adapter: postgres
    dsn: ${AUDIT_DATABASE_URL}  # required, no default
  oauth:
    callback_mode: public
    callback_url: ${OAUTH_CALLBACK_URL}  # e.g. https://loamss.example.com/oauth/callback
  console:
    wizard_gate: setup_token
    setup_token: ${LOAMSS_SETUP_TOKEN}   # if absent, auto-generated + printed on first start
```

### Auto-detection

If `runtime.profile` is unset and `LOAMSS_PROFILE` is unset, Loamss
detects the deployment environment from well-known env vars and picks
a profile:

| Platform        | Detected via                  | Implies profile |
| --------------- | ----------------------------- | --------------- |
| Cloud Run       | `K_SERVICE`                   | `cloud`         |
| GKE / Kubernetes| `KUBERNETES_SERVICE_HOST`     | `cloud`         |
| Fly.io          | `FLY_APP_NAME`                | `cloud`         |
| Render          | `RENDER`                      | `cloud`         |
| Railway         | `RAILWAY_ENVIRONMENT`         | `cloud`         |
| nothing matches | —                             | `local`         |

Explicit `--profile` or `LOAMSS_PROFILE` always wins over detection.

### Database adapter SPI

Today, each subsystem has a concrete `Store` struct that takes a
`*sql.DB` opened against a local SQLite file. The change:

1. Introduce a `runtime.Database` interface that exposes the operations
   the runtime stores need (open / close / health-check / driver type).
2. Two implementations: `database:sqlite` (current, default for
   `local`) and `database:postgres` (new, default for `cloud`).
3. Each subsystem's `Store` takes a `runtime.Database` instead of
   opening its own connection.
4. Subsystems define their schemas via golang-migrate migration files,
   one set per driver. Migrations run on startup, idempotently.

The runtime.db Postgres schema is one logical database with separate
schemas per subsystem:

```
runtime/
├── permission.clients
├── permission.grants
├── permission.approvals
├── source.configured
├── source.cursors
├── capsule.installed
├── oauth.clients
└── memory_layer.entities, .threads, .entry_links, etc.
```

The audit.db gets its own DSN with its own logical schema (just one
table + the chain head pointer):

```
audit/
├── events           (id, ts, type, principal_kind, principal_id,
│                     target_kind, target_id, outcome, prev_hash,
│                     this_hash, payload jsonb)
└── chain_head       (singleton row; current head_hash for atomic
                      append-and-update)
```

### Audit export

The audit log is exportable from any Postgres backend via standard
tools (pg_dump, logical replication to BigQuery, Debezium → Kafka,
etc.). To make the export shape stable, the `audit.events` schema
freezes at v0.2.0. Adding columns is allowed; renaming or dropping is
breaking.

A future `loamss audit export --format=jsonl --since=...` CLI will
stream events out in a stable JSON Lines format for archival or
SIEM ingestion, but that's not in this RFC.

### OAuth callback rework

The orchestrator currently:

1. Picks a random free localhost port.
2. Starts a one-shot HTTP listener.
3. Constructs the OAuth URL with `redirect_uri=http://127.0.0.1:<port>/callback`.
4. Opens the user's browser to the OAuth URL.
5. The provider redirects back to the loopback listener.
6. The listener captures `code`, exchanges for tokens, hands them to
   the capsule.

This works on a laptop. On a cloud host the loopback listener is
unreachable from the OAuth provider.

The change:

- `oauth.callback_mode: loopback` keeps today's behavior. Default for
  `local` profile.
- `oauth.callback_mode: public` makes the runtime mount a permanent
  `/oauth/callback` endpoint on its public surface. PKCE state +
  per-flow nonces stored in `runtime.db` (already are — just need to
  expand the schema slightly to remember which loopback / public mode
  each in-flight flow expected). Default for `cloud` profile.
- The `redirect_uri` passed to the OAuth provider is computed from
  `oauth.callback_url` (cloud) or the loopback port (local).
- Operators configuring an OAuth provider register the public URL
  upfront. Calendar / Gmail capsule docs get a "Cloud deployment
  callback URL" note.

### Setup-token gate

The wizard endpoints (`/console/probe`, `/console/init`, etc.) and the
dashboard endpoints (`/console/state`, the source/capsule/grant CRUD)
need a gate when `console.wizard_gate: setup_token`.

Mechanics:

1. On first start with `setup_token` gating enabled:
   - If `LOAMSS_SETUP_TOKEN` env var is set, use it verbatim.
   - Otherwise generate a 32-byte random token, print it to stderr in
     the startup banner ("FIRST-RUN TOKEN: xxxxx — save this; you need
     it to complete setup"), and persist it (hashed) in `runtime.db`.
2. The console emits a setup-token entry field on its first page. The
   user pastes the token. The console exchanges it for a session
   cookie (signed, short TTL).
3. Subsequent dashboard requests require the cookie.
4. Once the wizard completes, the token is invalidated. From then on,
   the console requires a paired admin client (TBD: probably an
   "admin" capability separate from the per-client paired model).

This is a v0.2.0 minimum-viable auth front-door. A proper SSO /
OAuth-on-the-console flow is a follow-up.

### Container packaging

A `Dockerfile` lands at `runtime/Dockerfile`:

- Multi-stage build: `golang:1.25-alpine` for the build, `alpine:3` for
  runtime.
- Static binary, no glibc dependency.
- Multi-arch (`linux/amd64`, `linux/arm64`) via `buildx`.
- Built and pushed by the existing release workflow to
  `ghcr.io/loamss/loamss:vX.Y.Z` and `ghcr.io/loamss/loamss:latest`.
- Image does **not** pre-bake Bun, Python, or any capsule runtime.
  Operators who run capsules in the cloud build a derived image:

```dockerfile
FROM ghcr.io/loamss/loamss:0.2.0
RUN apk add --no-cache npm && npm install -g bun
# ... install whatever capsules / runtimes you need ...
```

This is the BYO model — explicit, smaller default image, no
unused dependencies in container scans.

### Health endpoints

- `/healthz` — keeps current shape (returns runtime version + status).
- `/readyz` — new. Returns 200 only when both databases are reachable
  and migrations are caught up. Used by Cloud Run / GKE readiness
  probes. Returns 503 during startup until DBs respond.

## Schema details

### `runtime.db` (Postgres)

Each subsystem owns a schema; the database itself is one logical
Postgres database.

Migrations:

- Per-subsystem versioned migrations under
  `runtime/internal/<subsystem>/migrations/postgres/`.
- Versioned with golang-migrate naming (`0001_initial.up.sql` /
  `0001_initial.down.sql`).
- The same subsystem keeps a sibling `migrations/sqlite/` directory
  for the local-mode story.
- A small bootstrap routine on startup runs all subsystem migrations
  for the selected driver. Failure is fatal.

Concrete first-cut tables (illustrative, not exhaustive):

```sql
-- permission schema
CREATE TABLE permission.clients (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    paired_at   TIMESTAMPTZ NOT NULL,
    metadata    JSONB,
    revoked_at  TIMESTAMPTZ
);

CREATE TABLE permission.grants (
    id                     TEXT PRIMARY KEY,
    principal_kind         TEXT NOT NULL,    -- capsule | client
    principal_id           TEXT NOT NULL,
    capability             TEXT NOT NULL,
    scope                  JSONB,
    rationale              TEXT,
    framing                TEXT,
    requires_approval      BOOLEAN NOT NULL DEFAULT FALSE,
    expires_at             TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL,
    revoked_at             TIMESTAMPTZ
);

-- source schema
CREATE TABLE source.configured (
    id           TEXT PRIMARY KEY,
    name         TEXT UNIQUE NOT NULL,
    adapter_id   TEXT NOT NULL,
    config       JSONB,
    added_at     TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    last_sync_at TIMESTAMPTZ,
    last_status  TEXT
);

-- ...etc for capsule, oauth, memory_layer
```

(Full schema lives in the migration files; this doc captures intent.)

### `audit.db` (Postgres)

Separate DSN, separate Postgres logical database, separate schema.

```sql
CREATE TABLE audit.events (
    id              TEXT PRIMARY KEY,             -- ULID
    ts              TIMESTAMPTZ NOT NULL,
    event_type      TEXT NOT NULL,
    principal_kind  TEXT NOT NULL,
    principal_id    TEXT NOT NULL,
    target_kind     TEXT,
    target_id       TEXT,
    outcome         TEXT NOT NULL,                -- success | denied | error
    prev_hash       BYTEA NOT NULL,
    this_hash       BYTEA NOT NULL,
    payload         JSONB NOT NULL,
    CONSTRAINT chain_link UNIQUE (this_hash)
);

CREATE INDEX audit_events_ts            ON audit.events (ts DESC);
CREATE INDEX audit_events_principal     ON audit.events (principal_kind, principal_id, ts DESC);
CREATE INDEX audit_events_type          ON audit.events (event_type, ts DESC);

CREATE TABLE audit.chain_head (
    id            INT PRIMARY KEY CHECK (id = 1),  -- singleton
    head_hash     BYTEA NOT NULL,
    last_event_id TEXT NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL
);
```

Concurrency: `INSERT events` + `UPDATE chain_head` happen in a single
transaction with `SELECT ... FOR UPDATE` on `chain_head`. This
preserves the current SQLite-with-`BEGIN IMMEDIATE` hash-chain
integrity invariant under Postgres MVCC.

## Configuration shape

Final config shape after this RFC:

```yaml
runtime:
  profile: cloud                  # local | cloud (or auto-detected)
  listen_addr: 0.0.0.0:7777       # honors PORT env var when unset
  data_dir: /var/lib/loamss       # local files (sqlite if used, capsules dir, etc.)
  database:
    adapter: postgres             # sqlite | postgres
    dsn: ${DATABASE_URL}
    max_open_conns: 25
    max_idle_conns: 5
  audit:
    adapter: postgres
    dsn: ${AUDIT_DATABASE_URL}
    max_open_conns: 10
  oauth:
    callback_mode: public         # loopback | public
    callback_url: https://loamss.example.com/oauth/callback
  console:
    wizard_gate: setup_token      # none | setup_token
    setup_token: ${LOAMSS_SETUP_TOKEN}
storage:
  adapter: storage:gcs            # or storage:s3, storage:fs-encrypted
  config: { ... }
memory:
  adapter: memory:pgvector        # or memory:sqlite, etc.
  config: { ... }
models:
  - adapter: model:openai
    config: { api_key_env: OPENAI_API_KEY }
log:
  level: info
  format: json                    # json in cloud profile, text in local
```

The user-supplied YAML stays as small as it can: profile + the
required cloud-specific URLs. Everything else has a profile-driven
default.

## Implementation phases

### Phase 1 — Database adapter SPI + Postgres for runtime.db

- Define `runtime.Database` interface.
- Refactor every `Store` to take an adapter (no behavior change).
- Implement `database:postgres`.
- Per-subsystem migration files (sqlite + postgres).
- Tests against a real Postgres via testcontainers (we have the
  pattern from `memory:pgvector`).
- Audit chain integrity test runs against both backends.

Exit criterion: `LOAMSS_PROFILE=cloud DATABASE_URL=... loamss start`
boots cleanly, runtime tables exist in Postgres, all existing tests
pass.

### Phase 2 — Postgres for audit.db

- Implement `audit:postgres` with the transactional `chain_head`
  update.
- Audit-tail / audit-read queries work against either backend.

Exit criterion: audit chain integrity verified across daemon restarts
under Postgres.

### Phase 3 — OAuth public callback + setup token

- `oauth.callback_mode: public` path.
- `/oauth/callback` HTTP route.
- Setup-token generation, persistence, exchange, invalidation.
- Console gate.

Exit criterion: calendar-ingestor capsule completes its OAuth flow
against a runtime running behind a public URL.

### Phase 4 — Profile system + auto-detection

- `runtime.profile` config key.
- `LOAMSS_PROFILE` env var.
- Cloud-platform env var detection.
- Banner shows the active profile + how it was chosen.

Exit criterion: `loamss start` in a Cloud Run container without any
flags reports "profile: cloud (detected: K_SERVICE)" in the startup
banner.

### Phase 5 — Container + deploy guides

- `runtime/Dockerfile` multi-stage, multi-arch.
- GH Actions workflow extension to build + push to GHCR.
- `docs/deploy-cloud-run.md` — start-to-finish.
- `docs/deploy-gke.md` — Helm chart minimum.
- `docs/deploy-fly.md` — for comparison.

Exit criterion: a Cloud Run service running `ghcr.io/loamss/loamss:0.2.0`
with `DATABASE_URL` + `AUDIT_DATABASE_URL` + `LOAMSS_SETUP_TOKEN`
passes the wizard, ingests notes from a `storage:gcs` bucket, answers
`memory.query` via a paired client.

### Phase 6 — Release v0.2.0

- Tag.
- Release notes call out the deployment-profile shape.
- `getting-started.md` gets a "Run in the cloud" section pointing at
  the deploy guides.
- ROADMAP marks Phase 2.5 "single-tenant cloud deployment" done.

## Open questions

- **Setup-token persistence after wizard completes.** Today's plan:
  invalidate the token, require a paired admin client. What's the
  shape of "admin"? A new `console.admin` capability that an initial
  client receives at wizard completion? Should be cleared up before
  Phase 3.
- **Multiple admins / shared deployments.** Once "admin" exists, can
  there be more than one? Default yes — a primary admin pairs, then
  invites a secondary. Out of scope for this RFC; flag for v0.3.
- **Cloud SQL IAM auth.** Reuses `memory:pgvector`'s code path; can
  land as a follow-up adapter that wraps the standard `database:postgres`.
- **Audit-log shipping primitives.** The schema is exportable via
  pg_dump / logical replication. A first-party `loamss audit export`
  CLI that streams JSONL out is desirable but not blocking. Track as
  a separate item for the next release.

## What this RFC does NOT change

- The capsule manifest format.
- The MCP-over-HTTP+SSE wire protocol.
- The MCP-over-stdio capsule transport.
- The permission model (capability + scope + requires_user_approval).
- The audit log's logical shape (events, principals, outcomes,
  hash chain).
- The single-tenant model. A cloud-deployed Loamss serves the same
  one principal-set as a laptop-deployed one.

If any of those change in this work, that's a sign we're outside
Scope A and need a separate RFC.
