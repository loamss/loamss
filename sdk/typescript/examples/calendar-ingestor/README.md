# `calendar-ingestor` — OAuth-using reference ingestor

The canonical OAuth ingestor for Loamss. Pulls Google Calendar
events into memory on the runtime's scheduler, using Google's
`syncToken` protocol for incremental updates. Counterpart to
[`rss-ingestor`](../rss-ingestor/) — same lifecycle, but adds the
full OAuth surface.

## What it exercises

| Primitive | This capsule uses it for |
|---|---|
| Manifest `oauth:` block with `provider: google` | Tells the runtime to use the well-known Google OAuth config |
| Per-user OAuth client store | One client_id covers every Google capsule the user installs |
| `oauth.access_token` MCP tool | The capsule's only OAuth API — returns a bearer, transparently refreshing |
| Runtime-driven browser flow | "Connect Google Calendar" button → browser → loopback → tokens stored |
| `cursor.get` / `cursor.set` | Per-calendar Google `syncToken` (the protocol's incremental-sync handle) |
| `memory.upsert` | One memory entry per event, in namespace `calendar-<id>` |
| Scheduled trigger | Runtime ticks the capsule's `sync` tool every 15 minutes |

## One-time setup: a Google OAuth client

You need an OAuth client ID from Google Cloud Console. **Loamss
never sees it until you paste it** — it lives in your Google
project, you control it, and any Google ingestor capsule installed
later reuses the same one.

1. Go to [console.cloud.google.com](https://console.cloud.google.com)
   and create (or pick) a project.
2. APIs & Services → Enabled APIs → enable **Google Calendar API**.
3. APIs & Services → OAuth consent screen → User Type: External.
   Add your own Google account under "Test users." Scopes: skip
   the optional ones; Loamss requests `calendar.readonly` at
   pairing time.
4. APIs & Services → Credentials → Create Credentials → OAuth
   client ID → Application type: **Desktop app**. Name it
   "Loamss Calendar" or similar. (Desktop-type clients support
   `http://127.0.0.1` arbitrary-port redirects, which is what
   Loamss's loopback listener uses.)
5. Copy the client ID (and the secret if Google shows one —
   desktop clients on PKCE don't need it, but Loamss accepts it
   if you give it).

Tell Loamss about the client:

```bash
curl -X POST http://127.0.0.1:7777/console/oauth/clients/google \
  -H 'Content-Type: application/json' \
  -d '{"client_id":"abc.apps.googleusercontent.com"}'
```

Or do it from the dashboard once the OAuth-clients pane lands
(in progress).

## Install the capsule

```bash
cd sdk/typescript/examples/calendar-ingestor
bun run build
loamss capsule install ./
```

At install the runtime:

1. Validates the manifest. `oauth.provider: google` is well-known
   so the endpoints come from `runtime/internal/oauth/providers.go`
   — no inline URLs needed.
2. Grants `memory.write` (scoped to `calendar-event` entities) and
   `external.http` (scoped to `www.googleapis.com`).
3. Inserts a row in the `sources` table with `name=calendar-ingestor`
   and `adapter_id=source:calendar`.
4. Starts the capsule subprocess.

The Sources pane now shows the ingestor with status "Needs auth."

## Connect Google Calendar

Kick off the browser flow:

```bash
curl -X POST 'http://127.0.0.1:7777/console/oauth/begin?capsule=calendar-ingestor'
```

The runtime:

1. Allocates an ephemeral 127.0.0.1 port for the callback.
2. Generates a PKCE verifier + state.
3. Opens your browser to Google's consent screen.
4. Captures the redirect on the loopback.
5. Exchanges the code for tokens (the PKCE verifier never leaves
   the runtime).
6. Stores the `refresh_token` in the capsule's encrypted
   credentials blob (at `capsules/calendar-ingestor/credentials.json`).

After you click "Allow" in the browser, the listener returns a
"✓ Connected — close this tab" page and shuts down. Tokens are
now in place.

Poll the status:

```bash
curl 'http://127.0.0.1:7777/console/oauth/status?capsule=calendar-ingestor'
# {"capsule":"calendar-ingestor","connected":true}
```

## What syncing looks like

10 seconds after install (the `initial` delay in the manifest):

```
scheduler tick
  → host.Client("calendar-ingestor").CallTool("sync", {})
      → capsule calls oauth.access_token → runtime returns
        cached bearer (or refreshes transparently)
      → capsule calls cursor.get → per-calendar syncToken map
      → capsule calls Google: GET .../events?syncToken=... (first
        sync: no syncToken; Google sends a full-sync payload and
        a brand-new syncToken)
      → for each event:
          capsule calls memory.upsert
              { namespace: "calendar-primary",
                id: <google event id>,
                content: "Standup\nTue 9:00 → 9:30\n...",
                metadata: { calendar_id, status, start, end,
                            attendees, participants, entities,
                            ... } }
      → capsule calls cursor.set with the new syncToken
      → capsule returns { records_added, records_updated,
                          bytes_ingested, errors,
                          per_calendar: [...] }
  → scheduler writes summary into source.Store.last_sync_summary
  → scheduler emits source.sync.completed audit entry
```

Every 15 minutes after that: scheduler ticks → bearer refreshes
in the runtime if needed → events sync.

## Configure which calendars to ingest

The reference build syncs `primary` (your default Google calendar).
Override at run time with an env var until the per-capsule config
surface lands:

```bash
LOAMSS_CALENDARS="primary,family@group.calendar.google.com" \
  loamss start
```

## What lands in memory

One entry per event, in namespace `calendar-<slug>`. For
recurring events, each instance is its own entry (singleEvents=true).
Cancelled events are upserted with `status=cancelled` in metadata —
the memory layer doesn't delete them, but consumers can filter on
`metadata.status`.

Each entry's metadata includes:

- `calendar_id` — `primary`, `family@group.calendar.google.com`, …
- `start`, `end` — RFC3339 timestamps or date-only for all-day events
- `all_day` — true for date-only events
- `status` — `confirmed` / `tentative` / `cancelled`
- `attendees` — list of email addresses
- `participants` — structured: `[{email, name, role}]`. Surfaces
  these to the memory layer's entity resolver so "Sarah from this
  meeting" stitches with "Sarah in this email."
- `entities` — `["calendar-event"]` — locks the memory layer to
  the declared entity type
- `url` — Google Calendar deep link

## Inspect what came in

```bash
loamss source show calendar-ingestor
# Shows the most recent sync summary + counters

loamss audit tail --type source.sync.completed
# Stream of completed syncs with counters + via=capsule_ingestor
```

## Handling revoked access

If you revoke the capsule from Google's [third-party app
permissions](https://myaccount.google.com/permissions):

- Next sync fails. The capsule calls `oauth.access_token`; the
  runtime tries to refresh; Google returns `invalid_grant`.
- `oauth.access_token` returns the structured
  `oauth.reauth_required` error.
- The capsule's `sync` reports `errors: 1` with the message
  surfaced.
- A future dashboard chip will surface this as a "Re-authenticate"
  prompt; for now, hit `/console/oauth/begin?capsule=calendar-ingestor`
  again.

The cursor is preserved — once you re-auth, the next sync picks up
where the last one left off.

## What it does NOT do (deliberately)

- **No write access to Calendar.** Manifest scope is
  `calendar.readonly`. To actually create events, install an
  actuator capsule with `calendar.write` + per-call approval.
- **No proxying.** Every API call goes capsule → Google. The
  runtime issues bearer tokens but doesn't sit in the request
  path.
- **No event content beyond what the API returns.** Attachments,
  meeting links to third-party providers (Zoom, Teams) are
  preserved in the metadata as-is; the capsule doesn't follow them.
