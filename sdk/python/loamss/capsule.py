"""Top-level capsule entry point.

A capsule's ``main.py`` constructs a :class:`Capsule` and calls
``await capsule.start()``. That:

1. Opens the stdio transport (or whatever streams were passed).
2. Runs the MCP handshake: ``initialize`` → reply with the
   capsule's name/version/capabilities; ``initialized`` →
   notification.
3. Serves ``tools/list`` and ``tools/call`` against the
   capsule's tool registry.
4. Blocks until the runtime closes stdin (graceful shutdown) or
   the capsule calls :meth:`Capsule.stop`.

Mirrors ``createCapsule().start()`` in the TS SDK so capsule code
ports cleanly between languages.
"""

from __future__ import annotations

import asyncio
import logging
import sys
from typing import Any

from .content import error_result
from .jsonrpc import ErrorCodes, RPCError
from .manifest import Manifest
from .runtime import RuntimeClient
from .tool import Tool, ToolContext
from .transport import Transport, TransportStreams

logger = logging.getLogger(__name__)


# The protocol version we declare in the initialize response.
# Matches the version the runtime's MCP handler expects.
PROTOCOL_VERSION = "2025-03-26"


class Capsule:
    """A running capsule. Construct then call :meth:`start`.

    ``streams`` defaults to the process's stdin/stdout (the only
    case in production). Tests inject in-memory streams via
    :meth:`from_streams`.
    """

    def __init__(
        self,
        manifest: Manifest,
        tools: list[Tool],
        *,
        streams: TransportStreams | None = None,
    ) -> None:
        if not manifest.name:
            raise ValueError("Capsule requires a manifest with a non-empty name")
        self._manifest = manifest
        self._tools: dict[str, Tool] = {}
        for t in tools:
            if t.name in self._tools:
                raise ValueError(f"duplicate tool name: {t.name}")
            self._tools[t.name] = t
        self._streams = streams
        self._transport: Transport | None = None
        self._initialized = False

    async def start(self) -> None:
        """Run the capsule's MCP loop until the runtime closes
        stdin (which happens on graceful shutdown).
        """

        streams = self._streams or await _stdio_streams()
        self._transport = Transport(streams, self._handle)
        runtime = RuntimeClient(self._transport)
        # Tool handlers need access to the runtime client; we
        # construct one ToolContext per capsule and hand it to
        # every handler.
        self._ctx = ToolContext(runtime=runtime)
        await self._transport.start()

    def stop(self) -> None:
        """Break out of the MCP loop. Idempotent."""

        if self._transport is not None:
            self._transport.stop()

    # --- internal -----------------------------------------------------------

    async def _handle(self, method: str, params: Any) -> Any:
        # The MCP method set is small enough that an if-ladder is
        # clearer than a dispatch table. Order: handshake first,
        # then the two tool methods.
        if method == "initialize":
            return self._handle_initialize(params)
        if method == "initialized":
            # Notification; no result expected. We don't track the
            # state machine strictly — any subsequent request will
            # be served. The runtime sends this; we just
            # acknowledge by returning normally.
            return None
        if method == "ping":
            # MCP ping is a no-op heartbeat the runtime may send.
            return {}
        if method == "tools/list":
            return {"tools": [t.descriptor() for t in self._tools.values()]}
        if method == "tools/call":
            return await self._handle_tools_call(params)
        # Unknown method — let the transport turn this into a
        # JSON-RPC error response.
        raise RPCError(
            ErrorCodes.METHOD_NOT_FOUND,
            f"method not found: {method}",
        )

    def _handle_initialize(self, params: Any) -> dict[str, Any]:
        # We accept any client protocol version; if the runtime
        # ever wants strict negotiation that'd happen here. The
        # response shape matches what the runtime emits in the
        # other direction.
        self._initialized = True
        return {
            "protocolVersion": PROTOCOL_VERSION,
            "capabilities": {"tools": {"listChanged": False}},
            "serverInfo": self._manifest.to_initialize_info(),
        }

    async def _handle_tools_call(self, params: Any) -> Any:
        if not isinstance(params, dict):
            raise RPCError(
                ErrorCodes.INVALID_PARAMS, "tools/call: params must be an object"
            )
        name = params.get("name")
        if not name or not isinstance(name, str):
            raise RPCError(ErrorCodes.INVALID_PARAMS, "tools/call: name required")
        args = params.get("arguments", {}) or {}
        if not isinstance(args, dict):
            raise RPCError(
                ErrorCodes.INVALID_PARAMS, "tools/call: arguments must be an object"
            )

        tool = self._tools.get(name)
        if tool is None:
            raise RPCError(ErrorCodes.UNKNOWN_TOOL, f"unknown tool: {name}")

        try:
            result = await tool.handler(args, self._ctx)
        except RPCError:
            # Re-raise so the transport renders the right code.
            raise
        except Exception as e:  # noqa: BLE001 — broad: we want graceful errors
            logger.exception("tool handler raised on %s", name)
            return error_result(f"tool {name} failed: {e}")
        # Auto-wrap a bare content-block into a ToolResult so the
        # 90% case where a handler returns ``content.json(...)``
        # just works.
        if isinstance(result, dict) and "content" in result:
            return result
        if isinstance(result, dict) and result.get("type") in {"text", "image"}:
            return {"content": [result]}
        # Fallback: serialise unknown shapes as JSON text.
        from .content import json as json_block

        return {"content": [json_block(result)]}


# --- top-level convenience -------------------------------------------------


def create_capsule(*, manifest: Manifest, tools: list[Tool]) -> Capsule:
    """Mirror of the TS SDK's ``createCapsule()``. Builds a
    :class:`Capsule`; the caller then awaits ``capsule.start()``.
    """

    return Capsule(manifest=manifest, tools=tools)


async def _stdio_streams() -> TransportStreams:
    """Wrap the process's stdin/stdout in asyncio Stream wrappers.

    The fiddly part: stdin must be wrapped in a non-blocking
    :class:`asyncio.StreamReader`; stdout needs a writer that
    drains. We use ``loop.connect_read_pipe`` + ``connect_write_pipe``
    which is the supported way on POSIX. Tests inject their own
    streams via the ``streams=`` keyword and never call this.
    """

    loop = asyncio.get_running_loop()
    reader = asyncio.StreamReader()
    protocol = asyncio.StreamReaderProtocol(reader)
    await loop.connect_read_pipe(lambda: protocol, sys.stdin)
    writer_transport, writer_protocol = await loop.connect_write_pipe(
        asyncio.streams.FlowControlMixin, sys.stdout
    )
    writer = asyncio.StreamWriter(
        writer_transport,
        writer_protocol,
        None,
        loop,
    )
    return TransportStreams(reader=reader, writer=writer)


__all__ = ["Capsule", "create_capsule", "PROTOCOL_VERSION"]
