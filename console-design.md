# Console Design Exploration v0.1 (draft)

This document explores the design of the **Loamss console** — the user-facing UI that sits on top of the CLI surface. It is not a spec yet. The goal is to surface the design decisions worth making before any code lands, with enough concrete material that we (or anyone reading) can argue about them.

> **Status: design exploration.** No code in this commit. Once we agree on the IA + first-run flow + tech architecture, the implementation starts.

## What the console is for

The CLI works. Every operation the runtime supports — pairing, granting, syncing, auditing, exporting — has a `loamss` command. But the CLI is a power-user surface. Most users will never type `loamss grant create --principal-kind client --principal-id cli-01H... --capability memory.read --scope-json '{...}'`.

The console exists to make the same operations **seamless** for a non-CLI user. The bar:

> **A first-time user can get a working Loamss with one connected source, one paired app, and a sensible grant scope in under five minutes — without reading a doc, without opening a terminal, and without typing any command they don't recognize.**

If we hit that bar, configuration is solved.

## Design principles

These are the values that pick between options when there's a trade-off:

1. **Defaults work.** A user who never visits Settings should still have a runtime that's correctly configured. Every adapter, scope, and grant has a sane default.
2. **No jargon in primary text.** "Adapter", "ULID", "capability", "principal" appear in advanced views. The primary nouns are "Source", "App", "Memory", "Activity".
3. **State is visible.** The user always knows what's running, what's syncing, what's been granted, and to whom. The dashboard makes the runtime legible at a glance.
4. **Approvals are obvious + cheap.** Pending approvals show as a badge with a count. A consequential action approval is one tap, not a form.
5. **Undo is one click.** Revoke a client, revoke a grant, remove a source, delete a capsule — each has a clear button. Confirm dialogs are reserved for actions that destroy data.
6. **The audit log is a feature, not a debug artifact.** It's surfaced as "Activity" on the dashboard, filterable from there, and every action in the console links to its corresponding audit entry.

## Information architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Loamss · [user@host]                                  ● healthy  │  ← status bar
├──────────────┬──────────────────────────────────────────────────────┤
│ Dashboard    │                                                      │
│ Sources      │                                                      │
│ Apps         │            (page-specific content)                   │
│ Capsules     │                                                      │
│ Memory       │                                                      │
│ Activity     │                                                      │
│ Settings     │                                                      │
└──────────────┴──────────────────────────────────────────────────────┘
```

Seven top-level destinations. Each one corresponds to a concrete runtime concept:

| Tab | Backed by | Primary action |
| --- | --- | --- |
| Dashboard | Composite — sources, clients, recent activity, alerts | "What's happening right now?" |
| Sources | `loamss source` | "Connect a source / sync now / remove" |
| Apps | `loamss client` + `loamss grant` | "Pair an app / show what it can do / revoke" |
| Capsules | `loamss capsule` | "Install / configure / uninstall a capsule" |
| Memory | `loamss memory entities/threads` | "Browse derived entities + threads" |
| Activity | `loamss audit log/tail` | "What happened, when, by whom" |
| Settings | `loamss config` + adapter wiring | "Adapters, models, backup, advanced" |

This is intentionally narrow — seven tabs, not twelve. Anything that doesn't fit cleanly into one of these lives inside `Settings → Advanced`.

## First-run wizard

The most important screen. If we get this right, the rest is recoverable. If we get this wrong, the user closes the tab.

### Step 0 — Welcome

```
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│                       Welcome to Loamss                             │
│                                                                     │
│         Your personal data infrastructure. Your data, your          │
│         storage, your audit log. We're going to get you set         │
│         up in about three minutes.                                  │
│                                                                     │
│         ┌───────────────────────────────────────────────────┐       │
│         │  Let's go                                         │       │
│         └───────────────────────────────────────────────────┘       │
│                                                                     │
│         [Already have a config file? Import it instead.]            │
└─────────────────────────────────────────────────────────────────────┘
```

No choices. Just "go" or "I'm already advanced." The advanced path drops directly into the config-file editor (the same view that lives in `Settings → Advanced → Edit raw config`).

### Step 1 — Storage

```
Where should Loamss keep your data?

  ●  ┌────────────────────────────────────────────────────┐
     │  Encrypted local folder                            │ ← default; pre-selected
     │  ~/.loamss/storage   (we'll create this)           │
     └────────────────────────────────────────────────────┘

  ○  Use cloud storage (S3, B2, R2)            [Coming soon]

  ○  Custom location                                      ▾

                                              [ Continue ]
```

The default is pre-selected, the second-most-used path is one click away, and the advanced path is collapsed. The user can finish this step without thinking by hitting Continue.

**What "Custom location" expands into:**

```
  ●  Custom location
     Path:    [/Users/me/Documents/MyLoamss            ]
     Encrypt: [✓] (recommended — AES-256-GCM)

     ⓘ If you turn off encryption, the data is stored as
       plain files. Other users on this machine could read
       them. Audit log entries warn about this; you can
       always turn encryption on later (we'll migrate).
```

Inline warnings, not buried in tooltips. The audit log connection makes the "we'll tell you when this matters" promise explicit.

### Step 2 — Memory

```
How should Loamss organize what it knows about you?

  ●  Local SQLite (fast, no extra setup)        ← default
  ○  Postgres + pgvector                        [Coming soon]
  ○  Chroma / Qdrant                            [Coming soon]

                                              [ Continue ]
```

Single default in v0.1. The list of options exists so the user knows there ARE options.

### Step 3 — Models

```
Want Loamss to use AI to organize what it sees?

  Loamss can use a model to embed your data for fast search,
  summarize threads, and resolve entities across sources.
  This is optional — you can skip and add a model later.

  ○  Skip for now (ingestion + browsing works, search is exact-match)

  ○  Use Anthropic Claude
     API key:  [sk-ant-···                                          ]
     We'll store this in your OS keychain. Get a key →

  ○  Use a local model via Ollama
     Detected at  http://localhost:11434  ✓
     Default model: llama3.2

                                              [ Continue ]
```

This is the screen most users will care about. Two real choices (Claude or Ollama), one "skip" that's a legitimate option. The Anthropic key field shows a "get a key →" inline link to the right URL. Ollama auto-detects.

### Step 4 — Connect something (optional)

```
Want to connect a source right now?

  This is the first place Loamss starts knowing things. You
  can skip and come back to it later.

  ○  Gmail               (the first reference source — used to test
                          OAuth + sync flow on real data)
  ○  More sources coming soon: Calendar, Drive, Slack, Files…

  ●  Skip — I'll connect sources later

                                              [ Finish setup ]
```

The optional "connect a source now" step exists so a new user can land on a dashboard that has something in it. But it's optional — `Finish setup` works on its own.

If the user picks Gmail, they hop to the Gmail connection sub-flow (see **Sources → Add** below) and come back to a "done!" screen.

### Done — Dashboard

```
┌─────────────────────────────────────────────────────────────────────┐
│  ✓ You're set up                                                    │
│                                                                     │
│  Loamss is running. Here's what to do next:                         │
│                                                                     │
│   1. Connect a source         (Sources →)                           │
│   2. Pair an app              (Apps →)                              │
│   3. Install a capsule        (Capsules →)                          │
│                                                                     │
│  Or just leave it. The runtime stays out of your way.               │
└─────────────────────────────────────────────────────────────────────┘
```

The "you're done" state is itself a small directory of next actions. No mandatory follow-up; nothing scolds the user for not having connected anything.

## Ongoing flows

### Dashboard

The home screen after first-run.

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Dashboard                                                        │
│                                                                     │
│  ┌──── Sources ──────────────┐  ┌──── Apps ────────────────────┐    │
│  │                           │  │                              │    │
│  │  gmail-personal     ●     │  │  ChatGPT laptop      ●       │    │
│  │     last sync 2m ago      │  │     last seen 3h ago         │    │
│  │     12,408 entries        │  │     2 grants                 │    │
│  │                           │  │                              │    │
│  │  + Connect source         │  │  + Pair app                  │    │
│  └───────────────────────────┘  └──────────────────────────────┘    │
│                                                                     │
│  ┌──── Activity (last hour) ────────────────────────────────────┐   │
│  │                                                              │   │
│  │  2:14pm  ChatGPT laptop  memory.query  ok    Project Alpha?  │   │
│  │  2:12pm  gmail-personal  sync.completed ok   +14 messages    │   │
│  │  1:55pm  ChatGPT laptop  memory.query  ok    Sarah notes     │   │
│  │  1:30pm  Daily Briefing  memory.query  ok    today's tasks   │   │
│  │                                                              │   │
│  │  → View all activity                                         │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌──── Pending approvals ───────────────────────────────────────┐   │
│  │                                                              │   │
│  │  (nothing pending)                                           │   │
│  │                                                              │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

What this conveys:

- Sources + Apps panels mirror each other — they're the two "things that talk to Loamss" surfaces
- Recent activity is right there, not buried in a tab
- Pending approvals always visible (even when empty, so the user expects to see it)
- The dashboard is the audit log made human

### Sources → Add

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Sources › Add                                                    │
│                                                                     │
│   Pick a source to connect:                                         │
│                                                                     │
│    ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐ │
│    │     Gmail        │  │   Calendar       │  │     Drive        │ │
│    │                  │  │                  │  │                  │ │
│    │   [ Connect ]    │  │ [Coming soon]    │  │ [Coming soon]    │ │
│    └──────────────────┘  └──────────────────┘  └──────────────────┘ │
│                                                                     │
│    ┌──────────────────┐  ┌──────────────────┐                       │
│    │     Slack        │  │     Notion       │                       │
│    │ [Coming soon]    │  │ [Coming soon]    │                       │
│    └──────────────────┘  └──────────────────┘                       │
│                                                                     │
│   Don't see what you want? Connectors are extensions —              │
│   anyone can write one. See the [Source SPI →]                      │
└─────────────────────────────────────────────────────────────────────┘
```

Picking **Gmail** drops into the OAuth-client-onboarding flow (the contents of `docs/setup-gmail.md` made interactive — a click-through wizard instead of a doc). Each step:

1. **Name this connection** → `gmail-personal` (default; editable)
2. **Sign in with Google** → opens the browser, captures the loopback, persists tokens
3. **What should we sync?** → all mail / labeled folders / a search query (defaults: all mail, last 30 days)
4. **Sync now or schedule?** → sync now / hourly / daily / manual

This is the cardinal test of "seamless." If a user can complete those four steps without confusion, we've cleared the bar.

### Apps → Pair

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Apps › Pair a new app                                            │
│                                                                     │
│   Show this code to the app:                                        │
│                                                                     │
│             ┌─────────────────────┐                                 │
│             │                     │                                 │
│             │   [ QR CODE HERE ]  │                                 │
│             │                     │                                 │
│             │      5QUK-5EPE      │                                 │
│             └─────────────────────┘                                 │
│                                                                     │
│   Or paste this URL into the app's "Connect to Loamss" field:       │
│                                                                     │
│             http://127.0.0.1:7777/pair?code=5QUK-5EPE               │
│                                                                     │
│   Expires in 9:53.   [ Generate new code ]                          │
│                                                                     │
│   ⓘ Once the app connects, you'll see a permission slip here        │
│     showing exactly what it's asking for. You can narrow            │
│     what it gets before approving.                                  │
└─────────────────────────────────────────────────────────────────────┘
```

When the app redeems the code, the screen transitions to a **permission slip** view:

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Apps › ChatGPT laptop wants permission                           │
│                                                                     │
│  An app called "ChatGPT laptop" has just paired with your Loamss.   │
│  It's asking for the following permissions:                         │
│                                                                     │
│  ┌──── memory.read ─────────────────────────────────────────────┐   │
│  │ Read entries from your memory.                               │   │
│  │                                                              │   │
│  │  Namespaces:  [ ✓ ] gmail-personal                           │   │
│  │               [   ] gmail-work (you don't have this)         │   │
│  │  Excluded:    [ ✓ ] health                                   │   │
│  │  Expires:     [ never ▾ ]                                    │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌──── memory.query ────────────────────────────────────────────┐   │
│  │ Semantic search over your memory.                            │   │
│  │  (Same scope as memory.read above)                           │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│            [ Approve all ]   [ Approve narrower ]   [ Deny ]        │
└─────────────────────────────────────────────────────────────────────┘
```

The **scope is narrowable inline**. The user doesn't have to know the canonical capability surface — the slip shows the same words the capability registry uses, but with friendly framing ("Read entries from your memory" vs `memory.read`).

### Activity (audit)

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Activity                                                         │
│                                                                     │
│   Filter: [ all events ▾ ]  [ all clients ▾ ]  [ last 24h ▾ ]       │
│   Search: [                                                       ] │
│                                                                     │
│  ─── Today ──────────────────────────────────────────────────────   │
│                                                                     │
│  2:14pm  ChatGPT laptop                                  memory.    │
│          "memory.query"   ok                            query       │
│          query: "Project Alpha"                                     │
│          → 3 results, 47 ms                                         │
│                                                                     │
│  2:12pm  gmail-personal                                  source.    │
│          "source.sync.completed"   ok                   sync        │
│          14 records added, 482 KB                                   │
│                                                                     │
│  1:30pm  Daily Briefing (capsule)                       memory.     │
│          "memory.query"   ok                            query       │
│          via capsule callback                                       │
│          → 21 results, 31 ms                                        │
│                                                                     │
│  ─── Yesterday ──────────────────────────────────────────────────   │
│                                                                     │
│  ...                                                                │
└─────────────────────────────────────────────────────────────────────┘
```

Filters use the same predicates the `loamss audit log` CLI uses (actor, capability, outcome, time). Each row expands to show the full audit entry (JSON view + hash-chain verification status).

## Technical architecture

Two questions worth deciding before code:

### Question 1 — Where does the console run?

**Option A: Embedded in the runtime binary.**

```
loamss start
  → HTTP listener serves both:
        /mcp           (MCP surface, bearer auth)
        /console/*     (static Next.js export, plus the
                        small API the console needs)
  → User opens http://127.0.0.1:7777/console in a browser
```

Pros:
- Single process, single port, single thing to install
- No "did you start the console?" support questions
- Static Next.js export embedded via Go's `embed.FS`

Cons:
- Couples the console's release cadence to the runtime binary
- Console size adds to binary size (~1–2 MB after gzip)

**Option B: Separate process the user runs alongside.**

```
loamss start                  (the daemon)
loamss-console (or `bun ...`)  (a separate Node/Bun process)
```

Pros:
- Console can iterate independently
- Console can use Next.js's server-side features (SSR, server actions) — not just static export
- Easier to develop without rebuilding the Go binary

Cons:
- Two processes to manage
- Two ports to remember
- The console needs its own bearer-token pairing flow

### Recommendation

**Option A for v1**, possibly evolving to A+B for power users later. Reasons:

1. The seamlessness goal demands one-process simplicity. "Run `loamss start`, open the URL" is a tractable user instruction. "Run two things and configure them to point at each other" is not.
2. Next.js's static export covers everything the console needs. The console is a thin client over the existing MCP surface — it doesn't need SSR or server actions.
3. Embedding gives us free authentication: the console can mint a self-pair on first open (browser hits a local-only endpoint that returns a session token; same machinery as the existing pair flow, just without the QR step).

### Question 2 — How does the console authenticate itself?

The runtime requires bearer tokens for every MCP call. The console is just another MCP client from the runtime's POV — so it needs a token. Options:

- **A. The console is a pre-paired client baked in at `loamss init`.** First run creates a `console-local` client with a fixed token; subsequent runs reuse it. Audit log shows all console activity attributed to this client.

- **B. The console pairs itself on first open via a localhost-only shortcut.** Visiting `http://127.0.0.1:7777/console` for the first time triggers an automatic pairing (the runtime trusts the localhost origin); the token is stored in the browser via `httpOnly` cookie.

- **C. The console uses a separate, narrower auth method** (e.g., HMAC of a shared secret in `~/.loamss/console.key`).

Path **B** is the smoothest UX — the user just opens the URL. Path **A** is simpler but means a token sits in the runtime config indefinitely. Path **C** is more secure but adds a primitive.

**Recommendation: B**, with the localhost-binding enforced at the HTTP listener level (the runtime by default binds to `127.0.0.1` already; the console pairing endpoint is additionally restricted to `Origin: http://127.0.0.1:*` requests).

## Tech stack

Aligning with the project conventions:

- **Framework**: Next.js (App Router) + React + TypeScript + Tailwind
- **Package manager**: Bun (per global CLAUDE.md)
- **State**: Server Components for static-shape pages; client-side state via Zustand or just React state — no Redux
- **Talking to the runtime**: `@loamss/sdk` (the MCP client library we already shipped); no bespoke HTTP code
- **Build output**: `next export` → static files embedded into the Go binary via `embed.FS`
- **Dev mode**: `bun dev` on a separate port, with the runtime daemon serving only the MCP surface

## Open design questions

These are the calls worth making before the first code commit. None of them block the IA above; each affects implementation detail.

1. **Should the console be the default landing page when the user runs `loamss start`?** Or should the user have to type `loamss console` separately? (I lean: print the URL on `loamss start` startup, don't auto-open.)
2. **How do we handle multi-runtime setups?** (One user, two instances — laptop + home server.) Switch with a dropdown? Profiles?
3. **Mobile companion app** (called out in ROADMAP Phase 3) — is it a separate Next.js app reusing the same components, a separate React Native app, or a stripped-down web app?
4. **Dark mode?** Probably default to system preference; offer a toggle in Settings. (No major design implications — just a Tailwind variant.)
5. **Telemetry?** Per the principles in `CLAUDE.md`, anything that phones home goes through the same consent system as everything else. The console can compute usage stats locally; nothing leaves the machine without an explicit grant.

## Out of scope for v1 console

- Visual capsule builder (drag-and-drop tools / resources / capabilities). Capsules are code; the console manages them but doesn't write them.
- Memory entity-graph visualization. The data shape supports it, but a force-directed graph view is a Phase 2 polish item.
- Cross-runtime federation UI. ROADMAP Phase 3.
- Marketplace / capsule registry UI. ROADMAP Phase 2/3.

## Next steps (assuming this design lands)

1. Pick the open-design-question defaults
2. Scaffold `console/` as a Next.js app per the existing repo layout
3. Build the first-run wizard against the live runtime
4. Build the Sources + Apps tabs
5. Build the dashboard
6. Build the Activity tab last (it's mostly a read-only view over data we already expose)

## Related

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — runtime overview the console sits on top of
- [`cli.md`](cli.md) — every console action maps to an existing CLI command
- [`mcp-surface.md`](mcp-surface.md) — the wire protocol the console will use via `@loamss/sdk`
- [`permission-model.md`](permission-model.md) — what a permission slip is rendering
- [`sources.md`](sources.md) / [`memory-layer.md`](memory-layer.md) — the data shapes the console renders
- [`docs/connect-your-app.md`](docs/connect-your-app.md) — the third-party-app counterpart to the console's "Pair an app" flow
