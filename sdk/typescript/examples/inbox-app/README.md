# inbox-app example

A minimal Path-B app that pairs with a user's Loamss, lists what tools the user granted it, queries memory, and subscribes to the SSE stream.

This is the smallest interesting client-side use of `@loamss/sdk` — the symmetric companion to the [`hello-world` capsule example](../hello-world/) on the server side.

## Run it

Terminal 1 — start a Loamss runtime:

```bash
loamss start
```

Terminal 2 — generate a pairing code via the runtime's CLI:

```bash
loamss client pair --name "Inbox App"
# Prints something like:
#   Pairing code for "Inbox App":
#     5QUK-5EPE
```

Terminal 3 — pair the example app once:

```bash
cd sdk/typescript/examples/inbox-app
bun src/pair.ts http://127.0.0.1:7777 5QUK-5EPE
# ✓ Paired client cli-01H...
#   token saved to ./inbox-app.token
```

Then run the app:

```bash
bun src/run.ts
# Connected as client cli-01H... (Inbox App)
#
# Tools available (4):
#   - audit.read — ...
#   - client.info — ...
#   - memory.query — ...
#   - memory.show — ...
#
# memory.query → permission denied (issue a memory.read grant)
#
# Subscribing to SSE for 5 seconds (Ctrl-C to stop earlier)...
#   [hello] {"server":"loamss","version":"dev","protocolVersion":"2025-03-26"}
```

The "permission denied" is expected — a freshly-paired client has no grants. To grant `memory.read`:

```bash
loamss grant create \
    --principal-kind client \
    --principal-id <cli-01H...> \
    --capability memory.read
```

Then re-run `bun src/run.ts` and `memory.query` will succeed.

## What's in here

| File | Purpose |
| --- | --- |
| `src/pair.ts` | One-time pairing dance: redeems a code, persists the token |
| `src/run.ts` | Authenticated runtime use: tools.list, tools.call, subscribe |
| `inbox-app.token` (gitignored) | The bearer token. NEVER commit this. |

## Why two scripts?

In a real app, "pair" and "run" are typically different UI surfaces:

- **Pair** is a one-time onboarding flow ("Connect to your Loamss" → button → user pastes a code → token persists).
- **Run** is every subsequent session — the app reads the persisted token and acts.

Splitting the example mirrors this. Production apps don't keep both code paths in the same screen.
