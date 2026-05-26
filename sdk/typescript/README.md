# @loamss/sdk

TypeScript SDK for building on **Loamss**. Two surfaces, one package:

- **Capsules** — sandboxed extensions that run *inside* a user's Loamss runtime. Use `createCapsule` for these. Speaks MCP-over-stdio as a subprocess the runtime spawns.
- **Path-B apps** — external apps that *pair with* a user's Loamss and call tools through the MCP HTTP surface. Use `pair` + `createClient` for these. Speaks MCP-over-HTTP + SSE with bearer-token auth.

> **Status: v0.1, evolving.** 43 tests pass; the wire protocol tracks Loamss runtime `v0.1` (MCP protocol version `2025-03-26`). Expect breaking changes before v1.0.

## Install

```bash
bun add @loamss/sdk
# or: npm install @loamss/sdk
```

> Note: the `@loamss` org on npm is reserved but not yet published-to. While the SDK is in-repo, capsules can pull it from the workspace path; once Phase 2 ships, `bun add @loamss/sdk` will resolve to npm.

## Hello, world

```ts
// src/index.ts
import { createCapsule, defineTool } from "@loamss/sdk";

const hello = defineTool({
  name: "hello",
  description: "Say hello to someone (or to the world).",
  inputSchema: {
    type: "object",
    properties: { who: { type: "string" } },
  },
  handler: (input: { who?: string }) => `Hello, ${input.who ?? "world"}!`,
});

await createCapsule({
  manifest: {
    name: "com.example.hello-world",
    version: "0.1.0",
    author: { name: "you" },
  },
  tools: [hello],
}).start();
```

Pair with a `capsule.yaml` (see `examples/hello-world/capsule.yaml`) and install:

```bash
loamss capsule install /path/to/capsule
```

The full hello-world is in [`examples/hello-world/`](./examples/hello-world/), along with five more reference capsules:

| Example | Role | Demonstrates |
|---|---|---|
| [`hello-world`](./examples/hello-world/) | minimal | The smallest possible capsule — one tool, no permissions |
| [`daily-brief`](./examples/daily-brief/) | organizer | Reading memory across threads/entities and calling `model.call` to summarize |
| [`approval-demo`](./examples/approval-demo/) | actuator | The `requires_user_approval` consequential-action gate |
| [`inbox-app`](./examples/inbox-app/) | exposer | Exposing structured resources back to MCP clients |
| [`rss-ingestor`](./examples/rss-ingestor/) | ingestor (no-auth) | Scheduled trigger + `cursor.{get,set}` + `memory.upsert` for public feeds |
| [`calendar-ingestor`](./examples/calendar-ingestor/) | ingestor (OAuth) | The full Google OAuth path: `oauth.access_token`, runtime-driven browser flow, transparent refresh |

The two ingestors together (`rss-ingestor` + `calendar-ingestor`) cover every capsule-ingestor primitive the runtime exposes — see [`../../docs/capsule-ingestor-primitives.md`](../../docs/capsule-ingestor-primitives.md) for the design.

## Hello, world (Path-B app)

```ts
import { pair, createClient } from "@loamss/sdk";

// One-time: redeem a code from `loamss client pair --name "..."`.
const result = await pair("http://127.0.0.1:7777", "5QUK-5EPE");
// Persist result.token + result.endpointUrl. The token is shown ONCE.

// Every session afterwards:
const client = createClient({
  endpoint: result.endpointUrl,
  token: result.token,
});

const tools = await client.tools.list();
const hits = await client.tools.call("memory.query", {
  namespace: "gmail-personal",
  limit: 10,
});

// Subscribe to live notifications:
for await (const ev of client.subscribe()) {
  if (ev.event === "resources/updated") {
    // re-render UI…
  }
}
```

The full client example is in [`examples/inbox-app/`](./examples/inbox-app/).

## Concepts

### Capsule

The unit you ship to users. A capsule has a manifest (declares name, version, capabilities it needs), one or more tools, and an entrypoint the runtime spawns as a subprocess.

### Tool

A function the runtime can invoke on behalf of an MCP client (a paired app, an external AI tool, the user's `loamss` CLI). Each tool has:

- A name (mounted by the runtime as `<capsule-name>.<tool-name>`)
- A JSON Schema describing its inputs
- A handler — async function that receives the decoded input + a `ctx` argument

The handler's return value is auto-wrapped into the MCP content shape: strings become text blocks, objects become JSON-encoded text blocks, or you can return an explicit `ToolResult` for full control.

### Runtime callbacks

A handler can call back into the runtime through `ctx.runtime`:

```ts
const memorySummary = defineTool({
  name: "summarize_recent",
  description: "Summarize recent memory entries",
  inputSchema: { type: "object" },
  handler: async (_, { runtime }) => {
    const hits = await runtime.tools.call("memory.query", {
      namespace: "gmail-personal",
      limit: 20,
    });
    return { content: hits.content };
  },
});
```

Every `runtime.tools.call(...)` goes through the runtime's permission engine. Capsules can only call tools the user granted them — there's no implicit trust just because the call originated from inside a capsule.

### Trust + audit

Capsules are sandboxed. The runtime:

- Spawns the capsule with the env + working directory declared in the manifest
- Wires stdio for MCP frames; stdout is reserved for protocol traffic (logs MUST go to stderr — use `runtime.log()` or `console.error`)
- Permission-checks every callback from the capsule against the grants the user issued at install time
- Emits a `tool.invoked` audit entry on every tool call (inbound or callback)

A capsule never sees other capsules' state, the storage adapter's encryption key, or other paired clients' tokens.

## Manifest (capsule.yaml)

The SDK accepts a `CapsuleManifest` object in `createCapsule()` to populate the initialize-handshake response. The on-disk `capsule.yaml` carries the same shape plus packaging metadata (`runtime.entrypoint`, requested permissions, install-time validation). See `capsule-spec.md` at the repo root for the full schema.

## API

### Capsule side (you're building something that runs *inside* a user's Loamss)

| Export | Purpose |
| --- | --- |
| `createCapsule(opts)` | Construct + start the capsule. Returns a `CapsuleHandle`. |
| `defineTool(t)` | Typed-builder for tools. Just forwards the value; exists for IDE ergonomics. |
| `text(s)`, `json(v)`, `image(b64, mime)`, `result(...blocks)`, `errorResult(...blocks)` | Build explicit MCP content blocks. |
| `RPCError(code, msg, data?)` | Throw from a handler to return a specific JSON-RPC error code. |
| `ErrorCodes` | Constants: `PermissionDenied = -32001`, `UnknownTool = -32003`, etc. |
| `Transport`, `processStreams()` | Lower-level transport (custom test streams, advanced use). |

### Client side (you're building something that *pairs with* a user's Loamss)

| Export | Purpose |
| --- | --- |
| `pair(endpoint, code, opts?)` | Redeem a one-time pairing code → bearer token. |
| `createClient(opts)` | Construct an authenticated client. Returns a `LoamssClient`. |
| `client.tools.list()`, `client.tools.call(name, args)` | Discover + invoke runtime tools. |
| `client.resources.list()`, `client.resources.read(uri)` | Read runtime resources. |
| `client.subscribe()` | AsyncIterable of SSE events from the runtime. |
| `client.call(method, params)` | Low-level escape hatch for arbitrary JSON-RPC methods. |
| `AuthorizationError` | Thrown on HTTP 401 (token revoked/expired). |
| `ApprovalRequiredError` | Thrown when a tool needs consequential-action approval. Carries `approvalId` + `capability`. |
| `parseSSE(stream)` | Low-level SSE parser (`createClient.subscribe()` wraps this). |

## Development

```bash
bun install     # install dev deps
bun test        # run tests (Bun's built-in test runner)
bun tsc --noEmit  # typecheck
```

Tests use in-memory pipes to drive transport + capsule behavior without spawning subprocesses; see `test/pipe.ts`.

## License

Apache-2.0 — same as the Loamss runtime.

## Related

- [`capsule-spec.md`](../../capsule-spec.md) — the capsule format
- [`mcp-surface.md`](../../mcp-surface.md) — what the runtime exposes to capsules
- [`permission-model.md`](../../permission-model.md) — the capability framework callbacks go through
- [`extensibility.md`](../../extensibility.md) — anti-patterns to avoid in capsule code
