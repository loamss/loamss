# Building Native Loamss Apps

This document is for developers building an application where **Loamss is the backing data store from the start** — not an integration added later, but the substrate the app is designed around. It complements `mcp-surface.md`, which describes the wire protocol; this doc covers the design pattern.

> **Status: this is the design center.** Loamss exists primarily to be
> the substrate Path A native apps are built on. Other reading
> orders (capsule authors, Path B integrators, source-connector
> writers) all branch off this thesis — see `README.md`,
> `CLAUDE.md`, and `ROADMAP.md` for the lineage. Patterns and
> examples here will keep evolving as native apps ship, but the
> overall shape is settled.

## What this document is and is not

This spec describes:

- What it means for an app to be "Loamss-native" vs. "Loamss-compatible"
- The architectural pattern: how a native app's data flow differs from a traditional app
- What you gain and what you give up
- Worked examples — a note-taking app, a journaling tool, a creator platform
- Handling the cases that don't exist in traditional architectures (offline, multi-Loamss users, graceful fallback)
- The honest ecosystem reality: most users today don't have a Loamss

This spec does **not** describe:

- The MCP wire protocol (see `mcp-surface.md`)
- Capsule development (see `capsule-spec.md` — capsules are a different role)
- How to add Loamss as an *optional* integration to an existing app (that's the simpler case, covered by `mcp-surface.md` alone)

## The honest ecosystem reality

Loamss assumes an ecosystem of apps that treat user-owned data substrates as first-class. **That ecosystem doesn't exist yet.** Most apps today hold user data in their own databases, with at most an "export to ZIP" feature for compliance. Building an app native to Loamss means going against the current.

There are two paths for a developer thinking about this:

### Path A — Native Loamss apps (this doc)

Your app is designed around Loamss from day one. The user's Loamss IS the storage layer. Your backend stores essentially nothing about the user's content — only metadata needed to render the app (auth tokens, UI preferences, your own session state).

**Examples:**

- A note-taking app where notes are entities in the user's Loamss memory
- A journaling app where entries are written to the user's storage adapter
- A creator publishing platform that reads videos from the user's S3 via signed URLs (`scenarios.md` §5)
- A research collaboration tool that exposes specific datasets through scoped permissions
- A personal AI assistant that pairs with the user's Loamss for all context

The bet: users who care enough about data ownership will choose your app over a traditional alternative. The market is small today but real and growing.

### Path B — Existing apps adding Loamss support

Your app already exists with its own user accounts and database. You add MCP client support so users can optionally connect their Loamss for context, or to write specific outputs back to. Your storage stays where it is; Loamss is one of several context sources.

**This path is covered by `mcp-surface.md` alone.** You're building an MCP client like any other. The integration is significant but doesn't require rethinking your architecture.

Path B is the path most apps will eventually take. Path A is the path that grows the ecosystem and makes Path B compelling.

The rest of this doc is about Path A.

## The architectural pattern for a native Loamss app

A native Loamss app has a fundamentally different shape than a traditional cloud app.

### Traditional app

```
┌─────────────┐    ┌──────────────────┐    ┌─────────────┐
│  Frontend   │───>│  App backend     │───>│  App DB     │
│  (browser   │    │  (your servers)  │    │  (your DB)  │
│   / mobile) │<───│                  │<───│             │
└─────────────┘    └──────────────────┘    └─────────────┘
```

You own everything. User data lives in your database. When the user leaves, you keep their data (or delete it on request). Lock-in is structural.

### Native Loamss app

```
┌─────────────┐    ┌──────────────────┐    ┌─────────────────────┐
│  Frontend   │───>│  App backend     │───>│  User's Loamss      │
│  (browser   │    │  (thin layer)    │    │  (their substrate)  │
│   / mobile) │<───│  - auth glue     │<───│                     │
└─────────────┘    │  - UI state      │    └─────────────────────┘
                   │  - pairing flow  │
                   └──────────────────┘
```

Your backend exists mostly to bootstrap the connection and hold non-content metadata (auth tokens, UI preferences, maybe a list of which Loamss instance each user is paired with). The actual user content — notes, journal entries, photos, code, attachments, whatever your app is about — lives in the user's Loamss.

This is genuinely different. Your app doesn't own a copy of the user's content. When the user uninstalls your app, you have nothing to delete; their content stays with them. When you sunset the app, the user's data isn't held hostage.

### Data flow in a native app

A user takes an action in your app. Say they create a new note. The flow:

1. User types a note in your frontend, hits save
2. Your frontend sends it to your backend
3. Your backend doesn't store the note — it calls the user's Loamss MCP surface with the user's per-app credential
4. The runtime checks: is this app authorized to write notes? Yes (capability granted at pairing time, scoped to "notes" entity type)
5. The note is written to the user's storage, indexed in memory, logged in the audit trail
6. Your backend returns success to the frontend
7. The note now lives in the user's Loamss — not in your database

When the user comes back tomorrow and asks for their notes:

1. Frontend asks your backend for the user's notes
2. Backend queries the user's Loamss: `memory.query(entities: ["app.yours/note"], time_range: ...)`
3. Loamss returns the notes (gated by the app's scoped grant)
4. Backend passes them to the frontend

Your backend is thin. Most of what would normally be a database is now a call to the user's Loamss.

## What you gain

### Structural data ownership

The user's data is in their storage, not yours. This is the whole pitch. It's not just a marketing line — it's true in the actual data flow.

Compliance is mostly automatic. GDPR data portability? The user already has the data. Right to deletion? You don't have a copy to delete. Data residency? It's wherever the user put their storage.

### Lower backend costs and lower risk

You don't store the user's content. You don't pay for that storage. You don't carry the liability of a breach exposing it. Your database becomes orders of magnitude smaller — just auth glue and UI state.

For a creator platform with terabytes of user-uploaded video, this is the difference between "we run a CDN" and "we just route signed URLs."

### Cross-app data sharing for free

The user's other Loamss-native apps see the same entities (if granted access). A note created in your note-taking app is queryable by an AI assistant the user has paired. A journal entry is searchable by their personal search tool. Your app isn't a silo by default; it participates in the user's data substrate.

### Sunsettable without harm

If your app fails, gets acquired, or you decide to move on, the users don't lose anything. They keep all the content they created with your app. They can move to a competing Loamss-native app and import their existing data (it's already in the right place — their Loamss).

This is a feature, not a bug. It means users will trust you faster because they can't be hurt by your departure.

## What you give up

### Network dependency on the user's Loamss

If the user's Loamss is unreachable — they're offline, their NAS is down, their hosted instance is migrating — your app can't read or write their content. You need a story for this.

Options:

- **Read-only cache** of recent content in your backend or the frontend, for offline reading
- **Outbound write queue** that holds new writes until the Loamss is reachable
- **Local-first** patterns where the frontend has a small CRDT-style log that syncs to Loamss when available

This is solvable but it's real complexity. Native Loamss apps need to think about availability in a way traditional apps don't.

### No more "we have all the data" analytics

You can't run business intelligence queries against your users' content the way a traditional SaaS can. You don't have it.

You can ask the user to grant you `audit.read` to see their usage patterns *of your app*. You can ask them for opt-in metrics events. You can run aggregate stats from your own session data (sign-ins, feature touches). But you can't, for example, run a "top 100 most-common note tags" query across your user base — that data doesn't live in your hands.

This is the right outcome philosophically. It's also genuinely a constraint on product analytics.

### Users without Loamss can't use your app at all

This is the big one. A native Loamss app requires the user to have a Loamss. Today that's roughly nobody.

Three responses, ordered by ambition:

1. **Niche launch**: ship to the small early-adopter market that has or will install Loamss. Grow with the ecosystem.
2. **Hybrid mode**: offer a fallback where your app uses its own database for users without Loamss, with a migration path when they install one. This is a substantial complication but it's how Path A apps will be viable in the early years.
3. **Bundled experience**: your app ships with a tiny embedded Loamss instance or a hosted one (the Phase 3 hosted option). The user gets Loamss without knowing they have one. This works for some app categories, not for the data-sovereignty-conscious ones (those users want to bring their own).

There's no good way around this constraint. You're early; the ecosystem will catch up; in the meantime, your audience is the people who want to be early.

## Worked examples

### Example 1 — A native note-taking app ("Loamss Notes")

Frontend: standard note-editor UI. Backend: thin Node.js or Go service.

**Storage**: notes are written to the user's Loamss as entities of type `app.loamssnotes/note`. The entity schema includes `title`, `body`, `tags`, `created_at`, `updated_at`. The capsule that defines this entity type is bundled with the app.

**Permissions requested at pairing**:
- `memory.write` (scope: `entities: ["app.loamssnotes/note"]`, provenance required)
- `memory.read` (same scope)
- `memory.query` (same scope, plus search by tag/title)

**Backend stores**: user_id → loamss_endpoint mapping, per-app credentials, UI preferences. That's it.

**Offline story**: frontend caches recent notes in IndexedDB. New notes go into a write queue locally and sync to the user's Loamss when reachable. If a write is older than 24 hours and still unsent, the user gets a notification.

**Cross-app benefit**: a user with this app and ChatGPT both paired to their Loamss can ask ChatGPT about their notes. ChatGPT queries the same memory the notes app writes to.

### Example 2 — A native creator publishing platform ("Vibez")

This is the scenario from `scenarios.md` §5, viewed from the platform's side.

**Storage**: nothing. Videos live in the creator's S3 bucket. The platform reads them via signed URLs Loamss issues; the platform never holds a copy.

**Permissions requested**:
- `content.list`, `content.read` (scope: `tag:public`)
- `content.metrics.write` (to report plays back)
- `content.revenue.write` (to report ad earnings back)

**Backend stores**: creator_id → loamss_endpoint mapping, per-app credentials, platform-side editorial state (featured-content flags, moderation queues), view-side caching of metadata for performance.

**Offline story for creators**: creator's Loamss being unreachable just means the platform serves the last-cached metadata; new uploads are blocked until the Loamss is back.

**Offline story for viewers**: viewers stream from the creator's S3 via signed URLs. If the creator's storage is down, the videos don't play (same as if YouTube's CDN had a region down). The audit log on the creator's side records every URL issuance regardless.

### Example 3 — A native personal-AI assistant ("Mind")

Frontend: chat interface. Backend: routes between the user's chosen model and their Loamss for context.

**Storage**: chat history is written to the user's Loamss as entities of type `app.mind/conversation` and `app.mind/message`. The user-facing model conversation lives where the model lives (OpenAI, Anthropic, etc.).

**Permissions requested**:
- Full read scope on memory and files for context (subject to user's data-class exclusions)
- `memory.write` for storing conversations
- `events.subscribe` (optional) for proactive notifications about new emails, etc.

**Backend stores**: user_id → loamss + model_provider mapping, API keys (encrypted), UI preferences. No conversation content.

**Differentiator**: when the user switches model providers (Anthropic → OpenAI → local Llama → next year's thing), their entire conversation history follows them, because it's in their Loamss, not in the chat app's database.

## Common patterns

### Pairing flow at first run

When a user first opens your native Loamss app, the app needs to bootstrap a connection to their Loamss. The flow:

1. App asks: "Where is your Loamss?" — URL or paste pairing code
2. User runs `loamss client pair --name "Your App"` on their machine and pastes the code
3. App requests permissions via the pairing flow (`mcp-surface.md`)
4. User reviews and grants
5. App stores the per-app credential (encrypted at rest on your backend) and the user's Loamss endpoint
6. Future requests authenticate with that credential

The first-run flow is more involved than "sign up with email" — it's the price of the model. Make it as smooth as possible.

### Hybrid mode (with and without Loamss)

Most native apps in the early years will need a fallback. Pattern:

- User signs up the traditional way (email/password or OAuth)
- App stores their content in your database for now
- App prompts: "Connect your Loamss to take your data with you"
- When the user connects: your backend migrates their existing content to their Loamss, marks their account as "Loamss-paired," and starts using Loamss for all future content
- If the user later disconnects: their content stays in their Loamss (you don't get to keep a copy)

This is more code to maintain but it's how Path A apps survive the early ecosystem.

### Multiple Loamss instances per user

Some users will eventually run multiple Loamss instances — one personal, one for work, one for a research project. Your app should let them pair with whichever they want, and possibly choose at write-time which one a given piece of content goes to.

This is forward-looking; don't build it for v1. But design data models that don't assume a single backing Loamss.

### Subscribing to changes

Some apps need to know when the user's Loamss content changes outside the app — e.g., a notes app should refresh when the user edits a note via another tool that has write access.

The MCP subscription primitive (`mcp-surface.md` §Subscriptions) is the mechanism. It's deferred to Phase 2 of Loamss runtime; apps that need it before that have to poll.

## Comparison: when to choose Path A

| Situation | Likely answer |
|---|---|
| Building a new productivity tool for AI-heavy users | Path A — your differentiator is exactly the Loamss promise |
| Adding "AI features" to an existing app | Path B — keep your database, add MCP client support |
| Building a creator platform | Path A — bandwidth + content ownership are both unlocks |
| Building a B2B SaaS | Probably Path B — enterprise data residency rules are different |
| Building a personal AI assistant | Path A — the whole point is user-owned context |
| Building anything where you currently store user content | Consider Path A seriously; the lock-in cost may be worth shedding |

## Open questions

- **Hybrid-mode migration tooling**: should the runtime provide a `loamss import-from-app` command that helps users move data from a traditional app into their Loamss? Probably yes; spec deferred.
- **App-side capsules**: should Loamss-native apps be encouraged to bundle a custom capsule that defines their entity types, ingestors for their own historical data, etc.? Probably yes — this becomes a Phase 2 pattern.
- **Authentication standardization**: today, each native app's pairing flow is bespoke. A common library or SDK pattern (`@loamss/app-sdk`?) would simplify Path A development considerably.
- **Cross-app data conventions**: if two note-taking apps both write to the user's Loamss as `app.foo/note` and `app.bar/note`, the user effectively has two note collections that don't merge. Worth thinking about canonical entity types for the most common app categories — but this is community work, not a Loamss-runtime decision.
- **Discovery of native apps**: where do users find Loamss-native apps? Today there's nowhere. A registry/directory will likely emerge in Phase 2.
- **Bundled Loamss instances**: some apps may want to ship with a tiny embedded Loamss for users who don't have one. Is this supported? Probably yes — the runtime is designed to be embeddable. Spec implications deferred.
