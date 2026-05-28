# Getting started

A walkthrough for installing Loamss on your laptop and pairing an
external agent against it. Plain command list — pick the install
path that fits your machine and follow the steps top to bottom.

End-to-end time: about 10 minutes if `brew install` is fast.

> **What Loamss is.** A substrate that your apps and AI tools write
> to and read from over MCP. The long-term shape is **native Loamss
> apps** (an email app, a notes app, a calendar app) that use your
> Loamss as their backing store — see [`native-apps.md`](../native-apps.md)
> for the architectural pattern. This getting-started walks the
> simpler entry path: install the substrate, pair an external AI agent,
> see the trust boundary work. The same primitives carry over to
> Path A native apps when you're ready to build one.

---

## What you'll need

- macOS (Apple Silicon or Intel) or Linux (arm64 or amd64)
- [Homebrew](https://brew.sh/) or the ability to download a tarball
- Optional but recommended: [Ollama](https://ollama.com/) for local embeddings
  and chat — Loamss works without it if you'd rather use a hosted
  model provider (OpenAI, Anthropic, Mistral)

You do **not** need:

- A Loamss account (there isn't one — it's a local daemon)
- A cloud subscription
- Docker

---

## 1. Install

### Homebrew (recommended)

```bash
brew tap loamss/loamss
brew install loamss
```

This puts `loamss` on your `PATH`. Same formula works on macOS and
Linux.

### Direct download

If you'd rather skip Homebrew:

```bash
# Replace linux-arm64 with your OS + arch
curl -L -O https://github.com/loamss/loamss/releases/latest/download/loamss-v0.1.5-linux-arm64.tar.gz
tar xzf loamss-v0.1.5-linux-arm64.tar.gz
sudo mv loamss-v0.1.5-linux-arm64/loamss /usr/local/bin/
```

Available targets: `darwin-arm64`, `darwin-amd64`, `linux-arm64`,
`linux-amd64`. The runtime is a single static binary with the
dashboard embedded — no further setup.

### Build from source

If you want the latest unreleased code:

```bash
git clone https://github.com/loamss/loamss
cd loamss/runtime
make build
sudo mv bin/loamss /usr/local/bin/
```

Requires Go 1.25+ and [Bun](https://bun.sh) (Bun only builds the
console; the runtime itself is pure Go).

### Verify

```bash
loamss version
# loamss v0.1.5
#   commit: ...
#   ...
```

---

## 2. (Optional) Install a local model

Loamss uses a model adapter for two things:

- **Embedding** — turning your notes / emails / docs into vectors for
  semantic search
- **Generation** — if you run capsules that summarize text or want to
  use `model.call` directly

You can configure any combination of providers. The fastest path with
no API keys is Ollama:

```bash
brew install ollama
brew services start ollama       # macOS
# Linux: see https://ollama.com/download

# Pull a small embedding model (~270 MB)
ollama pull nomic-embed-text

# Optional: pull a small chat model for generation (~1.3 GB)
ollama pull llama3.2:1b
```

If you'd rather use a hosted provider, set an env var the wizard will
detect:

```bash
export ANTHROPIC_API_KEY=...    # or OPENAI_API_KEY=...
```

---

## 3. First run

```bash
loamss start --open
```

The first run does two things:

1. Prints a banner with the local URL (`http://127.0.0.1:7777` by default)
2. Opens your browser at that URL — a short wizard walks you through
   choosing storage, memory, and model adapters

The defaults the wizard suggests:

| Choice    | Default                         | Notes                                            |
| --------- | ------------------------------- | ------------------------------------------------ |
| Storage   | `storage:fs-encrypted`          | AES-256-GCM on your local filesystem             |
| Memory    | `memory:sqlite`                 | Single-host vector store; backed by SQLite       |
| Models    | Ollama (if detected) or `none`  | Hosted providers visible if their env var is set |

These are reasonable for a laptop. If you want Postgres / pgvector,
S3, GCS, or a hosted vector store, the wizard exposes those options
too — see [`adapter-interface.md`](../adapter-interface.md) for the
full list.

When the wizard finishes you land on the **Dashboard**. The runtime
is now running in your terminal; leave it. Open a second terminal
for everything below.

---

## 3b. First run on a cloud host (Cloud Run, Fly, GKE)

If you're running Loamss on a public-internet URL instead of your
laptop, you need one extra step before opening the wizard: **the
setup token**.

The threat model: when the runtime binds to `0.0.0.0:$PORT` (the
default in the cloud profile), `/console/*` is reachable from
anywhere. The first person to hit the wizard could install
capsules, pair credentials, and take over the instance. The setup
token closes that hole — it's a one-time bearer token, generated at
startup, that you (the operator) prove possession of before the
wizard accepts your config.

### Where the gate turns on

| Scenario                                                              | Gate active? |
| --------------------------------------------------------------------- | ------------ |
| `loamss start` on your laptop (`127.0.0.1`)                           | no           |
| `LOAMSS_PROFILE=cloud loamss start`                                   | yes          |
| Running inside Cloud Run / Fly / GKE / Render (auto-detected `cloud`) | yes          |
| `LOAMSS_SETUP_TOKEN=<value> loamss start` anywhere                    | yes          |

When the gate is active, every `/console/*` request and the `/pair`
endpoint require `Authorization: Bearer <token>`. `/healthz` and
`/version` stay public so your load balancer's health checks
continue to work.

### Step 1 — find your setup token

When the runtime boots in cloud profile without `LOAMSS_SETUP_TOKEN`
set, it generates a fresh one and prints it once to standard
output. Search your logs for the banner:

```
  ↪  Setup token: 6f4c8e5a91...
     Provide it as Authorization: Bearer <token> on the first
     /console/init request. The token is single-use; subsequent
     access requires a paired-client credential.
```

Sample one-liners for the common platforms:

```bash
# Cloud Run
gcloud run services logs read <service-name> | grep "Setup token:" | head -1

# Fly.io
fly logs | grep "Setup token:" | head -1

# Kubernetes / GKE
kubectl logs <pod> | grep "Setup token:" | head -1
```

If you'd rather supply the token yourself (so it doesn't appear in
log aggregation), set `LOAMSS_SETUP_TOKEN` in your deploy
configuration. The runtime uses your value verbatim, never logs it,
and the gate behaves identically.

### Step 2 — open the wizard with the token

The fastest path is a `?setup=` URL parameter. Build it once and
click:

```
https://your-loamss.example.com/?setup=6f4c8e5a91...
```

The console reads the parameter on first paint, stashes the token in
your browser's `localStorage`, and strips the parameter from the
address bar (so the URL in your history is just
`https://your-loamss.example.com/`). From this point the wizard
behaves identically to the laptop flow.

If you've already opened the URL without the parameter — say you
typed it from memory — the Welcome screen shows a small
**"Cloud deploy? Paste your setup token."** link. Click it, paste,
proceed.

### Step 3 — complete the wizard

The wizard form is unchanged from the laptop flow. When you click
**Finish**, the console sends `Authorization: Bearer <token>` along
with the wizard's payload to `/console/init`. The runtime writes
your config, burns the token, and records two entries in the audit
log:

```bash
loamss audit log --type setup_token.issued --type setup_token.consumed
```

You should see exactly one of each. From now on the setup token is
no longer accepted — the runtime persists this fact to
`<data_dir>/.setup-consumed` so a restart or cold-start doesn't
re-open the gate.

### Step 4 — pair your first real client

The dashboard polls `/console/state`, which is still gated. Since
the setup token is gone, you need to pair a real client (Claude
Desktop, the CLI, a custom MCP tool) and use its bearer credential.

The simplest path: SSH or `kubectl exec` into the running instance
once and use the CLI from there.

```bash
loamss client pair --name "operator"
# → pairing code: ABCD-1234
loamss client pair complete ABCD-1234
# → bearer: a long opaque string — keep it safe
```

Paste that bearer into your dashboard's `localStorage` under the
key `loamss.client_bearer` (a follow-up release will add a paste
field on the dashboard itself; for v0.2 it's a manual step). Reload
the dashboard — it polls cleanly.

### Re-opening the wizard

If you need to re-run the wizard (config got into a bad state,
you're moving the runtime to a new database), delete the consumed
marker and restart:

```bash
rm <data_dir>/.setup-consumed
# restart the instance (Cloud Run: redeploy, Fly: `fly machine restart`)
```

The next start generates a fresh setup token. The previous token
stays invalid.

---

## 4. Get something into your Loamss

In the long-term shape, the way data gets into your Loamss is **an
app writes it there** — your email app writes your email, your notes
app writes your notes. That ecosystem is early. Two ways to bootstrap
something queryable today:

### Option A: pair an external agent that writes (recommended)

The [`demo-agent`](../sdk/typescript/examples/demo-agent/) example is
an external MCP client (Node + a local Ollama model). It can both
**read** memory.query results and, with `memory.write` granted,
**write** new entries. Use it to see the substrate work end-to-end
without depending on legacy data sources. Walk-through is in the
[demo-agent README](../sdk/typescript/examples/demo-agent/README.md).

### Option B: migrate legacy data via a transitional source connector

If you have years of notes or email already sitting in legacy
locations, source connectors mirror that data into your Loamss as a
one-time migration. **These connectors are transitional — they
exist for users whose data still lives in legacy SaaS. The long-term
shape is apps that write to Loamss in the first place.**

The fastest connector to try is the local files connector:

#### Via the dashboard

1. Click **Sources** → **+ Add source**
2. Pick `source:files`
3. Give it a name (e.g. `notes`)
4. Set `root` to a folder of your choice (`~/Documents/notes`,
   `~/Obsidian`, anything readable)
5. Set `namespace` to the same name (this is what `memory.query` uses
   to scope results)
6. Click **Save**, then **Sync now**

#### Via the CLI (equivalent)

```bash
loamss source add source:files --name notes \
  --config root=~/Documents/notes \
  --config namespace=notes
loamss source sync notes
```

You should see something like:

```
✓ Synced "notes": 42 added, 0 updated, 78,134 bytes, 0 errors (1.2s)
```

If you configured an embedding model, those entries are now
searchable. If you didn't, the entries still land in storage; they
just won't surface in `memory.query` until an embedding model is
wired and the source is re-synced.

For Gmail, see [`docs/setup-gmail.md`](setup-gmail.md). For Calendar,
RSS, and other transitional connectors, see the in-repo capsules
under [`sdk/typescript/examples/`](../sdk/typescript/examples/).

---

## 5. Ask it something

### Via the dashboard

Click **Sources** → your source → **Search**, type a query.

### Via the CLI

```bash
loamss memory query "what did Sarah say about the contract"
```

You'll get a ranked list of entries with similarity scores. The
lowest score is the closest match. If the same query returns
identical results regardless of how you phrase it, you're searching
without embeddings — install an embedding model (Section 2) and
re-sync.

### Via an external MCP client (Claude Desktop, your own script, ...)

Loamss exposes the same MCP surface to external clients as it does
to capsules. The flow:

1. Generate a pairing code: `loamss client pair --name "<app name>"`
2. The external app redeems the code via POST `/pair` and receives a
   bearer token
3. You issue grants for the capabilities the app needs:
   ```bash
   loamss grant create \
     --principal-kind client \
     --principal-id cli-... \
     --capability memory.read \
     --scope-json '{}' \
     --rationale "let this tool read memory"
   ```
4. The app speaks MCP-over-HTTP+SSE against your runtime's `/mcp`
   endpoint

A complete worked example lives at
[`sdk/typescript/examples/demo-agent/`](../sdk/typescript/examples/demo-agent/) —
a small Node script that uses a local Ollama model and pairs with
your runtime over MCP. Its README walks the whole pair + grant +
query flow.

For a longer write-up of integrating an existing app, see
[`docs/connect-your-app.md`](connect-your-app.md).

---

## 6. Where things live

```
~/.loamss/
├── config.yaml         the wizard's output; safe to hand-edit
└── data/
    ├── runtime.db      permissions, grants, sources, capsules
    ├── audit.db        hash-chained audit log
    ├── memory.db       vectors + metadata (if memory:sqlite)
    └── storage/        the user's actual files (if storage:fs-encrypted)
```

Override these paths via the config file or `LOAMSS_DATA_DIR` env var.

---

## 7. Common issues

### `loamss start` says "address already in use"

A previous run didn't shut down cleanly. Either change the port:

```yaml
# in ~/.loamss/config.yaml
runtime:
  listen_addr: 127.0.0.1:7888
```

Or kill the orphaned process: `pkill -f "loamss start"`.

### Sync runs but `memory.query` returns nothing

Two likely causes:

1. **No embedding model configured.** Check `loamss doctor` — if it
   reports "no embedding-capable model adapter", install Ollama with
   `nomic-embed-text` (or wire OpenAI / Anthropic) and re-sync.
2. **The source synced before the model was wired.** Remove + re-add
   the source so it re-ingests with embeddings:
   ```bash
   loamss source remove notes
   loamss source add source:files --name notes \
     --config root=~/Documents/notes --config namespace=notes
   loamss source sync notes
   ```

### `loamss source sync` fails with "credentials not set"

The source uses OAuth (Gmail, Calendar, …). Run
`loamss source authenticate <name>` to walk through the auth flow.

### I want to start over

```bash
pkill -f "loamss start"
rm -rf ~/.loamss        # wipes everything — including ingested data
loamss start
```

`loamss export` makes a portable archive of your data before you
wipe, if you want to keep it.

---

## What's next

- **Build a native (Path A) app.** This is the long-term shape Loamss
  is designed for — an app where your Loamss IS the backing store.
  Start with [`native-apps.md`](../native-apps.md) for the pattern
  and worked examples; [`mcp-surface.md`](../mcp-surface.md) is the
  wire-level reference.
- **Integrate an existing app (Path B).** Add MCP client support to
  an app that already has its own database. See
  [`docs/connect-your-app.md`](connect-your-app.md).
- **Write a capsule.** Capsules sit inside the substrate — organizers
  that build memory, exposers that publish data, actuators that take
  action. See [`docs/build-your-first-capsule.md`](build-your-first-capsule.md).
- **Migrate legacy data.** Transitional source connectors for Gmail,
  Calendar, RSS, files — see [`sources.md`](../sources.md). Useful
  one-time; not the steady-state pattern.
- **Inspect the audit log.** `loamss audit tail` shows the last N
  events with their hash chain. Every grant check (allow or deny) is
  in there.

---

## Where to ask for help

- Bugs / setup snags: [open an issue](https://github.com/loamss/loamss/issues)
- Design questions: [discussions](https://github.com/loamss/loamss/discussions)
- Security: see [`SECURITY.md`](../SECURITY.md)

This is early software. The runtime, dashboard, CLI, SDK, and
distribution pipeline are stable enough to use day-to-day; the
capsule marketplace, mobile companion, federation, and hosted
offering are still ahead. The roadmap at [`ROADMAP.md`](../ROADMAP.md)
shows what's shipped and what isn't.
