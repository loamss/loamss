# sdk/typescript/CLAUDE.md

Subsystem context for Claude Code sessions working in `sdk/typescript/`.

## What lives here

The TypeScript SDK for building Loamss capsules. Published (eventually) as `@loamss/sdk`. Currently in-repo; the `@loamss` org on npm is reserved but not published-to until Phase 2.

## What's done

- `src/transport.ts` — MCP-over-stdio (newline-delimited JSON-RPC 2.0)
- `src/jsonrpc.ts` — wire types + `RPCError` + standard error codes
- `src/manifest.ts` — types mirroring capsule-spec.md
- `src/capsule.ts` — `createCapsule({ manifest, tools }).start()`
- `src/tool.ts` — `defineTool` builder + `ToolContext`
- `src/runtime.ts` — `RuntimeClient` (the capsule's view of the runtime via callbacks)
- `src/content.ts` — `text`, `json`, `image`, `result`, `errorResult` content helpers
- `examples/hello-world/` — minimal capsule + capsule.yaml
- `test/{transport,capsule,runtime}.test.ts` — 14 tests, bun test runner

## Conventions

### Style

- Strict TypeScript (`strict`, `noUncheckedIndexedAccess`, etc.)
- ES Modules + `verbatimModuleSyntax` false
- No third-party deps in the published surface
- Bun is the dev runtime; production should run under Bun, Node 20+, or Deno

### Stdout is reserved

The runtime reads MCP frames from a capsule's stdout. Any debug output must go to stderr. The SDK provides `runtime.log()` (sends to runtime as `logging/message` notification) — capsule code should never `console.log(...)` to stdout. A stray `console.log` corrupts the JSON-RPC stream and the runtime will disconnect.

### Tests

- `bun test` is the runner
- **Important**: `bun test` has a known issue with `TransformStream` (hangs in the runner; works standalone). Tests use the `makePipe()` helper in `test/pipe.ts` instead — a controller-backed ReadableStream/WritableStream pair that behaves the same way.
- In-memory pipes drive transport behavior without spawning subprocesses

### Variance

The tool registry stores `Tool<any, any>` internally because Tool is contravariant in `Input` and we want a heterogeneous array. `defineTool<I, O>` preserves the precise types for IDE ergonomics; the runtime registry erases them.

## Adding a new feature

Follow the substrate-layer order:

1. **Spec change first** — if a wire-level addition (new MCP method, new content type), update `mcp-surface.md` / `capsule-spec.md` at the repo root.
2. **Runtime support** — implement in `runtime/internal/mcp/` first.
3. **SDK support** — then wrap in the TypeScript surface.

The SDK should NEVER expose primitives the runtime doesn't speak. If a feature isn't in the runtime, it doesn't belong here yet.

## What's expected to land next

In rough priority order:

1. **MCP client library** — for Path-B apps pairing with a user's Loamss (currently only the capsule-side wire protocol is wrapped; HTTP+SSE client pairing is unbuilt)
2. **Tutorial** — "Build Your First Capsule" walking through install + run + grants
3. **Workspace setup** — bun workspaces so `examples/*` can `import "@loamss/sdk"` instead of relative paths
4. **Python SDK** — same surface, same wire protocol; deferred until TS surface is stable
