# @loamss/sdk

TypeScript SDK for building **Loamss capsules** — sandboxed extensions that run inside a user's Loamss runtime.

A capsule is a subprocess the runtime spawns and talks to over MCP-over-stdio (newline-delimited JSON-RPC 2.0). This SDK handles the framing, lifecycle, manifest types, and runtime callbacks so you can write a capsule in ~30 lines instead of ~500.

> **Status: v0.1, evolving.** The wire protocol tracks Loamss runtime `v0.1` (MCP protocol version `2025-03-26`). Expect breaking changes before v1.0.

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

The full hello-world is in [`examples/hello-world/`](./examples/hello-world/).

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

| Export | Purpose |
| --- | --- |
| `createCapsule(opts)` | Construct + start the capsule. Returns a `CapsuleHandle`. |
| `defineTool(t)` | Typed-builder for tools. Just forwards the value; exists for IDE ergonomics. |
| `text(s)`, `json(v)`, `image(b64, mime)`, `result(...blocks)`, `errorResult(...blocks)` | Build explicit MCP content blocks. |
| `RPCError(code, msg, data?)` | Throw from a handler to return a specific JSON-RPC error code. |
| `ErrorCodes` | Constants: `PermissionDenied = -32001`, `UnknownTool = -32003`, etc. |
| `Transport`, `processStreams()` | Lower-level transport (custom test streams, advanced use). |

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
