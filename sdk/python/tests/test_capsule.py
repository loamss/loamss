"""End-to-end capsule tests.

We construct a real :class:`Capsule`, hand it an in-memory stream
pair, drive the MCP handshake + tools/list + tools/call from the
"runtime side", and assert the responses.
"""

from __future__ import annotations

import asyncio
import json

import pytest

from loamss import Capsule, Manifest, content, tool

# Re-use the in-memory pipe helper from test_transport.
from .test_transport import _read_one_frame, make_memory_streams


@pytest.mark.asyncio
async def test_handshake_and_tools_list() -> None:
    capsule_side, runtime_side = make_memory_streams()

    @tool(name="echo", description="echoes input")
    async def echo(args, ctx):
        return content.json(args)

    capsule = Capsule(
        manifest=Manifest(name="test-capsule", version="0.1.0"),
        tools=[echo],
        streams=capsule_side,
    )
    task = asyncio.create_task(capsule.start())

    # initialize
    runtime_side.writer.write(
        (
            json.dumps(
                {
                    "jsonrpc": "2.0",
                    "id": 1,
                    "method": "initialize",
                    "params": {"protocolVersion": "2025-03-26", "capabilities": {}},
                }
            )
            + "\n"
        ).encode()
    )
    init = await asyncio.wait_for(
        _read_one_frame(runtime_side.reader), timeout=1
    )
    assert init["id"] == 1
    assert init["result"]["serverInfo"] == {
        "name": "test-capsule",
        "version": "0.1.0",
    }
    assert init["result"]["protocolVersion"] == "2025-03-26"

    # tools/list
    list_frame = json.dumps({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
    runtime_side.writer.write((list_frame + "\n").encode())
    listing = await asyncio.wait_for(
        _read_one_frame(runtime_side.reader), timeout=1
    )
    assert listing["id"] == 2
    tools = listing["result"]["tools"]
    assert len(tools) == 1
    assert tools[0]["name"] == "echo"
    assert tools[0]["description"] == "echoes input"

    capsule.stop()
    runtime_side.writer.close()
    await asyncio.wait_for(task, timeout=1)


@pytest.mark.asyncio
async def test_tools_call_runs_handler_and_returns_content() -> None:
    capsule_side, runtime_side = make_memory_streams()

    @tool(name="add")
    async def add(args, ctx):
        return content.json({"sum": args["a"] + args["b"]})

    capsule = Capsule(
        manifest=Manifest(name="math", version="0.1.0"),
        tools=[add],
        streams=capsule_side,
    )
    task = asyncio.create_task(capsule.start())

    # initialize (skip; we don't actually require it for tool calls
    # in the current implementation, matching the TS SDK)
    runtime_side.writer.write(
        (
            json.dumps(
                {
                    "jsonrpc": "2.0",
                    "id": 1,
                    "method": "tools/call",
                    "params": {"name": "add", "arguments": {"a": 2, "b": 3}},
                }
            )
            + "\n"
        ).encode()
    )
    resp = await asyncio.wait_for(_read_one_frame(runtime_side.reader), timeout=1)
    assert resp["id"] == 1
    text = resp["result"]["content"][0]["text"]
    assert json.loads(text) == {"sum": 5}

    capsule.stop()
    runtime_side.writer.close()
    await asyncio.wait_for(task, timeout=1)


@pytest.mark.asyncio
async def test_unknown_tool_returns_error_code() -> None:
    capsule_side, runtime_side = make_memory_streams()

    capsule = Capsule(
        manifest=Manifest(name="empty", version="0.1.0"),
        tools=[],
        streams=capsule_side,
    )
    task = asyncio.create_task(capsule.start())

    runtime_side.writer.write(
        (
            json.dumps(
                {
                    "jsonrpc": "2.0",
                    "id": 1,
                    "method": "tools/call",
                    "params": {"name": "ghost"},
                }
            )
            + "\n"
        ).encode()
    )
    resp = await asyncio.wait_for(_read_one_frame(runtime_side.reader), timeout=1)
    # -32003 is UnknownTool in our ErrorCodes class.
    assert resp["error"]["code"] == -32003

    capsule.stop()
    runtime_side.writer.close()
    await asyncio.wait_for(task, timeout=1)


@pytest.mark.asyncio
async def test_handler_exception_becomes_iserror_result() -> None:
    capsule_side, runtime_side = make_memory_streams()

    @tool(name="boom")
    async def boom(args, ctx):
        raise ValueError("nope")

    capsule = Capsule(
        manifest=Manifest(name="oops", version="0.1.0"),
        tools=[boom],
        streams=capsule_side,
    )
    task = asyncio.create_task(capsule.start())

    runtime_side.writer.write(
        (
            json.dumps(
                {
                    "jsonrpc": "2.0",
                    "id": 1,
                    "method": "tools/call",
                    "params": {"name": "boom", "arguments": {}},
                }
            )
            + "\n"
        ).encode()
    )
    resp = await asyncio.wait_for(_read_one_frame(runtime_side.reader), timeout=1)
    # The handler raised a generic exception, not RPCError; the
    # capsule wraps it as a tool-level isError result rather than
    # a JSON-RPC error response.
    assert "error" not in resp
    assert resp["result"]["isError"] is True

    capsule.stop()
    runtime_side.writer.close()
    await asyncio.wait_for(task, timeout=1)


def test_capsule_construction_rejects_duplicate_tools() -> None:
    @tool(name="dup")
    async def t1(args, ctx):
        return content.text("a")

    @tool(name="dup")
    async def t2(args, ctx):
        return content.text("b")

    with pytest.raises(ValueError, match="duplicate"):
        Capsule(
            manifest=Manifest(name="x", version="0.1.0"),
            tools=[t1, t2],
        )


def test_capsule_construction_rejects_empty_name() -> None:
    with pytest.raises(ValueError, match="name"):
        Capsule(
            manifest=Manifest(name="", version="0.1.0"),
            tools=[],
        )
