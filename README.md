# Loamss

**Personal data infrastructure. Your apps work on your data, in your substrate — not in someone else's database.**

Open source. Self-hosted. Your storage, your keys, your data. Single Go binary; first run is a three-minute wizard.

---

## The 60-second version

Today your data is scattered across other companies' databases. Your email is at Gmail. Your notes are at Notion. Your photos are at Apple or Google. Your AI tools — Claude, ChatGPT, Cursor — each build their own picture of you, separately, also somewhere else. The apps own the data; you rent the experience.

Loamss flips that. **You run a substrate. Your apps connect to it.** An email app writes your email into your Loamss. A notes app writes your notes there. AI tools pair in and read what you've allowed. Every read and every write is scoped, audited, and revocable. When you stop using an app, your data stays with you. When you switch AI tools, your memory follows.

> **You own your data. Your apps work on it. You decide who sees what. You watch what happened. You take it with you when you leave.**

## What you can actually do with it today

### Run apps that don't own your data

A new generation of apps — **native Loamss apps** — uses your Loamss as their backing store. The email app holds no email; the notes app holds no notes; the calendar app holds no events. All of it lives in your Loamss. Switch apps and your data stays. Sunset an app and you lose nothing. See [`native-apps.md`](native-apps.md) for the architectural pattern and worked examples.

This is the long-term shape. The ecosystem is early — we ship the substrate; building Path A apps is what the SDK is for.

### Give every AI tool the same brain

Pair Claude, ChatGPT, Cursor each with their own scoped grant against your Loamss. Same memory, different scopes. Ask any tool *"what did Sarah and I decide about the contract?"* — answered from your own substrate. The Activity pane shows every query each tool ran, every denial, every grant scope change. **Shipped end-to-end** — pairing, scope projection, memory layer, audit chain.

### Bring a specialist in for two hours, watch them leave

A healthcare appointment, legal consult, accountant filing. The professional's intake tool gets a time-boxed grant — `health.read`, last 12 months, expires in 2 hours, auto-revoke. When the timer runs out access vanishes. The audit log retains the full record as a consent receipt forever.

Every primitive — time-bounded grants, data-class scoping, hash-chained audit log — is in place today. The QR-code mobile companion lands in Phase 3; the dashboard already drives the flow.

### Publish content without surrendering it

A platform that supports MCP onboards you as a creator. You grant `content.list` + `content.read` scoped to `tag:public`. The platform streams directly from your own S3 bucket via signed URLs Loamss issues. Every play is logged. The platform writes metrics and revenue back as **attributed claims** — Loamss never silently merges a platform's numbers into ground truth.

If the platform sunsets, your library and analytics are still in your storage. Point a new platform at the same Loamss the next day and continue. **Every wire for this is shipped**; the missing piece is a reference platform to demonstrate it.

### Bring your legacy data with you

You already have years of email, calendar, notes inside Google / Notion / Dropbox. Loamss ships **transitional source connectors** (`source:files`, `source:gmail`, plus capsule ingestors for Calendar, RSS, …) that migrate that data into your Loamss. Once it's in, your apps and AI tools work against it the same as anything native. The long-term shape is apps that write to Loamss in the first place; source connectors are the bridge for users who don't have those apps yet.

### Walk away whenever you want

`loamss export` produces a complete archive of your storage, memory, and audit history. Point another runtime at it — or keep the archive and ditch the runtime. Nothing is held hostage. The walkaway path is a tested invariant, not a marketing claim.

---

## What you control

| | |
|---|---|
| **What goes in** | Your apps write to your Loamss directly; you also choose any data sources to ingest from legacy services. Nothing arrives that you didn't allow. |
| **Who gets access** | Every consumer — every AI tool, every app, platform, specialist, peer — pairs explicitly and gets scoped capabilities. No background access. |
| **What scope** | Read this folder. Search emails from this sender. Query memory excluding health. Publish content tagged `public`. You set the lines. |
| **For how long** | Grants can be time-bound (the clinic gets 2 hours) or open-ended (your daily AI tools stay paired until revoked). |
| **With what consequences** | Sending email, posting content, transferring money — consequential actions require explicit per-action approval. Reading is one thing; acting is another. |
| **What's recorded** | Every access — successful or denied — is logged. The audit trail is a first-class user surface, not a debug artifact. |
| **How to leave** | `loamss export` dumps everything. Walk away whenever. |

---

## How it fits together

```
┌─────────────────────────────────────────────────────────────┐
│  YOUR APPS + TOOLS (anything that speaks MCP)               │
│  Native apps (Path A) · External AI tools (Path B) ·         │
│  Platforms · Specialists · Peers · Services                  │
│  Each pairs explicitly. Each gets scoped capabilities.       │
│  Each call is logged.                                        │
└──────────────────────────┬──────────────────────────────────┘
                           │ MCP (paired, scoped, audited)
┌──────────────────────────▼──────────────────────────────────┐
│  LOAMSS RUNTIME — single binary, OS-level daemon            │
│  ┌────────────────────────────────────────────────────────┐ │
│  │ MCP surface · Permissions · Audit log · Pairing        │ │
│  ├────────────────────────────────────────────────────────┤ │
│  │ Memory layer (entities, threads, vectors)              │ │
│  ├────────────────────────────────────────────────────────┤ │
│  │ Storage adapter │ Memory adapter │ Capsule host        │ │
│  └──────┬──────────────────────────────┬───────────────────┘ │
│         │                              │ sandboxed via MCP  │
│         │                  ┌───────────▼─────────────────┐   │
│         │                  │  CAPSULES                   │   │
│         │                  │  organize · expose · act    │   │
│         │                  │  (+ transitional ingestors) │   │
│         │                  └─────────────────────────────┘   │
└─────────┼────────────────────────────────────────────────────┘
          │
┌─────────▼───────────────────────────────────────────────────┐
│  USER-OWNED RESOURCES                                       │
│  Storage (FS / SQLite / S3 / GCS / Postgres) │ Identity     │
│  Compute (laptop / NAS / server / cloud)     │ Model keys   │
└─────────────────────────────────────────────────────────────┘
```

The middle layer — Loamss — is what this project builds. The top and bottom layers belong to you. Apps write your content INTO the substrate; AI tools read it under scoped grants; capsules sit between, organizing what's there or acting on it.

---

## Capsules — the extensibility surface inside the substrate

A **capsule** is a packaged unit (TypeScript or Python today; WASM planned) that extends what Loamss can do with your data. Capsules are sandboxed subprocesses, signed, and gated by the same permission framework that gates external consumers — capsules are not trusted. Four roles, defined by what they do:

- **Organizers** read storage and build memory — entity resolution, summarization, embeddings, classification. The most common steady-state role.
- **Exposers** declare new MCP resources and tools for external consumers (e.g., the `content-publisher` capsule that exposes your videos to publishing platforms)
- **Actuators** take action in the outside world on your behalf — always gated by explicit user approval (send email via SMTP, post to social, etc.)
- **Ingestors** — *transitional*. Pull data IN from legacy services (Gmail, Calendar, Drive, …) into your storage. Useful for migrating historical data; long-term, native apps write into Loamss directly, removing the need.

The catalogue grows in the open marketplace, not in this repo. The two in-tree connectors (`source:files`, `source:gmail`) are SPI reference implementations covering the no-auth and OAuth extremes; new ingestors ship as capsules under the `ingestor` role.

The full primitive set for capsule ingestors — credential storage, cursor persistence, scheduled triggers, runtime-driven OAuth — landed in the most recent release. Two reference capsules in [`sdk/typescript/examples/`](sdk/typescript/examples/) cover the design spectrum: [`rss-ingestor`](sdk/typescript/examples/rss-ingestor/) (no-auth) and [`calendar-ingestor`](sdk/typescript/examples/calendar-ingestor/) (Google OAuth, with the runtime driving the browser flow). Together they demonstrate every primitive end-to-end.

---

## Two ways to build with Loamss

Loamss assumes apps that treat user-owned data substrates as first-class. **That ecosystem is new.** Two paths for builders:

**Path A — Native Loamss apps (the long-term shape).** Your app is designed around Loamss from day one. The user's Loamss IS the storage layer; your backend is a thin layer that holds essentially nothing about the user's content. Examples: an email app where messages live in the user's storage and memory; a note-taking app where notes are entities in the user's memory; a creator platform that streams videos from the user's S3 via signed URLs; a personal AI assistant whose entire context is in the user's substrate. See [`native-apps.md`](native-apps.md) for the pattern, tradeoffs, and worked examples. **This is what the project optimizes for.**

**Path B — Existing apps adding Loamss support.** Your app exists with its own storage and accounts. You add an MCP client so users can optionally pair their Loamss for context. Architecture unchanged; Loamss becomes one of several context sources. See [`docs/connect-your-app.md`](docs/connect-your-app.md). Path B is how the ecosystem warms up — existing AI tools (Claude, ChatGPT, Cursor) reach a user's Loamss via Path B until native Path A alternatives arrive.

---

## Status

**Phase 1 — working substrate, growing ecosystem.** Phase 0 (specs) is content-complete; the runtime boots end-to-end and the dashboard is interactive.

### Substrate and protocol

- ✅ Single-binary runtime with embedded dashboard (Next.js static export, served from the binary)
- ✅ MCP over HTTP+SSE with JSON-RPC 2.0; bearer-token client auth + per-client credentials
- ✅ Permission engine: capability + scope + `requires_user_approval`; per-grant audit
- ✅ Hash-chained audit log on SQLite (WAL, `BEGIN IMMEDIATE`) with `Verify` pass
- ✅ Capsule host: subprocess + MCP-over-stdio + permission-gated callbacks

### Substrate breadth (pick your backend)

- ✅ **Storage**: `fs-encrypted` (AES-256-GCM), `s3` (AWS / R2 / B2 / MinIO / Wasabi), `gcs` (Workload Identity, V4 presigned URLs)
- ✅ **Memory**: `sqlite-vec` (single-host), `pgvector` (with optional Cloud SQL IAM-auth), `chroma`, `qdrant`
- ✅ **Models**: `anthropic`, `openai` (chat + embeddings), `ollama` (local), plus `none` / `dummy` for graceful degradation
- ⏳ `model:mistral`

### Capsule ecosystem

- ✅ **TypeScript SDK** ([`@loamss/sdk`](sdk/typescript/)) — `bun add @loamss/sdk` / `npm install @loamss/sdk`. Full MCP-over-stdio capsule surface + Path-B client library, 43 tests. Published to npm at [`@loamss/sdk`](https://www.npmjs.com/package/@loamss/sdk) (tracks the runtime release tag).
- ✅ **Python SDK** ([`loamss_sdk`](sdk/python/)) — mirrors the TS shape, 19 tests
- ✅ **Reference examples** under [`sdk/typescript/examples/`](sdk/typescript/examples/):

  | Example | Role | Demonstrates |
  |---|---|---|
  | [`hello-world`](sdk/typescript/examples/hello-world/) | capsule (minimal) | The smallest possible capsule — one tool, no permissions |
  | [`daily-brief`](sdk/typescript/examples/daily-brief/) | capsule (organizer) | Reading memory across threads/entities and calling `model.call` to summarize |
  | [`approval-demo`](sdk/typescript/examples/approval-demo/) | capsule (actuator) | The `requires_user_approval` consequential-action gate |
  | [`inbox-app`](sdk/typescript/examples/inbox-app/) | capsule (exposer) | Exposing structured resources back to MCP clients |
  | [`rss-ingestor`](sdk/typescript/examples/rss-ingestor/) | capsule (ingestor, no-auth) | Scheduled trigger + `cursor.{get,set}` + `memory.upsert` for public feeds |
  | [`calendar-ingestor`](sdk/typescript/examples/calendar-ingestor/) | capsule (ingestor, OAuth) | The full Google OAuth path: `oauth.access_token`, runtime-driven browser flow, transparent refresh |
  | [`demo-agent`](sdk/typescript/examples/demo-agent/) | external Path-B agent | An external MCP client with a local Ollama brain. Shows the allowed/denied capability paths end-to-end — the trust contract made visible |
- ✅ **Capsule ingestor primitives** end-to-end: credentials store, cursor store, scheduled triggers, source-registry bridge, OAuth orchestrator with well-known provider registry (google, github), `oauth.access_token` MCP tool, `/console/oauth/*` HTTP surface
- ✅ **Auto-embedding on ingest** (v0.1.5) — when an embedding-capable model adapter is configured (Ollama with `nomic-embed-text`, OpenAI `text-embedding-3`, …), the memory layer fills in vectors for any entry that arrives without them. The standard flow `loamss source sync` → `memory.query` works out of the box on a fresh install; no organizer capsule required.
- ⏳ **Capsule marketplace** (registry MVP + certification pipeline)

### User-facing

- ✅ **CLI**: `init`, `doctor`, `start`, `open`, `status`, `version`, `config`, `capsule`, `source`, `client`, `grant`, `audit`, `approve`, `export`
- ✅ **Embedded dashboard**: first-run wizard (Welcome → Storage → Memory → Models → Connect → Done) + five interactive panes (Sources, Capsules, Apps, Approvals, Activity)
- ✅ **Config hot-reload** with restart-required signal for changes that can't be hot-swapped
- ✅ **Release binaries** via GitHub Actions: `loamss-darwin-{arm64,amd64}`, `loamss-linux-{arm64,amd64}` on each tag

See [`ROADMAP.md`](ROADMAP.md) for the phased plan.

---

## Try it locally

For the full walkthrough (install → wizard → first source → first query → external MCP client), see [`docs/getting-started.md`](docs/getting-started.md). The short version:

### Homebrew (macOS + Linux)

```bash
brew tap loamss/loamss
brew install loamss
loamss start --open
```

The same formula works on Apple Silicon, Intel Macs, and Linux (arm64 + amd64 — Homebrew has been Linux-native since 2019). [`homebrew/README.md`](homebrew/README.md) has the verification + tap setup details.

### Direct binary download

If you'd rather not use Homebrew:

```bash
# Pick the right tarball for your OS + arch from the latest release.
# Example: Linux arm64 (Raspberry Pi 4/5, AWS Graviton, …)
curl -L -O https://github.com/loamss/loamss/releases/latest/download/loamss-v0.1.5-linux-arm64.tar.gz
tar xzf loamss-v0.1.5-linux-arm64.tar.gz
./loamss-v0.1.5-linux-arm64/loamss start --open
```

The runtime has no runtime dependencies — embedded dashboard, static-linked SQLite, etc. — so it just runs.

### Build from source

For active development or to run the latest unreleased commit, you need Go 1.25+ and [Bun](https://bun.sh) (Bun builds the embedded dashboard; the runtime itself is pure Go):

```bash
git clone https://github.com/loamss/loamss
cd loamss/runtime
make build              # bun build (console) + go build (runtime)
./bin/loamss start --open
```

---

`--open` launches your browser at the daemon's URL. On a fresh install you land on the three-minute first-run wizard; subsequent runs land on the dashboard.

Three things to try once you're in:

1. **Add a source.** Sources pane → `+ Add source`, point it at a directory of Markdown (`~/Documents` works), watch the memory layer fill up.
2. **Install a capsule.** Capsules pane → `+ Install capsule`, paste `sdk/typescript/examples/daily-brief` from the cloned repo. The runtime issues its permission grants and shows the slip.
3. **Pair an external agent.** Apps pane → `+ Pair an app`, then run [`sdk/typescript/examples/demo-agent`](sdk/typescript/examples/demo-agent/) — a small Node script that uses a local Ollama model and asks your memory questions through MCP. Watch the Activity feed log each call as allowed or denied.

`loamss export` produces a complete archive of your storage + memory + audit history. Walk away whenever.

---

## Reading order

**Curious**: this README → [`scenarios.md`](scenarios.md) → [`ROADMAP.md`](ROADMAP.md).

**Trying it**: [`docs/getting-started.md`](docs/getting-started.md) — install, first run, first source, first query.

**Building a capsule**: [`docs/build-your-first-capsule.md`](docs/build-your-first-capsule.md) → [`capsule-spec.md`](capsule-spec.md) → the TypeScript SDK at [`sdk/typescript/`](sdk/typescript/). For ingestor capsules specifically: [`docs/capsule-ingestor-primitives.md`](docs/capsule-ingestor-primitives.md).

**Integrating an existing app** (Path B): [`docs/connect-your-app.md`](docs/connect-your-app.md) → [`mcp-surface.md`](mcp-surface.md).

**Building a native app** (Path A): [`native-apps.md`](native-apps.md) → [`mcp-surface.md`](mcp-surface.md) → [`topology.md`](topology.md).

**Plugging in a backend**: [`adapter-interface.md`](adapter-interface.md).

**Contributing**: above plus [`CONTRIBUTING.md`](CONTRIBUTING.md) and [`CLAUDE.md`](CLAUDE.md) for project conventions.

---

## What Loamss is not

- **Not a chat app.** The chat surfaces are whatever you already use; Loamss is what they connect to when you let them.
- **Not a model.** We don't train one. We don't host one. We route to whichever you configure — local Ollama, hosted Anthropic, OpenAI, whatever.
- **Not a data host.** You bring storage. We don't operate the database your data sits in.
- **Not a walled garden.** Capsules from anywhere, consumers from anywhere, storage anywhere.
- **Not a SaaS lock-in.** If we stop being useful, you point another runtime at your data and walk away.
- **Not "for AI."** AI tools are one class of consumer. The framework treats every consumer the same.

---

## License

[Apache-2.0](LICENSE). Open source, with a patent grant. Contributions welcome — see [`CONTRIBUTING.md`](CONTRIBUTING.md). Security reports: [`SECURITY.md`](SECURITY.md).

## Links

- Repo: [github.com/loamss/loamss](https://github.com/loamss/loamss)
- Site: [loamss.com](https://loamss.com) (placeholder)
- Full spec corpus: [`ARCHITECTURE.md`](ARCHITECTURE.md) links to every spec document.
