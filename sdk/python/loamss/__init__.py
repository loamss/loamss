"""Loamss capsule SDK — Python.

Build capsules in Python; wire shape matches the TypeScript SDK
exactly, so the runtime treats the two interchangeably.

Quick start::

    import asyncio
    from loamss import Capsule, Manifest, content, tool

    @tool(name="echo", description="echoes input")
    async def echo(args, ctx):
        return content.json(args)

    async def main():
        capsule = Capsule(
            manifest=Manifest(name="echo-py", version="0.1.0"),
            tools=[echo],
        )
        await capsule.start()

    asyncio.run(main())

See ``sdk/python/examples/hello-world/`` for the canonical
example, and ``capsule-spec.md`` at the repo root for the manifest
+ wire contract.
"""

from . import content
from .capsule import PROTOCOL_VERSION, Capsule, create_capsule
from .jsonrpc import ErrorCodes, JSONRPCError, RPCError
from .manifest import Author, Manifest
from .runtime import ResourceDescriptor, RuntimeClient, ToolDescriptor
from .tool import Tool, ToolContext, ToolHandler, tool
from .transport import Transport, TransportStreams

__all__ = [
    "Author",
    "Capsule",
    "ErrorCodes",
    "JSONRPCError",
    "Manifest",
    "PROTOCOL_VERSION",
    "RPCError",
    "ResourceDescriptor",
    "RuntimeClient",
    "Tool",
    "ToolContext",
    "ToolDescriptor",
    "ToolHandler",
    "Transport",
    "TransportStreams",
    "content",
    "create_capsule",
    "tool",
]
