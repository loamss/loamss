# Loamss Architecture

This is the full technical picture. For a one-paragraph summary, see `README.md`. For end-to-end use cases the design must support, see `scenarios.md`. For the CLI surface, see `cli.md`. For Claude Code context, see `CLAUDE.md`. For the build sequence, see `ROADMAP.md`.

## The shape

Loamss is a **three-layer stack**:

```
┌─────────────────────────────────────────────────────────┐
│  Consumers (AI tools, platforms, peer Loamsses, scripts) │
├─────────────────────────────────────────────────────────┤
│  Loamss — runtime, memory, MCP surface, permissions      │
├─────────────────────────────────────────────────────────┤
│  User-owned resources (storage, identity, compute)      │
└─────────────────────────────────────────────────────────┘
```

The middle layer is what we build. The top layer is anything that speaks MCP — Claude, ChatGPT, Cursor, an agent, a content platform, another Loamss, the user's own scripts. The bottom layer is whatever storage the user has pointed us at and whatever machine the runtime runs on. We define the interfaces; the user owns the implementations of the top and bottom.

## The runtime, in one sentence

Loamss is a daemon that ingests data into user-owned storage, builds memory on top of it, and exposes governed views to external MCP clients while logging every access.

## Core components

These are the always-present parts of the runtime. Everything else is either a capsule (extensibility) or user-brought (resource).

### 1. The Runtime Daemon

The orchestrator. Single binary, cross-platform. Runs on the user's laptop, home server, or hosted instance. It is the only component that sees plaintext memory and storage. Responsibilities:

- Host the MCP surface (component 4) and answer client requests
- Run capsules (component 8) and mediate their access
- Enforce permissions (component 5) on every read and write
- Manage source ingestion (capsule-driven, component 8a)
- Maintain the audit log (component 6)
- Provide the local API the console (component 7) talks to

The runtime holds **no persistent business state of its own**. Its only local state is operational: capsule registrations, permission grants, paired clients, OAuth tokens for connected sources, audit log shards, session state. Everything semantic belongs in user-owned storage and memory.

### 2. The Storage Adapter Layer

A driver interface for backends that hold the user's bytes. Adapters at MVP:

- Local filesystem (encrypted at rest)
- SQLite (encrypted)
- S3-compatible object storage
- Postgres

The user picks one (or a combination) at deploy time. The runtime reads and writes only through this interface. Adapter contracts live in `adapter-interface.md`.

### 3. The Memory Layer

The product. This is what makes Loamss valuable; everything else exists in service of it.

Memory is more than vector embeddings. It is:

- **A vector index** for semantic recall
- **An entity store** with resolution (knowing "Sarah Chen at Acme" is the same Sarah across email, calendar, and notes)
- **A relationship graph** linking people, projects, topics, files, events
- **Episodic summaries** — compressed records of what happened when
- **Provenance** — every memory entry knows where it came from (which source, which capsule, what time) and who wrote it (capsule? external client claim?)

The memory layer ships with adapters for the embedding/index backend:

- SQLite + sqlite-vec (the all-local default)
- pgvector (when the user is on Postgres)
- Chroma, Qdrant (when scale or features demand)

The entity store, graph, and episodic layers run **inside the runtime**, on top of whichever adapter holds the vectors. They are not separate services. The memory adapter spec covers what backends must provide; the rest is runtime code.

### 4. The MCP Surface

The interface the outside world talks to. Loamss runs an MCP server endpoint that paired clients connect to. Through it:

- **Resources** are listed and read (data, files, memory entries, content blobs)
- **Tools** are invoked (search, query, write actions)
- **Events** are written back by clients (metrics, revenue claims, status updates)
- **Subscriptions** push updates to clients when underlying data changes (future)

Every request that crosses this surface passes through the permission framework before any storage or memory adapter is touched.

For large binary content (video, audio), the MCP surface issues **signed URLs into the user's storage** rather than proxying bytes. The runtime never becomes a CDN. See `scenarios.md` §5 for the creator publishing flow.

The MCP surface specification lives in `mcp-surface.md`.

### 5. The Permission Framework

Capability-based consent. Two flavors, sharing the same mechanism:

- **Capsule capabilities** — what a capsule can do *to* your data when running (e.g., a capsule's manifest declares `email.read` with a sender scope; the runtime enforces it at every read).
- **Client scopes** — what an external MCP client can see *of* your data when querying (e.g., ChatGPT is paired with `memory.query` scoped to "people, projects, but not health").

Both share:

- A capability namespace (e.g., `email.read`, `calendar.write`, `memory.query`, `content.publish`)
- A scope schema specific to each capability (paths, tags, time windows, entity classes)
- An optional `requires_user_approval` flag for consequential actions
- Optional expiry / auto-revoke
- An audit trail entry on every check

When a permission slip is presented at install time (capsule) or pairing time (client), the user can narrow scope and set expiry. Grants are revocable at any time via console, CLI (`loamss grant revoke`), or auto-expiry.

Permission model details: `permission-model.md`.

### 6. The Audit Log

A first-class user-facing surface, not a debug artifact. Every:

- Capsule data access (read or write)
- External client request (read, tool call, event write)
- Model call (which model, by which capsule, on which data)
- External action (email sent, file shared, payment recorded)
- Grant change (created, modified, revoked)
- Pairing event (client added, removed, re-authenticated)

…produces an audit log entry. Entries are append-only, structured (JSONL), and exportable.

The runtime keeps a hot copy in local state; full retention belongs in user-owned storage (write-through). This survives runtime data loss and stays under user control.

### 7. The Console

The user-facing web UI. Runs as a static site against the runtime's local API. No external dependencies. Surfaces:

- Setup wizard (storage, memory, connectors, optional model keys)
- Sources (data ingestion adapters: list, add, sync, OAuth lifecycle)
- Capsules (installed, available, install/uninstall, configure, permission slips)
- Clients (paired external MCP clients: list, pair, revoke, scope edit)
- Permissions (all grants across capsules and clients — issue, modify scopes, set expiry, revoke, review the slip)
- Audit log (filterable, searchable, exportable)
- Memory browser (people, projects, topics; query, correct, forget)
- Runtime status (health, version, logs)

The console is **opt-in via config**. The runtime can be deployed two ways:

- **Headless** (default in v0.1): CLI + MCP surface only. Permission management uses `loamss grant list/show/revoke` and the pairing flow.
- **Console-enabled**: the runtime also serves the console on its listen address, providing the same operations through a GUI. The same underlying grant store, audit log, and adapters serve both surfaces.

The toggle lives in the config file (see `adapter-interface.md`):

```yaml
console:
  enabled: false       # default; set true to serve the console
  path: /console       # URL prefix where the console mounts
```

When disabled, the console assets are not served and any HTTP request to the console path returns 404. Users who prefer to manage the runtime entirely via the CLI never run anything else; users who want a GUI flip the flag.

## Capsules

Capsules are the extensibility surface. A capsule is a packaged, signed unit that the runtime invokes via MCP (subprocess by default). The capsule spec lives in `capsule-spec.md`.

### Taxonomy

Capsules are grouped by what they do. The runtime treats them uniformly; the taxonomy is for clarity and discovery.

#### 8a. Ingestors

Pull data from external sources into user storage. One-way sync, no agent behavior, declared schemas. Examples: `gmail-ingestor`, `gcal-ingestor`, `drive-ingestor`, `slack-ingestor`, `github-ingestor`, `fs-ingestor`.

Reference ingestors ship with the runtime. The community fills the long tail. From the user's perspective, ingestors are managed via `loamss source` — a deliberate vocabulary split (see `cli.md`).

#### 8b. Organizers

Read from storage, write to memory. They build the brain: entity resolution, summarization, relationship extraction, embedding generation, classification. Examples: `email-organizer`, `people-resolver`, `episodic-summarizer`, `topic-extractor`.

Organizers are where the **model router** matters — they are the primary internal consumers of LLM calls. Organizers ship with the runtime by default but can be replaced or supplemented by user-installed alternatives.

#### 8c. Exposers

Define new MCP resource types and tool surfaces to expose to external clients. Examples: `content-publisher` (videos, audio, posts), `health-exposer` (medical records with clinical-grade scoping), `notes-exposer`.

Exposers extend the MCP surface. They declare what they offer (resource shapes, tools, event sinks) and the runtime mounts those into the paired-client interface, gated by scopes.

#### 8d. Actuators

Take action in the outside world on behalf of the user. Examples: `email-sender`, `calendar-writer`, `payment-actuator`, `social-poster`. Actuators almost always require `requires_user_approval` on their capabilities; this is the bright line between "an AI helped me" and "an AI did something to my world."

A single capsule can wear multiple hats (e.g., `email-drafter` reads, exposes a drafting tool, and acts as an actuator on send). The taxonomy is descriptive, not exclusive.

### 9. The Capsule Registry

Where capsule authors publish and users discover. Required surfaces:

- HTTPS API for `loamss capsule install <name>`
- Web UI for browsing
- Signing and signature verification
- Versioning, semver, update channels
- Reputation and reviews
- Reporting and takedown for malicious capsules

The registry is open. Anyone can run their own. The canonical one is operated by the project but the runtime can point at any compliant registry.

### 10. The SDKs

For capsule developers. Each SDK provides:

- A capsule manifest builder
- MCP server scaffolding (matched to the chosen capsule type)
- Permission request helpers
- Memory query helpers
- Audit-friendly logging
- Local test harness — run a capsule against fixture data without a real runtime

TypeScript first, Python second. Other languages welcome.

## User-brought resources

The bottom layer of the stack. These are not components Loamss implements; they are the resources Loamss consumes.

### Storage

Wherever the user wants their data to live. Local disk, home NAS, S3 bucket they own, hosted Postgres. We provide adapters. They hold the keys.

### Compute

Where the runtime itself runs. Laptop, home server, NAS, hosted instance. The framework is platform-agnostic.

### Identity and credentials

OAuth tokens for connected services. Loamss manages the lifecycle; the user owns them; revocation is one click.

### Model access (optional)

API keys for whichever models they trust — Claude, OpenAI, Mistral, local Ollama, etc. Used by **organizer capsules** to build memory and by **actuator capsules** for generation tasks. External clients (ChatGPT, Cursor, etc.) bring their own model access; Loamss does not proxy LLM calls on their behalf.

A user can run Loamss with no model keys at all — they get raw storage, keyword memory, and external clients still work. Semantic memory and organizer capsules require at least one model.

## Deferred components

Real eventually, not now.

### Agent host (Phase 2)

Long-running capsules — "watch my inbox this week," "summarize my Slack daily" — need lifecycle management beyond request/response. The agent host gives the runtime a way to schedule, monitor, and restart standing capsules with their own state.

### Federation (Phase 3)

Selective data sharing between Loamss instances — household members sharing a calendar, a small team sharing a project's documents, a doctor's office reading a patient's record for a scheduled visit. The current design treats federation as "another MCP client, but the client is another Loamss." When federation lands, **A2A** (Agent2Agent Protocol) may be a better fit for parts of it than MCP. Decision deferred to Phase 3; MCP is the working assumption for now.

### Inter-capsule communication (Phase 2+)

Currently capsules cannot call other capsules. When this lands (likely for orchestration capsules), the right shape is also A2A, not MCP. See `capsule-spec.md` open questions.

## Pairing and trust establishment

The missing primitive in earlier drafts. Pairing is how an external MCP client establishes trust with an Loamss the first time.

The flow:

1. User runs `loamss client pair --name "<client name>"` (or clicks **Pair Client** in the console). Loamss generates a one-time, short-lived pairing code (and a QR-encoded equivalent).
2. User pastes the code (or scans the QR) into the client's MCP configuration. The client uses it to fetch a per-client credential from Loamss.
3. The client connects, presents its credential, and requests scopes. Loamss shows a permission slip; user grants or narrows.
4. Per-client credential is stored in runtime local state. Future requests authenticate against it. The credential is revocable.

The same flow handles ChatGPT, Cursor, a clinic's intake AI, a content platform like the one in `scenarios.md` §5, and (in Phase 3) a peer Loamss.

## Key flows

### Flow A — External MCP client requests context (the canonical case)

```
ChatGPT (paired): "What's the context on Sarah's contract?"

1. Client → MCP surface: tool call email.search(sender:sarah) + memory.query(entity:sarah)
2. Runtime → Permission framework: client has email.search? memory.query?
   → grants exist, scopes match → allowed → logged
3. Runtime → Memory adapter: fetch Sarah's entity record + recent interactions
4. Runtime → Storage adapter: fetch matching email threads
5. Runtime → MCP surface: return structured response
6. Client uses its own model to draft a reply (Loamss is not involved in the LLM call)
7. (Optional) Client → MCP surface: tool call email.send(...)
   → consequential action → runtime pauses for user approval (push to console/phone)
   → user approves → runtime invokes actuator capsule → email sent → logged
```

This is the shape most external traffic takes. Loamss provides context; the client provides the model.

### Flow B — Source ingestion

```
User: loamss source add gmail

1. Console → Runtime: launch OAuth for Gmail
2. User completes OAuth; tokens stored in runtime local state
3. Runtime → gmail-ingestor capsule: start sync
4. Capsule → Gmail API → fetches messages → writes to storage via storage adapter
5. Capsule emits ingestion events → organizer capsules subscribe
6. Organizer capsules: read new data, call models (via router), write to memory
7. Audit log: every fetch, every model call, every memory write
```

### Flow C — Capsule install

```
User browses registry, taps install on email-drafter@1.4.0

1. Runtime → Registry: fetch capsule + signature
2. Runtime: verify signature against author's published key
3. Runtime → Console: present permission slip
   "This capsule wants: email.read, email.send, files.read (contracts), memory.read, model.call"
4. User: reviews, grants, optionally narrows scopes
5. Runtime: install capsule, record grants, register with permission framework
6. Capsule ready for invocation (by user, by other capsules in Phase 2+, or scheduled)
```

### Flow D — Event write-back from external client

```
Vibez (paired content platform): "user's video abc123 was played 1 time, 47s watch"

1. Client → MCP surface: event write content.metrics + content.revenue
2. Runtime → Permission framework: client has content.metrics.write? content.revenue.write?
   → grants exist → allowed → logged
3. Runtime → Memory adapter: append event to provenance-tagged event store
   (stored as "Vibez's claim," never silently merged with other platforms' claims)
4. Audit log records the write
5. Future memory queries (e.g., "plays per video this month") aggregate across all
   write-back clients' claims, returning per-source attribution
```

### Flow E — First-run deployment

```
User installs runtime binary, runs `loamss init`

1. Storage: user chooses (local / S3 / Postgres / …) and provides credentials
2. Memory: user accepts default (sqlite-vec if local; pgvector if Postgres) or chooses
3. MCP surface: bound to localhost by default; expose externally only on explicit opt-in
4. Models (optional): user adds API keys, sets default routing rules (or skips)
5. Sources: user picks data sources, OAuth flows handle auth
6. Runtime starts. Ingestion begins. Organizer capsules build memory in background.
   Console is reachable. Pairing is ready for the first external client.
```

## Trust model

The system has five trust tiers, in decreasing order of privilege.

- **The user** — fully privileged. Can revoke any grant, uninstall any capsule, swap any adapter, change any rule.
- **The runtime** — trusted code. The central enforcer. Open source so users can verify.
- **Adapters** — semi-trusted. Run in the runtime's process for performance. The runtime ships with a vetted set; third parties may add their own.
- **Capsules** — untrusted code. Run sandboxed (subprocess + MCP). Only see data they're granted. Only call models and external services through the runtime.
- **External MCP clients** — least trusted, scoped explicitly. Authenticate via paired credentials. Can only read or write through declared scopes. Cannot execute code inside the runtime.

Models occupy an orthogonal trust axis. The model router (when used by organizer/actuator capsules) decides which model sees what data based on user rules. Forbidden data classes (e.g., health) can be locked out of hosted models entirely.

## What this architecture is *not*

- Not a chat surface. Chat happens in clients that pair with Loamss via MCP.
- Not a model trainer or fine-tuner. We call models; we don't train them.
- Not a database. Storage is the user's job and lives in the bottom layer.
- Not a search engine. Memory provides personal retrieval; we don't index the public web.
- Not a CDN. For binary content, we issue signed URLs and stay out of the bandwidth path.
- Not a cloud. The hosted option exists for convenience but is not the primary or canonical deployment.

## How it changes if the working assumptions shift

The architecture deliberately stays loose on a few points so that future decisions don't require structural rewrites:

- **MCP wire details** — versions, transports — are mediated by the MCP surface component. If MCP itself evolves, only that layer changes.
- **A2A** for federation and inter-capsule — if adopted, slots in alongside MCP at the same surface, with the permission framework treating peer Loamsses and orchestration capsules the same way it treats other clients/capsules.
- **Sandboxing approach** for capsules — currently subprocess + MCP. WASM remains an option for Phase 2+. Switching does not affect the manifest, permission, or audit surfaces.
- **Hosted runtime** (Phase 3) — same binary, same components, different deployment substrate. The user still brings storage and identity; the hosted offering provides compute.

## Open questions

Tracked here until `docs/open-questions.md` lands.

- **Capsule isolation**: subprocess + MCP for v1. WASM later. Inter-capsule communication blocked at v1 (open question for v0.2 of the capsule spec).
- **Standing tasks**: agent host in Phase 2. Until then, capsules are invoked on-demand or by user-scheduled triggers.
- **Inter-capsule and federation transport**: MCP for now, A2A may be the right shape later. Decision deferred.
- **Audit log placement**: hot copy in runtime local state, write-through to user storage for long-term retention.
- **Event write-back trust**: clients' written claims (Vibez says you got 10k plays) are stored as attributed claims, not silently merged into "ground truth." Reconciliation policies are user-configurable.
- **Public-publish UX**: when a client's grant effectively exposes data to a wide audience (a publishing platform, a doctor's office network), the permission slip must distinguish "private read by your AI" from "publish to that platform's audience." Same capability, different framing — needs a name in the permission model spec.
- **Pairing transport for federation**: pairing-code for human-initiated client pairs is fine. Loamss-to-Loamss pairing may need a richer flow with mutual authentication. Phase 3.
- **MCP surface external exposure**: localhost by default. Tailscale-style overlay, public ingress, or both? Initial leaning: ship localhost + Tailscale-friendly first; explicit public exposure is opt-in with a strong warning.
