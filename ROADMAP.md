# Loamss Roadmap

The plan is structured so each phase ships something usable, even if narrow. Each phase ends with a "stop and validate" moment — real users on real data — before committing to the next.

> **Framing.** The long-term shape Loamss optimizes for is **apps built on top of a user's Loamss as their backing store** — see [`native-apps.md`](native-apps.md). Source connectors (Gmail, Calendar, Drive, …) exist as **transitional migration tools** for users whose data still lives in legacy SaaS; they are not the design center. Phases below reflect that ordering: the substrate and the SDK come first, the marketplace + native-app patterns get the long-term investment.

## Phase 0 — Foundations (weeks 1–4)

Goal: lock down the contracts before we build anything that depends on them.

- [x] Capsule spec v0.1 published (`capsule-spec.md`)
- [x] MCP surface v0.1 published (`mcp-surface.md`)
- [x] Permission model v0.1 published (`permission-model.md`)
- [x] Adapter interface specs: storage, memory, model (`adapter-interface.md`)
- [x] Audit log schema v0.1 (`audit-spec.md`)
- [x] Runtime skeleton in Go: HTTP API, lifecycle, config loading
- [x] Dev environment: Makefile targets, containerized test backends (pgvector, chroma, qdrant), GitHub Actions CI + release pipeline
- [x] CLAUDE.md files in each top-level directory with subsystem context

**Stop and validate**: walk three external developers through the specs. Ask them to design a capsule on paper. Note where they get confused.

## Phase 1 — Vertical slice (weeks 5–12)

Goal: one user, one device, one real workflow, end to end.

- [x] Runtime: capsule loader (subprocess + MCP), permission enforcement, model routing, audit logging
- [x] Storage adapter: `storage:fs-encrypted` (AES-256-GCM at-rest encryption on the local filesystem)
- [x] Memory adapter: `memory:sqlite` (with embedding-aware k-NN search)
- [x] Model adapter: `model:anthropic` (+ bonus `model:ollama` for local inference, plus `model:dummy` / `model:none`)
- [x] First reference *transitional* connector: Gmail (`source:gmail`) — proves the Source SPI with a real provider. Transitional, not steady-state: for users migrating off Gmail into a Loamss-native email app.
- [x] Second reference *transitional* connector: `source:files` — no-auth path, used for migrating legacy local files. With Gmail, completes the SPI reference set (no-auth + OAuth). All further data sources ship as capsule ingestors. **All source connectors are transitional bridges, not the long-term architecture.**
- [x] Reference capsule: daily briefing (`sdk/typescript/examples/daily-brief/`)
- [x] Auto-embedding on ingest (v0.1.5) — closes the "ingest worked, query returns nothing" gap for the standard flow. The memory layer auto-embeds entries that arrive without vectors when an embedding-capable model adapter is configured.
- [x] Console: setup wizard (Welcome → Storage → Memory → Models → Connect → Done) + five interactive panes (Sources, Capsules, Apps, Approvals, Activity). Embedded in the runtime binary.
- [x] CLI: Phase 1 MVP cut shipped (init, doctor, start, status, version, config, capsule, client, grant, audit, approve, export, source)
- [x] Docs: getting started, building your first capsule — shipped: [docs/getting-started.md](docs/getting-started.md), [docs/setup-gmail.md](docs/setup-gmail.md), [docs/build-your-first-capsule.md](docs/build-your-first-capsule.md), [docs/connect-your-app.md](docs/connect-your-app.md), [sources.md](sources.md)
- [x] Distribution: Homebrew tap ([`loamss/homebrew-loamss`](https://github.com/loamss/homebrew-loamss)) + npm SDK ([`@loamss/sdk`](https://www.npmjs.com/package/@loamss/sdk)), both auto-published on tag via GitHub Actions
- [x] External-agent reference (`sdk/typescript/examples/demo-agent/`) — Path-B MCP client with local Ollama brain, demonstrates allowed + denied capability paths end-to-end
- [ ] Reference capsule: email triage ("clear my inbox with my approval per send")

**Deliverable**: a person can install Loamss on their laptop, ingest their files and Gmail, install two capsules, and use them for a week without us touching it.

**Stop and validate**: ten beta users run the daily-briefing flow for two weeks. Measure: time-to-first-useful-response after install; number of permission interactions per day; audit-log clarity; what they wish existed.

## Phase 2 — Ecosystem foundations (weeks 13–24)

Goal: developers can build third-party capsules; users have real choice in backends.

- [x] Storage adapters: `storage:s3` (AWS / R2 / B2 / MinIO / Wasabi), `storage:gcs` (with Workload Identity)
- [x] Memory adapters: `memory:pgvector` (with optional Cloud SQL IAM-auth mode), `memory:chroma`, `memory:qdrant`
- [x] Model adapters: `model:openai` (chat + embeddings); `model:ollama` already shipped in Phase 1
- [ ] Model adapter: `model:mistral`
- [x] SDKs: TypeScript and Python, with local test harnesses
- [x] Connector framework + docs so third parties can build their own — see [`docs/build-your-first-source-connector.md`](docs/build-your-first-source-connector.md)
- [x] **Capsule ingestor primitives** so connectors can ship outside the runtime tree:
    - Credential MCP tools (`credentials.set` / `credentials.get` / `credentials.delete`) — capsule-namespaced, runtime-encrypted
    - Cursor MCP tools (`cursor.set` / `cursor.get`) — capsule-owned incremental-sync state
    - Scheduled trigger — runtime drives the capsule's sync callback on a cadence
    - Source-registry bridge — capsule ingestors register as sources visible in `loamss source list` and the console
    - OAuth callback bridge — runtime owns the loopback listener + PKCE; capsule receives access tokens via `oauth.access_token` MCP tool with transparent refresh
- [x] Reference capsule ingestors: [`rss-ingestor`](sdk/typescript/examples/rss-ingestor/) (no-auth) + [`calendar-ingestor`](sdk/typescript/examples/calendar-ingestor/) (Google OAuth)
- [x] Memory: entity resolution + thread derivation in the memory layer; auto-embedding on ingest (v0.1.5)
- [ ] **Native-app SDK + reference apps** — a worked example Path A app (likely a Loamss-native notes app or email app) that uses the user's Loamss as its backing store, plus an `@loamss/app-sdk` package that codifies the pairing + write-through pattern. This is the highest-leverage Phase 2 item: it's what makes the substrate worth running.
- [ ] Capsule registry MVP: API, web UI, signing, versioning — geared toward **organizers, exposers, actuators**, with transitional ingestors as a smaller category.
- [ ] Capsule certification: review pipeline for the canonical registry
- [ ] Agent host: support for standing tasks ("watch my inbox this week")
- [ ] Memory: episodic summarization, knowledge graph queries
- [ ] Creator publishing surface: `content.video` (and friends) MCP resource type with signed-URL streaming, `events.write` capability for platform-side metrics/revenue write-back. See `scenarios.md` §5 and §6.

Transitional ingestors (Calendar, Drive, Slack, GitHub, Notion, Linear, …) ship as capsules in the marketplace, not as in-tree code. The two in-tree connectors (`source:files`, `source:gmail`) remain as the SPI reference implementations. As Path A native apps emerge for each category, demand for the matching ingestor drops.

**Stop and validate**: capsule developer day. Invite 30 builders. Watch where the SDK frustrates them. Ship five third-party capsules to the registry, plus at least one Path A reference app.

## Phase 3 — Hosted offering and federation (weeks 25–40)

Goal: lower the floor for non-technical users without compromising the principles.

- [ ] Hosted runtime instances (the user still owns storage and keys)
- [ ] One-click deploy from the website
- [ ] Per-instance encrypted backups to user-chosen storage
- [ ] Federation v0.1: invited cross-instance access (household calendars, doctor visit grants)
- [ ] Mobile companion app (notifications, approvals, voice in/out)
- [ ] Billing: inference passthrough plus a hosting fee

**Stop and validate**: 1,000 hosted users using it daily. Open the canonical registry to public submissions.

## Phase 4 — Going wide

- [ ] Standardization push: get the capsule spec ratified somewhere neutral
- [ ] Enterprise / team mode (small orgs with shared capsule libraries)
- [ ] Healthcare-grade audit and compliance modes
- [ ] Real-time data streams (sensors, IoT, financial feeds)
- [ ] Multi-runtime interoperability tests with at least one external implementation

## Non-goals (explicitly out of scope)

- Training or fine-tuning models. We are infrastructure for models others train.
- Becoming the default storage. Users bring storage. Always.
- An ad-supported tier. Conflicts with the trust model.
- Closed-source extensions of the runtime. The core stays open.

## How to use this roadmap with Claude Code

- Each phase should have its own working branch and tracking issue.
- Inside a phase, work bottom-up: specs first, then runtime support, then adapters, then capsules, then console surfaces.
- When a phase's "stop and validate" reveals problems, fix in place — don't pile on the next phase's work.
- The capsule spec and permission model are particularly expensive to break after Phase 1. Treat changes to those with extra scrutiny.
