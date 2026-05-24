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

## Detailed screens — every tab

The dashboard and three flows above set the visual language. Below is a sketch for every other top-level view, plus the high-leverage sub-flows (connect-to-Gmail wizard, consequential-action approval).

### Sources tab (list view)

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Sources                                          [+ Add source]  │
│                                                                     │
│   2 sources connected · last sync 2 minutes ago                     │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  ●  gmail-personal                            source:gmail  │    │
│  │     12,408 entries · synced 2m ago · sync ok               │    │
│  │                                                             │    │
│  │     Next sync: in 58 min   [ Sync now ]   [ Manage ▾ ]      │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  ●  calendar-personal                       source:calendar │    │
│  │     482 events · synced 4m ago · sync ok                    │    │
│  │                                                             │    │
│  │     Next sync: in 56 min   [ Sync now ]   [ Manage ▾ ]      │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                                                     │
│  + Add another source                                               │
└─────────────────────────────────────────────────────────────────────┘
```

- The status dot is green for `sync ok`, yellow for `auth required`, red for `error`.
- "Manage ▾" is a compact menu — Pause, Edit config, Re-authenticate, Remove.
- "Add another source" at the bottom mirrors the top-right button — discoverable from either direction.

### Sources → Add → Gmail wizard

The point-of-truth for "seamless". Four steps, each one click of forward motion away from the user. The actual OAuth complexity (Google Cloud project creation, OAuth consent screen, test users) gets boxed into Step 1 with the option to skip if the user already has credentials.

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Sources › Add › Gmail                          Step 1 of 4 · ●○○○│
│                                                                     │
│  We need an OAuth client for your Google account                    │
│                                                                     │
│  Loamss talks to Gmail through Google's OAuth. You need an OAuth    │
│  client (it identifies "Loamss running on your machine" to Google). │
│                                                                     │
│  This is a one-time setup. We'll guide you through it.              │
│                                                                     │
│   ┌───────────────────────────────────────────────────────────┐     │
│   │  ○  Walk me through it (10 min, opens Google Cloud)       │     │
│   │                                                            │     │
│   │  ●  I already have credentials                             │     │
│   │      Client ID:     [ ...apps.googleusercontent.com    ]   │     │
│   │      Client secret: [ GOCSPX-...                       ]   │     │
│   │      ⓘ Stored in your OS keychain. Get yours at           │     │
│   │        console.cloud.google.com →                          │     │
│   └───────────────────────────────────────────────────────────┘     │
│                                                                     │
│                                              [ Back ]  [ Continue ] │
└─────────────────────────────────────────────────────────────────────┘
```

"Walk me through it" opens a side panel with `docs/setup-gmail.md` rendered inline (numbered, with deep-links to the right Google Cloud Console pages). The user clicks through it without leaving the console.

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Sources › Add › Gmail                          Step 2 of 4 · ●●○○│
│                                                                     │
│  Sign in with Google                                                │
│                                                                     │
│   ┌───────────────────────────────────────────────────────────┐     │
│   │   [ G ]  Continue with Google                              │     │
│   └───────────────────────────────────────────────────────────┘     │
│                                                                     │
│   We'll open Google's consent screen in a new tab. After you        │
│   approve, this window will continue automatically — you don't      │
│   need to copy anything.                                            │
│                                                                     │
│   ⓘ The first time you see "App isn't verified" — that's expected.  │
│     Your OAuth client is in "Testing" mode (that's the right        │
│     setting for personal use). Click Advanced → Go to Loamss.       │
└─────────────────────────────────────────────────────────────────────┘
```

The "App isn't verified" inline explanation is critical — it's the most-confusing moment in the existing CLI flow, and a sentence here removes a whole class of support questions.

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Sources › Add › Gmail                          Step 3 of 4 · ●●●○│
│                                                                     │
│  What should we sync from Gmail?                                    │
│                                                                     │
│  Name this connection                                               │
│     [ gmail-personal                                              ] │
│     (Lower-case, hyphens. You can have multiple Gmail connections.) │
│                                                                     │
│  Time range                                                         │
│     ○  Everything                                                   │
│     ●  Last 30 days   (recommended for first sync)                  │
│     ○  Last 90 days                                                 │
│     ○  Custom              From: [ 2026-01-01 ]                     │
│                                                                     │
│  Filter (optional)                                                  │
│     [                                                             ] │
│     Use Gmail's search syntax. Examples: `from:newsletters@x.com`,  │
│     `label:important`, `after:2026/01/01`. Leave empty for all mail.│
│                                                                     │
│  Cap on first sync                                                  │
│     [ 1000 ▾ ]   (you can raise this in Settings later)             │
│                                                                     │
│                                              [ Back ]  [ Continue ] │
└─────────────────────────────────────────────────────────────────────┘
```

Defaults are conservative ("Last 30 days", "1000 messages") because a maximum-pull first sync is the kind of thing that surprises a user when they see "5 GB downloaded" three hours in. The "you can raise this in Settings later" reassurance is in-line.

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Sources › Add › Gmail                          Step 4 of 4 · ●●●●│
│                                                                     │
│  When should Loamss sync?                                           │
│                                                                     │
│   ●  Every hour                                                     │
│   ○  Every 4 hours                                                  │
│   ○  Daily                                                          │
│   ○  Manual only                                                    │
│                                                                     │
│   Also: Sync now after I'm done? [ ✓ ]                              │
│                                                                     │
│                                              [ Back ]  [ Finish ]   │
└─────────────────────────────────────────────────────────────────────┘
```

Done state:

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Sources › gmail-personal                                         │
│                                                                     │
│   ✓ Connected to Gmail                                              │
│                                                                     │
│   Sync starting now…  (47 of 1000 ▓▓░░░░░░░░░░░░░░░░░░░░ 4%)        │
│   ETA ~2m remaining                                                 │
│                                                                     │
│   [ View activity ]    [ Pause sync ]    [ Open dashboard ]         │
└─────────────────────────────────────────────────────────────────────┘
```

Live progress is the difference between "did anything happen?" and "I can see it working." The progress bar should reflect actual records-ingested, not a fake spinner.

### Sources → Detail view

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Sources › gmail-personal                                         │
│                                                                     │
│   ●  gmail-personal      source:gmail                               │
│                                                                     │
│   ┌──── Sync status ─────────────────────────────────────────────┐  │
│   │  Last sync:  2 minutes ago · ok · +14 messages, 482 KB       │  │
│   │  Next sync:  in 58 minutes (hourly)                          │  │
│   │  Total:      12,408 entries · 4.2 GB                         │  │
│   │                                                              │  │
│   │  [ Sync now ]   [ Pause ]   [ Change schedule ▾ ]            │  │
│   └──────────────────────────────────────────────────────────────┘  │
│                                                                     │
│   ┌──── Recent sync history ────────────────────────────────────┐   │
│   │  2:14pm   ok   +14 messages, 482 KB     12s                  │   │
│   │  1:14pm   ok   +27 messages, 1.1 MB     18s                  │   │
│   │  12:14pm  ok   +6 messages, 220 KB      9s                   │   │
│   │  11:14am  ok   +33 messages, 2.4 MB     21s                  │   │
│   │  → View all sync history                                     │   │
│   └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│   ┌──── Configuration ──────────────────────────────────────────┐   │
│   │  Filter:     (all mail)                            [ Edit ] │   │
│   │  Cap:        no limit on incremental syncs         [ Edit ] │   │
│   │  Schedule:   hourly                                [ Edit ] │   │
│   │  OAuth:      sarah@example.com    [ Re-authenticate ]       │   │
│   └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
│   ┌──── Danger zone ────────────────────────────────────────────┐   │
│   │  Remove this source                                          │   │
│   │  Stops syncing and deletes the stored credentials.           │   │
│   │  Already-ingested entries stay in your memory until you      │   │
│   │  delete them via Memory → Manage.                            │   │
│   │  [ Remove gmail-personal ]                                   │   │
│   └─────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

Three regions: live status (top), history (middle), config (bottom). The "Danger zone" pattern (separated, labeled, two-step confirm) reserves visual weight for actions the user is going to do once and never undo.

### Apps tab (list view)

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Apps                                              [+ Pair app]   │
│                                                                     │
│   2 apps connected                                                  │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  ●  ChatGPT laptop                              active      │    │
│  │     2 grants · last seen 3h ago · 47 calls today             │    │
│  │                                                             │    │
│  │     memory.read   ← gmail-personal (excludes health)         │    │
│  │     memory.query  ← same scope                               │    │
│  │                                                             │    │
│  │     [ Adjust grants ]   [ View activity ]   [ Revoke ]       │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  ●  Inbox app (smoke-test)                       active      │    │
│  │     1 grant · last seen 2m ago · 3 calls today               │    │
│  │                                                             │    │
│  │     memory.read   ← gmail-personal                           │    │
│  │                                                             │    │
│  │     [ Adjust grants ]   [ View activity ]   [ Revoke ]       │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                                                     │
│  + Pair another app                                                 │
└─────────────────────────────────────────────────────────────────────┘
```

The two grants for ChatGPT are summarized inline (capability → scope) so the user doesn't have to drill into a detail view to see "what does this app know about me?"

### Apps → Adjust grants

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Apps › ChatGPT laptop › Adjust grants                            │
│                                                                     │
│  ┌──── memory.read ─────────────────────────────────────────────┐   │
│  │ Read entries from your memory.                               │   │
│  │                                                              │   │
│  │  Namespaces:  [ ✓ ] gmail-personal                           │   │
│  │               [   ] calendar-personal                        │   │
│  │  Excluded:    [ ✓ ] health                                   │   │
│  │  Expires:     [ never ▾ ]                                    │   │
│  │                                                              │   │
│  │  [ Save changes ]   [ Revoke this grant ]                    │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌──── memory.query ────────────────────────────────────────────┐   │
│  │ Semantic search.                                             │   │
│  │ (Same scope as memory.read above)                            │   │
│  │  [ Save changes ]   [ Revoke this grant ]                    │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌──── Available but not granted ───────────────────────────────┐   │
│  │  memory.write          [ Grant ]                             │   │
│  │  files.read            [ Grant ]                             │   │
│  │  ... (others by capability registry)                         │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

The "Available but not granted" section makes the universe of capabilities discoverable without requiring the user to consult a separate spec.

### Capsules tab

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Capsules                                         [+ Install]     │
│                                                                     │
│   1 capsule installed                                               │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  ●  Daily Briefing                              v0.2.0       │    │
│  │     com.loamss.example.daily-brief                          │    │
│  │     Runs hourly · last run 47 minutes ago                    │    │
│  │                                                             │    │
│  │     Tools:     2 (greet, daily_brief)                        │    │
│  │     Grants:    memory.read (gmail-personal + calendar)       │    │
│  │     Model:     anthropic / claude-sonnet-4-5                 │    │
│  │                                                             │    │
│  │     [ View output ]  [ Configure ]  [ Pause ]  [ Uninstall ] │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                                                     │
│  + Install another capsule                                          │
└─────────────────────────────────────────────────────────────────────┘
```

### Capsules → Install (from a path)

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Capsules › Install                                               │
│                                                                     │
│   Capsules are sandboxed extensions that run inside your runtime.   │
│   They're code. You install them from a folder you trust.           │
│                                                                     │
│   ┌──── From local folder ───────────────────────────────────┐      │
│   │   [ /Users/me/dev/daily-brief                       ▾ ]   │      │
│   │   [ Browse… ]                                             │      │
│   │                                                           │      │
│   │   We'll validate the manifest before installing.          │      │
│   └───────────────────────────────────────────────────────────┘     │
│                                                                     │
│   ┌──── From the registry ───────────────────────────────────┐      │
│   │   [Coming soon]                                           │      │
│   └───────────────────────────────────────────────────────────┘     │
│                                                                     │
│                                              [ Cancel ]   [ Next ]  │
└─────────────────────────────────────────────────────────────────────┘
```

After validating the manifest:

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Capsules › Install › Review                                      │
│                                                                     │
│   You're installing Daily Briefing v0.2.0                           │
│                                                                     │
│   ┌──── It will get these capabilities ─────────────────────────┐   │
│   │                                                              │   │
│   │  ● memory.read                                               │   │
│   │    "Needed to surface recent entries to the caller."         │   │
│   │     Scope:  [ ✓ ] gmail-personal                             │   │
│   │             [ ✓ ] calendar-personal                          │   │
│   │             [ ✓ ] exclude health                             │   │
│   │                                                              │   │
│   │  ● model.call (anthropic)                                    │   │
│   │    "Needed to generate the daily briefing text."             │   │
│   │     Cost ceiling: $0.50/day  [ Edit ]                        │   │
│   │                                                              │   │
│   └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│   ┌──── It will provide these tools ────────────────────────────┐   │
│   │  • greet — Say hello to someone.                             │   │
│   │  • daily_brief — Generate a brief for today.                 │   │
│   └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│                                              [ Cancel ]  [ Install ]│
└─────────────────────────────────────────────────────────────────────┘
```

The install confirmation IS the permission slip for a capsule. Same UI primitive used for app pairing.

### Memory tab (entities)

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Memory                                                           │
│                                                                     │
│   [ Entities ] [ Threads ] [ Browse all ]                           │
│                                                                     │
│   Filter:  [ all namespaces ▾ ]  [ person ▾ ]                       │
│   Search:  [                                                      ] │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Sarah Smith                              gmail-personal     │    │
│  │     sarah@example.com  +1 alias                              │    │
│  │     208 entries · most recent 2 days ago                     │    │
│  └─────────────────────────────────────────────────────────────┘    │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Bob Lee                                  gmail-personal     │    │
│  │     bob@example.com                                          │    │
│  │     74 entries · most recent 4 hours ago                     │    │
│  └─────────────────────────────────────────────────────────────┘    │
│  ...                                                                │
└─────────────────────────────────────────────────────────────────────┘
```

Clicking an entity opens a detail view:

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Memory › Sarah Smith                                             │
│                                                                     │
│   Sarah Smith                                                       │
│   person · gmail-personal · 208 entries                             │
│                                                                     │
│   Aliases:  sarah@example.com (email)                               │
│             Sarah Smith (name)                                      │
│                                                                     │
│   First seen:  March 4, 2024                                        │
│   Most recent: 2 days ago                                           │
│                                                                     │
│  ┌──── Recent entries ─────────────────────────────────────────┐    │
│  │  2 days ago    from  "Re: Project Alpha kickoff"             │    │
│  │  3 days ago    to    "Project Alpha kickoff"                 │    │
│  │  5 days ago    cc    "Budget review"                         │    │
│  │  ...                                                         │    │
│  │  → View all 208 entries                                      │    │
│  └──────────────────────────────────────────────────────────────┘    │
│                                                                     │
│  ┌──── Threads involving Sarah ────────────────────────────────┐    │
│  │  • Project Alpha kickoff           4 entries                 │    │
│  │  • Budget review                   6 entries                 │    │
│  │  • Q2 planning                     12 entries                │    │
│  └─────────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────────┘
```

The Memory tab is the "what does Loamss know about me?" surface. Making it browsable is part of the trust story.

### Memory tab (threads)

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Memory                                                           │
│                                                                     │
│   [ Entities ] [ Threads ] [ Browse all ]                           │
│                                                                     │
│   Filter:  [ all namespaces ▾ ]                                     │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Project Alpha kickoff                    gmail-personal     │    │
│  │     4 entries · last activity 2 hours ago                    │    │
│  │     Sarah Smith, Bob Lee                                     │    │
│  └─────────────────────────────────────────────────────────────┘    │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Q2 planning                              gmail-personal     │    │
│  │     12 entries · last activity 1 day ago                     │    │
│  │     Sarah Smith, Carol Wu, Bob Lee, +2                       │    │
│  └─────────────────────────────────────────────────────────────┘    │
│  ...                                                                │
└─────────────────────────────────────────────────────────────────────┘
```

### Settings tab

```
┌─────────────────────────────────────────────────────────────────────┐
│  ⌂ Settings                                                         │
│                                                                     │
│   ┌──── Storage ─────────────────────────────────────────────────┐   │
│   │  storage:fs-encrypted at ~/.loamss/storage                   │   │
│   │  AES-256-GCM · 4.2 GB used                                   │   │
│   │  [ Change location ]   [ Re-key ]                            │   │
│   └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│   ┌──── Memory ──────────────────────────────────────────────────┐   │
│   │  memory:sqlite at ~/.loamss/memory.db                        │   │
│   │  12,890 entries · dimension 1536                             │   │
│   │  [ Vacuum / re-index ]                                       │   │
│   └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│   ┌──── Models ──────────────────────────────────────────────────┐   │
│   │  model:anthropic   claude-sonnet-4-5     $1.42 used today    │   │
│   │  model:ollama      llama3.2 (local)      embeddings          │   │
│   │  [ Add model ]   [ Routing rules ▸ ]                         │   │
│   └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│   ┌──── Backup + export ────────────────────────────────────────┐   │
│   │  Last export:  March 12, 2026                                │   │
│   │  [ Export now ]   [ Schedule weekly ]                        │   │
│   └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│   ┌──── Advanced ────────────────────────────────────────────────┐   │
│   │  Edit raw config file ▸                                      │   │
│   │  Capability registry ▸                                       │   │
│   │  Diagnostics + logs ▸                                        │   │
│   │  Pair another device (mobile) ▸                              │   │
│   └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

Settings is the only screen where the words "adapter" and "capability registry" appear in primary text. Everywhere else, they live behind the friendly names.

### Pending approval (consequential action)

The trickiest flow: an app calls `email.send` (or any capability with `DefaultApproval=false`). The runtime returns `-32002 approval_required` to the app and notifies the console. The user sees a card on the dashboard:

```
┌─────────────────────────────────────────────────────────────────────┐
│  ┌──── ⚠ Approval needed ──────────────────────────────────────┐    │
│  │                                                              │    │
│  │  ChatGPT laptop wants to:                                    │    │
│  │                                                              │    │
│  │       Send email                                             │    │
│  │       To:       sarah@example.com                            │    │
│  │       Subject:  Re: Project Alpha kickoff                    │    │
│  │       Body:     Sounds great — let's discuss at 2pm…         │    │
│  │                                                              │    │
│  │       (Full draft preview ▸)                                 │    │
│  │                                                              │    │
│  │       Capability: email.send                                 │    │
│  │       Asked 14 seconds ago · expires in 4:46                 │    │
│  │                                                              │    │
│  │   [ Approve ]    [ Approve + remember ]    [ Deny ]          │    │
│  └──────────────────────────────────────────────────────────────┘    │
│                                                                     │
│  (rest of dashboard…)                                               │
└─────────────────────────────────────────────────────────────────────┘
```

Three buttons:

- **Approve** — one-shot. The next email.send request prompts again.
- **Approve + remember** — converts the grant from `requires_approval=true` to a narrower grant scoped to *this kind of action* (e.g., "any email to sarah@example.com" or "any email" depending on what the user wants).
- **Deny** — rejects; the runtime returns the deny to the app; the app's UI tells the user.

The "remember" path is the one that makes the system stop nagging the user without sacrificing transparency. The narrowing is a side panel that opens before commit.

## Visual language (preliminary)

This isn't a spec — just the visual choices the wireframes assume:

- **Typography**: system font stack (no web fonts; offline-first). One sans for UI, one mono for code/IDs.
- **Colors**: neutral grays + 3 semantic colors (green=ok, yellow=needs attention, red=error). One brand accent kept minimal — the console looks like infrastructure software, not a SaaS product.
- **Density**: comfortable, not packed. Information cards have breathing room. Lists are scannable; details are reachable in one click.
- **Iconography**: minimal. Inline status dots, a few action glyphs (sync, pause, delete). No decorative illustration.
- **Motion**: skeleton loaders for async data; subtle transitions between states. No splash screens.

When the design partner / `frontend-design` skill takes a pass, this is the brief to challenge. The wireframes describe the IA + flows; the visual identity is wide open.

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

### Decision (2026-05-24)

**Option A — embedded in the runtime binary.** Reasons:

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
