# memory:pgvector — Postgres + pgvector memory backend

Phase 2 production-scale vector store. Uses Postgres with the
[`pgvector`](https://github.com/pgvector/pgvector) extension —
the de-facto standard for relational vector search.

Use this instead of `memory:sqlite` when:

- You're past ~100K entries and the brute-force scan in
  `memory:sqlite` is starting to feel slow.
- You want native indexing (`ivfflat`, `hnsw`) for sub-linear
  search.
- The runtime + memory layer are on separate hosts (containerised
  daemon talking to a managed Postgres).
- You need shared memory across multiple loamss instances —
  `table_suffix` lets several instances live in one database
  without stepping on each other.

## Config

In your `~/.loamss/config.yaml`:

```yaml
memory:
  adapter: memory:pgvector
  config:
    dsn: "postgres://loamss:secret@db.example.com:5432/loamss?sslmode=require"
    dimension: 1536           # required — must match the embedding model's output
    table_suffix: "default"   # optional — prefixes the table; lets multiple
                              # loamss instances share one database
    metric: cosine            # default; "l2" or "inner" also accepted
```

### DSN

Standard libpq-style URL. pgx accepts most of the connection
parameters libpq does, plus pool-tuning via query string:

- `?sslmode=disable | require | verify-full`
- `?pool_max_conns=10` (defaults to 10 if not set)
- `?pool_min_conns=2`

For managed services (RDS / Cloud SQL / Neon / Supabase / etc.)
the provider gives you the URL; use it verbatim. For local
Postgres in Docker, `postgres://postgres:loamss@127.0.0.1:5432/loamssdb?sslmode=disable`.

Credentials may also come from `DATABASE_URL` or
`LOAMSS_PGVECTOR_DSN` env vars — set whichever pattern matches
your secret-management policy.

### Dimension

Required. Cannot change after the table is created. Match it to
your embedding model:

| Embedding model | Dimension |
|---|---|
| OpenAI `text-embedding-3-small` | 1536 |
| OpenAI `text-embedding-3-large` | 3072 |
| OpenAI `text-embedding-ada-002` | 1536 |
| Anthropic (no native embedding API) | route to OpenAI/Ollama |
| Ollama `nomic-embed-text` | 768 |
| Ollama `mxbai-embed-large` | 1024 |
| BGE-M3 (BAAI) | 1024 |

If you change models post-deploy, drop and re-create the table —
or set a fresh `table_suffix` and re-ingest.

### Metric

| Value | pgvector operator | Use case |
|---|---|---|
| `cosine` (default) | `<=>` | text embeddings; magnitude-invariant similarity |
| `l2` | `<->` | image embeddings, anything Euclidean |
| `inner` | `<#>` | when your embeddings are pre-normalised |

The adapter normalises the distance value so smaller = closer
regardless of which metric you pick.

### Table suffix

Tables are named `loamss_memory_<suffix>`. Default is
`loamss_memory_default`. Useful when:

- Multiple loamss instances share a database (e.g., one per
  user, one schema).
- You want to A/B test a new embedding model without dropping
  existing data — point the new instance at
  `table_suffix: bge_v2` while the old one keeps using
  `default`.

The suffix is validated against `[a-z0-9_]+` — no SQL injection
surface, even though it's interpolated into the table name.

## Provisioning the database

The adapter creates its own table on first Init. The pgvector
extension itself must already be installed:

```sql
CREATE EXTENSION IF NOT EXISTS vector;
```

Most managed Postgres services pre-install pgvector; check your
provider's docs. For local Postgres in Docker:

```bash
docker run --rm -d --name loamss-pg -p 5432:5432 \
  -e POSTGRES_PASSWORD=loamss \
  -e POSTGRES_DB=loamssdb \
  pgvector/pgvector:pg17
```

The official `pgvector/pgvector` image bundles the extension
ready to go.

## Indexes (operator responsibility)

The adapter works without an index — queries do a sequential
scan, fine up to a few thousand entries. Past that, you'll want
either `ivfflat` (fast to build, good recall) or `hnsw` (slower
to build, better recall + speed at scale):

```sql
-- ivfflat for moderate-sized datasets (≤1M)
CREATE INDEX ON loamss_memory_default
USING ivfflat (vector vector_cosine_ops)
WITH (lists = 100);

-- hnsw for production-scale workloads
CREATE INDEX ON loamss_memory_default
USING hnsw (vector vector_cosine_ops)
WITH (m = 16, ef_construction = 64);
```

Match the `_cosine_ops` / `_l2_ops` / `_ip_ops` suffix to your
configured `metric`. The adapter doesn't manage indexes
intentionally — `pg_settings`, data distribution, and your
workload all dictate the right shape, and pgvector's index
choices change with versions. We leave it to the operator.

## Capabilities supported

| SPI method | Status | Notes |
|---|---|---|
| Init / Close / HealthCheck | ✓ | pool-based; `Ping` for health |
| Upsert | ✓ | single round-trip; uses `ON CONFLICT (id) DO UPDATE` |
| BatchUpsert | ✓ | one round-trip via `pgx.Batch` for the whole batch |
| Get | ✓ | returns `ErrNotFound` for missing ids |
| Search | ✓ | k-NN with optional metadata equality filtering |
| Delete | ✓ | idempotent — missing id is not an error |
| Stats | ✓ | row count + dimension + pg version + metric + table name |

## Operational notes

- **Vector dimension is fixed** at the table level — you can't
  upsert a different-dimensioned vector after the table is
  created.
- **Metadata filtering** uses `metadata->>'key' = 'value'`
  (jsonb text-extraction). For complex predicates (numeric
  range, set-in), add a `jsonb_path_ops` GIN index on the
  metadata column.
- **Connection pooling**: pgxpool defaults to 10 max
  connections. Tune via `?pool_max_conns=N` in the DSN.
- **Long-lived connections** survive Postgres restarts — pgx's
  pool auto-reconnects. If your Postgres has a strict
  `idle_in_transaction_session_timeout`, set it high enough
  that the adapter's `BatchUpsert` (which holds a transaction
  for the duration) won't be evicted.

## Future work

- Index hint surfaced in the adapter so the user can specify
  the desired index type at Init and the adapter ensures it
  exists.
- Per-row TTL via `pg_cron` integration or background pruning
  task — currently the runtime's memory layer handles
  expiry above the adapter.
- The dashboard's Memory pane is unbuilt; once it lands, the
  pgvector adapter's `Stats` output will surface there directly.

## See also

- [`adapter-interface.md`](../adapter-interface.md) — full
  memory SPI contract.
- [`internal/adapter/memory/pgvector/pgvector.go`](../runtime/internal/adapter/memory/pgvector/pgvector.go)
  — the implementation.
- [`memory-layer.md`](../memory-layer.md) — how the runtime's
  memory layer sits above the adapter.
