# Source Connector Specification v0.1 (draft)

> **Read this first — source connectors are transitional.** Loamss's
> design center is **apps writing to your Loamss as their backing
> store** (see [`native-apps.md`](native-apps.md)). Source connectors
> exist for users whose data currently lives in legacy SaaS (Gmail,
> Calendar, Drive, Slack, …) and need to mirror it into their Loamss
> as a one-time migration. The long-term shape is native apps that
> write to Loamss directly, removing the need to scrape upstream
> services. This spec stays in place because the SPI is real and the
> existing connectors work; just don't read it as "the way Loamss
> primarily acquires data." That's the inverse of the actual thesis.

This document defines the **source connector** layer: the components that pull data from external systems (Gmail, Calendar, Slack, Drive, …) into the user's storage and memory. Source connectors are the ingestion edge of the runtime when used; everything that arrives in user memory via this path got there because a source put it there, or because a capsule transformed something a source put there.

> **Status: draft.** The interface will change before v1.0. Connector authors targeting a pre-v1.0 spec should expect breaking changes.

## What this document is and is not

This spec defines:

- The contract every source connector must satisfy
- The lifecycle the runtime drives a source through (configure → authenticate → sync … sync … remove)
- How sources interact with the storage and memory adapters
- The credential storage convention
- The audit shape sources emit
- The MVP set of connectors that ship with the canonical runtime

This spec does **not** define:

- Wire protocols for talking to specific backends (Gmail v1, Google Calendar v3, Slack RTM, etc.) — that's the connector implementer's concern
- The semantic memory layers (entity resolution, graph, episodic summaries) that run on top of what sources write
- Push-notification / webhook delivery (deferred to a later spec)

The companion code is in `runtime/internal/source/`. The `source.Source` interface and its supporting types in `source.go` are the authoritative Go contract; this document is the spec it implements.

## Where sources sit in the system

```
┌──────────────────────────────────────────────────────┐
│  External services (Gmail, Calendar, Slack, Drive…)  │
└────────────────────┬─────────────────────────────────┘
                     │  HTTP/REST · OAuth · API quotas
┌────────────────────▼─────────────────────────────────┐
│  Source connector  (source:files, source:gmail)      │
│  - per-instance config + credentials                 │
│  - opaque cursor for incremental sync                │
│  - emits audit entries on every state change         │
└────────┬─────────────────────────┬───────────────────┘
         │ raw payload bytes       │ normalized entries
         ▼                         ▼
┌────────────────────┐    ┌──────────────────────────┐
│ Storage adapter    │    │ Memory adapter           │
│ sources/<name>/... │    │ namespace=<name>         │
└────────────────────┘    └──────────────────────────┘
```

Two principles fall out of the picture:

1. **Sources are not capsules.** A source has only the runtime's trust — there is no separate capsule sandbox. The runtime grants each source the storage + memory + credential surface it declared at configure time; nothing more.
2. **Sources don't query.** They write raw payloads and normalized entries; reading + summarizing + entity resolution is the job of organizer capsules. Keeping the read/write split clean makes it possible to swap an organizer capsule without re-ingesting and makes audit-by-source straightforward.

## The Source interface

A connector implements one Go interface. The full signature lives in `runtime/internal/source/source.go`; this is the contract in prose.

```go
type Source interface {
    ID() string                                                    // "source:<name>"
    Init(ctx, deps Deps) error                                     // one-shot bind
    AuthStatus(ctx) (AuthStatus, error)                            // are creds valid?
    BeginAuth(ctx) (AuthFlow, error)                               // start interactive flow
    CompleteAuth(ctx, params map[string]string) error              // finish flow
    Sync(ctx, cursor []byte) (SyncResult, error)                   // one ingestion pass
    HealthCheck(ctx) error                                         // cheap reachability probe
    Close(ctx) error                                               // release resources
}
```

### Lifecycle

```
                ┌────────┐    Configure
                │  new   │ ◄────────── user runs `loamss source add`
                └───┬────┘
                    │ Init(Deps)
                    ▼
                ┌────────┐
                │  idle  │
                └───┬────┘
                    │ BeginAuth() → user opens URL
                    │ CompleteAuth() ← code arrives via loopback / paste
                    ▼
              ┌─────────────┐
              │ authenticated│  ◄──── token persisted via CredentialStore
              └──────┬──────┘
                     │ Sync(cursor)
                     ▼
              ┌──────────┐
              │  syncing │  ◄──── many times across many process invocations
              └──────┬───┘
                     │ Close()  /  `loamss source remove`
                     ▼
                  ┌─────┐
                  │ done│
                  └─────┘
```

The runtime is allowed to call `Sync` repeatedly on a long-lived source instance. Each call gets the cursor the source returned last time. Sources are responsible for using the cursor for incremental fetch; full re-syncs on every call are a correctness failure for any non-trivial source.

### Init dependencies

`Init` receives a `Deps` struct with everything the source is allowed to touch:

| Field | What it is | Why the source has it |
| --- | --- | --- |
| `SourceName` | User-chosen handle (e.g. `gmail-personal`) | Namespace for storage paths + memory entries |
| `Config` | Opaque per-instance config map | Source-specific knobs (scope, query, max records, …) |
| `Storage` | Narrowed storage-adapter surface (Write/Read/Exists/Delete) | Raw payload persistence |
| `Memory` | Narrowed memory-adapter surface (Upsert/Delete) | Normalized entry persistence |
| `Credentials` | Per-source CredentialStore | OAuth tokens, API keys, anything that must survive restarts |
| `Logger` | slog-shaped logger pre-scoped to the source | Diagnostic logging without binding to `log/slog` directly |

Sources never see the permission engine, the audit writer, the MCP surface, or other sources' state. The runtime emits audit entries on the source's behalf at the lifecycle boundaries listed below.

### Auth flows

`AuthFlowKind` enumerates the handshake shape. Connectors pick one based on what their provider supports.

| Kind | What happens | Used by |
| --- | --- | --- |
| `none` | No interactive step; static config validates as-is | API-key sources, file-system watchers |
| `browser` | Source starts a loopback HTTP listener and returns a URL; the runtime displays the URL; the user's browser hits the loopback and the source captures the code itself | `source:gmail` (loopback redirect with PKCE) |
| `code_paste` | Source returns a URL; user opens it, completes the flow, pastes a code back into the CLI | Fallback for headless servers; some legacy providers |
| `device_code` | OAuth 2.0 device-authorization grant (RFC 8628); source polls while the user enters a code on another device | Reserved for future use |

For `browser` flows, `CompleteAuth` blocks on the source's internal loopback until the callback arrives (or `ctx` is canceled). For `code_paste`, the runtime passes the code in `params["code"]`.

### Cursors

A cursor is an opaque `[]byte` the source defines. The runtime persists it between `Sync` calls and hands it back on the next one. Empty cursor means "first sync — sweep from scratch."

Sources should encode cursors as compact, self-describing blobs (typically JSON) so they can extend the shape without a schema migration. The `source:gmail` connector, for example, encodes:

```json
{"history_id": "12345678", "last_sync_time": "2026-05-24T15:30:00Z"}
```

## SyncResult

Every `Sync` returns a `SyncResult` whose counters and errors flow into the audit log and into the `last_sync_summary` column of the sources table.

| Field | Meaning |
| --- | --- |
| `Cursor` | The new cursor — runtime overwrites the persisted one if `Sync` returned no top-level error |
| `RecordsAdded` | Newly-ingested records |
| `RecordsUpdated` | Records that were already present and refreshed in place |
| `BytesIngested` | Total raw payload bytes written to storage this pass |
| `Started` / `Finished` | Wall-clock bounds |
| `Errors` | Per-record failures (the sync didn't abort) |

If `Sync` aborts before completing, it returns a non-nil error AND a partial `SyncResult` (the cursor is typically unchanged so the next attempt re-tries the same window).

## Storage + memory layout

Sources write to two backends. The path / namespace convention is:

| Backend | Path / namespace | Shape |
| --- | --- | --- |
| Storage | `sources/<source_name>/<source-defined subtree>` | Raw payload bytes (RFC822 EML, JSON blobs, attachment bodies, …) |
| Memory | namespace = `<source_name>`, id = stable external id | One entry per logical record (one message, one event, one file) |

The `<source_name>` segment is the user's chosen handle, not the source id. Two configured Gmail sources (`gmail-personal` and `gmail-work`) share no storage path or memory namespace — grants can scope by source via `memory.namespace`.

### Memory entry shape

```
Namespace:  <source_name>            // "gmail-personal"
ID:         <stable external id>     // Gmail message id
Content:    <plaintext summary>      // subject + snippet for Gmail
Metadata:   {
    "source": "<source_name>",
    "adapter_id": "<source-id>",
    "<provider-specific fields>": ...
}
Embeddings: nil                      // sources don't embed; organizers do
```

Sources keep `Content` compact (subject + snippet for Gmail; title + summary for Calendar) and stash everything else in `Metadata`. Full-body parsing, HTML stripping, attachment extraction, entity resolution — none of that is a source's concern.

## Credentials

Credentials live at `sources/<source_name>/credentials.json` inside the storage adapter. The adapter's own encryption (if any) provides at-rest protection — `storage:fs-encrypted` uses AES-256-GCM, which is the v0.1 default.

The CredentialStore interface is intentionally minimal:

```go
type CredentialStore interface {
    Get(ctx) (map[string]any, error)   // ErrNoCredentials if absent
    Set(ctx, creds map[string]any) error
    Delete(ctx) error                  // idempotent
}
```

The runtime constructs one CredentialStore per configured source, scoped to its name. Sources do NOT see other sources' credentials and do NOT see the storage path the credentials live at.

> **Walkaway invariant**: pointing a fresh runtime at the same storage adapter recovers every source's credentials. The user does not have to re-run OAuth flows after a machine move. This is the same property `loamss export` / re-import preserves.

## Audit

The runtime — not the source — emits audit entries at every state change:

| Type | When | Outcome |
| --- | --- | --- |
| `source.added` | `loamss source add` succeeds | success |
| `source.authenticated` | `CompleteAuth` returns nil | success |
| `source.sync.started` | About to call `Sync` | success |
| `source.sync.completed` | `Sync` returned | success / denied / error |
| `source.removed` | `loamss source remove` succeeds | success |

Each entry carries `subject={kind: source, id: <source_name>}` and a `data` map with the source's adapter id plus relevant numeric fields (records added/updated, bytes ingested, error count). The hash chain links these entries into the same tamper-evident log as every other runtime event.

Sources can also emit their own audit entries through the logger (debug-level), but the canonical lifecycle entries above are the runtime's job.

## Configuration

The user configures a source via `loamss source add`:

```bash
loamss source add source:gmail \
    --name gmail-personal \
    --config client_id=$GOOGLE_OAUTH_CLIENT_ID \
    --config client_secret=$GOOGLE_OAUTH_CLIENT_SECRET
```

The `--config` flag is repeatable; values land in the source's `Config` map. The source's `Init` validates the shape — bad config surfaces here, not silently at the next sync.

`loamss source show` masks values for any config key whose lowercased name contains `secret`, `password`, `token`, `api_key`, or `credential`. The per-source `show --json` output stays verbatim — that's the programmatic path callers opt into.

## CLI surface

```
loamss source add <adapter-id> --name <handle> [--config k=v ...]
loamss source list [--json]
loamss source show <name> [--json]
loamss source authenticate <name>
loamss source sync <name> [--json]
loamss source remove <name> [--yes]
```

The full `loamss` CLI lives in [`cli.md`](cli.md). The behavior of these subcommands is the source SPI surface, expressed as a CLI.

## Trust model

The trust calculus for sources differs from capsules — sources are *first-party runtime code*, capsules are *third-party sandboxed code*.

| | Source | Capsule |
| --- | --- | --- |
| Lives in | the runtime binary | a subprocess |
| Has direct access to | storage adapter, memory adapter, the network | only what its manifest declares; reaches storage/memory through the runtime |
| Trust level | full (its code is part of the runtime) | sandboxed (no implicit trust) |
| Audit | runtime emits lifecycle entries on the source's behalf | every callback gets a per-call permission check + audit entry |

This asymmetry is intentional. **The in-tree path is for SPI reference implementations, not for catalogue growth.** New data-source connectors ship as capsules under the `ingestor` role from `capsule-spec.md`. Locking provider-specific code into the runtime fossilizes the catalogue and forces a runtime release for every new connector — exactly the trap `extensibility.md` flags.

## Reference SPI implementations (in-tree)

Two connectors ship in the runtime as the SPI's reference implementations. They are not the catalogue — they are the shape a capsule ingestor matches.

| Adapter ID | Status | Why it's in-tree |
| --- | --- | --- |
| `source:files` | ✅ shipped | The frictionless no-auth demo — proves the SPI works without any credentials machinery |
| `source:gmail` | ✅ shipped | The OAuth + incremental-sync reference — proves the SPI handles loopback OAuth, cursor persistence, rate limits, and attachments |

These two cover the two ends of the SPI: no-auth and full OAuth. Everything else (Calendar, Drive, Slack, GitHub, Notion, RSS, Linear, …) belongs in the capsule marketplace, not in `runtime/internal/source/`.

See [`docs/setup-gmail.md`](docs/setup-gmail.md) for the user-facing OAuth setup for `source:gmail`. We don't plan to add more in-tree connectors; if a future SPI gap surfaces (write-back, push subscriptions, etc.) we'll extend the SPI and update one of the reference implementations to cover it.

## Adding a new source connector

**The capsule ingestor role is the path.** See `capsule-spec.md` (the `ingestor` role) and [`docs/build-your-first-source-connector.md`](docs/build-your-first-source-connector.md). A capsule ingestor:

- Implements the same lifecycle shape as `source.Source`, but as MCP-tool callbacks the runtime drives
- Receives credentials via runtime-mediated MCP tools (no plaintext on disk; the runtime encrypts via the storage adapter)
- Persists its cursor via the storage MCP tools under its own namespace
- Gets scheduled the same way in-tree sources do (`loamss source sync` and the scheduler)

If you're a contributor and find yourself wanting to add a connector to `runtime/internal/source/`, stop and ask first. The answer is almost certainly: ship it as an ingestor capsule.

The first connector that pulls write-back capabilities (e.g. `source:gmail-send`) will need the spec extended — today everything assumes read-only. That extension is its own design discussion.

## Related specs

- [`capsule-spec.md`](capsule-spec.md) — capsules, including the future ingestor role (third-party sources)
- [`adapter-interface.md`](adapter-interface.md) — storage / memory / model SPIs that sources consume
- [`audit-spec.md`](audit-spec.md) — entry shape and chain semantics
- [`permission-model.md`](permission-model.md) — grant scopes, including `memory.namespace`
- [`cli.md`](cli.md) — the full `loamss` CLI surface
- [`docs/setup-gmail.md`](docs/setup-gmail.md) — Google OAuth client setup for `source:gmail`
