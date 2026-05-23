# Audit Log Specification v0.1 (draft)

This document defines the audit log — the durable, queryable, tamper-evident record of everything that happens inside a Loamss runtime. It is the **source of truth for what was accessed, by whom, when, and with what outcome**, and it is a first-class user-facing surface, not a debug artifact.

> **Status: draft.** Event types and the redaction policy will evolve as the runtime is implemented. Schema additions are minor-version changes; field renames or removals are breaking.

## What this document is and is not

This spec defines:

- The universal entry schema every audit record conforms to
- The complete catalog of event types the runtime emits
- The tamper-evidence model (hash chain) that makes the log integrity-checkable
- Storage layout, rotation, and retention
- The redaction policy that keeps sensitive content out of the log
- Query, export, and subscription surfaces

This spec does **not** define:

- The on-disk format of the runtime's hot audit store beyond requiring it be append-only
- Console UI for browsing the audit log
- Specific alerting or anomaly-detection logic on top of the log (a capsule's job, if one wants it)

## Why audit is a first-class surface

Every claim Loamss makes about user control is verifiable only if the user can answer four questions on demand:

- **Who has access right now?** Answerable from the grants table (covered in `permission-model.md`).
- **What did they actually do?** Answerable from the audit log.
- **Was anything denied?** Same.
- **Has the record been tampered with?** Answerable from the tamper-evidence chain.

If the audit log fails on any of these, the user-control story collapses. So the log is engineered to be **complete** (every gated operation produces an entry), **understandable** (entries are structured, not freeform text), **durable** (write-through to user storage), and **integrity-checkable** (hash-chained).

## Universal entry schema

Every audit entry, regardless of type, has the same envelope:

```json
{
  "id": "aud-01HVZBCDEFGHJKMNPQRSTVWXYZ",
  "timestamp": "2026-05-23T16:11:45.123Z",
  "type": "grant.create",
  "actor": {
    "kind": "user" | "capsule" | "client" | "runtime" | "system",
    "id": "user" | "capsule:email-drafter@1.4.0" | "client:abc123"
  },
  "subject": {
    "kind": "grant" | "resource" | "memory" | "tool" | "capsule" | "client" | "source" | "config",
    "id": "grt-01HVZ..." | "loamss://files/contracts/v3.pdf"
  },
  "outcome": "success" | "denied" | "error" | "pending" | "n/a",
  "data": { /* event-specific payload — schema per type */ },
  "context": {
    "request_id": "req-...",
    "session_id": "sess-...",
    "correlation_id": "...",
    "ip": "...",
    "user_agent": "...",
    "runtime_version": "0.1.0"
  },
  "prev_hash": "sha256:...",
  "hash": "sha256:..."
}
```

Field rules:

- **`id`**: ULID for monotonic time-ordering across the chain
- **`timestamp`**: RFC 3339 with millisecond precision in UTC
- **`type`**: dotted lowercase identifier from the catalog below
- **`actor`**: never null — `system` is used for runtime-initiated events with no human or principal
- **`subject`**: may be null for runtime-level events (e.g., `runtime.start`)
- **`outcome`**: `n/a` is used for purely informational events (start/stop); everything else has a meaningful outcome
- **`data`**: event-type-specific; schema is defined per type in the catalog
- **`context`**: optional fields filled when applicable; the runtime never invents context it doesn't have
- **`prev_hash`** and **`hash`**: the tamper-evidence chain (see below)

## The event catalog

Organized by domain. Every type that any spec references appears here.

### Capsule lifecycle

| Type | Outcome modes | `data` includes |
|---|---|---|
| `capsule.install` | success, denied, error | name, version, signature, grants requested |
| `capsule.update` | success, error | name, from_version, to_version |
| `capsule.uninstall` | success | name, version, grants revoked |
| `capsule.invoke` | pending → success/denied/error | name, tool, args_hash |
| `capsule.crash` | n/a | name, exit_code, stderr_tail |

### Client lifecycle and authentication

| Type | Outcome modes | `data` includes |
|---|---|---|
| `client.pair.attempt` | success, denied, error | name, public_key_fingerprint |
| `client.pair.success` | success | client_id, granted_scopes |
| `client.pair.fail` | denied, error | reason |
| `client.revoke` | success | client_id, reason (user-initiated, expired, etc.) |
| `client.authenticate` | success (sampled), denied | client_id, sample_count |

### Grants

| Type | Outcome modes | `data` includes |
|---|---|---|
| `grant.create` | success | grant_id, principal, capability, scope, framing, expires_at |
| `grant.modify` | success | grant_id, before, after |
| `grant.revoke` | success | grant_id, reason (user, expiry, principal_removed) |
| `grant.expire` | success | grant_id |

### Permission checks

| Type | Outcome modes | `data` includes |
|---|---|---|
| `check.allow` | success (sampled) | grant_id, capability, scope_match |
| `check.deny` | denied | capability, scope_attempted, reason |

`check.allow` is sampled by default (1 in N), but the `data` retains enough to reconstruct activity rates. `check.deny` is **always full** — denials are rare and important.

### Approvals (consequential actions)

| Type | Outcome modes | `data` includes |
|---|---|---|
| `approval.requested` | pending | requester, action, target, content_summary |
| `approval.granted` | success | request_id, decided_by, latency_ms |
| `approval.denied` | denied | request_id, decided_by, reason |
| `approval.timeout` | error | request_id, timeout_at |

### Sources (data ingestion)

| Type | Outcome modes | `data` includes |
|---|---|---|
| `source.add` | success, denied | type, name, scopes |
| `source.remove` | success | name |
| `source.sync.start` | n/a | name, prior_cursor |
| `source.sync.complete` | success | name, items_added, items_updated, items_removed |
| `source.sync.fail` | error | name, error_class, retry_in |

### Storage

| Type | Outcome modes | `data` includes |
|---|---|---|
| `storage.read` | success (sampled), denied, error | path, byte_count |
| `storage.write` | success, denied, error | path, byte_count |
| `storage.delete` | success, denied, error | path |
| `storage.url.issued` | success | path, ttl_seconds, consumer |

`storage.read` is sampled at a high rate; the runtime maintains per-actor counters that are flushed periodically into the audit log to track activity without one entry per byte. Writes and deletes are always logged in full.

### Memory

| Type | Outcome modes | `data` includes |
|---|---|---|
| `memory.upsert` | success | entity_id, entity_type, source, provenance |
| `memory.delete` | success, denied | entity_id, reason |
| `memory.forget` | success | entity_id, scope (cascade or single) |
| `memory.query` | success (sampled), denied | query_hash, k, filter, result_count |

`memory.query` content is hashed by default to avoid logging sensitive query text; full payload retained if the requesting client has `audit.full_query_log` capability (rare; mostly for debugging).

### Model calls

| Type | Outcome modes | `data` includes |
|---|---|---|
| `model.call.start` | n/a | adapter, model_id, task, prompt_hash, prompt_tokens, data_classes |
| `model.call.complete` | success | adapter, model_id, completion_tokens, cost, latency_ms |
| `model.call.fail` | error | adapter, model_id, error_class |
| `model.embed` | success (sampled), error | adapter, model_id, batch_size, total_tokens |

Prompt and completion content are not stored in the audit log by default; only hashes and token counts. The audit log records that a call happened, not the content. The content is in memory (with its own access control).

### External actions

| Type | Outcome modes | `data` includes |
|---|---|---|
| `external.http.request` | success, error, denied | host, method, path_hash, status |
| `email.send` | success, denied, error | recipient, subject_hash, message_hash |
| `messages.send` | success, denied, error | channel, message_hash |
| `payment.attempt` | success, denied, error | provider, amount, recipient_hash |
| `content.publish.url_issued` | success | content_id, consumer, ttl |

Outbound actions are **always fully logged** — these are the most consequential operations and the most important to audit.

### Event write-backs (clients writing into Loamss)

| Type | Outcome modes | `data` includes |
|---|---|---|
| `event.write` | success, denied, error | source, type, subject, data_summary |

The full payload of a client-written event is stored in memory as an attributed claim; the audit entry records the metadata of the write.

### Runtime

| Type | Outcome modes | `data` includes |
|---|---|---|
| `runtime.start` | n/a | version, config_hash |
| `runtime.stop` | n/a | reason |
| `runtime.error` | error | error_class, fatal |
| `config.change` | success | section, before_hash, after_hash |
| `adapter.error` | error | adapter, error_class, retry |

## Tamper-evidence: the hash chain

Every audit entry's `hash` field is computed as:

```
hash = SHA-256(prev_hash || canonical_json(entry_without_hash))
```

Where `canonical_json` uses sorted keys and no whitespace. The first entry's `prev_hash` is the genesis constant (`sha256:0000...`).

This means:

- **Tampering with any entry** invalidates the hash of every subsequent entry — detectable by replay verification
- **Removing an entry** leaves a gap that doesn't chain — detectable
- **Reordering entries** breaks the chain — detectable
- **Appending fabricated entries** can be detected if the runtime periodically commits the latest hash to user storage and external attestation surfaces

The runtime ships `loamss audit verify` which replays the chain and reports any break.

This is not cryptographic strong tamper-resistance — a sufficiently determined attacker with write access can re-forge the entire chain. For most threat models (accidental corruption, post-hoc denial, basic external attacker), it is more than enough. Higher assurance options (per-entry signing with hardware-backed keys, Merkle commitments to external transparency logs) are deferred to future versions.

## Storage and retention

Two stores, with different retention policies:

### Hot store (runtime-local)

- Backed by SQLite in `~/.loamss/audit/` by default
- Append-only; never modified in place
- Bounded size (default: 7 days or 1 GB, whichever comes first)
- Old entries rotated out when the bound is hit
- Indexed for fast querying (`atlas audit tail` and `audit log` use it)

### Cold store (user storage, durable)

- Written through to the user's configured storage adapter
- Stored as **gzipped JSONL files** rotated daily: `audit/YYYY/MM/DD.jsonl.gz`
- Each daily file's chain is closed with a manifest containing the final hash, plus a count and time range
- Retention: indefinite by default; user-configurable
- The cold store is the **canonical record**; the hot store is a queryable cache

Failure of the cold store does not block runtime operations — the runtime buffers and retries — but a sustained outage triggers a warning surfaced to the console and `loamss doctor`.

## Redaction policy

The default rule: **the audit log records that something happened and what its shape was, not its full content.**

| Field type | Default treatment | Notes |
|---|---|---|
| Email body | Hash | Stored in memory, not audit |
| Email subject | Hash | Same |
| Message body | Hash | Same |
| Tool arguments (general) | Hash | Full payload only if `requires_user_approval` was set (user already saw it) |
| Memory query text | Hash | Full only with explicit grant |
| Model prompt | Hash + token count | Full only with `audit.full_query_log` (rare debugging) |
| Model completion | Hash + token count | Same |
| Signed URLs | Path + TTL only | URL strings contain bearer credentials; never logged |
| API keys, OAuth tokens | Never logged | Strict rule, enforced by runtime serialization |
| Phone numbers, emails as identifiers | Logged | These are first-class identity fields |
| File paths | Logged | Necessary for "what files were touched" |
| Recipient addresses on send | Logged | Strict invariant — must be auditable |

The redaction policy is itself a config value. The user can dial it up (more hashing) or down (more content). The default is calibrated for the common case: enough to reconstruct activity, not enough to leak content from an audit dump.

## Query

Audit entries are queryable through three surfaces:

### CLI

Defined in `cli.md`:

```bash
loamss audit tail                                       # live stream
loamss audit log --since=2026-05-01 --client=chatgpt    # filtered
loamss audit log --capsule=email-drafter --type=email.send
loamss audit log --outcome=denied                       # all denials
loamss audit verify                                     # chain integrity check
loamss audit export --format=jsonl --since=...          # bulk export
```

### Console

The audit log is a top-level console surface. Filterable by time range, type, actor, subject, outcome. Searchable by free text within `data`.

### MCP (for paired clients with `audit.read`)

A paired client with the `audit.read` capability can query the audit log via tools on the MCP surface. The scope can be narrowed to the client's own activity, all activity, or specific time ranges. This is what makes the audit log useful to monitoring capsules and external dashboards the user opts into.

## Export format

`loamss audit export` produces a **JSONL stream** matching the on-disk cold-store format. The stream is self-contained: it includes the genesis hash, every entry verbatim, and a final manifest entry with the last hash and total entry count.

Importing a stream into another Loamss (via `loamss audit import`) appends to the chain, preserving the full historical record across runtime migrations.

## Subscribers

Capsules can subscribe to audit events by declaring `audit.subscribe` in their manifest with a filter expression:

```yaml
- capability: audit.subscribe
  scope:
    types: ["check.deny", "approval.requested"]
  rationale: "Surface security-relevant events to a dashboard."
```

The subscribed capsule receives matching entries shortly after they're committed. Subscriptions are **read-only** — capsules cannot write to the audit log directly (only the runtime can).

External clients may also subscribe via the MCP subscription primitive once Phase 2 lands; not part of v0.1.

## Audit-as-resource

The audit log itself is exposed as an MCP resource type:

```
loamss://audit/entry/aud-01HVZ...
loamss://audit/query?since=...&type=...
```

Clients with the `audit.read` capability can read individual entries and run queries. The audit log is treated like any other resource — gated by scope, logged on access (an `audit.read` of the audit log produces its own audit entry; this is intentional and recursive).

## Open questions

- **Hash algorithm**: SHA-256 today. Move to SHA-3 or BLAKE3 if performance or post-quantum concerns shift the calculus. Versioned per-chain header to allow migration.
- **Cross-runtime continuity**: when a user moves Loamss between machines, the audit chain continues. But what if two runtimes start independently and a user wants to merge? Initial leaning: forbidden — merging chains breaks tamper-evidence semantics. Migration is unidirectional.
- **External transparency log**: should the runtime optionally publish chain heads to a public transparency log (rekor-style) for stronger non-repudiation? Defer; opt-in if it lands.
- **Sampling rate for hot-path events**: defaults specified but not tuned. Tuning is a Phase 1 implementation concern.
- **Audit log size growth**: the cold store grows unbounded by default. Should the runtime offer a "compact" operation that summarizes old entries (e.g., "between dates X and Y, capsule Z made 12,847 successful storage reads")? Possibly — would lose detail but preserve chain integrity if structured correctly. Defer to v0.2.
- **Cross-actor correlation**: `request_id` and `correlation_id` are defined but not specified. The runtime needs a consistent convention for propagating these across capsule invocations and client requests; spec'd separately in implementation.
- **Privacy of denial reasons**: a `check.deny` entry includes the reason. Sometimes the reason itself is sensitive (e.g., "client lacks `health.read` for `data_classes_excluded: [hiv_status]`"). Initial leaning: reasons are stored verbatim; user can apply post-hoc redaction on export.
- **Mandatory event types for compliance**: if Loamss is later used in regulated contexts (healthcare, finance, education), specific event types and retention rules may be required. Out of scope for v0.1; revisit when real compliance demand emerges.
