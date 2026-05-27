# Implementation plan — v0.2

A continuance plan for what happens between v0.1.5 (shipped) and
v0.2.0. Living document: edit as work lands, decisions change, or
priorities shift.

Status: drafting. Last updated: v0.1.5 + reframe.

---

## Where we are right now

**Shipped through v0.1.5:**

- Single-binary runtime, embedded console, MCP-over-HTTP+SSE.
- Permission engine (capability + scope + per-action approval).
- Hash-chained audit log on local SQLite.
- Capsule host with MCP-over-stdio + permission-gated callbacks.
- Storage adapters: `fs-encrypted`, `s3`, `gcs`.
- Memory adapters: `sqlite-vec`, `pgvector` (incl. Cloud SQL IAM),
  `chroma`, `qdrant`.
- Model adapters: `anthropic`, `openai`, `ollama`, `none`/`dummy`.
- Source SPI + two reference connectors (`source:files`, `source:gmail`).
- Capsule ingestor primitives (credentials, cursor, scheduled trigger,
  source-registry bridge, OAuth orchestrator).
- Six reference capsules incl. an external Path-B demo-agent.
- TypeScript SDK published to npm (`@loamss/sdk@0.1.5`); Python SDK in-tree.
- Homebrew tap, GitHub Actions release pipeline, multi-arch binaries.
- Memory layer auto-embedding on ingest (the v0.1.5 fix).
- Docs reframed around the substrate thesis (`apps on Loamss`).

**Strategic posture (settled by the v0.1.5 reframe):**

- The long-term shape is **Path A native apps** writing into the
  user's Loamss as their backing store (`native-apps.md`).
- Source connectors are **transitional** — they migrate legacy data
  from SaaS into a user's Loamss.
- Cloud-deployable single-tenant first; multi-tenant / Fleet / SSO
  deferred until at least one design-partner conversation moves.

---

## Goals for v0.2.0

In one sentence: **the same binary that runs on a laptop also runs as
a stable, addressable substrate in the cloud, and the first Path A
reference app exists to prove the substrate is worth running.**

Concretely, "v0.2.0 ships" means:

1. `docker run loamss:v0.2.0` (or `gcloud run deploy`, or
   `fly launch`) brings a runtime up against a managed Postgres and
   a user-owned object store, with no code changes from the laptop
   install.
2. A **Path A reference app** lives at `examples/` — a small,
   complete, native Loamss app that demonstrates the pattern from
   `native-apps.md`. Picking the category is a workstream decision
   below.
3. The Homebrew install path works for anyone, not just the author
   (the private-repo blocker is closed).
4. The reframe survives — readers landing on the repo see the
   substrate-thesis story consistently across README, ROADMAP,
   getting-started, and at least one worked Path A example.

---

## Non-goals (explicit punts)

These are real product directions, just not in v0.2.0. Recording them
here so the scope doesn't drift.

- **Multi-tenant runtime.** No `tenant_id` retrofit. One Loamss
  serves one user / one principal-set, period.
- **Horizontal autoscaling within a tenant.** Cloud Run / GKE
  deployments use `min/max = 1`. The capsule scheduler, OAuth
  orchestrator, and audit chain are not yet multi-instance-safe.
- **Federation between Loamss instances.** No cross-instance grant
  delegation. (The "per-employee + per-team + per-project" pattern
  discussed in design conversations is real and probably right —
  later.)
- **OAuth source-callback rework for the cloud.** Calendar / Gmail /
  Drive ingestor capsules continue to require a localhost callback.
  They work fine on a laptop install; they break on a cloud install.
  Since source ingestion is transitional and steady-state Path A apps
  remove the need, this is deferred.
- **SSO / SCIM / OIDC token validation.** Pair-code stays the only
  pairing primitive in v0.2.
- **Enterprise / Fleet control plane.** Not until a design partner
  is actually engaged.
- **Mobile companion app.** Phase 3 territory.
- **Capsule marketplace + signing pipeline.** Phase 2 ROADMAP item;
  not blocking v0.2.

---

## Workstreams

Ordered roughly by dependency. W1–W3 are blocking for the cloud
goal; W4 (Path A reference) can land in parallel because it doesn't
depend on cloud deploy to be a useful example.

### W1 — Make the install path work for non-authors (small but blocking)

**Why first**: README claims `brew install loamss` works. Today it
404s for anyone who isn't authenticated against the private GitHub
repo. Cloud deployment isn't the most pressing blocker — the
install-from-tap blocker is.

**Two options:**

| Option                         | Effort   | Trade                                                                                                                                                                                                |
| ------------------------------ | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **A. Make the repo public.**   | Hours    | Cleanest. The README, docs, code are all openly published. Some legacy operational artifacts (env names, etc.) may need scrubbing. Also makes npm provenance possible later (currently blocked).      |
| **B. Mirror release tarballs to a public CDN/bucket.** | 1–2 days | Repo stays private. Set up a workflow that copies release artifacts to a public GCS bucket / Cloudflare R2. Homebrew formula points at the public URL. More machinery to maintain. |

**Recommendation: A** unless there's a specific reason to keep the
repo private. The codebase is intended to be Apache-2.0 open source
per the LICENSE; private was a release-pipeline artifact, not a
design choice.

**Decision needed**: pick A or B. Then execute.

**Exit criterion**: a fresh machine can `brew tap loamss/loamss &&
brew install loamss && loamss start` with no auth setup.

---

### W2 — Externalize runtime state (the core cloud-deploy enabler)

**See:** [`rfc-cloud-deployment.md`](rfc-cloud-deployment.md) for the
full design. This workstream executes Phase 1 + Phase 2 of that RFC
with the scope adjustments captured below.

#### Sub-tasks, in order

1. **Database adapter SPI.** Define a `runtime.Database` interface.
   Refactor every existing `Store` (`permission`, `source`, `capsule`,
   `oauth`, `memory_layer`) to take the interface rather than open
   its own SQLite handle. No behavior change. **~2 days.**

2. **Postgres implementation of `runtime.Database`.** Per-subsystem
   golang-migrate migration files (`migrations/postgres/`,
   `migrations/sqlite/`). Idempotent migration run at startup. Test
   matrix runs against both backends via testcontainers (we already
   have the pattern from `memory:pgvector`). **~3 days.**

3. **Audit writer SPI + Postgres implementation.** Separate DSN per
   the v0.2 design decision (`runtime.audit.dsn` distinct from
   `runtime.database.dsn`). Append-optimized schema with
   transactional `chain_head` update for hash-chain integrity.
   Verify chain holds across daemon restarts on Postgres. **~3 days.**

4. **Audit export utility.** A `loamss audit export --format jsonl
   [--since ...]` CLI that streams events out in a stable JSONL
   shape suitable for archival or SIEM ingestion. The schema this
   exports freezes at v0.2.0 as a forward-compatibility contract.
   **~1 day.**

5. **Listener bind + `PORT` env var.** Honor `$PORT` when set, bind
   `0.0.0.0:$PORT` instead of `127.0.0.1:7777` when running in cloud
   mode. **A few hours.**

6. **Setup-token gate for the console.** When the runtime detects
   it's running publicly, the dashboard and wizard endpoints are
   gated by a one-time setup token generated at first start (auto-
   printed if not provided via `LOAMSS_SETUP_TOKEN`). Token
   exchanged for a signed-cookie session; invalidated when wizard
   completes. **~3 days.**

7. **`--profile` flag + cloud auto-detection.** `runtime.profile:
   local | cloud` config key. `LOAMSS_PROFILE` env var. Detection
   from `K_SERVICE` / `KUBERNETES_SERVICE_HOST` / `FLY_APP_NAME` /
   `RENDER` / `RAILWAY_ENVIRONMENT`. Explicit always wins.
   **~1 day.**

**Total**: ~2 weeks of focused work. Sequential because each
sub-task builds on the previous.

**Exit criterion**: `LOAMSS_PROFILE=cloud DATABASE_URL=...
AUDIT_DATABASE_URL=... LOAMSS_SETUP_TOKEN=... loamss start` boots
against Postgres, the wizard requires the token, runtime tests
pass under both backends, audit chain verified.

---

### W3 — Cloud-deployment packaging + guides

Depends on W2.

1. **Dockerfile.** Multi-stage (`golang:1.25-alpine` build →
   `alpine:3` runtime). Static binary. Built and pushed to GHCR by
   the existing release workflow at
   `ghcr.io/loamss/loamss:vX.Y.Z`. Multi-arch via `buildx`. **~1
   day.**

2. **Cloud Run deploy guide.** End-to-end recipe: create Postgres
   instance, create GCS bucket, build/push image, `gcloud run
   deploy` with the right env vars, wizard completion against the
   public URL with the setup token. Lands at
   `docs/deploy-cloud-run.md`. **~1 day** (mostly writing).

3. **Fly.io deploy guide as the comparison option.** Same recipe,
   `fly launch` shape. Lands at `docs/deploy-fly.md`. **~half a
   day.**

4. **GKE Helm chart minimum.** A minimal chart with persistent
   Postgres expected as a sibling; deploy guide at
   `docs/deploy-gke.md`. **~2 days.** Lower priority than the other
   two; Cloud Run + Fly cover most personal cases.

**Total**: ~4 days for Cloud Run + Fly (the two most useful);
GKE another 2 if we want it in this release.

**Exit criterion**: a follow-the-doc deploy to Cloud Run produces a
running Loamss reachable at a public URL, with the demo-agent able
to pair against it and query memory.

---

### W4 — First Path A reference app (the substrate-is-worth-running proof)

This is the highest-leverage Phase-2 ROADMAP item and the most
important non-cloud workstream. Without a worked Path A example,
"apps on Loamss" stays a slogan.

**Decision needed: which app category?**

| Category   | Pros                                                                                                  | Cons                                                                            |
| ---------- | ----------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| **Notes**  | Simplest data model. Wide audience. Existing exemplars (Obsidian, Bear) make the contrast obvious.    | Crowded space; differentiation has to be "your notes are portable", not features. |
| **Journal**| Even simpler than notes. Daily entries, no folder/tag hierarchy needed at v1.                        | Smaller audience. Easier to demo, harder to build a real user base around.       |
| **Email**  | The highest-impact category — "your email lives in your substrate" is the strongest demo there is.    | Substantially more work (MIME parsing, SMTP sending, IMAP for back-compat).      |
| **Calendar**| Clear data model. Demoable via federation later.                                                     | Tight integration with other people's calendars makes the "your data" pitch muddier. |

**Recommendation: Notes.** Simplest end-to-end, shortest path to a
real worked example. Saves email for v0.3+ as a much bigger second
example. (If you want the splashier demo, Journal is even faster to
build and produces a cleaner narrative.)

**Sub-tasks (assuming Notes):**

1. **Capsule manifest** defining the `app.loamss-notes/note` entity
   type — schema (`title`, `body`, `tags`, `created_at`,
   `updated_at`), permission requests (`memory.write` + `memory.read`
   + `memory.query` scoped to that entity type). **~2 days.**

2. **Backend** — thin Bun/TS service that handles pairing on first
   run, persists the bearer token + Loamss endpoint per user, and
   proxies CRUD/query against the user's Loamss. Holds no note
   content. **~3-4 days.**

3. **Frontend** — minimal but real. Note list, note editor,
   search-as-you-type, tag pills. Aesthetic clean enough to demo.
   **~4-5 days.**

4. **Pairing UX** — the moment of consent matters. The first-run
   flow ("paste your Loamss URL, paste your pairing code") needs to
   feel like a feature, not a chore. **~1-2 days.**

5. **Worked-example doc** — `docs/build-your-first-app.md` walks a
   developer through reproducing this app. Lands once the app
   itself is solid. **~2-3 days.**

**Total**: ~2-3 weeks for the app + doc. Can parallelize with W2/W3.

**Exit criterion**: the Notes app runs locally, pairs with a user's
Loamss, creates/edits/searches notes that are persisted in the
user's Loamss, survives `loamss source remove notes-app && loamss
source list` (i.e. the data is in the user's substrate, not in the
app's backend).

---

### W5 — Finish the v0.1.5 demo recording

Small, but it's on the open task list (#73). The recording started
during v0.1.5 capture stopped because Terminal-tier permissions
blocked typing into Terminal. Two paths:

- **Re-record on the cloud install** (after W3 lands) — show
  installing Loamss on Cloud Run, pairing the demo-agent against
  the public URL. More impressive than the laptop recording. **~1
  day.**
- **Re-record on laptop with the BYO-terminal approach** — type
  commands yourself in a visible Terminal while screen-recording.
  Less polished but faster. **~30 min.**

**Recommendation: wait until W3 lands, re-record on cloud.** Closes
both the cloud-deploy narrative and the demo at once.

---

## What goes into the v0.2.0 release notes

Drafted now so the goalposts are visible:

> **v0.2.0 — single-tenant cloud deployment + first native app**
>
> Loamss now runs in the cloud. Same binary as the laptop install;
> point it at a Postgres + an object store with `LOAMSS_PROFILE=cloud`
> and a setup token, get a stable substrate at a public URL. Cloud
> Run, Fly.io, and GKE deploy guides included.
>
> First Path A reference app shipped: a native notes app where notes
> live in *your* Loamss, not the app's database. Uninstall the app;
> your notes stay. Switch apps; your notes follow.
>
> Plus: Postgres backends for runtime + audit (audit exportable as
> JSONL), one-time setup-token wizard gate, `--profile` flag,
> Dockerfile (`ghcr.io/loamss/loamss:v0.2.0`).
>
> Brew install path repaired — anonymous users can now install via
> the public Homebrew tap.

---

## Risks + open questions

### Repo public-vs-private (W1)

**Question**: any reason to keep `loamss/loamss` private?

I don't see one. The codebase is Apache-2.0 by intent; private was a
release-pipeline workaround that's now causing user-facing
breakage. If there's a reason (something in commit history, a
private contract obligation, etc.), it should be surfaced before
W1. If no reason, just flip it public.

### Notes vs. Journal vs. Email for W4

**Question**: which Path A app shape for the reference?

Notes is the safe choice. Journal is the easiest. Email is the most
impressive. I'd default to Notes unless you have a strong instinct
for one of the others.

### Setup-token UX (W2 #6)

**Question**: paste in browser, or click email-style magic link?

Magic link is cleaner UX but requires either configuring an SMTP
relay on the Loamss side (out of scope) or relying on the user to
copy from the container logs. Paste-in-browser is uglier but
self-contained.

Recommendation: paste-in-browser for v0.2; magic link as a v0.3
follow-up if a UX complaint emerges.

### Capsule subprocess model on Cloud Run

**Question**: do we ship a Dockerfile that includes Bun (the TS
capsule runtime), or do we keep the base image minimal and document
"extend this image to add your capsule runtimes"?

Decision already made in `rfc-cloud-deployment.md`: BYO image. Base
image is small; users who run TS capsules extend with `FROM
ghcr.io/loamss/loamss:v0.2 ... RUN apk add bun`.

### What if W4 (Notes app) reveals SDK gaps?

Likely outcome: writing the first real Path A app will surface
missing primitives in the SDK (entity-type registration ergonomics,
offline write queue, pairing-flow helpers). Treat each gap as a
small SDK PR; don't let it derail W4. If the gaps are big,
re-evaluate scope.

---

## Cadence + how this plan evolves

- **Patch releases** (`v0.1.6`, `v0.1.7` …) can ship at any point
  for small fixes that don't fit the v0.2 storyline.
- **Pre-releases** (`v0.2.0-alpha.1`, etc.) once W1 + W2 land —
  invites internal testing of the cloud deploy before W3 + W4 are
  done.
- **v0.2.0** when W1, W2, W3, W4 all exit. W5 is nice-to-have.

This document gets updated each time a workstream lands or a
decision changes. When v0.2.0 ships, this file is archived and a
new `docs/plan-v0.3.md` takes over.

---

## What I'd do next, if asked right now

1. **Decide W1** (repo public vs CDN proxy). 5 minutes.
2. **Start W2 sub-task #1** (database adapter SPI refactor). The
   work that unlocks everything else.
3. **In parallel — pick W4 app category** (Notes / Journal / Email)
   so the design work can start while W2 is in flight.

Tell me which of those calls you want to make, and I'll proceed.
