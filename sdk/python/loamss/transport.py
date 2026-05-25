"""MCP-over-stdio transport.

The Loamss runtime spawns a capsule subprocess and pipes JSON-RPC
2.0 frames over stdin/stdout. Framing is newline-delimited JSON
(each message on its own line, terminated by ``\\n``) — same as
the runtime-side transport in ``runtime/internal/mcp/transport.go``
and the TypeScript SDK's ``transport.ts``.

The transport multiplexes both directions on the same pipes:

- Runtime → capsule: tools/list, tools/call, …  (the ``handler``)
- Capsule → runtime: memory.query, files.read, … (the ``request``
  coroutine)

Pending capsule→runtime requests are tracked by their JSON-RPC id;
the response is routed back to the awaiting :class:`asyncio.Future`.

IMPORTANT: stdout is RESERVED for MCP frames. Any debug/log output
must go to stderr (use :meth:`RuntimeClient.log` or
``print(..., file=sys.stderr)``). A stray ``print()`` will corrupt
the stream and the runtime will disconnect the capsule.
"""

from __future__ import annotations

import asyncio
import json
import logging
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any

from .jsonrpc import (
    ErrorCodes,
    JSONRPCError,
    JSONRPCMessage,
    JSONRPCNotification,
    JSONRPCRequest,
    JSONRPCResponse,
    RPCError,
    parse_message,
)

logger = logging.getLogger(__name__)


# Handler is invoked for every inbound request and notification. It
# returns the result for the JSON-RPC response (or raises an
# RPCError / generic Exception). Notifications discard the return.
RequestHandler = Callable[[str, Any], Awaitable[Any]]


@dataclass
class TransportStreams:
    """The two halves of a stdio transport. In production these wrap
    the capsule subprocess's stdin/stdout pipes; in tests they're
    in-memory ``asyncio.StreamReader`` / ``asyncio.StreamWriter`` pairs.
    """

    # Capsules read frames from `reader` (the runtime's writes) and
    # write frames to `writer` (the runtime reads them). The names
    # are from the capsule's perspective.
    reader: asyncio.StreamReader
    writer: asyncio.StreamWriter


class Transport:
    """One-per-capsule transport.

    Spin up with :meth:`start`; it runs forever, reading frames from
    the reader and dispatching to the handler. Call :meth:`request`
    to send a capsule→runtime request and ``await`` the response.
    Call :meth:`stop` to break out of the read loop cleanly.

    Concurrency model: one task reads frames; handlers run inline
    (not in their own tasks) which matches the TS SDK's behaviour
    and keeps the wire-order semantics simple. A handler that
    awaits a long operation will block subsequent frames from being
    processed — capsules that need parallel handling should spawn
    their own tasks inside the handler.
    """

    def __init__(self, streams: TransportStreams, handler: RequestHandler) -> None:
        self._streams = streams
        self._handler = handler
        self._next_id = 0
        self._pending: dict[int | str, asyncio.Future[Any]] = {}
        self._stopped = asyncio.Event()
        self._read_task: asyncio.Task[None] | None = None
        # Serialise writes — concurrent writers would interleave
        # bytes mid-frame and corrupt the stream.
        self._write_lock = asyncio.Lock()

    async def start(self) -> None:
        """Run the read loop until the reader closes or :meth:`stop`
        is called. Exceptions bubble up.
        """

        if self._read_task is not None:
            raise RuntimeError("Transport.start called twice")
        self._read_task = asyncio.create_task(self._read_loop())
        await self._stopped.wait()
        # Ensure the read task is collected even on clean shutdown.
        if not self._read_task.done():
            self._read_task.cancel()
            import contextlib

            with contextlib.suppress(asyncio.CancelledError):
                await self._read_task

    def stop(self) -> None:
        """Break out of the read loop on the next iteration. Idempotent."""

        self._stopped.set()

    async def request(self, method: str, params: Any = None) -> Any:
        """Send a JSON-RPC request and await the response.

        Used for capsule→runtime calls (e.g., ``runtime.tools.call``).
        Raises :class:`RPCError` if the runtime responds with an
        error; otherwise returns ``result``.
        """

        rpc_id = self._next_id
        self._next_id += 1

        future: asyncio.Future[Any] = asyncio.get_running_loop().create_future()
        self._pending[rpc_id] = future

        try:
            await self._send(
                JSONRPCRequest(id=rpc_id, method=method, params=params)
            )
            return await future
        finally:
            self._pending.pop(rpc_id, None)

    async def notify(self, method: str, params: Any = None) -> None:
        """Send a notification (request without an id, no response)."""

        await self._send(JSONRPCNotification(method=method, params=params))

    # --- internal -----------------------------------------------------------

    async def _read_loop(self) -> None:
        try:
            while not self._stopped.is_set():
                line = await self._streams.reader.readline()
                if not line:
                    # EOF — the runtime closed our stdin.
                    self._stopped.set()
                    self._reject_pending(ConnectionError("transport stream closed"))
                    return
                stripped = line.strip()
                if not stripped:
                    continue
                try:
                    raw = json.loads(stripped)
                except json.JSONDecodeError as e:
                    logger.warning("transport: bad JSON frame: %s", e)
                    continue
                try:
                    msg = parse_message(raw)
                except ValueError as e:
                    logger.warning("transport: %s", e)
                    continue
                await self._dispatch(msg)
        except asyncio.CancelledError:
            raise
        except Exception:  # pragma: no cover - protective catch-all
            logger.exception("transport: read loop crashed")
            self._stopped.set()
            self._reject_pending(ConnectionError("transport read loop crashed"))
            raise

    async def _dispatch(self, msg: JSONRPCMessage) -> None:
        if isinstance(msg, JSONRPCResponse):
            self._route_response(msg)
            return
        if isinstance(msg, JSONRPCNotification):
            # Notifications discard return value; exceptions don't
            # become responses (no id to respond to). We log and
            # continue.
            try:
                await self._handler(msg.method, msg.params)
            except Exception:
                logger.exception(
                    "transport: handler raised on notification %s", msg.method
                )
            return
        if isinstance(msg, JSONRPCRequest):
            await self._handle_request(msg)

    async def _handle_request(self, req: JSONRPCRequest) -> None:
        try:
            result = await self._handler(req.method, req.params)
            await self._send(JSONRPCResponse(id=req.id, result=result))
            return
        except RPCError as e:
            err = JSONRPCError(code=e.code, message=str(e), data=e.data)
        except Exception as e:  # noqa: BLE001 — broad: wrap as internal-error
            logger.exception("transport: handler raised on %s", req.method)
            err = JSONRPCError(code=ErrorCodes.INTERNAL_ERROR, message=str(e))
        await self._send(JSONRPCResponse(id=req.id, error=err))

    def _route_response(self, resp: JSONRPCResponse) -> None:
        if resp.id is None:
            logger.warning("transport: response without id, dropping")
            return
        future = self._pending.pop(resp.id, None)
        if future is None or future.done():
            logger.warning("transport: response for unknown id %r", resp.id)
            return
        if resp.is_error:
            assert resp.error is not None
            future.set_exception(
                RPCError(resp.error.code, resp.error.message, resp.error.data)
            )
        else:
            future.set_result(resp.result)

    async def _send(self, msg: JSONRPCMessage) -> None:
        payload = json.dumps(msg.to_dict(), separators=(",", ":"))
        data = (payload + "\n").encode("utf-8")
        async with self._write_lock:
            self._streams.writer.write(data)
            await self._streams.writer.drain()

    def _reject_pending(self, exc: Exception) -> None:
        for future in self._pending.values():
            if not future.done():
                future.set_exception(exc)
        self._pending.clear()


__all__ = ["Transport", "TransportStreams", "RequestHandler"]
