"""Transport tests.

We use ``asyncio.StreamReader`` / ``asyncio.StreamWriter`` over an
in-memory ``BufferedProtocol`` pair (via
:func:`asyncio.open_connection`) to give the transport a real
stream pair to read/write — same idea as the TS SDK's
``makePipe()`` helper.
"""

from __future__ import annotations

import asyncio
import json

import pytest

from loamss.jsonrpc import RPCError
from loamss.transport import Transport, TransportStreams

# --- in-memory pipe helper -------------------------------------------------


def make_memory_streams() -> (
    tuple[TransportStreams, TransportStreams]
):
    """Build a back-to-back pair of stream pairs.

    Returns (capsule_side, runtime_side). Anything the capsule
    writes shows up on the runtime's reader and vice versa.
    Each side reads what the other side writes.
    """

    cap_to_run_reader = asyncio.StreamReader()
    run_to_cap_reader = asyncio.StreamReader()

    cap_to_run_proto = asyncio.StreamReaderProtocol(cap_to_run_reader)
    run_to_cap_proto = asyncio.StreamReaderProtocol(run_to_cap_reader)

    # Build feeder writers — we drive bytes directly via
    # feed_data() so we don't need a real subprocess.
    class _Feeder:
        def __init__(self, target: asyncio.StreamReader) -> None:
            self._target = target

        def write(self, data: bytes) -> None:
            self._target.feed_data(data)

        async def drain(self) -> None:
            await asyncio.sleep(0)

        def close(self) -> None:
            self._target.feed_eof()

        async def wait_closed(self) -> None:
            await asyncio.sleep(0)

    # The transport calls writer.write and writer.drain only.
    cap_writer = _Feeder(cap_to_run_reader)
    run_writer = _Feeder(run_to_cap_reader)

    # Use the protocol fields to keep type checking quiet, even
    # though we never feed through them.
    _ = cap_to_run_proto, run_to_cap_proto

    capsule = TransportStreams(reader=run_to_cap_reader, writer=cap_writer)
    runtime = TransportStreams(reader=cap_to_run_reader, writer=run_writer)
    return capsule, runtime


async def _read_one_frame(reader: asyncio.StreamReader) -> dict:
    line = await reader.readline()
    return json.loads(line.decode())


# --- tests ------------------------------------------------------------------


@pytest.mark.asyncio
async def test_inbound_request_is_routed_to_handler() -> None:
    capsule_side, runtime_side = make_memory_streams()
    seen: list[tuple[str, dict]] = []

    async def handler(method: str, params):
        seen.append((method, params))
        return {"echo": params}

    t = Transport(capsule_side, handler)
    task = asyncio.create_task(t.start())

    # Send a request from the runtime side.
    payload = json.dumps(
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "ping",
            "params": {"x": 1},
        }
    ) + "\n"
    runtime_side.writer.write(payload.encode())

    # Read the response the capsule wrote back.
    frame = await asyncio.wait_for(
        _read_one_frame(runtime_side.reader), timeout=1
    )
    assert frame["id"] == 1
    assert frame["result"] == {"echo": {"x": 1}}
    assert seen == [("ping", {"x": 1})]

    t.stop()
    runtime_side.writer.close()
    await asyncio.wait_for(task, timeout=1)


@pytest.mark.asyncio
async def test_capsule_request_awaits_response() -> None:
    capsule_side, runtime_side = make_memory_streams()

    async def handler(method: str, params):
        # Capsule's handler shouldn't be called in this test; if
        # it is, the test will fail with this raise.
        raise AssertionError(f"unexpected inbound call: {method}")

    t = Transport(capsule_side, handler)
    task = asyncio.create_task(t.start())

    # Fire a capsule→runtime request.
    pending = asyncio.create_task(t.request("memory.show", {"id": "x"}))

    # Read the request the capsule wrote.
    sent = await asyncio.wait_for(_read_one_frame(runtime_side.reader), timeout=1)
    assert sent["method"] == "memory.show"
    rpc_id = sent["id"]

    # Reply from the runtime side.
    runtime_side.writer.write(
        (
            json.dumps({"jsonrpc": "2.0", "id": rpc_id, "result": {"ok": True}})
            + "\n"
        ).encode()
    )

    result = await asyncio.wait_for(pending, timeout=1)
    assert result == {"ok": True}

    t.stop()
    runtime_side.writer.close()
    await asyncio.wait_for(task, timeout=1)


@pytest.mark.asyncio
async def test_handler_rpc_error_returns_structured_response() -> None:
    capsule_side, runtime_side = make_memory_streams()

    async def handler(method: str, params):
        raise RPCError(-32002, "user approval required", data={"approval_id": "apr-1"})

    t = Transport(capsule_side, handler)
    task = asyncio.create_task(t.start())

    runtime_side.writer.write(
        (json.dumps({"jsonrpc": "2.0", "id": 1, "method": "x"}) + "\n").encode()
    )
    frame = await asyncio.wait_for(_read_one_frame(runtime_side.reader), timeout=1)
    assert frame["error"]["code"] == -32002
    assert frame["error"]["data"] == {"approval_id": "apr-1"}

    t.stop()
    runtime_side.writer.close()
    await asyncio.wait_for(task, timeout=1)


@pytest.mark.asyncio
async def test_generic_exception_becomes_internal_error() -> None:
    capsule_side, runtime_side = make_memory_streams()

    async def handler(method: str, params):
        raise RuntimeError("kaboom")

    t = Transport(capsule_side, handler)
    task = asyncio.create_task(t.start())

    runtime_side.writer.write(
        (json.dumps({"jsonrpc": "2.0", "id": 1, "method": "x"}) + "\n").encode()
    )
    frame = await asyncio.wait_for(_read_one_frame(runtime_side.reader), timeout=1)
    # -32603 is the JSON-RPC InternalError fallback the transport
    # wraps unknown exceptions in.
    assert frame["error"]["code"] == -32603

    t.stop()
    runtime_side.writer.close()
    await asyncio.wait_for(task, timeout=1)


@pytest.mark.asyncio
async def test_eof_rejects_pending_requests() -> None:
    capsule_side, runtime_side = make_memory_streams()

    async def handler(method: str, params):
        return None

    t = Transport(capsule_side, handler)
    task = asyncio.create_task(t.start())

    pending = asyncio.create_task(t.request("memory.show"))

    # Drain the request that just went out.
    await asyncio.wait_for(_read_one_frame(runtime_side.reader), timeout=1)

    # Close the inbound stream. The transport should reject
    # outstanding pending requests with a ConnectionError.
    capsule_side.reader.feed_eof()

    with pytest.raises(ConnectionError):
        await asyncio.wait_for(pending, timeout=1)

    await asyncio.wait_for(task, timeout=1)
