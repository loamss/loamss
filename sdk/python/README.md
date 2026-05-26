# Loamss SDK — Python

Python 3.11+ SDK for building Loamss capsules. Same wire shape as the [TypeScript SDK](../typescript/) — the Loamss runtime treats the two interchangeably, so a capsule's choice of language is purely an author preference.

## Status

Phase 2 — surface complete, 19 tests pass. Mirrors the TypeScript SDK exactly: transport, jsonrpc, manifest, tool, runtime, capsule, content. Zero runtime dependencies — everything's stdlib (`asyncio`, `json`, `dataclasses`).

> **Example coverage**: today the Python SDK ships one example (`hello-world`). The TypeScript SDK ships six (including the two reference ingestors and the OAuth-using calendar capsule). The wire protocol is identical — a capsule written in TS can be ported line-for-line — but Python-native ports of the larger examples (`rss-ingestor`, `calendar-ingestor`, `daily-brief`) are not yet here. PRs welcome.

Not yet on PyPI; install from this directory for now.

## Install

From a checkout of this repo:

```bash
# editable install for development
pip install -e .[dev]
```

Once published:

```bash
pip install loamss-sdk
```

## Quick start

```python
import asyncio
from loamss import Capsule, Manifest, content, tool

@tool(name="hello", description="Say hello")
async def hello(args, ctx):
    name = (args.get("name") or "world").strip() or "world"
    return content.text(f"Hello, {name}!")

async def main():
    capsule = Capsule(
        manifest=Manifest(name="hello-py", version="0.1.0"),
        tools=[hello],
    )
    await capsule.start()

asyncio.run(main())
```

The capsule reads MCP frames from stdin and writes responses to stdout. To install + invoke it from a running Loamss runtime, point `loamss capsule install` at a directory containing the script + a `capsule.yaml`. See [`examples/hello-world/`](examples/hello-world/) for the minimum complete setup.

## Surface

| Module | TS equivalent | What it does |
|---|---|---|
| `loamss.jsonrpc` | `src/jsonrpc.ts` | JSON-RPC 2.0 dataclasses, `RPCError`, `ErrorCodes`, `parse_message` |
| `loamss.transport` | `src/transport.ts` | MCP-over-stdio, newline-delimited JSON over async streams |
| `loamss.manifest` | `src/manifest.ts` | `Manifest` + `Author` dataclasses for the initialize handshake |
| `loamss.tool` | `src/tool.ts` | `@tool` decorator, `Tool` dataclass, `ToolContext` |
| `loamss.runtime` | `src/runtime.ts` | `RuntimeClient` — the capsule's callback view (`runtime.tools.call`, `runtime.resources.read`, `runtime.log`) |
| `loamss.capsule` | `src/capsule.ts` | Top-level `Capsule`; handles initialize / tools/list / tools/call |
| `loamss.content` | `src/content.ts` | Content-block builders: `text`, `json`, `image`, `result`, `error_result` |

Any capsule written in TS can be ported line-for-line to Python by swapping names — same method order, same field names on the wire.

## Calling back into the runtime

Inside a tool handler, `ctx.runtime` is the capsule's view of the runtime:

```python
@tool(name="show_recent_threads")
async def show_recent_threads(args, ctx):
    res = await ctx.runtime.tools.call("threads.list", {"limit": 5})
    return res  # forward the runtime's response verbatim
```

Errors raise `RPCError`. Capsules handling user-approval flows catch the `-32002` code:

```python
from loamss import RPCError

try:
    res = await ctx.runtime.tools.call("threads.list")
except RPCError as e:
    if e.code == -32002:  # user approval required
        return content.json({
            "status": "pending",
            "approval_id": (e.data or {}).get("approval_id"),
        })
    raise
```

## Stdout is reserved

The runtime reads MCP frames from a capsule's stdout. **Do not `print()` to stdout from capsule code** — it corrupts the JSON-RPC stream and the runtime disconnects.

Use `ctx.runtime.log(level, msg)` for structured logs (sent as a `logging/message` notification to the runtime, which writes them to the daemon log). For ad-hoc debugging, use `print(..., file=sys.stderr)`.

## Testing

```bash
pip install -e .[dev]
pytest
```

Tests use in-memory stream pairs (no subprocess spawn) — the same pattern the TypeScript SDK's `makePipe()` helper provides. See `tests/test_transport.py` for the helper and `tests/test_capsule.py` for end-to-end handshake + tool-call tests.

## Adding a feature

Follow the same substrate-layer order as the TS SDK:

1. **Spec change first** — wire-level additions (new MCP method, new content type) update `mcp-surface.md` / `capsule-spec.md` at the repo root.
2. **Runtime support** — implement in `runtime/internal/mcp/` first.
3. **TS SDK** — wrap in the TypeScript surface.
4. **Python SDK** — only then mirror here.

The Python SDK never exposes primitives the runtime doesn't speak.

## License

Apache-2.0 (matches the repo root). See [`../../LICENSE`](../../LICENSE).
