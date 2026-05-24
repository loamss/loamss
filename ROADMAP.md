# Loamss Roadmap

The plan is structured so each phase ships something usable, even if narrow. Each phase ends with a "stop and validate" moment — real users on real data — before committing to the next.

## Phase 0 — Foundations (weeks 1–4)

Goal: lock down the contracts before we build anything that depends on them.

- [x] Capsule spec v0.1 published (`capsule-spec.md`)
- [x] MCP surface v0.1 published (`mcp-surface.md`)
- [x] Permission model v0.1 published (`permission-model.md`)
- [x] Adapter interface specs: storage, memory, model (`adapter-interface.md`)
- [x] Audit log schema v0.1 (`audit-spec.md`)
- [ ] Runtime skeleton in Go: HTTP API, lifecycle, config loading
- [ ] Dev environment: `make` targets, containerized test backends, CI
- [ ] CLAUDE.md files in each top-level directory with subsystem context

**Stop and validate**: walk three external developers through the specs. Ask them to design a capsule on paper. Note where they get confused.

## Phase 1 — Vertical slice (weeks 5–12)

Goal: one user, one device, one real workflow, end to end.

- [x] Runtime: capsule loader (subprocess + MCP), permission enforcement, model routing, audit logging
- [x] Storage adapter: `storage:fs-encrypted` (AES-256-GCM at-rest encryption on the local filesystem)
- [x] Memory adapter: `memory:sqlite` (with embedding-aware k-NN search)
- [x] Model adapter: `model:anthropic` (+ bonus `model:ollama` for local inference, plus `model:dummy` / `model:none`)
- [x] First reference connector: Gmail (`source:gmail`) — proves the Source SPI with a real provider. The SPI is generic; Gmail is the demonstration, not the target.
- [ ] Connector: Google Calendar
- [ ] Reference capsule: daily briefing ("what's on my plate today")
- [ ] Reference capsule: email triage ("clear my inbox with my approval per send")
- [ ] Console: setup wizard, capsule install, permission grants, audit log viewer
- [x] CLI: Phase 1 MVP cut shipped (init, doctor, start, status, version, config, capsule, client, grant, audit, approve, export, source)
- [x] Docs: getting started, building your first capsule — shipped: [docs/setup-gmail.md](docs/setup-gmail.md), [docs/build-your-first-capsule.md](docs/build-your-first-capsule.md), [docs/connect-your-app.md](docs/connect-your-app.md), [sources.md](sources.md)

**Deliverable**: a person can install Loamss on their laptop, connect Gmail and Calendar, install two capsules, and use them for a week without us touching it.

**Stop and validate**: ten beta users run the daily-briefing flow for two weeks. Measure: time-to-first-useful-response after install; number of permission interactions per day; audit-log clarity; what they wish existed.

## Phase 2 — Ecosystem foundations (weeks 13–24)

Goal: developers can build third-party capsules; users have real choice in backends.

- [ ] Storage adapters: filesystem, S3-compatible, Postgres
- [ ] Memory adapters: pgvector, Chroma, Qdrant
- [ ] Model adapters: OpenAI, Mistral, local via Ollama; routing rules
- [ ] Connectors: Google Drive, iCloud, Slack, GitHub
- [ ] Connector framework + docs so third parties can build their own
- [ ] SDKs: TypeScript and Python, with local test harnesses
- [ ] Capsule registry MVP: API, web UI, signing, versioning
- [ ] Capsule certification: review pipeline for the canonical registry
- [ ] Agent host: support for standing tasks ("watch my inbox this week")
- [ ] Memory: entity resolution, episodic summarization, knowledge graph queries
- [ ] Creator publishing surface: `content.video` (and friends) MCP resource type with signed-URL streaming, `events.write` capability for platform-side metrics/revenue write-back. See `scenarios.md` §5 and §6.

**Stop and validate**: capsule developer day. Invite 30 builders. Watch where the SDK frustrates them. Ship five third-party capsules to the registry.

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
