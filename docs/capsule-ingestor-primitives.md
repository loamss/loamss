# Capsule ingestor primitives — design RFC

**Status**: implemented; doc retained as the spec the implementation honors.
**Scope**: the runtime ↔ capsule contract needed to let a capsule act as a data-source connector (the `ingestor` role from `capsule-spec.md`) without losing the things in-tree `source.Source` connectors get for free.

> **Framing.** Source ingestion — whether in-tree or via capsule —
> is a **transitional** path. The long-term shape Loamss optimizes for
> is **apps writing to your Loamss as their backing store** (see
> [`../native-apps.md`](../native-apps.md)); ingestors exist for users
> whose data still lives in legacy SaaS. This spec stays accurate so
> migration tooling can be built well; just don't read it as "the
> primary way data arrives in Loamss." That's the inverse of the
> thesis.

## Why this exists

The repo decision: the in-tree path is for SPI reference implementations only. `source:files` (no-auth) and `source:gmail` (OAuth) cover the two ends of the SPI. Every other data source — Calendar, Drive, Slack, GitHub, Notion, Linear, RSS, … — ships as a capsule under the ingestor role. See [`sources.md`](../sources.md) and [`CLAUDE.md`](../CLAUDE.md) "What to avoid."

For that decision to be more than aspiration, three things the runtime hands an in-tree connector today need analogues a capsule subprocess can reach via MCP:

1. **Credential storage** — in-tree sources call `deps.Credentials.Set/Get/Delete`. The runtime encrypts the blob via the configured storage adapter. A capsule subprocess can't reach `deps.Credentials` directly; it needs MCP tools.

2. **Cursor persistence** — in-tree sources return a cursor from `Sync(ctx, cursor)`; the runtime hands the same bytes back on the next call. A capsule needs the same round-trip without minting its own storage path.

3. **Scheduled invocation** — `loamss source sync` and the scheduler call an in-tree source's `Sync` method. A capsule's "sync" needs to be invokable on the same cadence, but since the capsule is a subprocess, the call goes runtime → capsule, not capsule → runtime.

4. **OAuth callback bridge** — OAuth-using in-tree sources start a loopback HTTP listener via `AuthFlowBrowser`. A capsule subprocess can't safely bind a port the user trusts. The runtime owns the listener; it forwards the captured code into the capsule.

The first two are the same shape — capsule-namespaced encrypted blob storage via MCP tools the capsule calls. The third is a runtime-driven callback. The fourth is a paired tool + callback. None of them require a new transport — everything is JSON-RPC over the existing MCP-over-stdio framing.

## Conventions this design adopts

- **Tool namespace**: matches the existing `memory.*`, `audit.*`, `client.*` pattern. New tools: `credentials.*`, `cursor.*`, `oauth.*`.
- **Capsule-namespaced state**: the runtime keys credentials and cursors by `(capsule_install_id, source_name, key)`. A capsule can never read another capsule's blobs, nor another installation of itself.
- **Audit**: every primitive emits an audit entry. Credential reads and writes especially — the user's audit log is the trust anchor.
- **Manifest declarations**: anything that affects the user's permission decision (schedule cadence, OAuth provider, scopes requested) is in `capsule.yaml`, not registered at runtime. Users see what they're approving before install.
- **Run mode**: ingestor capsules run as **persistent** subprocesses — the runtime keeps them alive between scheduled triggers so cold-start cost isn't paid on every sync. This matches how `actuator` and `exposer` capsules already run.

## The four primitives

### 1. Credentials MCP tools

The capsule's view of `CredentialStore`. Three tools.

#### `credentials.set`

```json
{
  "name": "credentials.set",
  "description": "Store an encrypted credential blob, scoped to this capsule installation.",
  "inputSchema": {
    "type": "object",
    "required": ["key", "value"],
    "properties": {
      "key":   { "type": "string", "pattern": "^[a-z0-9_.-]+$" },
      "value": { "type": "string", "description": "Plaintext. The runtime encrypts before persisting via the storage adapter." },
      "expires_at": {
        "type": "string", "format": "date-time",
        "description": "Optional. The runtime treats reads after this point as a miss. Use for refresh-token expiry."
      }
    }
  },
  "outputSchema": { "type": "object", "properties": { "ok": { "const": true } } }
}
```

#### `credentials.get`

```json
{
  "name": "credentials.get",
  "description": "Read a previously-set credential.",
  "inputSchema": {
    "type": "object", "required": ["key"],
    "properties": { "key": { "type": "string" } }
  },
  "outputSchema": {
    "type": "object",
    "properties": {
      "found": { "type": "boolean" },
      "value": { "type": "string", "description": "Present only when found=true." },
      "expires_at": { "type": "string", "format": "date-time" }
    }
  }
}
```

#### `credentials.delete`

```json
{
  "name": "credentials.delete",
  "inputSchema": {
    "type": "object", "required": ["key"],
    "properties": { "key": { "type": "string" } }
  }
}
```

#### Storage shape

Behind the scenes the runtime maps `(capsule_install_id, key)` to an encrypted blob written via the configured `storage:` adapter under the path `loamss/credentials/<install_id>/<key>.enc`. Encryption uses the runtime's at-rest key (same one `storage:fs-encrypted` uses for source credentials today).

#### Capability

`credentials.read`, `credentials.write` declared in the manifest. The runtime enforces capsule-namespacing automatically — the capability does not need to enumerate keys.

#### Audit

Every `set`/`get`/`delete` emits an `audit.credentials.<op>` entry with the capsule id and key (never the value).

---

### 2. Cursor MCP tools

The capsule's view of the cursor `source.Source.Sync(ctx, cursor)` receives + returns. Two tools (no delete — clearing the cursor is `set` with an empty value, which means "resync from scratch on next trigger").

#### `cursor.set`

```json
{
  "name": "cursor.set",
  "description": "Persist the source's incremental-sync cursor for this installation.",
  "inputSchema": {
    "type": "object", "required": ["value"],
    "properties": {
      "value": { "type": "string", "description": "Opaque to the runtime. Typically a JSON blob the capsule designs. Empty string = no cursor." }
    }
  }
}
```

#### `cursor.get`

```json
{
  "name": "cursor.get",
  "outputSchema": {
    "type": "object",
    "properties": {
      "value": { "type": "string", "description": "Empty string if no cursor has been set." }
    }
  }
}
```

#### Why not just use `credentials`?

Cursors are not secrets. The audit shape differs — every cursor write happens on every sync; surfacing those at the same priority as credential writes would drown the audit log. Cursors get a quieter audit shape (`audit.source.sync.cursor_advanced`, batched at sync completion) and live in plaintext.

---

### 3. Scheduled triggers

The runtime invokes a capsule's sync callback on a cadence. The cadence is declared in the manifest so users see it at install time.

#### Manifest declaration

```yaml
roles:
  - ingestor                  # NEW: enables the schedule + cursor primitives

ingestor:
  source_id: source:hackernews-personal
  schedule:
    interval: 5m              # ISO-8601 short form. min 1m. max 24h.
    initial: 30s              # First sync this long after install (so the user
                              # sees data quickly without burning the API quota).
  on_trigger: sync            # The capsule-side tool name to invoke. Must be
                              # registered in the capsule's `tools:` list.
```

`schedule.interval` parses as Go's `time.ParseDuration`. Empty/missing schedule = no automatic trigger; user-initiated `loamss source sync` still works.

#### Wire sequence

```
runtime scheduler ticks            runtime ─→ capsule
                  │                  │
                  │   compose JSON-RPC request
                  │   { method: "tools/call",
                  │     params: { name: "sync", arguments: {} } }
                  ▼
                  ─────────────────▶
                  ◀────────────────  { result: { records_added: 12, ... } }

                  audit.source.sync.completed
```

This is the *same* mechanism the runtime already uses to call capsule tools from external clients. The difference is the originator: a scheduler tick rather than an inbound MCP request. The capsule code doesn't need a separate handler.

#### Tools list contract

The tool the scheduler invokes (`on_trigger`) must:
- Be in the capsule's exposed `tools:` list with `requires_user_approval: false`
- Accept an empty/optional input object
- Return a structured result with the same shape as `source.SyncResult` (see below) — the runtime parses this to fill the audit trail.

#### Returned shape

Mirrors `source.SyncResult` so the audit + dashboard can show the same numbers for capsule ingestors as for in-tree sources:

```json
{
  "records_added":   12,
  "records_updated": 3,
  "bytes_ingested":  87234,
  "errors": [
    { "record_id": "msg-991", "message": "media-type unknown; skipped" }
  ]
}
```

The runtime supplies `started_at` and `finished_at` itself (wall-clock around the call). The capsule never lies about timing — that protects the audit log.

#### User-initiated sync

`loamss source sync <name>` invokes the same callback. From the capsule's perspective there is no difference; it's the same `tools/call`.

---

### 4. OAuth callback bridge (revised)

> **Design revised mid-implementation.** The original sketch had the capsule driving the flow via `oauth.begin` + `loamss.oauth.completed` callback + `oauth.refresh`. That was unnecessarily complex once the dashboard, approvals queue, and credentials store were already in place. The revised design moves the entire flow into the runtime; the capsule's only OAuth surface is `oauth.access_token`. The original is preserved at the bottom of this section for the design trail.

The runtime owns the entire OAuth flow. The capsule does not see the auth code, the PKCE verifier, the redirect URI, or even the refresh token — it just asks the runtime for an access token when it needs one. The dashboard surfaces "Connect Google Calendar"-style buttons; the user clicks one, browser opens, they approve, done.

#### Manifest declaration

```yaml
oauth:
  provider: google              # Well-known identifier; runtime knows the
                                # endpoints + defaults. Inline endpoints below
                                # only required when provider isn't well-known.
  scopes:
    - https://www.googleapis.com/auth/calendar.readonly
  # Optional — provider-specific extras the runtime adds to the auth URL.
  extra_params:
    access_type: offline
    prompt: consent
  # Optional — endpoints required only when provider is not in the runtime's
  # well-known registry.
  # authorization_endpoint: https://...
  # token_endpoint: https://...
```

Well-known providers ship in the runtime: `google`, `github` at minimum (extensible). Their endpoints + sensible defaults (PKCE always on; provider-specific extras like Google's `access_type=offline`) live in `runtime/internal/oauth/providers.go`. A capsule targeting one of those providers only needs `provider:` + `scopes:` — three lines instead of seven. Non-well-known providers fill in `authorization_endpoint` + `token_endpoint` inline.

The OAuth `client_id` is **not** in the manifest. It's per-user-per-provider (the user creates one in their own Google Cloud Console etc.) and is collected at install time by the dashboard, stored separately from per-capsule data. Multiple capsules that target the same provider share the same client_id.

#### `oauth.access_token` — the only MCP tool

```json
{
  "name": "oauth.access_token",
  "description": "Return a valid bearer access token for this capsule's OAuth provider. Refreshes transparently when the cached token is expired.",
  "inputSchema": { "type": "object", "additionalProperties": false },
  "outputSchema": {
    "type": "object",
    "required": ["access_token"],
    "properties": {
      "access_token": { "type": "string", "description": "Bearer token. Use as `Authorization: Bearer <token>` against the provider's API." },
      "expires_at":   { "type": "string", "format": "date-time" }
    }
  }
}
```

Behavior:

1. Read the capsule's stored `access_token` + `expires_at` from its credentials.
2. If still valid (≥ 60s remaining), return it.
3. Otherwise read `refresh_token`, POST to the provider's token endpoint, store the new `access_token` + (rotated, if any) `refresh_token`, return.
4. If refresh fails (revoked, expired, or no refresh token stored): return a tool error with code `oauth.reauth_required` AND surface a `PendingApproval` in the dashboard's Approvals pane.

That's the entire capsule-side OAuth API. No callbacks, no flow management, no token storage.

#### Browser flow (runtime-driven)

The dashboard's Sources pane shows a "Connect <Provider>" button next to any ingestor capsule whose OAuth state is "needs auth" or "expired."

```
POST /console/oauth/begin?capsule=calendar-ingestor
   → runtime reads the capsule's oauth manifest + the user's stored
     client_id for that provider
   → if no client_id stored: 400 with "set up an OAuth client first"
   → otherwise:
       - picks an ephemeral loopback port
       - generates PKCE verifier + state
       - stores flow context in runtime.db
       - opens the browser (`open` / `xdg-open` / `start`)
       - returns 202 { flow_id, redirect_uri }

[user approves in browser → Google sends them to
http://127.0.0.1:<ephemeral>/oauth/callback?code=...&state=...]

   → ephemeral listener captures, matches state, posts code+verifier
     to provider's /token endpoint
   → stores access_token, refresh_token, expires_at, scope into the
     capsule's credentials via the existing CapsuleCredentialStore
   → ephemeral listener returns "✓ Connected — close this tab"
   → ephemeral listener shuts down
```

The dashboard polls `/console/oauth/status?capsule=...` to update the button state. The capsule's `oauth.access_token` calls just work after this.

#### Re-auth via the existing approvals queue

When `oauth.access_token` finds the refresh token revoked, the runtime adds a `PendingApproval` entry of kind `oauth.reauth`. The user sees a chip in the dashboard's Approvals pane: "⚠️ calendar-ingestor lost access to Google. [Re-authenticate]." Click → same browser flow as the first time. No new surface to learn — the user already approves grants via this pane.

#### Per-user client_id storage

Stored in `runtime.db` under a new `oauth_clients` table, keyed by provider name. Set via:

```
POST /console/oauth/clients/{provider}
{ "client_id": "...", "client_secret": "..." }   // client_secret optional
```

The dashboard's capsule-install flow inspects the manifest; if it declares `oauth.provider: google` and no client is stored yet, it adds a step: "Calendar Ingestor needs a Google OAuth client. [How to create one →]. Paste here:" Once set, subsequent Google capsules skip this prompt.

#### Why is this safer / simpler than v1?

| Concern | v1 | Revised |
|---|---|---|
| PKCE verifier scope | Lives in runtime | Lives in runtime |
| Loopback listener owner | Runtime | Runtime (ephemeral, per-flow) |
| Token storage | Capsule (via credentials.set) | Runtime (under same credentials path) |
| MCP tools added | 3 (`oauth.begin`, `oauth.refresh`, callback) | 1 (`oauth.access_token`) |
| Capsule SDK shape | OAuth-specific handler + 3 calls | Single call: `accessToken()` |
| Re-auth UX | Capsule must error → user must notice | Approvals pane surface |
| Where user finds client_id | Env var (read docs) | Dashboard prompt at install |

The capsule still does the resource-side API calls (`https://www.googleapis.com/calendar/v3/...`). The runtime handles only the auth-edge plumbing.

---

#### v1 design (superseded; kept for the design trail)

The original design called for the capsule to drive the flow via three MCP tools (`oauth.begin`, `oauth.refresh`) plus a runtime → capsule callback (`loamss.oauth.completed`). The capsule's authenticate-tool would call `oauth.begin`, get back a URL, surface it to the user, then handle the resulting tokens via the callback and persist them via `credentials.set`. Refresh worked similarly through `oauth.refresh`.

That design was correct, but it forced every ingestor capsule to implement OAuth orchestration code that did the same thing every time. The revised design moves that orchestration into the runtime once and exposes only the access-token read to capsules. The runtime is now the OAuth client; the capsule is just the API consumer.

The principle didn't change: the runtime holds the PKCE verifier, owns the loopback, does the token exchange, never lets the capsule see secrets it doesn't need. The revision just shifts where the work happens to better match the actual UX (one button in the dashboard, transparent token use in the capsule).

---

## Worked example: a hypothetical `calendar-ingestor` capsule

This is what a third-party Calendar connector looks like once these primitives ship.

### `capsule.yaml`

```yaml
spec_version: "0.1"

name: calendar-ingestor
version: 0.1.0

author:
  name: somebody@example.com
  url: https://github.com/example/loamss-calendar-ingestor

roles:
  - ingestor

permissions:
  - capability: memory.write
    rationale: Writes calendar events into memory under namespace=calendar.
  - capability: external.http
    scope:
      hosts: ["www.googleapis.com"]
    rationale: Calls the Google Calendar API for events.

ingestor:
  source_id: source:calendar
  schedule:
    interval: 15m
    initial: 30s
  on_trigger: sync

oauth:
  provider: google                # well-known; endpoints from runtime registry
  scopes:
    - https://www.googleapis.com/auth/calendar.readonly
  extra_params:
    access_type: offline
    prompt: consent

tools:
  - name: sync
    description: Sync calendar events since the last cursor.
    input_schema: { type: object }
```

No `authenticate` tool, no `credentials.{read,write}` permissions. The dashboard's "Connect Google Calendar" button drives the auth flow runtime-side; the capsule isn't involved until it calls `oauth.access_token` to get a bearer for an API request.

### Capsule code (TypeScript, sketch)

```typescript
import { createCapsule, defineTool, manifest } from "@loamss/sdk";

const cap = createCapsule({
  manifest,
  tools: [
    defineTool({
      name: "sync",
      description: "Sync calendar events since the last cursor.",
      inputSchema: { type: "object" },
      handler: async (_, { runtime }) => {
        // One call: runtime returns a valid bearer, refreshing
        // transparently if needed. If the refresh token has been
        // revoked, this throws oauth.reauth_required and the
        // runtime adds an entry to the Approvals pane.
        const { access_token } = await runtime.oauth.accessToken();

        const { value: cursorRaw } = await runtime.cursor.get();
        const cursor = cursorRaw ? JSON.parse(cursorRaw) : { syncToken: null };

        const events = await fetchEvents(access_token, cursor.syncToken);

        let added = 0;
        for (const ev of events.items) {
          await runtime.memory.upsert({
            namespace: "calendar",
            id: ev.id,
            content: `${ev.summary}\n\n${ev.description ?? ""}`,
            metadata: { start: ev.start, end: ev.end, attendees: ev.attendees },
          });
          added++;
        }

        await runtime.cursor.set(JSON.stringify({ syncToken: events.nextSyncToken }));
        return { records_added: added };
      },
    }),
  ],
});

cap.start();
```

Notice what's not here: no `authenticate` tool, no `oauth.begin`, no `loamss.oauth.completed` handler, no `credentials.set` for tokens. The runtime handles all of it. The capsule is just a calendar-events-to-memory adapter.

### Lifecycle (analogous to the in-tree source lifecycle from `sources.md`)

```
loamss capsule install ./calendar-ingestor
   → manifest validated; user approves perms (memory.write, external.http)
   → dashboard detects oauth.provider=google + no client_id stored yet
   → dashboard prompts: "Calendar Ingestor needs a Google OAuth client.
                         Paste your client_id here:"
   → runtime stores client_id under oauth_clients/google
   → ingestor source_id registered alongside in-tree sources
   → capsule subprocess started (persistent run mode)

Dashboard's Sources pane shows: "calendar-ingestor — Needs auth [Connect Google]"

User clicks Connect Google
   → POST /console/oauth/begin?capsule=calendar-ingestor
   → runtime: allocates ephemeral loopback port, generates PKCE + state,
     stores flow context, opens the browser

User approves in Google
   → loopback captures the code at the ephemeral port
   → runtime POSTs code+verifier to oauth2.googleapis.com/token
   → runtime stores access_token + refresh_token + expires_at +
     scope under capsules/calendar-ingestor/credentials.json
   → ephemeral listener returns "✓ Connected — close this tab"
   → dashboard polls status → renders "Connected (as user@gmail.com)"

15 seconds later: scheduler initial tick
   → runtime invokes capsule's `sync` tool
     → capsule calls `oauth.access_token` → runtime returns cached
       access_token (still valid)
     → capsule fetches events from Google
     → capsule calls `memory.upsert` for each event
     → capsule calls `cursor.set` with the new syncToken
   → returns { records_added: 47 }
   → runtime emits source.sync.completed

Every 15m: scheduler ticks → invokes sync → access token refreshes
transparently in the runtime when stale.

A week later: user revokes calendar-ingestor's access from
Google's third-party app dashboard.
   → next sync: capsule calls oauth.access_token
     → runtime tries to refresh; provider returns 400 invalid_grant
     → runtime adds PendingApproval entry: "calendar-ingestor lost
       access. [Re-authenticate]"
     → oauth.access_token returns oauth.reauth_required to the capsule
     → capsule reports records_added=0 errors=1 in its SyncResult
   → user sees the chip in the Approvals pane, clicks Re-authenticate
   → same browser flow as install; tokens refresh; next tick syncs

loamss source remove source:calendar
   → runtime stops the capsule subprocess
   → runtime deletes the credential blob (cursor + tokens + everything)
   → the per-user oauth_clients/google entry is NOT deleted — the
     client_id is shared across Google capsules and the user might
     install another Drive ingestor tomorrow that reuses it
```

The user never knows the difference between in-tree and capsule. `loamss source list` shows them together. The dashboard's Sources pane lists them together. The audit log entries have the same shape.

---

## What changes elsewhere

When this design is approved, the spec changes propagate:

- **[`mcp-surface.md`](../mcp-surface.md)** § "Runtime-provided tools" — add `credentials.*`, `cursor.*`, `oauth.*` to the always-present set, with the schemas above.
- **[`capsule-spec.md`](../capsule-spec.md)** — define the `ingestor` role's manifest section (`ingestor:` block + `oauth:` block); define the runtime → capsule callback `loamss.oauth.completed`; add `credentials.{read,write}` and `network.fetch` to the canonical capability list (if not already present).
- **[`permission-model.md`](../permission-model.md)** — clarify that `credentials.{read,write}` are capsule-namespaced by construction; the capability does not enumerate keys.
- **[`audit-spec.md`](../audit-spec.md)** — add entry types `audit.credentials.{set,get,delete}`, `audit.source.sync.cursor_advanced`, `audit.oauth.{begin,completed,refresh_failed}`.
- **[`sources.md`](../sources.md)** — section "Adding a new source connector" gets a concrete example block linking to this RFC.
- **[`docs/build-your-first-source-connector.md`](build-your-first-source-connector.md)** — gets a capsule-side companion section once the primitives ship, demonstrating the calendar-ingestor flow above.

## Implementation order

Smallest dependency tree first:

1. **Credentials MCP tools** (`credentials.{set,get,delete}`) — pure runtime addition; reuses the existing per-source credential store. The new wrinkle is keying by capsule install id rather than source name.
2. **Cursor MCP tools** (`cursor.{set,get}`) — same shape as credentials but plaintext + quieter audit.
3. **Manifest schema** — add the `ingestor:` + `oauth:` blocks to `capsule-spec.md` and the YAML validator.
4. **Source registry bridge** — when an ingestor capsule installs, register its `source_id` in the source registry so `loamss source list` and the dashboard see it.
5. **Scheduled triggers** — runtime-side scheduler that ticks per ingestor's `schedule.interval` and invokes the capsule's `on_trigger` tool. Audit hooks.
6. **OAuth callback bridge** — runtime-owned loopback listener; `oauth.begin` / `oauth.refresh` tools; `loamss.oauth.completed` callback.
7. **Reference ingestor capsule** — ship one in `sdk/typescript/examples/` to demonstrate the loop end-to-end. Probably an RSS or HackerNews ingestor first (no OAuth), then a follow-up with OAuth.

Each step is independently testable. Steps 1–4 can land without 5 (capsules can use the credentials/cursor tools on user-initiated sync). Steps 1–5 can land without 6 (any non-OAuth ingestor works). Step 7 lives or dies on what shipped before it.

## Open questions

These are the things to resolve before locking the wire — flagging here rather than guessing.

- **Per-source vs per-capsule keying.** A single capsule could in principle expose multiple `source_id`s (e.g. a Google capsule that ingests Gmail and Calendar). Today the design keys credentials and cursor by `capsule_install_id` only. Does it need a `source_id` dimension too? Leaning yes; cheap to add now, expensive to retrofit.

- **Trigger backpressure.** If a sync takes longer than the schedule interval, what happens? Two reasonable choices: skip the next tick (current preference), or queue. Skip is simpler and matches what real cron does.

- **OAuth client secrets.** PKCE handles native/desktop OAuth without a client secret. Some providers still require a client secret regardless. The current design supports this via `credentials.set("oauth_client_secret", ...)` at install time, which the runtime reads when doing the exchange. Worth explicit in `capsule-spec.md`.

- **Refresh token rotation.** Google and others rotate refresh tokens. The runtime should pick that up from the `/token` response and persist the new refresh token via `credentials.set` automatically, without the capsule noticing. Behavior should be documented.

- **Manifest field name churn.** `roles: [ingestor]` vs `kind: ingestor` vs `role: ingestor`. The existing manifest uses `roles:` as a list because a capsule can wear multiple roles. Keeping that.

- **Initial sync semantics.** `schedule.initial: 30s` is a UX nicety so users see data quickly. Is it worth the complexity? Leaning yes — the alternative is the first sync happens at the first full interval (e.g. 15 minutes after install for the calendar example), which feels slow.

## Out of scope for this RFC

- **Push subscriptions.** Some providers (Gmail, Slack) support webhook push instead of polling. That's a separate primitive — `subscriptions.register` style — and orthogonal to the four above. Defer until at least one capsule ingestor is shipping and someone asks for push.

- **Write-back (`source:gmail-send` shape).** Per `sources.md`, write-back capabilities will need the SPI extended. Out of scope here; ingestors are read-only by design.

- **Multi-account-per-source.** Two `calendar-ingestor` installs (work + personal) is supported by the existing source-instance model; nothing in this RFC blocks it, but cross-installation key collisions deserve a test.
