"""Wire-shape tests for jsonrpc.py.

The contract these lock in: serialising any of our dataclasses
produces bytes the Go runtime + TS SDK both accept, and parsing
the runtime's frames returns the right variant.
"""

from __future__ import annotations

import json

import pytest

from loamss.jsonrpc import (
    JSONRPC_VERSION,
    JSONRPCError,
    JSONRPCNotification,
    JSONRPCRequest,
    JSONRPCResponse,
    RPCError,
    parse_message,
)


def test_request_round_trip() -> None:
    req = JSONRPCRequest(id=1, method="ping", params={"x": 1})
    raw = req.to_dict()
    assert raw == {
        "jsonrpc": JSONRPC_VERSION,
        "id": 1,
        "method": "ping",
        "params": {"x": 1},
    }
    parsed = parse_message(raw)
    assert isinstance(parsed, JSONRPCRequest)
    assert parsed.id == 1
    assert parsed.method == "ping"
    assert parsed.params == {"x": 1}


def test_request_without_params_omits_field() -> None:
    req = JSONRPCRequest(id="a", method="ping")
    assert "params" not in req.to_dict()


def test_notification_has_no_id() -> None:
    note = JSONRPCNotification(method="logging/message", params={"msg": "hi"})
    raw = note.to_dict()
    assert "id" not in raw
    parsed = parse_message(raw)
    assert isinstance(parsed, JSONRPCNotification)
    assert parsed.method == "logging/message"


def test_response_success() -> None:
    resp = JSONRPCResponse(id=1, result={"ok": True})
    raw = resp.to_dict()
    assert raw == {"jsonrpc": JSONRPC_VERSION, "id": 1, "result": {"ok": True}}
    parsed = parse_message(raw)
    assert isinstance(parsed, JSONRPCResponse)
    assert not parsed.is_error
    assert parsed.result == {"ok": True}


def test_response_error() -> None:
    err = JSONRPCError(code=-32600, message="bad request", data={"hint": "x"})
    resp = JSONRPCResponse(id=2, error=err)
    raw = resp.to_dict()
    assert raw["error"] == {
        "code": -32600,
        "message": "bad request",
        "data": {"hint": "x"},
    }
    parsed = parse_message(raw)
    assert isinstance(parsed, JSONRPCResponse)
    assert parsed.is_error
    assert parsed.error is not None
    assert parsed.error.code == -32600


def test_parse_message_rejects_unknown_shape() -> None:
    with pytest.raises(ValueError):
        parse_message({"jsonrpc": "2.0"})  # neither id+method nor id+result


def test_rpc_error_carries_code_and_data() -> None:
    err = RPCError(-32002, "approval required", data={"approval_id": "apr-1"})
    assert str(err) == "approval required"
    assert err.code == -32002
    assert err.data == {"approval_id": "apr-1"}


def test_json_serialisation_is_compact() -> None:
    # The Go runtime expects newline-delimited JSON; whitespace
    # inside frames is fine but the transport writes compact form.
    req = JSONRPCRequest(id=1, method="ping", params={"a": 1, "b": 2})
    line = json.dumps(req.to_dict(), separators=(",", ":"))
    assert "\n" not in line  # one frame per line; no embedded newlines
