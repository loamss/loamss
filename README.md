# Loamss

Loamss is open-source **personal data infrastructure**.

It is the place your data and memory live — under your control, in storage you own — and the governed interface that anything wanting access has to come through. AI tools, platforms you publish to, specialists you visit, peers you share with, services that report back, your own scripts: every grant you issue is explicit, every scope is yours to set, every access is logged, every grant is revocable.

**You own your data. You decide who sees it. You see what happened. You take it with you when you leave.**

## The idea

Your data is spread across services. Every platform you use — email, photos, calendar, messages, notes, files, recordings, health records, financial accounts, location history, the lot — owns its slice of you. You can extract bits when you remember to, but each platform decides what it shares and what it keeps. If a service disappears, your history goes with it. If you want to switch, you start over.

Loamss inverts that. Your data lives where you put it. Anything that wants to read it or act on it asks through Loamss's permission framework: who you are, what you've explicitly granted, in what scope, for how long. AI tools are one class of consumer that needs this kind of governed access; so are publishing platforms, clinics, schools, banks, household members, analytics services, and the long tail of integrations that accumulate in a digital life. The framework treats them all the same.

## What you control

**What goes in.** You connect data sources (Gmail, Calendar, files, messages, photos, health apps, financial records — anything you choose) and Loamss ingests them into your storage. Nothing gets pulled that you didn't connect.

**Who gets access.** Every external consumer — every AI tool, every platform, every specialist, every peer — must be explicitly paired with your Loamss and granted scoped capabilities. No background access. No "trusted" partners. No implicit grants.

**What scope.** Each grant is narrowed: read this folder, search emails from this sender, query memory excluding health, publish content tagged public. You set the lines. You can narrow further or revoke entirely at any time.

**For how long.** Grants can be time-bound (the clinic visit gets 2 hours, then access vanishes automatically) or open-ended (your daily AI tools stay paired until you revoke).

**With what consequences.** Consequential actions — sending email, posting content, transferring money, deleting data — require explicit per-action approval through the console or your phone. Reading is one thing; acting on your behalf is another.

**What's recorded.** Every access — successful or denied — is logged. The audit trail is a first-class user-facing surface, not a debug artifact. You can query "what did each service see last week," "what actions did capsule X take," "show me every denial in the past month."

**How to leave.** `loamss export` produces a full dump of your storage, memory, and audit history. Point a different runtime at the same data and walk away. The walkaway path is a tested invariant, not a marketing claim.

## How it fits together

```
┌─────────────────────────────────────────────────────────────┐
│  EXTERNAL CONSUMERS (anything that speaks MCP)              │
│  AI tools · Platforms · Specialists · Peers · Services · …  │
│  Paired explicitly. Scoped narrowly. Logged completely.     │
└──────────────────────────┬──────────────────────────────────┘
                           │ MCP (paired, scoped, audited)
┌──────────────────────────▼──────────────────────────────────┐
│  LOAMSS RUNTIME — single binary, OS-level daemon            │
│  ┌────────────────────────────────────────────────────────┐ │
│  │ MCP surface · Permissions · Audit log · Pairing        │ │
│  ├────────────────────────────────────────────────────────┤ │
│  │ Memory layer (entities, vectors, graph, episodic)      │ │
│  ├────────────────────────────────────────────────────────┤ │
│  │ Storage adapter │ Memory adapter │ Capsule host        │ │
│  └──────┬──────────────────────────────┬───────────────────┘ │
│         │                              │ sandboxed via MCP  │
│         │                  ┌───────────▼─────────────────┐   │
│         │                  │  CAPSULES                   │   │
│         │                  │  ingest │ organize │        │   │
│         │                  │  expose │ act              │   │
│         │                  └─────────────────────────────┘   │
└─────────┼────────────────────────────────────────────────────┘
          │
┌─────────▼───────────────────────────────────────────────────┐
│  USER-OWNED RESOURCES                                       │
│  Storage (FS / SQLite / S3 / Postgres) │ Identity (OAuth)   │
│  Compute (laptop / NAS / server)       │ Model keys (opt.)  │
└─────────────────────────────────────────────────────────────┘
```

The middle layer — Loamss — is what this project builds. The top and bottom layers belong to you.

## What it looks like in practice

**You decide who sees what, and you see what they did.** You pair your AI assistants with your Loamss. ChatGPT gets memory access scoped to "people, projects, not health." Cursor gets read access to engineering notes plus today's calendar. Each one knows enough to be useful, neither knows more than you allowed, and the audit log shows you every query they ran last week.

**You publish content without surrendering it.** You make videos. A social platform supports MCP and onboards you as a creator. You pair the platform with your Loamss and grant scoped read access to videos tagged `public`. The platform streams directly from your S3 bucket via signed URLs Loamss issues. Every play is logged. The platform writes back metrics and revenue events. If the platform disappears tomorrow, your library and analytics are still in your storage. You point a new platform at the same Loamss and continue.

**You bring a specialist in temporarily.** A clinic appointment. The clinic's intake AI has an MCP client. You scan a QR from their tablet. Your Loamss shows a permission slip: `health.read` for the last 12 months, scoped to health entities only, expires in 2 hours, auto-revoke. You approve. The clinic AI has what it needs for the visit. At 2 hours, access vanishes. The audit log keeps the full record forever.

**You take everything with you.** You decide Loamss isn't useful anymore, or another implementation is better, or your needs changed. `loamss export` produces a complete archive of your storage, memory, and audit history. Point another runtime at it. Or don't — keep the archive, ditch the rest. Nothing is held hostage.

These aren't speculative. See [`scenarios.md`](scenarios.md) for the seven end-to-end use cases the architecture must support.

## What Loamss is

- An open-source **runtime** that owns the lifecycle of your personal data and memory
- A **permission framework** with auditable, capability-based consent — for everything that wants access
- An **MCP server surface** that gives external consumers a uniform way to ask
- An open **capsule specification** for packaging the pieces that ingest, organize, expose, and act on your data
- A **registry** where capsule developers publish and users discover
- **Adapter layers** that let users plug in their own storage and memory backends
- A **console** for managing data sources, permissions, paired consumers, and the audit log

## What Loamss is not

- Not a chat app. The chat surfaces are whatever you already use; Loamss is what they connect to when you let them.
- Not a model. We don't train one. We don't call one on your behalf except as needed to organize your data.
- Not a data host. You bring storage. We don't operate the database your data sits in.
- Not a walled garden. Capsules from anywhere, consumers from anywhere, storage anywhere.
- Not a SaaS lock-in. If we stop being useful, you point another runtime at your data and walk away.
- Not "for AI." AI tools are one class of consumer. The framework treats every consumer the same.

## Two ways to build with Loamss

Loamss assumes apps that treat user-owned data substrates as first-class. **That ecosystem is new.** Today most apps hold user data in their own databases and offer at most a "download ZIP" export. Two paths exist for builders:

**Path A — Native Loamss apps.** Your app is designed around Loamss from day one. The user's Loamss IS the storage layer; your backend is a thin layer that holds essentially nothing about the user's content. Examples: a note-taking app where notes are entities in the user's Loamss; a creator platform that streams videos from the user's S3 via Loamss-issued signed URLs; an AI assistant whose entire context lives in the user's data substrate. See [`native-apps.md`](native-apps.md) for the pattern, worked examples, and the honest tradeoffs.

**Path B — Existing apps adding Loamss support.** Your app already exists with its own storage and user accounts. You add an MCP client so users can optionally pair their Loamss for context, or write specific outputs back to it. Your architecture is unchanged; Loamss becomes one of several context sources. See [`mcp-surface.md`](mcp-surface.md) for what to integrate with.

Path A grows the ecosystem. Path B follows when there's enough user demand to make it worth the effort. Today, the most leveraged work is on Path A.

## Capsules — the extensibility surface

A **capsule** is a packaged unit that extends what Loamss can do with your data. Capsules are sandboxed (subprocess + MCP, with WASM planned), signed, and permission-gated. Four roles, all defined by what they do:

- **Ingestors** pull data IN from external services (Gmail, Calendar, Drive, Slack, GitHub, health apps, financial services) into your storage
- **Organizers** read from storage and build memory — entity resolution, summarization, embeddings, classification
- **Exposers** declare new MCP resources and tools for external consumers to use (e.g., the `content-publisher` capsule that exposes your videos to publishing platforms)
- **Actuators** take action in the outside world on your behalf (send email, post to a platform, write a calendar event) — always gated by explicit user approval

Capsules are written in any language, packaged to the [capsule specification](capsule-spec.md), and installed via `loamss capsule install <name>` from the registry. They can be third-party. They are themselves scoped by the same permission framework that gates external consumers — capsules are not trusted.

## Status

**Phase 1 — reference runtime under active development.** Phase 0 (specs) is complete; the Go runtime now boots end-to-end on a paired MCP client.

Spec set (content-complete):

- ✅ [Architecture](ARCHITECTURE.md) — components, flows, trust model
- ✅ [Permission model](permission-model.md) — the capability framework
- ✅ [MCP surface](mcp-surface.md) — how external consumers talk to Loamss
- ✅ [Capsule spec](capsule-spec.md) — the package format
- ✅ [Adapter interface](adapter-interface.md) — storage / memory / model contracts
- ✅ [Audit log schema](audit-spec.md) — what gets logged, how it's chained, how it's queried
- ✅ [Extensibility](extensibility.md) — how the system grows without core changes
- ✅ [CLI surface](cli.md) — the `loamss` command shape
- ✅ [Scenarios](scenarios.md) — end-to-end use cases the design must support
- ✅ [Topology](topology.md) — front-facing-app data flows, auth boundaries, failure modes
- ✅ [Sources](sources.md) — data-source connector spec (Source SPI + lifecycle)
- ✅ [Memory layer](memory-layer.md) — entity + thread resolution above the memory adapter
- ✅ [Benchmarks](benchmarks.md) — baseline performance numbers and methodology

Reference runtime (in `runtime/`):

- ✅ HTTP listener with `/healthz`, `/version`, and the MCP surface
- ✅ JSON-RPC 2.0 + SSE transport; bearer-token client auth
- ✅ Hash-chained audit log (SQLite, WAL, `BEGIN IMMEDIATE`) + `Verify` pass
- ✅ Permission engine with scope match primitives + grant store + approval queue
- ✅ Capsule host: subprocess + MCP-over-stdio + permission-gated callbacks
- ✅ Storage adapters: `storage:fs-encrypted`, `storage:s3` (AWS / R2 / B2 / MinIO / Wasabi), `storage:gcs` (native Google Cloud Storage with Workload Identity, V4 presigned URLs from service accounts, ADC chain)
- ✅ Memory adapters: `memory:sqlite` (single-host brute-force k-NN), `memory:pgvector` (Postgres + pgvector with ivfflat / hnsw; optional Cloud SQL IAM-auth mode), `memory:chroma` (purpose-built embedding DB, easy to spin up), `memory:qdrant` (production-grade with rich filtering)
- ✅ Memory layer: entity + thread resolution above the adapter
- ✅ Model adapters: `model:none`, `model:dummy`, `model:anthropic`, `model:ollama`, `model:openai` (GPT chat-completions + `text-embedding-3` family — the natural embedding pair for `memory:pgvector`)
- ✅ Source connector framework + two reference connectors (`source:files`, `source:gmail`). The SPI is provider-agnostic — these are demonstrations, not the design target
- ✅ CLI: `init`, `doctor`, `start`, `open`, `status`, `version`, `config`, `capsule`, `client`, `grant`, `audit`, `approve`, `export`, `source`
- ✅ Console embedded in the runtime binary: first-run wizard + post-wizard dashboard, served at the runtime's listen address. Every dashboard pane (Sources, Capsules, Apps, Approvals, Activity) is interactive — install capsules, sync sources, approve grants, pair external clients without leaving the browser
- ✅ Capsule SDK (TypeScript) — `@loamss/sdk` in [`sdk/typescript/`](sdk/typescript/): MCP-over-stdio transport, tool registration, runtime-callback client, hello-world + daily-brief reference capsules
- ✅ MCP client library (TypeScript) — for Path-B apps pairing with a user's Loamss
- ⏳ Additional source connectors (Calendar, Drive, Slack, GitHub)
- ⏳ Python SDK
- ⏳ Additional adapters (s3, postgres, pgvector, openai, mistral)
- ⏳ Config hot-reload (today: edits via wizard or file require a `loamss start` restart)
- ⏳ Release binaries via GitHub Actions (today: build from source)

See [`ROADMAP.md`](ROADMAP.md) for the phased build plan.

## Try it locally

You need Go 1.22+ and [Bun](https://bun.sh) (Bun is used to build the embedded console UI; the runtime itself is pure Go).

```bash
git clone https://github.com/loamss/loamss
cd loamss/runtime
make build         # bun build (console) + go build (runtime)
./bin/loamss start --open
```

The `--open` flag launches your browser at the runtime's URL after the daemon starts. On a fresh install you land on the three-minute first-run wizard (Welcome → Storage → Memory → Models → Connect → Done); subsequent runs land on the dashboard.

Three things to try once you're in:

1. **Add a source.** In the dashboard's Sources pane click `+ Add source`, point it at a directory full of Markdown (`~/Documents` works), and watch the memory layer fill up.
2. **Install a capsule.** Click `+ Install capsule` and paste `sdk/typescript/examples/daily-brief` from the cloned repo. The runtime issues its permission grants and shows the slip.
3. **Pair an app.** Click `+ Pair an app`, give it a name, copy the code into any MCP-speaking client (Claude Desktop, your own script via `@loamss/sdk`), and watch the audit feed light up as it reads.

When you're done, `loamss export` produces a complete archive of your storage, memory, and audit history. Point another runtime at it or keep the archive — nothing is held hostage.

## Reading order, depending on who you are

**Curious**: this README, then [`scenarios.md`](scenarios.md), then [`ROADMAP.md`](ROADMAP.md) for timelines.

**Capsule developer**: start with [`docs/build-your-first-capsule.md`](docs/build-your-first-capsule.md). For depth: [`ARCHITECTURE.md`](ARCHITECTURE.md), [`capsule-spec.md`](capsule-spec.md), [`permission-model.md`](permission-model.md), and the TypeScript SDK at [`sdk/typescript/`](sdk/typescript/).

**External platform integrator** (you want your AI tool, content platform, or service to connect to user Loamss instances — Path B): start with [`docs/connect-your-app.md`](docs/connect-your-app.md). For depth: [`mcp-surface.md`](mcp-surface.md), relevant scenarios in [`scenarios.md`](scenarios.md), and [`topology.md`](topology.md) if you're a front-facing app with direct-from-storage delivery.

**Native app builder** (you want to build an app where Loamss is the backing data store from day one — Path A): [`native-apps.md`](native-apps.md), then [`mcp-surface.md`](mcp-surface.md) for the protocol details, then [`topology.md`](topology.md) for the deployment shape.

**Adapter author** (you want your storage backend, vector DB, or model provider to plug in): [`adapter-interface.md`](adapter-interface.md).

**Contributor / runtime engineer**: all of the above plus [`CLAUDE.md`](CLAUDE.md) for project conventions.

## Specs and design docs

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — the full technical picture
- [`ROADMAP.md`](ROADMAP.md) — what we're building in what order
- [`scenarios.md`](scenarios.md) — end-to-end use cases the design must support
- [`topology.md`](topology.md) — front-facing-app data flows, auth boundaries, failure modes
- [`sources.md`](sources.md) — data-source connector spec (Source SPI + lifecycle)
- [`memory-layer.md`](memory-layer.md) — entity + thread resolution above the memory adapter
- [`console-design.md`](console-design.md) — IA + first-run wizard + ongoing flows for the user-facing UI
- [`docs/setup-gmail.md`](docs/setup-gmail.md) — Google OAuth client setup for `source:gmail`
- [`docs/build-your-first-capsule.md`](docs/build-your-first-capsule.md) — walking-through tutorial: write, install, and invoke a capsule
- [`docs/connect-your-app.md`](docs/connect-your-app.md) — walking-through tutorial: pair an external app and drive Loamss from it
- [`permission-model.md`](permission-model.md) — the capability framework
- [`mcp-surface.md`](mcp-surface.md) — the MCP interface Loamss exposes to external consumers
- [`capsule-spec.md`](capsule-spec.md) — the capsule format
- [`adapter-interface.md`](adapter-interface.md) — storage / memory / model adapter contracts
- [`audit-spec.md`](audit-spec.md) — audit log schema and tamper-evidence chain
- [`extensibility.md`](extensibility.md) — what's open for extension and what's stable; anti-patterns in code review
- [`native-apps.md`](native-apps.md) — building apps where Loamss is the backing data store (Path A)
- [`cli.md`](cli.md) — the `loamss` CLI surface
- [`benchmarks.md`](benchmarks.md) — baseline performance numbers and methodology
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — how to contribute
- [`SECURITY.md`](SECURITY.md) — vulnerability disclosure policy
- [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) — community standards
- [`CLAUDE.md`](CLAUDE.md) — context for Claude Code agents working on this repo

## License

[Apache-2.0](LICENSE). Open source, with a patent grant. Contributions welcome — see [`CONTRIBUTING.md`](CONTRIBUTING.md). Security reports: [`SECURITY.md`](SECURITY.md).

## Canonical URLs

- Repo: [github.com/loamss/loamss](https://github.com/loamss/loamss)
- Site: [loamss.com](https://loamss.com) (placeholder)
