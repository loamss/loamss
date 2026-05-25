"""Tool definitions.

A capsule exposes one or more :class:`Tool` instances to the
runtime via ``tools/list``. When the runtime dispatches
``tools/call``, the matching tool's handler runs with the call
arguments and a :class:`ToolContext` (the capsule's view of the
runtime).

Two ways to construct a tool:

    @tool(name="echo", description="echo input")
    async def echo(args, ctx):
        return content.json({"got": args})

or, using the dataclass-style constructor:

    Tool(
        name="echo",
        description="echo input",
        input_schema={"type": "object"},
        handler=echo_handler,
    )

Both produce equivalent runtime behaviour. The decorator form is
the recommended ergonomic.
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from typing import Any

from .runtime import RuntimeClient


@dataclass
class ToolContext:
    """Passed to every tool handler. ``runtime`` is the capsule's
    callback client (memory.query, files.read, …); other fields
    can be added later without breaking handlers.
    """

    runtime: RuntimeClient


# Handler signature. Returns either a content-block dict (the SDK
# auto-wraps into ToolResult) or a complete ToolResult dict.
ToolHandler = Callable[[dict[str, Any], ToolContext], Awaitable[Any]]


@dataclass
class Tool:
    """A capsule-defined tool. The ``input_schema`` is the JSON
    Schema the runtime echoes to MCP clients via ``tools/list``;
    full per-call schema validation runs at invocation time and
    on the runtime side, so this SDK doesn't enforce it.
    """

    name: str
    handler: ToolHandler
    description: str = ""
    input_schema: dict[str, Any] = field(default_factory=lambda: {"type": "object"})

    def descriptor(self) -> dict[str, Any]:
        """Render the entry the runtime expects in
        ``tools/list``. The field shape mirrors the upstream MCP
        Tool descriptor.
        """

        out: dict[str, Any] = {"name": self.name}
        if self.description:
            out["description"] = self.description
        if self.input_schema:
            out["inputSchema"] = self.input_schema
        return out


def tool(
    *,
    name: str,
    description: str = "",
    input_schema: dict[str, Any] | None = None,
) -> Callable[[ToolHandler], Tool]:
    """Decorator turning an async function into a :class:`Tool`.

    Usage::

        @tool(name="echo", description="echoes its input")
        async def echo(args, ctx):
            return content.json(args)
    """

    schema = input_schema if input_schema is not None else {"type": "object"}

    def decorator(fn: ToolHandler) -> Tool:
        return Tool(
            name=name,
            description=description,
            input_schema=schema,
            handler=fn,
        )

    return decorator


__all__ = ["Tool", "ToolContext", "ToolHandler", "tool"]
