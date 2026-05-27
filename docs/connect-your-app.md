# Connect Your App to a User's Loamss

This tutorial walks you through building a **Path-B app** — an external app that pairs with a user's Loamss runtime and drives it through the MCP HTTP surface. By the end you'll have an app that:

1. Walks the user through a one-time pairing flow
2. Stores a bearer token for subsequent sessions
3. Lists what the user granted it, calls tools, handles permission denials gracefully
4. Subscribes to live notifications from the runtime

Estimated time: 20 minutes. Prerequisites: Bun ≥ 1.1, a running `loamss` runtime you can connect to (yours or a test instance).

> **Path A vs Path B — which one is right for you?**
>
> **Path A (native Loamss app — the design center):** your app is
> built around Loamss from day one. The user's Loamss IS your storage
> layer; your backend holds essentially nothing about the user's
> content. This is what the project optimizes for — an email app, a
> notes app, a calendar app, a creator platform, a personal AI
> assistant. See [`../native-apps.md`](../native-apps.md) for the
> architectural pattern and worked examples. **If you're building a
> new product, start there.**
>
> **Path B (this tutorial — pragmatic for existing apps):** your app
> already exists with its own storage and accounts. You add Loamss as
> an *additional* context source so users can optionally pair it.
> Architecture unchanged. Lower lift, narrower payoff. This is how
> the ecosystem warms up — Claude Desktop, ChatGPT, Cursor are all
> Path B integrations today.
>
> The SDK surface is identical for both paths; what differs is whose
> database your data lives in. This tutorial walks Path B because
> it's the simpler entry point. When you're ready for Path A, the
> same `@loamss/sdk` carries forward.

## Part 1 — Pair once

### 1.1 Project skeleton

```bash
mkdir loamss-companion && cd loamss-companion
bun init -y
bun add @loamss/sdk
```

Add `"type": "module"` to `package.json` if it isn't there.

### 1.2 The pairing dance

The runtime issues a bearer token in exchange for a one-time code the user pastes (or scans, or however your UI surfaces it). The token is shown **exactly once** — your app must persist it immediately.

Create `src/pair.ts`:

```ts
import { pair } from "@loamss/sdk";
import { writeFileSync } from "node:fs";

const [, , endpoint, code] = process.argv;
if (!endpoint || !code) {
  console.error("Usage: bun src/pair.ts <runtime-endpoint> <code>");
  console.error("Example: bun src/pair.ts http://127.0.0.1:7777 5QUK-5EPE");
  process.exit(1);
}

const result = await pair(endpoint, code, {
  metadata: { app_name: "loamss-companion", app_version: "0.1.0" },
});

writeFileSync(
  ".loamss-token.json",
  JSON.stringify(
    {
      endpointUrl: result.endpointUrl,
      token: result.token,
      clientId: result.client.id,
    },
    null,
    2,
  ),
  { mode: 0o600 }, // user-read-write only; token is sensitive
);

console.log(`✓ Paired as ${result.client.id}`);
console.log(`  Token saved to .loamss-token.json`);
```

Important security notes baked into the snippet above:

- The token file is `chmod 0600` (user-only). In a real app it lives in your platform's secure storage (Keychain on macOS, DPAPI on Windows, libsecret on Linux, encrypted cookie on web).
- The `metadata` you pass shows up in the user's audit log under `client.paired`. Use it to identify which version of your app paired in March 2026, six months from now.
- Never log the token. Never echo it to stdout. It is a bearer credential — anyone with it can act as your app against this user's Loamss.

### 1.3 Get the user to give you a code

The runtime's CLI shows the user how to generate a code:

```bash
loamss client pair --name "Loamss Companion"
```

Output (the user sees this on *their* machine):

```
Pairing code for "Loamss Companion":

  5QUK-5EPE

Expires at Mon, 24 May 2026 13:00:00 CDT (10m0s from now)

Have the client redeem with:
  loamss client pair complete 5QUK-5EPE
```

The user pastes `5QUK-5EPE` into your app's "Connect" UI (or scans it from a QR; or types it; your UI choice).

### 1.4 Run the pairing

```bash
bun src/pair.ts http://127.0.0.1:7777 5QUK-5EPE
```

Expected:

```
✓ Paired as cli-01KSDHEJGVNZPWDQKMH9YPJ6W1
  Token saved to .loamss-token.json
```

If you get `pair: pairing code not found` or `pair: pairing code expired`, regenerate the code (`loamss client pair ...`) — codes are one-time and live for 10 minutes.

## Part 2 — Use the runtime

### 2.1 The client surface

Create `src/run.ts`:

```ts
import {
  ApprovalRequiredError,
  AuthorizationError,
  createClient,
  RPCError,
} from "@loamss/sdk";
import { readFileSync } from "node:fs";

interface StoredToken {
  endpointUrl: string;
  token: string;
  clientId: string;
}

const stored: StoredToken = JSON.parse(
  readFileSync(".loamss-token.json", "utf8"),
);

const client = createClient({
  endpoint: stored.endpointUrl,
  token: stored.token,
});

// Step 1: discover what the user granted us.
const tools = await client.tools.list();
console.log(`Tools available (${tools.length}):`);
for (const t of tools) {
  console.log(`  ${t.name}`);
}
```

Run it:

```bash
bun src/run.ts
```

Expected:

```
Tools available (4):
  audit.read
  client.info
  memory.query
  memory.show
```

A freshly-paired client gets a baseline of "auth-only" tools — things that don't read user data but let your app introspect itself and the audit log. To do anything meaningful, the user has to grant capabilities.

### 2.2 Get the user to grant a capability

On the user's side:

```bash
loamss grant create \
    --principal-kind client \
    --principal-id cli-01KSDHEJGVNZPWDQKMH9YPJ6W1 \
    --capability memory.read
```

Expected:

```
✓ Granted memory.read to client cli-01KSDHEJGVNZPWDQKMH9YPJ6W1
  scope: (any)
  expires: never
```

The user can narrow scope (`--scope-json '{"namespace":"gmail-personal"}'`), set TTL (`--expires-in 24h`), or mark it as requiring per-call approval (`--requires-approval`). See [`permission-model.md`](../permission-model.md).

### 2.3 Try `memory.query` again

Edit `src/run.ts` to call `memory.query`:

```ts
// ... existing imports + client setup ...

try {
  const result = await client.tools.call("memory.query", {
    query: "",
    limit: 5,
  });
  console.log("\nResults:");
  console.log(result.content[0]?.text ?? "(empty)");
} catch (err) {
  if (err instanceof RPCError && err.code === -32001) {
    console.error("✗ Permission denied. The user hasn't granted memory.read.");
  } else if (err instanceof ApprovalRequiredError) {
    console.error(`✗ Approval required (id=${err.approvalId}).`);
    console.error("  Have the user run: loamss approve", err.approvalId);
  } else if (err instanceof AuthorizationError) {
    console.error("✗ Token rejected. Re-pair.");
  } else {
    throw err;
  }
}
```

Run:

```bash
bun src/run.ts
```

If the user seeded memory (with `loamss memory upsert ...`), you'll see results. If not, you'll see `(empty)`.

### 2.4 Handle the four error modes

Production Path-B apps handle four distinct outcomes from `tools.call`:

| Outcome | What it means | Your response |
| --- | --- | --- |
| Success | Tool ran, result returned | Render it |
| `AuthorizationError` (HTTP 401) | Token revoked / expired | Surface "your Loamss disconnected — reconnect" in your UI |
| `RPCError` with code `-32001` | Permission denied | "Loamss says no" — typically prompt the user to broaden the grant |
| `ApprovalRequiredError` (`-32002`) | Consequential action; needs user approval | Tell the user "approve in your Loamss console", poll for completion, retry |

The snippet above shows the shape. Wrap your real calls in similar try/catch — the four cases mean different UX flows.

## Part 3 — Subscribe to live updates

Polling tools.list every 5 seconds is wasteful. The runtime exposes an SSE stream of notifications.

Add to `src/run.ts`:

```ts
// ... existing code ...

console.log("\nListening for 10 seconds. Watch for events.");
const ctrl = new AbortController();
const timer = setTimeout(() => ctrl.abort(), 10_000);

try {
  for await (const ev of client.subscribe({ signal: ctrl.signal })) {
    console.log(`  [${ev.event}] ${JSON.stringify(ev.data)}`);
  }
} catch (err) {
  if ((err as { name?: string }).name !== "AbortError") throw err;
} finally {
  clearTimeout(timer);
}
```

Run, then in a different terminal trigger some activity by issuing any audited operation:

```bash
# Terminal 2 — issuing a grant generates audit events
loamss client list
```

Back in your app's output, you'll see:

```
Listening for 10 seconds. Watch for events.
  [hello] {"server":"loamss","version":"dev","protocolVersion":"2025-03-26"}
  [ping] {"timestamp":"2026-05-24T18:32:15Z"}
```

The `hello` event fires on connect (gives you the runtime's identity); `ping` fires every 15 seconds. Future events (`resources/updated`, `log/message`) layer on the same channel as the runtime adds them; the wire shape is in place but emission is Phase 2 work.

## Part 4 — A web-app UI sketch

The CLI flow above maps directly into a web app. The shape:

```ts
// In your app's "Connect to Loamss" page:
async function onConnectClick() {
  const code = promptUser("Paste your pairing code:");
  const endpoint = promptUser("Loamss URL:", "http://127.0.0.1:7777");
  const result = await pair(endpoint, code);
  await secureStore.put("loamss-token", JSON.stringify(result));
  navigate("/inbox");
}

// In your app's main session lifecycle:
async function bootClient() {
  const stored = JSON.parse(await secureStore.get("loamss-token"));
  return createClient({ endpoint: stored.endpointUrl, token: stored.token });
}

// In a React/Vue/Solid component:
const client = useLoamssClient();
const messages = await client.tools.call("memory.query", { namespace: "gmail" });
```

The browser fetch API is the same as Node/Bun fetch from the SDK's POV. `@loamss/sdk` works in browsers as-is.

For SSE, browsers have `EventSource`, but the SDK's `client.subscribe()` works fine — it uses `fetch` with `text/event-stream`. (Note: `EventSource` doesn't support custom headers, which would block `Authorization`. The fetch-based approach the SDK uses is the right path in browsers too.)

## Part 5 — What can go wrong

| Symptom | Cause | Fix |
| --- | --- | --- |
| `pair: pairing code not found` | Code typo or already-redeemed | Generate a fresh code with `loamss client pair`. Codes are one-time. |
| `pair: pairing code expired` | More than 10 minutes between generation and redemption | Generate a fresh code. |
| `AuthorizationError` on first request | Token wrong, or the user revoked your client | Have the user check `loamss client list`. If revoked, re-pair. |
| `RPCError(-32601, "method not found")` | You called a tool name that isn't mounted | `client.tools.list()` to see what's actually exposed. |
| Subscribe stream silently closes after a few seconds | Connection reset (proxy idle timeout, network blip) | Wrap in a reconnect-with-backoff loop. The SDK doesn't auto-reconnect; the runtime emits a fresh `hello` on each new connection. |
| Token works for `tools.list` but fails on `tools.call("memory.query")` with `-32001` | Client is paired but holds no `memory.read` grant | User runs `loamss grant create ...` to grant the capability. |
| `ApprovalRequiredError` and you don't know what to do | You called a consequential-action tool (email.send, files.write, etc.) that the user gated with per-call approval | Surface the `approvalId` in your UI; have the user run `loamss approve <id>` (or approve from the console once it ships); poll `client.tools.call("audit.read", { type: "approval.granted", ...})` to detect completion; retry the call. |

## Security checklist for production

Before shipping a Path-B app to real users:

- [ ] Bearer tokens stored in platform-secure storage (Keychain / DPAPI / libsecret / encrypted cookie). Never in localStorage.
- [ ] Tokens NEVER appear in logs, error messages, telemetry, or stack traces.
- [ ] Your app surfaces the user's Loamss endpoint URL prominently — they should be able to see WHICH runtime they're connected to and disconnect from it without uninstalling your app.
- [ ] HTTPS-only when the endpoint isn't `127.0.0.1` (a remote runtime over plain HTTP leaks the bearer token).
- [ ] When a token starts returning `AuthorizationError`, you surface a clear "reconnect" affordance — don't silently retry.
- [ ] Your README explains what capabilities your app needs and why, before the user grants them.
- [ ] You publish a per-app changelog of "what we ask for" — adding a new capability should be a visible change, not a silent expansion.

## Next steps

- **Resources**: `client.resources.list()` + `client.resources.read(uri)` for object reads (storage paths, audit views).
- **Approval flows**: build the polling loop that turns `ApprovalRequiredError` into a clean retry.
- **Multi-runtime**: nothing in the API binds you to one Loamss. An app can pair with several instances (the user's own + a household member's, for example) and route reads accordingly.
- **Path A**: if your app is small enough or new enough to be Loamss-native (the user's Loamss IS the storage), see [`native-apps.md`](../native-apps.md) for the architectural pattern.

## Related

- [`mcp-surface.md`](../mcp-surface.md) — the wire protocol behind `client.*`
- [`permission-model.md`](../permission-model.md) — how grants get scoped, narrowed, and approved
- [`scenarios.md`](../scenarios.md) — concrete Path-A and Path-B use cases the runtime supports
- [`topology.md`](../topology.md) — front-facing-app deployment shape (the production picture)
- [`sdk/typescript/`](../sdk/typescript/) — SDK source + the `examples/inbox-app/` worked example
- [`build-your-first-capsule.md`](./build-your-first-capsule.md) — the companion guide for the other side of MCP (capsules running inside Loamss)
