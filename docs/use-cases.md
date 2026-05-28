# Use cases: setup and use

End-to-end scenarios for Loamss as it stands today (v0.2.0-alpha.2).
Read this as "what can I actually do, and how?" — every scenario
below is wired through real code, not roadmap fiction.

If you want the install steps verbatim, jump to
[`getting-started.md`](getting-started.md) (laptop) or
[`deploying.md`](deploying.md) (cloud). This doc is the connective
tissue — it tells you which doc to read in which order for which
goal.

---

## Setup paths

You pick one. Both produce a working Loamss; they differ in where
the runtime runs and what the trust perimeter looks like.

### A. Laptop install (recommended for getting started)

**When this is right.** Personal use. Single device. You want the
fastest path to a working substrate with the lowest friction.

**Trust perimeter.** The listener binds `127.0.0.1:7777`, so only
processes on your machine can reach the runtime. No setup token,
no external auth — the laptop kernel IS the gate.

**Steps** (full walkthrough in [`getting-started.md`](getting-started.md)):

```bash
brew tap loamss/loamss
brew install loamss
loamss start --open
```

The first run opens a 3-minute wizard: pick a storage adapter
(default: `fs-encrypted` writes AES-256-GCM blobs to
`~/.loamss/storage/`), pick a memory adapter (default:
`memory:sqlite`), optionally configure a model (Ollama is
auto-detected; or paste an Anthropic/OpenAI key).

When the wizard finishes, you're on the dashboard. The runtime is
running in your terminal; leave it. Open a second terminal for
everything below.

**Cost:** $0.

### B. Cloud Run deploy (recommended for multi-device / shareable)

**When this is right.** You want to reach your Loamss from your
phone, your work laptop, and your tablet — without leaving any of
them running. Or you want a single instance for a small team /
family. Or you're prototyping a Path A native app and need a
public URL for OAuth redirects.

**Trust perimeter.** The listener binds `0.0.0.0:$PORT` (public
internet). The **setup-token gate** activates: every `/console/*`
and `/pair` request requires `Authorization: Bearer <token>`. The
token is issued at startup, single-use, and the consumption
persists in Cloud SQL so cold starts don't re-open the gate.

**Steps** (full walkthrough in [`deploying.md`](deploying.md)):

```bash
PROJECT_ID=your-gcp-project ./deploy/cloud-run.sh
```

The script provisions a `db-f1-micro` Cloud SQL Postgres, builds
the container via Cloud Build, deploys to Cloud Run, and prints
back the wizard URL `<service>/?setup=<token>`. Click that link;
the console reads the token from the URL, stashes it in
`localStorage`, strips it from the address bar, and the wizard
behaves identically to the laptop flow.

**Cost:** ~$15-20/month (mostly the always-on Cloud SQL instance;
Cloud Run itself scales to zero when idle).

**Verified end-to-end** in commit 23a871c against
`marketplace-487603` — the gate enforces, the wizard submit
succeeds, the consumption persists across Cloud Run revision
swaps. The platform quirks (GFE strips `/healthz`, `db-f1-micro`
requires `--edition=ENTERPRISE`) are baked into the deploy script.

---

## What you do after setup

The five things below are independent — pick one, all five, or
none, in any order. Every one is shipped today.

### 1. Pair Claude Desktop (or any MCP client)

**Why.** You want Claude / ChatGPT / Cursor to read what's in your
Loamss when you talk to it. "What did Sarah and I decide about
the contract?" should be answerable from your own substrate.

**How.**

The runtime exposes MCP over HTTP+SSE at `/mcp`. Claude Desktop
launches stdio subprocesses for its MCP servers, so we connect
them through a community proxy called
[`mcp-remote`](https://www.npmjs.com/package/mcp-remote) that
translates Claude's stdio expectations into HTTP+SSE calls.

1. **Mint a pairing code** (laptop terminal, or dashboard → Apps
   pane → `+ Pair an app`):

   ```bash
   loamss client pair --name "Claude Desktop"
   # → code: 5QUK-5EPE (valid 10 minutes)
   ```

2. **Redeem it for a bearer** (prints exactly once — copy now):

   ```bash
   loamss client pair complete 5QUK-5EPE
   # → ✓ Paired client "Claude Desktop" (cli-01KSDHEJG...)
   #   Bearer credential: loamss_eyJraWQ...long.opaque.string
   ```

3. **Issue at least one capability grant** so Claude can do
   something. For reading memory:

   ```bash
   loamss grant create \
     --principal-kind client \
     --principal-id cli-01KSDHEJG... \
     --capability memory.read
   ```

4. **Edit Claude Desktop's config** at
   `~/Library/Application Support/Claude/claude_desktop_config.json`
   (macOS) or `%APPDATA%\Claude\claude_desktop_config.json`
   (Windows). Create the file if it doesn't exist:

   ```json
   {
     "mcpServers": {
       "my-loamss": {
         "command": "npx",
         "args": [
           "-y",
           "mcp-remote",
           "http://127.0.0.1:7777/mcp",
           "--header",
           "Authorization: Bearer loamss_eyJraWQ...your.full.bearer"
         ]
       }
     }
   }
   ```

   For Cloud Run deploys, replace `http://127.0.0.1:7777` with
   your service URL (`https://loamss-...run.app`).

5. **Fully quit and reopen Claude Desktop.** It re-reads the
   config and spawns `npx -y mcp-remote ...` on first launch
   (the proxy is cached after the first fetch).

6. **Verify.** In a new conversation, ask Claude
   *"what tools do you have available from my-loamss?"* It should
   list `memory.query` (plus whatever else your grants allow).
   Watch `loamss audit log --actor-kind client` for the call
   records.

**What this gets you.** A scoped, audited bridge between Claude
and your Loamss. Every tool call is logged in
`loamss audit log --actor-kind client`. The grant can be revoked
from the Apps pane in one click — Claude loses access on its next
call.

**Repeat for every tool.** ChatGPT (custom GPT with MCP), Cursor,
your own script — each pairs independently with its own bearer
credential. Same memory, different scopes, separate audit trails.

#### On cloud deploys

The Claude Desktop config (step 4) is identical — just swap
`http://127.0.0.1:7777/mcp` for your Cloud Run service URL.

The bootstrap is the same too. When you complete the wizard,
`/console/init` mints a "Loamss Console" client and returns its
bearer in the response. The wizard JS stores that bearer in
`localStorage`, so the dashboard's `+ Pair an app` button works
immediately — same as on laptop.

The first thing you do after the wizard: open Apps → `+ Pair an
app` → name it "Claude Desktop" → get the 4-char code → redeem
it on a CLI you have shell access to (your laptop, after pointing
`LOAMSS_DATABASE_URL` at the Cloud SQL DSN), or via a small curl
against your service:

```bash
URL="https://your-loamss.run.app"
CONSOLE_BEARER="<the bearer the wizard captured; visible in browser DevTools localStorage.loamss.client_bearer>"

curl -X POST "$URL/pair" \
  -H "Authorization: Bearer $CONSOLE_BEARER" \
  -H "Content-Type: application/json" \
  -d '{"code":"5QUK-5EPE","metadata":{"app":"claude"}}'
# → {"token":"loamss_eyJ...", ...}
```

That `loamss_...` token is what goes into Claude Desktop's
`claude_desktop_config.json` — same JSON as step 4 on laptop, just
with your Cloud Run URL.

Detail: [`docs/connect-your-app.md`](connect-your-app.md),
[`docs/deploying.md`](deploying.md) §"What the gate looks like".

---

### 2. Pull in existing data

Two paths for getting data INTO the substrate. Pick whichever
matches what you have.

#### 2a. Local files (`source:files`)

**Why.** You have a folder of notes, Markdown docs, exported chat
logs, PDFs — and you want to ask your AI tools questions about
them.

**How.** Dashboard → Sources pane → `+ Add source` →
`source:files`. Point it at a directory. The runtime walks the
tree, hashes each file, and writes one memory entry per file with
the path as the entity and the file content as the text body. If
you have an embedding-capable model adapter configured (Ollama
with `nomic-embed-text`, OpenAI `text-embedding-3`, …), the
memory layer fills in vectors automatically.

```bash
# Equivalent CLI
loamss source add --adapter source:files \
  --name personal-notes \
  --config root=/Users/me/Documents/notes
loamss source sync personal-notes
```

The Activity pane shows the sync's progress; `loamss source list`
shows the last sync timestamp and how many entries landed.

**Query it.** From Claude: *"what did I write about the cap
table?"* → Claude calls `memory.query` → relevant snippets come
back, Claude answers with citations.

#### 2b. Gmail (`source:gmail`)

**Why.** Your email is a substantial body of decisions, context,
and relationships you'd like queryable.

**How.** This one needs a one-time Google OAuth client setup. Full
walkthrough in [`setup-gmail.md`](setup-gmail.md):

1. **One-time Google Cloud setup** (~5 min) — create an OAuth
   2.0 client in your Google Cloud project, paste the client ID
   + secret into the dashboard's OAuth tab.
2. **Connect Gmail** — Sources pane → `+ Add source` →
   `source:gmail`. Click "Connect Google." A browser tab opens,
   you grant the standard read-only Gmail scope, you're redirected
   back to the dashboard.
3. **Sync.** First sync walks recent messages (configurable
   window) and writes one memory entry per email, with sender +
   subject as the entity. Subsequent syncs are incremental.

**Query it.** *"what's the latest thread with Sarah about the
contract?"* → Claude pulls relevant emails, summarizes the thread,
shows you who said what when.

Detail: [`docs/setup-gmail.md`](setup-gmail.md),
[`docs/build-your-first-source-connector.md`](build-your-first-source-connector.md)
if you want to build your own.

---

### 3. Install a capsule

**Why.** Capsules are how Loamss is extended. Want a daily
briefing from your inbox + calendar? An RSS reader that puts
articles in your memory? A custom organizer that builds a graph
of your contacts? Each is a capsule.

The Loamss ecosystem ships several reference capsules under
[`sdk/typescript/examples/`](../sdk/typescript/examples/) covering
every role. Pick one to feel out the install + permission flow.

**How** (using `daily-brief` as the example):

1. **Clone** the repo if you haven't:
   ```bash
   git clone https://github.com/loamss/loamss && cd loamss
   ```

2. **Build** the capsule:
   ```bash
   cd sdk/typescript/examples/daily-brief
   bun install && bun run build
   ```

3. **Install** via the dashboard:
   Capsules pane → `+ Install capsule` → paste the absolute path
   to `sdk/typescript/examples/daily-brief`. The runtime parses
   the manifest, shows you a **permission slip**:

   > This capsule wants:
   > - `memory.read` (full)
   > - `model.call` (any configured model)
   > - to expose tool `daily.brief`
   >
   > [Cancel] [Install]

4. **Click Install.** The runtime issues the listed grants,
   copies the code into `<data_dir>/capsules/`, starts the
   subprocess, and hooks up the MCP stdio surface.

5. **Use it.** From Claude: *"give me my daily brief"* → Claude
   calls the new `daily.brief` tool the capsule exposes → the
   capsule reads your memory, calls a model to summarize, returns
   the brief.

**The four capsule roles** (see [`capsule-spec.md`](../capsule-spec.md)):

| Role | What it does | Reference example |
|---|---|---|
| **Organizer** | reads memory, builds derived state (entities, summaries, embeddings) | `daily-brief` |
| **Exposer** | declares new MCP tools/resources for clients to call | `inbox-app` |
| **Actuator** | takes action in the world (send mail, post, etc.) under approval gate | `approval-demo` |
| **Ingestor** | pulls data from legacy services into your storage | `rss-ingestor`, `calendar-ingestor` |

**Approval workflow.** Actuators (any capsule with
`requires_user_approval: true` on a capability) pause and surface
a row in the Approvals pane the moment they try to execute. You
approve/deny per-action; the decision is in the audit log.

Detail: [`docs/build-your-first-capsule.md`](build-your-first-capsule.md),
[`docs/capsule-ingestor-primitives.md`](capsule-ingestor-primitives.md).

---

### 4. Inspect what happened

**Why.** Trust requires verifiability. Loamss's whole pitch is
"every read, every write, every grant is logged" — this is how
you check that.

**Surface 1: the Activity pane.** Dashboard → Activity. Real-time
stream of every audit entry with filters (type, actor, outcome,
time range). Click an entry to see its full data + context blob.

**Surface 2: the CLI.**

```bash
# Last 50 entries, human-readable
loamss audit tail

# Filter by type + actor + time
loamss audit log \
  --type memory.query \
  --actor-kind client \
  --since 24h

# Verify the hash chain is intact (no tampering)
loamss audit verify
# → ✓ Chain integrity verified (1247 entries)

# Export everything as JSONL for compliance / long-term storage
loamss audit export > my-loamss-audit-2026.jsonl
```

**What's in there.** Every gated operation: client pairings,
grant issuances, capsule installs, source syncs, memory writes,
model calls, OAuth flows, approval decisions. Plus the
`setup_token.issued` / `setup_token.consumed` lifecycle events
from the deploy.

The chain is verified with `BEGIN IMMEDIATE` on SQLite and
`pg_advisory_xact_lock` on Postgres — concurrent writers can't
break the linkage. Tested up to 30 concurrent goroutines + 36
separate OS processes (see `internal/database/postgres_integration_test.go`).

Detail: [`audit-spec.md`](../audit-spec.md).

---

### 5. Leave with your data

**Why.** The substrate thesis says you own your data. The
walkaway path is the proof.

**How.**

```bash
loamss export --out my-loamss-backup.tar.gz
```

Produces a complete archive of:

- Every storage blob (decrypted to plain files)
- The full memory database (entities, threads, embeddings)
- The audit log (every entry, JSONL)
- The config that produced the install
- All paired-client credentials (so you can revoke them at the
  destination)

You can:

- **Point another Loamss at it.** `loamss import my-loamss-backup.tar.gz`
  on a fresh runtime — your substrate moves with you.
- **Keep the archive and stop running Loamss.** Your data is
  yours; nothing is held hostage.
- **Audit the archive on a third party's tooling.** The audit
  log's hash chain validates standalone.

This is a tested invariant. The export round-trip is exercised in
`internal/cli/export_test.go`.

---

## Picking what to read next

The map below is what to read based on what you're trying to do
right now.

| Goal | Read |
|---|---|
| **Install Loamss on my laptop, get to a working dashboard** | [`getting-started.md`](getting-started.md) |
| **Deploy Loamss to Cloud Run / Fly / GKE** | [`deploying.md`](deploying.md) |
| **Pair Claude Desktop / Cursor / ChatGPT with my Loamss** | [`connect-your-app.md`](connect-your-app.md) |
| **Index local files / docs into my memory** | [`getting-started.md`](getting-started.md) §4 |
| **Connect Gmail** | [`setup-gmail.md`](setup-gmail.md) |
| **Build a capsule** | [`build-your-first-capsule.md`](build-your-first-capsule.md) → [`capsule-spec.md`](../capsule-spec.md) |
| **Build a source ingestor capsule** | [`capsule-ingestor-primitives.md`](capsule-ingestor-primitives.md) |
| **Build a native app on top of Loamss** (Path A) | [`native-apps.md`](../native-apps.md) → [`mcp-surface.md`](../mcp-surface.md) |
| **Add a storage / memory / model backend** | [`adapter-interface.md`](../adapter-interface.md) |
| **Understand the trust model** | [`permission-model.md`](../permission-model.md) + [`audit-spec.md`](../audit-spec.md) |

---

## What's coming next (so you can plan)

The substrate is stable; the things still moving are at the edges:

- **Native Loamss Mail app** (Path A reference) — under design;
  see [`native-apps.md`](../native-apps.md).
- **Capsule marketplace** — registry MVP + certification pipeline.
  Today capsules install from filesystem paths or git URLs.
- **Mobile / tablet client** — Path B Claude Desktop equivalent
  for iOS / Android. Today, mobile usage is via the dashboard's
  responsive web UI from a browser.
- **`loamss setup-token reset`** — convenience CLI that wraps the
  `DELETE FROM runtime_state` you currently run yourself
  (see [`deploying.md`](deploying.md)).
- **Auto-pair the console on init success** — eliminates the
  manual paired-client step after `/console/init` consumes the
  setup token on cloud deploys.

See [`ROADMAP.md`](../ROADMAP.md) for the canonical sequence.
