"""Runtime client — the capsule's view of the Loamss runtime.

When a capsule wants to call back into the runtime (memory.query,
files.read, model.call, …) it goes through this client. The wire
shape is identical to the TS SDK; we just provide a Pythonic
namespace API (``runtime.tools.call(...)`` instead of a single
flat method).
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from .jsonrpc import RPCError
from .transport import Transport


@dataclass
class ToolDescriptor:
    """An entry in the runtime's ``tools/list`` response."""

    name: str
    description: str | None = None


@dataclass
class ResourceDescriptor:
    """An entry in the runtime's ``resources/list`` response."""

    uri: str
    name: str | None = None
    description: str | None = None


class _ToolsNamespace:
    """``runtime.tools.*`` — list and call runtime-side tools.

    The runtime exposes a flat set of tools (memory.show, threads.list,
    model.call, …); a capsule calls them by name. Per-call
    permission gating happens on the runtime side based on the
    capsule's principal — this client just speaks the wire.
    """

    def __init__(self, transport: Transport) -> None:
        self._t = transport

    async def list(self) -> list[ToolDescriptor]:
        res = await self._t.request("tools/list")
        items = res.get("tools", []) if isinstance(res, dict) else []
        return [
            ToolDescriptor(
                name=str(t.get("name", "")),
                description=t.get("description"),
            )
            for t in items
        ]

    async def call(self, name: str, args: dict[str, Any] | None = None) -> Any:
        """Call a runtime-side tool. Returns the raw ToolResult
        (typically ``{ "content": [...] }`` or
        ``{ "content": [...], "isError": True }``). Re-raises
        :class:`RPCError` on JSON-RPC errors — capsules that want to
        distinguish "user approval required" (-32002) from other
        failures catch ``RPCError`` and inspect ``.code``.
        """

        if not name:
            raise RPCError(-32602, "tools.call: name is required")
        return await self._t.request(
            "tools/call", {"name": name, "arguments": args or {}}
        )


class _ResourcesNamespace:
    """``runtime.resources.*`` — list and read runtime-side resources."""

    def __init__(self, transport: Transport) -> None:
        self._t = transport

    async def list(self) -> list[ResourceDescriptor]:
        res = await self._t.request("resources/list")
        items = res.get("resources", []) if isinstance(res, dict) else []
        return [
            ResourceDescriptor(
                uri=str(r.get("uri", "")),
                name=r.get("name"),
                description=r.get("description"),
            )
            for r in items
        ]

    async def read(self, uri: str) -> Any:
        if not uri:
            raise RPCError(-32602, "resources.read: uri is required")
        return await self._t.request("resources/read", {"uri": uri})


class RuntimeClient:
    """The capsule's view of the runtime, returned to handlers as
    ``ctx.runtime``. Wraps the transport's request side and
    exposes the same namespace shape the TS SDK does so
    capsule code ports between languages cleanly.
    """

    def __init__(self, transport: Transport) -> None:
        self._t = transport
        self.tools = _ToolsNamespace(transport)
        self.resources = _ResourcesNamespace(transport)

    def log(
        self,
        level: str,
        msg: str,
        extra: dict[str, Any] | None = None,
    ) -> None:
        """Send a ``logging/message`` notification to the runtime.

        Levels follow slog: ``debug | info | warn | error``. The
        notification is fire-and-forget — logging must never crash
        a handler — so errors are swallowed.
        """

        params: dict[str, Any] = {"level": level, "message": msg}
        if extra:
            params["extra"] = extra
        # We can't await here (this is a sync API), so we schedule
        # the send on the running loop. If there isn't one (e.g.
        # capsule is logging during shutdown) we drop the message.
        try:
            import asyncio

            loop = asyncio.get_running_loop()
            loop.create_task(self._safe_notify("logging/message", params))
        except RuntimeError:
            pass

    async def _safe_notify(self, method: str, params: Any) -> None:
        import contextlib

        # Logging must never crash a handler — broad suppression
        # is the contract here, not an oversight.
        with contextlib.suppress(Exception):
            await self._t.notify(method, params)


__all__ = ["RuntimeClient", "ToolDescriptor", "ResourceDescriptor"]
