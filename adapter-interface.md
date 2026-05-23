# Adapter Interface Specification v0.1 (draft)

This document defines the three adapter layers — **storage**, **memory**, and **model** — that let users plug their own backends into a Loamss runtime. Adapters are the pluggable substrates of the bottom layer of the stack. The runtime depends on the interfaces defined here, not on any specific backend.

> **Status: draft.** Interfaces will change before v1.0. Adapter authors targeting a pre-v1.0 spec should expect breaking changes; the runtime emits a warning when loading an adapter targeting a different spec version.

## What this document is and is not

This spec defines:

- The contract each adapter type must satisfy
- The trust level granted to adapter code
- How adapters are discovered, configured, and bound to runtime configuration
- The MVP set of adapters that ship with the canonical runtime
- The encryption boundary between the runtime and adapter-stored data

This spec does **not** define:

- Wire protocols for talking to specific backends (S3 SigV4, Postgres protocol, etc.) — that's the adapter implementer's concern
- The semantic memory layers that run *inside* the runtime on top of memory adapters (entity resolution, graph, episodic summaries — those live in `ARCHITECTURE.md` §3 and will get their own spec)
- How the model router decides which adapter to call (covered briefly here; full routing rules belong in a future `model-routing.md`)

## Why adapters

Loamss's core promise is that the user owns their storage, memory, and model access. The adapter pattern is how that promise becomes operational: a small interface contract that any backend can satisfy, with no special privilege for the canonical implementations. If the user's preferred storage is a NAS, a hosted Postgres, or a folder on disk, the same contract applies. If a new vector database emerges in two years, writing an adapter is a self-contained ~500-line job.

The runtime never reads or writes through a hardcoded backend. Every storage call, every memory operation, every model invocation goes through an adapter interface — even when the canonical adapter happens to be a thin wrapper over a library directly linked into the runtime.

## Common adapter contract

All adapters share a small set of conventions:

| Concern | Convention |
|---|---|
| **Registration** | Adapter declares a manifest (`adapter.yaml`) at the root, identifying type, name, version, supported spec version, and backend-specific config schema |
| **Lifecycle** | `Init(config)`, `HealthCheck()`, `Close()` |
| **Identity** | A stable adapter ID (`storage:sqlite-encrypted@1.2.0`) used in audit entries |
| **Errors** | Typed errors with stable codes (`adapter.connection_lost`, `adapter.not_found`, `adapter.permission_denied`) |
| **Config validation** | Reject malformed config at `Init` rather than at first use; runtime surfaces errors via `loamss doctor` |
| **Observability** | Emit structured logs and metrics through the runtime's logger; do not write to disk directly |
| **Threading model** | Adapters must be safe for concurrent calls; the runtime serializes nothing on the adapter's behalf |

All adapters are **in-process Go libraries** in v0.1. Out-of-process adapters (subprocess, sidecar, gRPC service) are deferred — the linkage cost is acceptable when the canonical adapters can be vendored and untrusted third-party storage drivers are rare.

## Storage adapter

The interface for the user's bytes. Anything from a single file to a multi-terabyte object store sits behind this contract.

### Operations

```go
type StorageAdapter interface {
    // Init binds the adapter to a backend; returns error on bad config or unreachable backend.
    Init(ctx context.Context, config map[string]any) error

    // Read returns the bytes at path, or adapter.ErrNotFound.
    Read(ctx context.Context, path string) ([]byte, error)

    // ReadStream returns a reader for byte-range or large-content access.
    ReadStream(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error)

    // Write puts bytes at path. Overwrites by default. Creates intermediate paths.
    Write(ctx context.Context, path string, content []byte) error

    // WriteStream is the streaming counterpart of Write.
    WriteStream(ctx context.Context, path string, content io.Reader) error

    // Delete removes the object at path. Returns nil if already absent (idempotent).
    Delete(ctx context.Context, path string) error

    // Exists is a cheap presence check.
    Exists(ctx context.Context, path string) (bool, error)

    // Metadata returns size, content-type guess, mtime, etag, and any backend-specific fields.
    Metadata(ctx context.Context, path string) (ObjectMetadata, error)

    // List streams matching paths. Prefix is interpreted as a path prefix.
    List(ctx context.Context, prefix string) (<-chan ListEntry, error)

    // SignedURL returns a time-bound URL the caller can hand to an external client
    // for direct fetch from the underlying storage. Adapters MAY return adapter.ErrUnsupported
    // if the backend cannot issue signed URLs (e.g., a local file with no HTTP front).
    // See mcp-surface.md for how the runtime uses this.
    SignedURL(ctx context.Context, path string, ttl time.Duration, op Op) (string, error)

    HealthCheck(ctx context.Context) error
    Close(ctx context.Context) error
}
```

`Op` is `OpRead` or `OpWrite`. Adapters that issue write URLs (for client-direct upload, e.g., a creator pushing a new video) must support both; read-only adapters return `adapter.ErrUnsupported` for `OpWrite`.

### Encryption boundary

Encryption is a **runtime concern by default** for object-style adapters and an **adapter concern** for database-style adapters:

| Adapter style | Encryption | Why |
|---|---|---|
| Object (FS, S3) | Runtime encrypts at the boundary; adapter stores opaque blobs | Backend has no concept of structure; uniform encryption is simpler and safer |
| Database (SQLite, Postgres) | Adapter uses native encryption (SQLite SEE, Postgres TDE) | Allows backend-side queries and indexing on metadata |

Each adapter declares which model it uses in its `adapter.yaml` (`encryption: runtime | native`). The runtime refuses to start with a backend that has no encryption path available.

**Keys never leave the runtime.** Adapters that handle native encryption receive the encryption key from the runtime at `Init` time and must not persist it outside of the backend's own key-storage facility.

### Path semantics

Paths are slash-separated, case-sensitive, treated as opaque by the adapter. The runtime imposes additional structure (e.g., `email/threads/<thread_id>.json`) but the adapter does not interpret it.

### MVP storage adapters

| Adapter | Path pattern | Encryption | SignedURL support |
|---|---|---|---|
| `storage:fs-encrypted` | Local filesystem under a configured root | Runtime | Optional (via local HTTP fronting) |
| `storage:sqlite-encrypted` | SQLite blob store with paths as keys | Native (SEE) | No |
| `storage:s3-compat` | S3 buckets (AWS, Backblaze B2, MinIO, R2) | Runtime + optional server-side | Yes (pre-signed URL) |
| `storage:postgres` | Postgres with bytea blob + path index | Native (TDE) | No |

The first two cover the all-local case (one user, one laptop) without requiring a network service. `s3-compat` is the path for users who want object storage. `postgres` is the path for users who want everything in one place if they're already running Postgres for something else.

## Memory adapter

The interface for vector storage and approximate nearest-neighbor search. The entity store, knowledge graph, episodic timeline, and provenance layer all run **inside the runtime**, on top of whichever memory adapter holds the vectors.

### Operations

```go
type MemoryAdapter interface {
    Init(ctx context.Context, config map[string]any) error

    // Upsert inserts or replaces a vector with associated metadata.
    // ID is opaque to the adapter; the runtime assigns and owns it.
    Upsert(ctx context.Context, id string, vector []float32, metadata map[string]any) error

    // Search returns the k nearest neighbors to the query vector, optionally filtered
    // by metadata predicates. Adapters that support pre-filtering should use it;
    // others may post-filter.
    Search(ctx context.Context, query []float32, k int, filter MetadataFilter) ([]SearchHit, error)

    // Get returns the vector and metadata for a known ID.
    Get(ctx context.Context, id string) (*Entry, error)

    // Delete removes an entry. Idempotent.
    Delete(ctx context.Context, id string) error

    // BatchUpsert is a hot path; adapters should optimize for throughput.
    BatchUpsert(ctx context.Context, entries []Entry) error

    // Stats returns count, dimension, backend health.
    Stats(ctx context.Context) (MemoryStats, error)

    HealthCheck(ctx context.Context) error
    Close(ctx context.Context) error
}
```

### What the adapter does NOT do

- Entity resolution (runtime concern, on top)
- Episodic summarization (runtime concern)
- Knowledge graph traversal (runtime concern)
- Provenance tracking (runtime concern; the adapter stores provenance fields as opaque metadata)
- Embedding generation (model adapter's concern; the runtime computes embeddings and passes vectors in)

This narrow contract makes adapters cheap to write and swap. The semantic layers all happen above the interface.

### MVP memory adapters

| Adapter | Backend | Best for | Notes |
|---|---|---|---|
| `memory:sqlite-vec` | SQLite + sqlite-vec | All-local default | Single file, no service |
| `memory:pgvector` | Postgres + pgvector | Postgres-using setups | Reuses storage Postgres connection |
| `memory:chroma` | Chroma | Standalone hosted setups | Network service |
| `memory:qdrant` | Qdrant | Scale + advanced filtering | Network service |

The default at install time is `sqlite-vec` if storage is local, `pgvector` if storage is Postgres. Other choices require explicit selection in `loamss init`.

## Model adapter

The interface for generation and embedding calls. Each adapter speaks to one provider (Anthropic, OpenAI, local Ollama, etc.). The model router (a runtime concern) decides which adapter to call for which task.

### Operations

```go
type ModelAdapter interface {
    Init(ctx context.Context, config map[string]any) error

    // Models returns the models this adapter knows how to call, with capabilities and limits.
    Models(ctx context.Context) ([]ModelDescriptor, error)

    // Generate produces text given a prompt and parameters.
    Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error)

    // GenerateStream streams generation incrementally.
    GenerateStream(ctx context.Context, req GenerateRequest) (<-chan GenerateChunk, error)

    // Embed produces a vector embedding for a text.
    Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)

    // EstimateCost returns the expected cost of a request before executing.
    // Used by routing rules with cost_ceiling scopes.
    EstimateCost(ctx context.Context, req GenerateRequest) (Cost, error)

    HealthCheck(ctx context.Context) error
    Close(ctx context.Context) error
}

type ModelDescriptor struct {
    ID            string         // "claude-sonnet-4.7"
    Capabilities  []string       // "text", "long_context", "vision", "embeddings", ...
    MaxTokens     int
    Hosted        bool           // false for local models — relevant to data-class routing
    Region        string         // for data residency rules
    CostHints     CostHints      // approximate per-token cost in USD
}
```

### What the model adapter does NOT decide

The adapter does **not** apply `forbidden_data_classes`, `cost_ceiling`, or any task routing logic. Those are the **model router's** job, which lives in the runtime. The adapter receives a fully-resolved request — model ID, prompt, params — and just executes it.

This separation is important: it keeps providers swappable without re-implementing safety rules in each adapter.

### The router (runtime, not adapter)

For context: when a capsule calls `model.call`, the runtime's router:

1. Reads the capsule's grant scope (`tasks`, `cost_ceiling`, `forbidden_data_classes`)
2. Inspects the prompt's data class tags
3. Enumerates available adapters and their declared models
4. Filters to models that satisfy capability requirements, are not hosted if data class forbids it, and fit cost ceiling
5. Picks per user routing rules (`task: drafting → Claude Sonnet`, `task: summarization → local Llama`, etc.)
6. Invokes the chosen adapter

The adapter sees only step 6.

### MVP model adapters

| Adapter | Provider | Notes |
|---|---|---|
| `model:anthropic` | Anthropic Claude | API key required |
| `model:openai` | OpenAI | API key required; configurable for OpenAI-compatible providers |
| `model:mistral` | Mistral | API key required |
| `model:ollama` | Local Ollama | No key; local network or unix socket |
| `model:none` | No-op | Allows running Loamss with no model access; organizer capsules degrade gracefully |

Users without model access get a usable Loamss with raw storage, keyword memory, and full external client access. Semantic memory and organizer-capsule functionality require at least one configured model adapter.

## Trust level

Adapters are **semi-trusted**:

- They run in the runtime's process for performance
- The runtime ships a vetted set; third-party adapters can be added but are clearly distinguished
- An adapter receiving an encryption key must use the backend's native key handling and never echo the key elsewhere
- Adapter network calls go through the runtime's HTTP client to inherit logging, timeouts, and any user proxy rules

This sits between **runtime code** (fully trusted) and **capsules** (untrusted, sandboxed). The choice reflects a pragmatic reality: adapters need byte-level access to storage and direct network calls to model providers, both of which are expensive to mediate through a sandboxing boundary.

A future spec version may move untrusted third-party adapters to an out-of-process model. The v0.1 contract is designed to keep that migration possible (adapter operations are already coarse-grained and serializable).

## Configuration

Users select adapters at `loamss init` time, and can change them via `loamss config` or by editing the runtime config file.

```yaml
# loamss-config.yaml
storage:
  adapter: storage:s3-compat
  config:
    bucket: my-loamss-data
    region: us-west-2
    credentials_source: env  # uses AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY

memory:
  adapter: memory:pgvector
  config:
    dsn: postgres://loamss@localhost/loamss
    table: memory_vectors

model:
  adapters:
    - id: model:anthropic
      config:
        api_key_env: ANTHROPIC_API_KEY
    - id: model:ollama
      config:
        endpoint: http://localhost:11434

routing:
  - task: drafting
    prefer: claude-sonnet-4.7
  - task: summarization
    prefer: llama-3.3
  - data_class: health
    forbidden_models: [hosted]
```

Switching storage adapters is the most expensive operation: the runtime offers `loamss storage migrate <new-adapter>` to copy data through, but the user is expected to plan and validate. Switching memory or model adapters is cheap.

## Versioning

Each adapter declares a spec version it targets:

```yaml
# adapter.yaml
name: storage:s3-compat
version: 1.4.0
spec_version: "0.1"
type: storage
```

The runtime supports a range of spec versions. Loading an adapter targeting a higher spec produces an error; a lower spec produces a warning and may degrade certain features.

## Open questions

- **Out-of-process adapters**: when, if ever? Possibly Phase 2 for third-party adapters that handle sensitive data; not a near-term need given the vetted MVP set.
- **Multi-storage**: can a user have files in S3 *and* memory in Postgres, with the runtime knowing which is which? Initial leaning: yes via path-prefix routing in the config. Spec deferred to v0.2.
- **Streaming vs. batching tradeoffs**: for very large ingestion (initial Gmail backfill), batch APIs in storage and memory adapters matter. Defined now but performance contracts are loose.
- **Backup / restore**: should adapters have a `Backup` and `Restore` operation, or is that always orchestrated at the runtime level via `loamss export`? Initial leaning: runtime-level, adapter exposes just the primitives.
- **Search across adapters**: if a user has two storage adapters configured (legacy local + current S3), can a single `List` see both? Probably yes via the runtime's storage facade, not the adapter contract.
- **Encryption key rotation**: the runtime owns keys, but rotating them affects every adapter. The migration story (re-encrypt in place vs. shadow copy + swap) is not specified.
- **Cost estimates**: the model adapter's `EstimateCost` is best-effort. Should the framework expose a hard "do not exceed" budget, or just a warning ceiling? Initial leaning: hard for `cost_ceiling` in scopes, warning for routing-default budgets.
- **Adapter for the audit log itself**: the audit log is mentioned in ARCHITECTURE.md as "hot copy in runtime state, write-through to user storage." Does it use the storage adapter, or a dedicated audit adapter? Defer to `audit-spec.md`.
