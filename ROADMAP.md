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

## Phase 2.5 — Loamss Cloud: hosted personal Loamss provisioning

Goal: a user who doesn't have a Loamss can sign up on `loamss.com` and get one provisioned for them — same runtime as the self-hosted install, just operated by the project. The trust contract is unchanged (the user owns their data; they can `loamss export` and walk away).

Treated as a separate phase because it's a real operational commitment, not just code. Building it depends on Phase 2's cloud-deployable runtime as the per-tenant unit.

- [ ] **Control plane (Loamss Cloud)** — closed-source service that:
    - Handles signup + auth (email/password and OAuth via Google/GitHub)
    - Provisions per-tenant resources: Postgres (database-per-tenant or schema-per-tenant), GCS bucket (bucket-per-tenant or prefix-per-tenant), Loamss container on Cloud Run/GKE
    - Routes `<user>.loamss.cloud` (or chosen subdomain) → the right container
    - Manages lifecycle (start, stop, upgrade, restart)
    - Per-tenant backups on a schedule
    - Off-boards cancelled users with full data export
- [ ] **Billing** — usage-based or flat-rate; Stripe integration. Pricing model TBD; tentative shape: free tier with conservative limits, paid plans for storage / mail volume / multi-instance.
- [ ] **Admin console** (project-side) — operational view across all tenants: resource usage, errors, abuse signals, billing state.
- [ ] **Abuse handling** for `mail.loamss.com` addresses — rate limits, verification, off-boarding endpoints.
- [ ] **Runtime adjustments** to support hosted (most already planned in Phase 2):
    - `LOAMSS_PRECONFIGURED=true` mode that skips the setup-token wizard (control plane delivers the admin bearer directly)
    - Audit-log export in a stable shape (already in Phase 2 plan)
    - Provisioning-time DB and storage shape preserved (both "fresh DB" and "schema in shared cluster" supported by the Postgres adapter; same for GCS)
- [ ] **Compliance scaffolding** — privacy policy, terms of service, GDPR data-handling docs, breach disclosure plan, basic SOC 2 readiness work.

**Operational cost shape**: ~$10-20/mo per active tenant for the substrate (container minutes + Postgres + storage); pricing has to cover that with headroom. ~1-2 hours/week of ongoing ops at small scale; grows linearly with users.

**Stop and validate**: 50 paying users on Loamss Cloud, NPS > 30, churn under 5%/month, three months without a P1 incident. Only after that, open public signup beyond the wait-list.

**This phase is the first time the project becomes a commercial entity in any meaningful sense.** The runtime stays open-source Apache-2.0; the control plane and its operational state are the commercial product (HashiCorp / Sentry / Plausible model). Funding question lands here: bootstrap the control plane by hand, or take small seed money to staff it properly. Out of scope for this roadmap; surfaces as a separate decision when Phase 2 is close to done.

## Phase 3 — Federation + mobile (after 2.5 lands)

Goal: connect runtimes to each other, and reach users where they actually are.

(Hosted-runtime / billing items moved up into Phase 2.5 since they're tightly coupled to that work.)

- [ ] Federation v0.1: invited cross-instance access (household calendars, doctor visit grants, the per-employee-+-team-Loamss enterprise pattern)
- [ ] OIDC token validation as a pairing primitive (alongside the bearer-token flow), enabling "Sign in with your Loamss" UX in apps
- [ ] Cross-instance discovery endpoint so a user's AI tool, paired with their personal Loamss, can find and federate-query team / project Loamsses they have access to
- [ ] Mobile companion app (notifications, approvals, voice in/out, pairing via QR code)

**Stop and validate**: 1,000 hosted users using it daily, with at least 100 of them using federated team/project Loamsses (the enterprise-pattern early test). Open the canonical capsule registry to public submissions.

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
