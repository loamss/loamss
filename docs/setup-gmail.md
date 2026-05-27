# Setting up `source:gmail`

> **Transitional connector.** This connector mirrors data **from
> Gmail** into your Loamss. It exists because most users still have
> years of email in Gmail today. The long-term shape Loamss
> optimizes for is a **native email app** that uses your Loamss as
> its backing store — see [`../native-apps.md`](../native-apps.md).
> If you're building such an app, you don't need this connector;
> your app writes mail into Loamss directly. If you have legacy
> Gmail data you want in your Loamss now, this guide gets you
> there.

The Gmail connector authenticates against Google via OAuth 2.0 and pulls messages into your Loamss runtime. Before you can run `loamss source authenticate gmail-personal`, you need to register Loamss as a "Desktop application" OAuth client in your own Google Cloud project. That's a one-time step per Loamss install.

This guide walks through it. Estimated time: 10 minutes.

> **What's at stake.** Google's OAuth client identifies *Loamss running on your machine* to Google. Loamss never sees your Gmail password and never gets a "logged-in" cookie — the only thing that survives the handshake is a refresh token, which Google can revoke from your account at any time (`myaccount.google.com/permissions`).

## 1 — Create a Google Cloud project (or pick an existing one)

1. Open <https://console.cloud.google.com/>.
2. Either create a new project (`Loamss-Personal` is a fine name) or pick an existing one. A project is just a billing/quota boundary; the Gmail API is free up to 1 billion quota units per day, which a personal install will never hit.

## 2 — Enable the Gmail API

1. From the project console, search for **"Gmail API"** in the top search bar.
2. Click the result and hit **Enable**.

You'll see a confirmation page with quota numbers. Personal usage stays under the free tier.

## 3 — Configure the OAuth consent screen

If this is a new project, Google asks you to configure the consent screen before you can create credentials.

1. Go to **APIs & Services → OAuth consent screen**.
2. Pick **External** (unless you have Google Workspace). External + Testing is fine for personal use — Loamss never goes through verification because no one else is using your OAuth client.
3. Fill in the required fields:
   - App name: `Loamss` (or whatever you'll recognize)
   - User support email: your address
   - Developer contact: same
4. Click **Save and Continue** through Scopes (you can add scopes later or leave blank — Loamss requests them at runtime), Test users, and Summary.
5. On **Test users**, add your Google account email. While the app is in "Testing" status, only test-user accounts can complete the OAuth flow.

## 4 — Create the OAuth client

1. Go to **APIs & Services → Credentials**.
2. Click **Create Credentials → OAuth client ID**.
3. **Application type: Desktop app.** This matters — Desktop-type clients accept loopback redirects (`http://127.0.0.1:<any port>`), which is what Loamss uses. Web-type clients require a fixed redirect URI registered in advance.
4. Name: `Loamss Desktop` (anything; only you see it).
5. Click **Create**.

Google shows you the **Client ID** and **Client secret**. Copy both — you'll paste them into the next command.

## 5 — Add the source to Loamss

```bash
loamss source add source:gmail \
    --name gmail-personal \
    --config client_id=<paste-client-id>.apps.googleusercontent.com \
    --config client_secret=GOCSPX-<paste-client-secret>
```

Expected output:

```
✓ Added source "gmail-personal" (source:gmail, src_01H...)
  Next: loamss source authenticate gmail-personal
```

> **Note on secrets.** The OAuth `client_secret` is sensitive but it is *not* a user secret in the strict sense — it identifies the Loamss-Gmail app to Google. Anyone who steals it can impersonate your Loamss install when talking to Google, but they still need a separate user-side consent to get an access token to your data. Loamss masks the value in `source show` output. For shared deployments, prefer creating a fresh OAuth client per environment rather than reusing one.

## 6 — Run the auth flow

```bash
loamss source authenticate gmail-personal
```

What happens:

1. Loamss starts a one-shot HTTP listener on `http://127.0.0.1:<random-port>/`.
2. Loamss prints a long Google URL. Open it in any browser that has your Google account active.
3. Google's consent screen appears. The first time, you'll see a "This app isn't verified" warning — click **Advanced → Go to Loamss (unsafe)**. This warning exists because your OAuth client is in Testing status; only your test-user accounts can get past it, which is exactly the boundary you want.
4. Approve the requested scope (`gmail.readonly` by default).
5. Google redirects to the loopback URL. The Loamss listener captures the code, shuts down, and prints `✓ Authenticated source "gmail-personal"`.

If you're on a headless server (no browser local to the machine), you have two options today:

1. **SSH tunnel** the loopback port: `ssh -L 9876:127.0.0.1:9876 user@server`. Run `loamss source authenticate gmail-personal` on the server; the loopback listener picks a random port, but you can capture it from the printed URL and re-tunnel. (Awkward — a fixed-port option is on the roadmap.)
2. **Run the auth flow locally**, then copy `<data_dir>/storage/sources/gmail-personal/credentials.json` to the server. The credential file is portable across machines that share the storage adapter's key.

A first-class code-paste fallback for fully headless deployments lands later; modern Google flows discourage out-of-band redirects, so it isn't the default.

## 7 — First sync

```bash
loamss source sync gmail-personal
```

The first sync is capped at `max_full_sync` messages (default 1000). Subsequent syncs use Gmail's History API for incremental fetch — they only download changes since the last cursor and are typically very fast.

Expected output:

```
✓ Synced "gmail-personal": 1000 added, 0 updated, 14823901 bytes, 0 errors (8.2s)
```

To override the cap on first sync:

```bash
loamss source remove gmail-personal --yes
loamss source add source:gmail --name gmail-personal \
    --config client_id=... --config client_secret=... \
    --config max_full_sync=50000
loamss source authenticate gmail-personal
loamss source sync gmail-personal
```

To scope ingestion to a Gmail search query (useful for testing or for users who only want certain mail in their substrate):

```bash
--config query="from:newsletters@example.com"
--config query="label:Important"
--config query="after:2026/01/01"
```

The `query` value uses Gmail's standard search syntax — same as what the Gmail web UI accepts.

## What lives where after a sync

Once a sync completes, you'll find:

```
<data_dir>/storage/sources/gmail-personal/
└── messages/
    ├── 18f3a2b9c4e1d5f6.eml
    ├── 18f3a2bc7891e0a3.eml
    └── ... (one EML per message)

<data_dir>/memory.db
└── memory entries keyed (namespace=gmail-personal, id=<message_id>)
    with subject + snippet content + provider-specific metadata
```

The `.eml` files are raw RFC822 — open them with any mail client to confirm the content survived round-tripping. The memory entries are what organizer capsules later read to build entity-resolved views (people, threads, projects); for now, the entries sit there until you install a capsule that consumes them.

## Common issues

**"This app isn't verified."** Expected. Your OAuth client is in Testing status and never needs to leave it for personal use — Google only requires verification for clients used by external users.

**"invalid_grant" on the second sync after a long pause.** Refresh tokens issued to Testing-status apps expire after 7 days of inactivity. Re-run `loamss source authenticate gmail-personal` to get a fresh refresh token. For a stable personal setup, move the OAuth client to "In production" status when prompted (no review needed for private use).

**Port already in use on the loopback callback.** Rare; the listener uses a kernel-assigned port (`127.0.0.1:0`), so collision means something raced it. Re-run the command.

**Sync says "auth required" right after authentication.** Check that the consent step actually completed — Google sometimes redirects back to the consent screen if the test-user list doesn't include the account that completed the flow. Verify your account is listed under **APIs & Services → OAuth consent screen → Test users**.

**"User must re-authenticate" later.** Refresh token revoked. Either the user removed Loamss from <https://myaccount.google.com/permissions>, or the token rotated and an old version is cached. Re-run `loamss source authenticate gmail-personal`.

## Scopes Loamss requests

Default: `https://www.googleapis.com/auth/gmail.readonly`

That grants list + get + history — everything Loamss needs to ingest. Loamss does NOT request `gmail.modify` or `gmail.send`; write-back via the Gmail connector is out of scope for v0.1.

To override the scope (advanced; useful if you want to lock Loamss to a subset like `gmail.metadata`):

```bash
--config scope=https://www.googleapis.com/auth/gmail.metadata
```

`gmail.metadata` gives headers and labels but not bodies. Loamss adapts — messages will appear in memory with metadata but no snippet/body.

## Revoking access

Two paths, equivalent in outcome:

```bash
loamss source remove gmail-personal --yes
```

— drops Loamss's stored credentials and stops the source from talking to Google. Or:

Open <https://myaccount.google.com/permissions>, find "Loamss" in the list, click **Remove access**. This invalidates the refresh token at Google's end; Loamss notices on the next sync and surfaces an "auth required" error.

Both leave the previously-ingested data in your storage. If you want that gone too, delete `<data_dir>/storage/sources/gmail-personal/`.

## Related

- [`sources.md`](../sources.md) — the Source connector spec
- [`permission-model.md`](../permission-model.md) — how grants scoped by source via `memory.namespace` work
- [`audit-spec.md`](../audit-spec.md) — what gets logged at each lifecycle step
