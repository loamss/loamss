# MCP Surface Specification v0.1 (draft)

This document defines the MCP-shaped surface the Loamss runtime exposes to external clients. It is the contract between the runtime and the outside world — every ChatGPT, Cursor, content platform, peer Loamss, or script that connects to a Loamss instance speaks to it through this surface.

> **Status: draft.** Surface will change before v1.0. Breaking changes after v1.0 require a major version bump and a migration path for existing paired clients.

## What this document is and is not

This spec covers Loamss-specific contracts layered on top of MCP. It does **not** restate the underlying MCP wire protocol — that's defined upstream at modelcontextprotocol.io. When this document says "tool call" or "resource," the semantics are MCP's; only the Loamss-specific shapes, capabilities, and policy attachments are defined here.

This spec also does **not** cover the internal MCP used between the runtime and locally-installed capsules. That contract lives in `capsule-spec.md`. The two MCP boundaries are deliberately distinct: external clients face this surface; capsules face the capsule-runtime surface.

## What the surface exposes

External clients see four kinds of interactions, all gated by the permission framework:

1. **Resources** — addressable read targets (files, memory entries, structured records, binary content).
2. **Tools** — invocable functions (search, query, perform an action).
3. **Events** — client-initiated writes back to Loamss (metrics, attestations, status updates from external platforms).
4. **Subscriptions** — push notifications when underlying data changes. Deferred to Phase 2.

## Resources

Resources are addressed by Loamss URIs:

```
loamss://<resource-type>/<identifier>[?<params>]
```

Examples:

```
loamss://memory/entity/sarah-chen-acme
loamss://email/thread/19a8b5c2
loamss://files/contracts/v3.pdf
loamss://content/video/abc123
loamss://content/video/abc123/thumbnail
```

A resource read returns either:

- **Structured metadata** (JSON) when the content is small or descriptive
- **A signed URL redirect** when the content is binary (video, audio, large files)

### Signed URL redirection (the bandwidth contract)

For binary content, the runtime issues a short-lived signed URL pointing **directly at the user's storage** (S3 bucket, local file server, etc.). The client streams bytes from storage, never through Loamss. This is the contract that keeps Loamss off the bandwidth path — required for the creator-publishing scenarios in `scenarios.md` §5 and §6.

```
Client → GET loamss://content/video/abc123/stream
Runtime → 307 Temporary Redirect
  Location: https://creator-bucket.s3.amazonaws.com/videos/abc123.mp4?X-Amz-Expires=600&...
```

- Default TTL: **10 minutes**
- The audit log records the URL issuance, including the consuming client and the resource
- The byte fetch from storage is the user's storage adapter's concern, not Loamss's
- TTL is per-resource configurable in exposer capsule manifests

### Resource discovery

Clients can list resources they have access to via standard MCP discovery. Results are **scoped** — a client only sees resources within its granted scope. A `health.read` grant scoped to "last 12 months" never reveals older health resources in listing responses.

## Tools

Tools follow standard MCP tool-call semantics: declared input schema, validated on entry, structured output. Two tool sources mount onto the surface:

- **Runtime-provided tools**: `memory.query`, `memory.show`, `audit.read`, `client.info`, `pairing.*`, and a small set of universally-available primitives. Always present.
- **Capsule-provided tools**: declared by exposer capsules (see `capsule-spec.md`). Mounted into the surface when the capsule is installed.

Every tool call:

1. Authenticates the client (per-client credential)
2. Checks the call against the client's granted scopes
3. Pauses for user approval if the tool's manifest declares `requires_user_approval`
4. Executes (with permission checks at every storage / memory adapter call inside the runtime)
5. Logs to the audit trail
6. Returns the structured result

### Consequential actions

Tools that take action in the outside world (send email, post content, transfer money, delete data) must declare `requires_user_approval: true` in the providing capsule's manifest. When such a tool is called:

- The runtime returns a `pending` response immediately
- A notification is pushed to the console / phone companion
- The user approves or denies
- The runtime resumes the tool call (or returns `denied`)

This is asynchronous; clients should not assume tool calls are always synchronous.

## Events

Events are the **write-back** surface — external clients pushing data **into** Loamss. This is how content platforms report plays and revenue, how scheduling systems push confirmations, how trackers push state.

Events differ from tool calls in two ways:

1. **Direction**: events are client → runtime writes, not requests for action.
2. **Storage**: events are stored as **attributed claims**, never silently merged with other sources. Vibez claiming 10,000 plays is stored as "Vibez asserts 10,000 plays at timestamp T" — queryable, but the runtime never treats it as ground truth.

### Event shape

Events use a CloudEvents-inspired envelope:

```json
{
  "id": "evt-01HVZ...",
  "type": "content.metrics",
  "source": "client://vibez",
  "time": "2026-05-23T15:00:00Z",
  "subject": "loamss://content/video/abc123",
  "data": {
    "plays": 1,
    "watch_seconds": 47
  }
}
```

- `id`: client-generated, used for idempotency on retries
- `type`: declared by an exposer capsule (e.g., `content.metrics`, `content.revenue`, `presence.update`)
- `source`: the writing client's identity (always set by Loamss from the credential, never trusted from the payload)
- `subject`: the Loamss URI the event refers to
- `data`: type-specific payload, validated against the exposer's declared schema

### Event sinks

An exposer capsule declares the event types it accepts:

```yaml
events:
  - type: content.metrics
    schema: { ... }
    capability: content.metrics.write
  - type: content.revenue
    schema: { ... }
    capability: content.revenue.write
```

Clients must hold the matching `*.write` capability and pass the schema validation. Permission denial and validation failures both produce audit entries.

### Event retention and query

Events are stored in the memory layer as a time-series, tagged by source and subject. They are queryable through `memory.query` with provenance preserved:

```
"How many plays did video abc123 get across all platforms last month?"
→ Aggregates events of type=content.metrics, subject=loamss://content/video/abc123
  Returns per-source breakdown: { vibez: 1037, resound: 482, ... }
```

## Subscriptions (Phase 2)

Clients will eventually be able to subscribe to changes — "tell me when new email arrives from sarah@acme.com," "tell me when memory entry X is updated." The transport (SSE, WebSocket, or MCP's evolving subscription primitives) and the rate-limiting model are deferred until Phase 2.

## Pairing

The bootstrapping primitive for a new external client. Specified here because it's the entry into the MCP surface for every other interaction.

### CLI-initiated pairing (the common case)

```
1. User: loamss client pair --name "ChatGPT laptop"
2. Runtime: generates a one-time code (default TTL: 10 minutes)
3. User: pastes the code into the client's MCP configuration
4. Client: POST /pair { code: "ABCD-1234", client_metadata: { name, public_key, ... } }
5. Runtime: validates code, presents permission slip in console
6. User: grants (and optionally narrows scope, sets expiry)
7. Runtime: issues per-client credential, returns endpoint URL + credential
8. Client: stores credential, future requests authenticate against it
```

### QR-initiated pairing (mobile / kiosk case)

Console / mobile companion app displays a QR code encoding the pairing code + endpoint URL. Same flow from step 4 onward. Used for the clinic-visit scenario (`scenarios.md` §2).

### Pairing invariants

- One-time codes are **single-use** and TTL-bound (default 10 min)
- Per-client credentials are **opaque bearer tokens** + a stable client ID
- Credentials are **revocable** at any time via `loamss client revoke` or the console
- Every pairing attempt is logged, successful or not

## Authentication

External clients authenticate using their per-client credential on every request:

```http
GET /resources/memory/entity/sarah-chen-acme
Authorization: Bearer <client-credential>
```

The runtime maps the credential to:

- Client ID
- Granted scopes
- Pairing metadata (name, paired-at, last-access)

Authentication failures are logged separately from authorization failures — failed auth often indicates a revoked credential or a compromise attempt.

## Authorization

Every request crossing the MCP surface is checked against the **permission framework** before any storage or memory adapter is touched. The check covers:

- Does the client hold a grant for the requested capability?
- Does the scope match the requested resource / tool / event?
- Has the grant expired or been revoked?
- Does the tool require user approval?
- Is the operation forbidden by data-class rules (e.g., `forbidden_data_classes: ["health"]`)?

Authorization decisions are **always logged** to the audit trail, including denials. Denials are not silent failures — clients receive an explicit error.

## Transport

The MCP surface is hosted by the runtime over one of:

| Transport | When | Default |
|---|---|---|
| HTTP + SSE | Standard remote / network case | Yes for paired clients |
| HTTP + WebSocket | When subscriptions land (Phase 2) | Future |
| stdio | Same-machine local clients (rare) | No |

Binding defaults:

- **`127.0.0.1:7777`** at first start
- Tailscale-style overlay exposure: documented opt-in
- Explicit public ingress: opt-in, with a strong console warning and a required confirmation

The runtime never auto-exposes the surface publicly. The trust model assumes external exposure is a deliberate user choice with informed consent.

## Audit hooks

Every interaction across this surface produces audit entries:

| Event | Audit entry |
|---|---|
| Pairing attempt (success or fail) | `pair.{success\|fail}` |
| Authentication (per request) | Sampled — full record on auth failure, request-count summary on success |
| Tool call | `tool.{name}.{success\|denied}` with arguments hash |
| Resource read | `resource.read.{type}` |
| Signed URL issuance | `resource.url.issued` with TTL and target |
| Event write | `event.{type}.{success\|denied}` with payload schema |
| Grant change | `grant.{create\|modify\|revoke}` |

Audit entries are surfaced via the console, the CLI (`loamss audit tail`), and the audit log export. See `cli.md` for the CLI surface.

## Error model

Standard MCP error responses, with Loamss-specific error codes for permission outcomes:

| Code | Meaning |
|---|---|
| `loamss.unauthenticated` | No or invalid credential |
| `loamss.unauthorized` | Credential valid, scope insufficient |
| `loamss.scope_mismatch` | Capability granted, but scope doesn't cover the request |
| `loamss.approval_pending` | Tool call is awaiting user approval |
| `loamss.approval_denied` | User denied a consequential action |
| `loamss.data_class_forbidden` | Routing rule blocks this data class for this client |
| `loamss.rate_limited` | Per-client bucket exhausted |
| `loamss.signed_url_expired` | TTL on a signed URL passed; re-request |

Error responses carry enough context for the client to either retry, request elevated scope, or surface a useful message to its own user — but never enough context to fingerprint the user's data beyond the request itself.

## Versioning

The surface declares a version string:

```
loamss-mcp-surface: 0.1
```

- **Minor bumps** (`0.1 → 0.2`): backward-compatible additions (new tools, new event types, new error codes). Old clients keep working.
- **Major bumps** (`0.1 → 1.0`, `1.0 → 2.0`): breaking changes. Existing paired clients are notified, may need re-pairing. Breaking changes after v1.0 will be rare and announced through the registry.

Capsules exposing tools or events declare the surface version they target. The runtime refuses to mount exposers that require a version higher than its own.

## Discovery

Clients can discover the surface using standard MCP discovery, scoped to their grants:

```
GET /discover
→ {
    "resources": ["memory", "email", "calendar"],          // only what's in scope
    "tools": ["memory.query", "email.search", ...],         // only callable tools
    "events": ["content.metrics"],                          // only writable event types
    "surface_version": "0.1",
    "server_capabilities": ["signed_urls", "approvals"]
  }
```

Tool, resource, and event schemas are fetched on demand from the same endpoint.

## Open questions

- **Subscriptions**: when do push updates land? Phase 2 alongside the agent host, or earlier?
- **Streaming responses**: tool calls that produce large or long-running results — chunked? SSE? Initial leaning: SSE for synchronous streaming, async with callback for long-running.
- **Federation peer pairing**: pairing codes for a peer Loamss are richer than a single string — likely a mutual-auth exchange with both sides showing permission slips. Spec deferred to Phase 3.
- **Cross-Atlas API beyond MCP**: do we expose any gRPC or REST surface for non-MCP integrations? Initial leaning: no — MCP-first, force consumers to speak the standard. Re-evaluate if real demand emerges.
- **Rate-limiting policy**: per-client buckets, but what defaults? Per-capability? Configurable per pairing? Defer to Phase 1 implementation.
- **Surface evolution under upstream MCP changes**: if MCP itself evolves, how do we manage the dual versioning? Initial leaning: pin to a known MCP version per surface major, advance the surface major when migrating.
- **Idempotency**: event writes carry an `id` for idempotency. Do tool calls also? Probably yes for consequential ones (`requires_user_approval`) to prevent double-send on retries.
- **Client-side capability discovery**: should clients be able to *request* additional scopes mid-session, or must scope changes always go through a fresh pairing/permission flow? Initial leaning: clients can request, runtime always asks the user.
