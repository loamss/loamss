# Capsule ingestor primitives — design RFC

**Status**: design, not yet implemented.
**Author**: project working notes; revisit before locking the wire.
**Scope**: the runtime ↔ capsule contract needed to let a capsule act as a data-source connector (the `ingestor` role from `capsule-spec.md`) without losing the things in-tree `source.Source` connectors get for free.

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

### 4. OAuth callback bridge

The runtime owns the loopback HTTP listener. The capsule declares the OAuth client metadata in the manifest; the runtime mints the auth URL, captures the code at the callback, and hands it to the capsule via a runtime → capsule callback.

#### Manifest declaration

```yaml
oauth:
  provider: google              # Identifier the user sees in the consent UI.
                                # Free-form; appears in audit entries.
  authorization_endpoint: https://accounts.google.com/o/oauth2/v2/auth
  token_endpoint: https://oauth2.googleapis.com/token
  scopes:
    - https://www.googleapis.com/auth/calendar.readonly
  client_id_env: GOOGLE_OAUTH_CLIENT_ID   # The runtime reads this from the
                                          # capsule's per-instance env (set by
                                          # the user at install). Never the
                                          # manifest itself — that's checked in.
  pkce: true                    # Recommended on by default.
```

The runtime never embeds OAuth client secrets; capsules that need a confidential-client exchange either use PKCE (recommended for desktop apps) or store the secret via `credentials.set` at install time.

#### `oauth.begin`

The capsule calls this to start a flow. Typically inside a `BeginAuth`-shaped tool the runtime invokes on the user's behalf.

```json
{
  "name": "oauth.begin",
  "inputSchema": {
    "type": "object",
    "properties": {
      "extra_params": { "type": "object", "description": "Provider-specific extras (prompt=consent, access_type=offline, …)." }
    }
  },
  "outputSchema": {
    "type": "object", "required": ["flow_id", "url"],
    "properties": {
      "flow_id": { "type": "string", "description": "Opaque handle; appears verbatim on the completion callback." },
      "url": { "type": "string", "description": "The URL the user opens in their browser." }
    }
  }
}
```

The runtime:
- Picks an unused loopback port
- Generates a PKCE verifier + state
- Builds the auth URL from the manifest + the state + a `redirect_uri` of `http://127.0.0.1:<port>/oauth/callback/<flow_id>`
- Stores the in-flight flow (`flow_id`, PKCE verifier, capsule install id) in `runtime.db`
- Returns the URL

The capsule is expected to surface the URL to the user (typically by returning it from the `tools/call` that started the flow). The dashboard's Approvals pane offers a "Open auth URL" button when an ingestor capsule has an in-flight flow.

#### Runtime → capsule callback: `loamss.oauth.completed`

When the user's browser hits the loopback, the runtime exchanges the code for tokens itself (it has everything: token endpoint, client_id from env, PKCE verifier). Then it calls a capsule-registered callback with the tokens. The capsule decides how to persist them.

```json
{
  "method": "loamss.oauth.completed",
  "params": {
    "flow_id":       "flw-01HVZ...",
    "access_token":  "ya29.a0...",
    "refresh_token": "1//0gM...",
    "token_type":    "Bearer",
    "expires_at":    "2026-05-25T23:47:00Z",
    "scope":         "https://www.googleapis.com/auth/calendar.readonly"
  }
}
```

The capsule's callback handler typically does:

```typescript
runtime.on("loamss.oauth.completed", async ({ refresh_token, expires_at, scope }) => {
  await runtime.credentials.set("refresh_token", refresh_token, { expires_at });
  await runtime.credentials.set("scope", scope);
});
```

The access token is short-lived; the refresh token is what survives.

#### Why does the runtime do the token exchange?

Three reasons:

1. **PKCE verifier never leaves the runtime.** The capsule asked for a flow; the runtime is the only party that needs the verifier to complete it. The capsule never sees it.
2. **The capsule doesn't need network egress to the provider's token endpoint just for the exchange.** Reduces the capsule's attack surface and `network:` capability scope.
3. **One implementation of the OAuth exchange.** Every capsule benefits from runtime-side hardening (rate limiting, retry, TLS pinning if we add it).

The capsule still does the resource-side API calls (`https://www.googleapis.com/calendar/v3/...`) itself — that's where the connector logic lives.

#### Token refresh

When the capsule needs an access token, it calls a new tool:

```json
{
  "name": "oauth.refresh",
  "description": "Exchange the stored refresh token for a fresh access token. The runtime knows the token endpoint from the manifest.",
  "outputSchema": {
    "type": "object",
    "properties": {
      "access_token": { "type": "string" },
      "expires_at":   { "type": "string", "format": "date-time" }
    }
  }
}
```

The runtime reads the refresh token from `credentials.get("refresh_token")`, posts to the token endpoint, returns the new access token. If the refresh fails (expired refresh token, revoked grant), it returns an error with code `oauth.refresh_failed` — the capsule should surface this to the user via the existing approvals/notifications path so they re-authenticate.

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
  - capability: credentials.read
    rationale: Reads stored OAuth tokens to call the Calendar API.
  - capability: credentials.write
    rationale: Persists OAuth tokens after authentication.
  - capability: network.fetch
    rationale: Calls https://www.googleapis.com/calendar/v3/.

ingestor:
  source_id: source:calendar
  schedule:
    interval: 15m
    initial: 30s
  on_trigger: sync

oauth:
  provider: google
  authorization_endpoint: https://accounts.google.com/o/oauth2/v2/auth
  token_endpoint: https://oauth2.googleapis.com/token
  scopes:
    - https://www.googleapis.com/auth/calendar.readonly
  client_id_env: GOOGLE_OAUTH_CLIENT_ID
  pkce: true

tools:
  - name: sync
    description: Sync calendar events since the last cursor.
    input_schema: { type: object }
  - name: authenticate
    description: Begin the OAuth flow. The user gets a URL to open.
    input_schema: { type: object }
```

### Capsule code (TypeScript, sketch)

```typescript
import { createCapsule, defineTool, manifest } from "@loamss/sdk";

const cap = createCapsule({
  manifest,
  tools: [
    defineTool({
      name: "authenticate",
      description: "Begin the OAuth flow.",
      inputSchema: { type: "object" },
      handler: async (_, { runtime }) => {
        const { url } = await runtime.oauth.begin();
        return { url, message: "Open this URL to authorize." };
      },
    }),
    defineTool({
      name: "sync",
      description: "Sync calendar events since the last cursor.",
      inputSchema: { type: "object" },
      handler: async (_, { runtime }) => {
        const { access_token } = await runtime.oauth.refresh();
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

cap.on("loamss.oauth.completed", async ({ refresh_token, expires_at }, { runtime }) => {
  await runtime.credentials.set("refresh_token", refresh_token, { expires_at });
});

cap.start();
```

### Lifecycle (analogous to the in-tree source lifecycle from `sources.md`)

```
loamss capsule install ./calendar-ingestor
   → manifest validated; user approves perms incl. credentials.{read,write}, network.fetch
   → ingestor source_id registered alongside in-tree sources
   → capsule subprocess started (persistent run mode)

User clicks "Authenticate" in the dashboard's Sources pane
   → runtime calls capsule's `authenticate` tool
     → capsule calls `oauth.begin`
       → runtime mints flow_id + auth URL + opens loopback listener
   → capsule returns URL
   → dashboard shows "Open this URL" button

User opens URL, approves in Google
   → loopback captures code at http://127.0.0.1:<port>/oauth/callback/<flow_id>
   → runtime exchanges code for tokens
   → runtime fires loamss.oauth.completed at capsule
     → capsule's handler calls `credentials.set("refresh_token", ...)`

15 seconds later: scheduler initial tick
   → runtime invokes capsule's `sync` tool
     → capsule calls `oauth.refresh`, then `cursor.get`
     → capsule fetches events from Google
     → capsule calls `memory.upsert` for each event
     → capsule calls `cursor.set` with the new syncToken
   → returns { records_added: 47 }
   → runtime emits audit.source.sync.completed

Every 15m thereafter: scheduler ticks → invokes sync → …

loamss source remove source:calendar
   → runtime stops the capsule subprocess
   → runtime deletes the credential blobs (credentials, cursor, in-flight oauth)
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
