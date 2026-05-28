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

### W1 — Make the install path work for non-authors ✅ DONE

Decision: option A (repo made public). Verified end-to-end:

```
$ brew tap loamss/loamss && brew install loamss && loamss version
Tapped 1 formula (14 files, 13.2KB).
🍺  /opt/homebrew/Cellar/loamss/0.1.5: 6 files, 47.3MB, built in 2 seconds
loamss v0.1.5
```

Side benefits unlocked by the repo going public:
- npm `--provenance` now possible (currently disabled in the
  release workflow because of the private-repo block — re-enable
  in a follow-up PR for future SDK publishes).
- README, scenarios, RFCs are all openly readable; no more
  "I'd link you but the repo is private" friction.

No further W1 work.

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

### W4 — First Path A reference app: native email on Loamss

**Decision: Loamss Mail** — a native email app where messages live in
the user's Loamss. Strongest possible demo of the substrate thesis
("switch email apps, your mail follows; uninstall the app, your mail
stays"). Largest of the v0.2 workstreams; runs in parallel with W2/W3.

**Decisions locked in for v0.2:**

- **Domain**: `mail.loamss.com` (subdomain of the registered loamss.com).
- **Relay provider**: **Postmark**. Best-in-class deliverability at small
  scale (~$15/mo for 10k messages). Replaceable by SES if volume grows.
- **Router service hosting**: **Cloud Run**. Same platform Loamss
  itself runs on; ~$5/mo for the router service. Tiny Go service.
- **Inbound shapes supported**: (1) fresh `<username>@mail.loamss.com`
  address (your router operates the mapping), (2) BYO — user owns a
  domain, configures MX to point at their own Postmark/Mailgun/SES,
  webhook POSTs to their Loamss directly. Both go through the same
  app on the user side; only the upstream differs.
- **Outbound**: SMTP via Postmark in both modes, with DKIM signing
  on `mail.loamss.com`. BYO users configure their own DKIM.

**Sub-tasks:**

1. **Mail infrastructure setup.** Register `mail.loamss.com` subdomain
   in DNS, set up Postmark account, configure SPF/DKIM/DMARC. Smoke-test
   inbound + outbound from a curl-driven script before any app code.
   **~1 week.** (Mostly DNS propagation waiting + Postmark verification.)

2. **Loamss Mail router service.** Small Go service running on Cloud Run.
   Routes:
     - `POST /webhooks/postmark/inbound` — Postmark POSTs inbound mail here
     - `POST /accounts/register` — user registers a `<username>@mail.loamss.com`
       address bound to their Loamss endpoint
     - Internal: lookup table mapping address → Loamss URL + per-user bearer
     - Dispatcher: forwards the MIME message to the matching Loamss via MCP
   Postgres for the lookup table. ~500 lines of Go. **~1 week.**

3. **`loamss-mail` capsule.** Defines:
     - Entity types: `app.loamss-mail/message`, `app.loamss-mail/draft`,
       `app.loamss-mail/thread`, `app.loamss-mail/contact`
     - MCP tools: `mail.send`, `mail.compose`, `mail.archive`, `mail.delete`
     - HTTP route exposed: `POST /capsule/loamss-mail/inbound` for the
       BYO flow (when user's own relay POSTs straight into their Loamss)
     - Memory-layer integration: JWZ threading + extracted text indexing
     - Permission preset for the pairing flow (see W6 below)
   Ships in `sdk/typescript/examples/loamss-mail/`. **~1 week.**

4. **Loamss Mail backend** — thin Bun service. Handles pairing on
   first run, persists per-user Loamss endpoint + admin bearer, exposes
   inbox/thread/composer APIs that proxy to the user's Loamss. Holds
   no email content of its own. **~4-5 days.**

5. **Loamss Mail frontend** — clean, minimal but real. Inbox list,
   thread view, composer, search-as-you-type. Real-time inbox updates
   via SSE subscription to the user's Loamss. **~1-2 weeks.**

6. **Default/customize permission slip (runtime + console change,
   benefits all apps).** See "W4b" below — split out because it's
   broader than email.

7. **Worked-example doc** — `docs/build-your-first-app.md`. Walks a
   developer through reproducing Loamss Mail (or a smaller version of
   it). Lands once the app is solid. **~3-4 days.**

**Total**: ~6-8 weeks. The longest pole in v0.2.

**Exit criteria**:
- A user can register `<chosen>@mail.loamss.com`, receive mail at that
  address into their Loamss, read/reply/send from the app.
- A user can configure BYO mode against `alice@her-own-domain.com` and
  the same app works without any dependency on the project's router.
- Uninstalling the app leaves all email intact in the user's Loamss;
  installing a different (future) Loamss email app sees the same data.

---

### W4b — Default/customize permission slip (runtime + console + spec)

Split out from W4 because it's a substrate-level feature, not an
app feature. Every Path A app benefits — Loamss Mail, future Loamss
Notes, future Loamss Calendar, etc.

**Sub-tasks:**

1. **Spec update.** `capsule-spec.md` (and the pairing-request
   schema in `mcp-surface.md`) add a `permissions.presets` block:
   apps declare one or more named permission bundles. Backward
   compatible — apps without presets keep the flat-grant-list shape.
   **~1 day.**

2. **Runtime support.** Permission engine accepts grants requested
   via preset id; audit records both the preset chosen and the
   resulting grant set. **~2 days.**

3. **Console UI.** Pairing screen shows preset radio buttons +
   inline grant summary + a "Customize" expansion. ~3 days.

4. **SDK helpers.** `@loamss/sdk` exposes a helper for app code to
   construct presets ergonomically. **~1 day.**

**Total**: ~1 week. Sequenced before the email app's pairing flow
needs it; can land in parallel with W4 sub-tasks #1–#3.

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

> **v0.2.0 — cloud-deployable substrate + native email**
>
> Loamss now runs in the cloud. Same binary as the laptop install;
> point it at a Postgres + an object store with `LOAMSS_PROFILE=cloud`
> and a setup token, get a stable substrate at a public URL. Cloud
> Run, Fly.io, and GKE deploy guides included.
>
> **Loamss Mail** ships as the first Path A reference app — a native
> email app where messages live in *your* Loamss, not the app's
> database. Get a fresh `<you>@mail.loamss.com` address, or use BYO
> with your own domain. Uninstall the app; your mail stays. Switch
> apps; your mail follows.
>
> The pairing flow now supports default-or-customize permission
> presets — apps suggest sensible defaults; you accept with one
> click or expand for fine control. Works for any Path A or Path B
> app.
>
> Plus: Postgres backends for runtime + audit (audit exportable as
> JSONL), one-time setup-token wizard gate, `--profile` flag,
> Dockerfile (`ghcr.io/loamss/loamss:v0.2.0`).
>
> Brew install path repaired — anonymous users can now install via
> the public Homebrew tap.

---

## Decisions settled

| Decision                              | Outcome                                              |
| ------------------------------------- | ---------------------------------------------------- |
| Public the repo (W1)                  | ✅ Done. `brew install loamss` works for anyone.     |
| W4 app category                       | **Email**. `loamss-mail` on `mail.loamss.com`.       |
| Inbound shapes                        | Fresh `<user>@mail.loamss.com` + BYO domain          |
| Relay provider                        | Postmark                                             |
| Router hosting                        | Cloud Run                                            |
| Setup-token UX                        | Paste-in-browser (magic link is v0.3 follow-up)      |
| Container language runtimes           | BYO image (per `rfc-cloud-deployment.md`)            |
| Permission slip                       | Default-preset-or-customize (W4b)                    |

## Designing for future hosted Loamss

The project has agreed to eventually offer a **hosted Loamss
provisioning service** ("Loamss Cloud") where users who don't have a
runtime can sign up and get one provisioned for them. **Not in v0.2
scope**, but several W2/W3 choices should preserve the path so v0.2
doesn't accidentally box it out. See ROADMAP Phase 2.5 for the longer
description.

Architectural alignment notes — keep these in mind during W2/W3:

1. **Setup-token bypass for preconfigured deployments.** The wizard
   gate (W2 sub-task #6) needs a `LOAMSS_PRECONFIGURED=true` mode
   that skips the setup-token flow entirely. In hosted, the operator
   (you) generates the admin bearer token at provisioning time and
   delivers it to the user out-of-band (email, dashboard); the
   wizard isn't run interactively. Implementation: when
   preconfigured, the runtime boots straight to dashboard with an
   admin client already paired (its credential comes from an env
   var or a mounted secret).

2. **Database provisioning patterns — both shapes supported.** A
   hosted operator might provision either:
   - **Database per tenant** (Postgres database per user; clean
     isolation, ~$10/mo each at the smallest Cloud SQL tier)
   - **Schema per tenant** (one Postgres cluster, one schema per
     user; cheaper, more migration complexity)
   The runtime should be agnostic — it sees a DSN, runs its
   migrations. Test both shapes in the Postgres-adapter test suite
   so hosted operators can pick.

3. **Storage provisioning patterns — both shapes supported.**
   - **Bucket per tenant** (cleanest walkaway story; the user owns
     their bucket)
   - **Prefix per tenant in a shared bucket** (cheaper)
   `storage:gcs` already accepts bucket + prefix, so no runtime
   change. Test both deployment shapes.

4. **Subdomain routing is the control plane's job.** The runtime
   doesn't need to know it's running at `<user>.loamss.cloud`. It
   binds `0.0.0.0:$PORT`, serves at whatever URL is routed to it.
   No changes needed.

5. **Audit log export is critical for hosted off-boarding.** When
   a hosted user cancels, you need to give them their data. The
   audit log export (W2 sub-task #4, JSONL format) is part of how
   "give the user everything we have on them" works in hosted. Keep
   the format stable.

6. **No multi-tenant retrofit.** Hosted = many single-tenant
   instances, orchestrated by an external control plane. The
   runtime itself stays single-tenant in v0.2 and forever. (If
   "multi-tenant runtime" ever comes up as an alternative, push
   back — it breaks the trust story.)

## Risks + open questions

### What if W4 (email app) reveals SDK gaps?

Likely outcome: writing the first real Path A app will surface
missing primitives in the SDK (entity-type registration ergonomics,
offline write queue, pairing-flow helpers). Treat each gap as a
small SDK PR; don't let it derail W4. If the gaps are big,
re-evaluate scope.

### Postmark cost at any meaningful scale

$15/mo for 10k inbound/outbound is fine for early access. If hosted
takes off, mail volume scales linearly with users; could hit
hundreds/month quickly. Plan now for SES as the eventual production
relay (10× cheaper at scale) but ship v0.2 on Postmark for setup
simplicity.

### Abuse handling for `@mail.loamss.com` addresses

Once you offer free addresses on your domain, people will sign up
to spam. Mitigations needed before opening signup publicly:
- Rate limit on registration per IP / fingerprint
- Email verification on the registering Loamss instance (paired
  client must demonstrate access to a real address first)
- Abuse reporting endpoint + an off-boarding workflow that disables
  an address without deleting the user's Loamss
- Block known disposable-email signup patterns

None of this is needed for *closed-beta* v0.2 (you + a few friends).
All of it is needed before opening public signup. Treat as a v0.3+
gate on "open Loamss Mail to the public."

### Operational reality of running a mail service

Even for closed beta, you'll be on the hook for:
- DNS / DKIM key management
- Postmark account uptime
- Router service uptime (Cloud Run scales to zero by default — but
  inbound mail can't wait 5s for a cold start, so `--min-instances=1`
  is required, ~$10/mo)
- Spam-folder placement issues from new users' addresses

Not insurmountable; just be honest with yourself that this changes
the project from "ship a binary" to "ship a binary + run a small
service." Documented under the W4 plan; revisit before opening
public signup.

### What if W4 (Notes app) reveals SDK gaps?

Likely outcome: writing the first real Path A app will surface
missing primitives in the SDK (entity-type registration ergonomics,
offline write queue, pairing-flow helpers). Treat each gap as a
small SDK PR; don't let it derail W4. If the gaps are big,
re-evaluate scope.

---

## Parallelization

```
Week 1-2:   W2 (database adapter SPI + runtime.db Postgres impl)
            || W4-prep: register mail.loamss.com subdomain, set up
               Postmark account, configure DNS (SPF/DKIM/DMARC),
               smoke-test inbound + outbound via curl

Week 3-4:   W2 (audit.db Postgres + setup-token gate + profile flag)
            || W4 (router service + loamss-mail capsule manifest +
               app backend skeleton)
            || W4b (spec update + permission preset runtime support)

Week 5:     W3 (Dockerfile, Cloud Run + Fly deploy guides)
            || W4 (app frontend + JWZ threading + memory-layer indexing)
            || W4b (console UI + SDK helper)

Week 6:     W2/W3 first cloud deploy + smoke
            || W4 (BYO inbound webhook + end-to-end against cloud deploy)
            || W5 re-record demo against the cloud install

Week 7-8:   End-to-end testing + release prep + v0.2.0 ship
```

Realistic v0.2.0 ship: **6-8 weeks**.

## Cadence + how this plan evolves

- **Patch releases** (`v0.1.6`, `v0.1.7` …) can ship at any point
  for small fixes that don't fit the v0.2 storyline.
- **Pre-releases** (`v0.2.0-alpha.1`, etc.) once W2 lands — invites
  internal testing of the cloud deploy before W3 + W4 are done.
  `v0.2.0-alpha.2` once W4b lands (the permission preset system is
  externally visible and worth pre-shipping for feedback).
- **v0.2.0** when W2, W3, W4, W4b all exit. W5 is nice-to-have.

This document gets updated each time a workstream lands or a
decision changes. When v0.2.0 ships, this file is archived and a
new `docs/plan-v0.3.md` takes over — likely focused on Loamss
Cloud (hosted provisioning, see ROADMAP Phase 2.5).

---

## What I'd do next, if asked right now

W1 is done. Three things to start in parallel:

1. **W2 sub-task #1** — database adapter SPI refactor (no behavior
   change). The work that unlocks everything else.
2. **W4-prep** — register `mail.loamss.com` subdomain, sign up for
   Postmark, configure DNS. Mostly waiting for propagation; you can
   kick this off while W2 lands and check in when it's verified.
3. **W4b sub-task #1** — spec update for permission presets in
   `capsule-spec.md` and `mcp-surface.md`. Small writing pass, then
   the runtime + console implementations follow.

Tell me to start and I'll begin with W2 sub-task #1.
