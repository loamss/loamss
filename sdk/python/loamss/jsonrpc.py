"""JSON-RPC 2.0 wire types — minimal subset Loamss capsules use.

MCP is layered on JSON-RPC 2.0; every message a capsule reads or
writes on stdio matches one of these shapes. Mirrors
``sdk/typescript/src/jsonrpc.ts`` field for field; the Go runtime's
transport speaks the same wire and the schemas have to match.

We use dataclasses (not TypedDicts) because the JSON-RPC message
objects are constructed programmatically inside the SDK and a
dataclass gives both ergonomic ``Request(id=..., method=..., ...)``
construction and free ``__repr__`` for debugging.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Final

# The literal version string every JSON-RPC 2.0 message carries.
JSONRPC_VERSION: Final[str] = "2.0"

# Request id: int per the spec, but MCP implementations commonly use
# strings, and the Go runtime emits both. We accept either.
JSONRPCId = int | str


@dataclass
class JSONRPCError:
    """Structured error block in a JSON-RPC error response."""

    code: int
    message: str
    data: Any = None

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {"code": self.code, "message": self.message}
        if self.data is not None:
            out["data"] = self.data
        return out

    @classmethod
    def from_dict(cls, raw: dict[str, Any]) -> JSONRPCError:
        return cls(
            code=int(raw["code"]),
            message=str(raw.get("message", "")),
            data=raw.get("data"),
        )


class RPCError(Exception):
    """Structured error capsule handlers raise to return a specific
    JSON-RPC code. Plain ``Exception`` raises are wrapped as -32603
    InternalError by the transport — the same fallback the TS SDK
    uses, so capsules ported between languages behave identically.
    """

    def __init__(self, code: int, message: str, data: Any = None) -> None:
        super().__init__(message)
        self.code = code
        self.data = data


@dataclass
class JSONRPCRequest:
    id: JSONRPCId
    method: str
    params: Any = None
    jsonrpc: str = JSONRPC_VERSION

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {
            "jsonrpc": self.jsonrpc,
            "id": self.id,
            "method": self.method,
        }
        if self.params is not None:
            out["params"] = self.params
        return out


@dataclass
class JSONRPCNotification:
    """A request with no id — the server (capsule) does not respond."""

    method: str
    params: Any = None
    jsonrpc: str = JSONRPC_VERSION

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {"jsonrpc": self.jsonrpc, "method": self.method}
        if self.params is not None:
            out["params"] = self.params
        return out


@dataclass
class JSONRPCResponse:
    """Either a success (``result``) or an error (``error``); exactly
    one of the two is populated. We model the union as a single
    dataclass rather than a sum type because Python's pattern
    matching across dataclass variants is awkward and the on-the-wire
    JSON is a single object either way.
    """

    id: JSONRPCId | None
    result: Any = None
    error: JSONRPCError | None = None
    jsonrpc: str = JSONRPC_VERSION

    @property
    def is_error(self) -> bool:
        return self.error is not None

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {"jsonrpc": self.jsonrpc, "id": self.id}
        if self.error is not None:
            out["error"] = self.error.to_dict()
        else:
            out["result"] = self.result
        return out


# JSONRPCMessage covers every inbound frame; the transport sniffs
# which kind it is.
JSONRPCMessage = JSONRPCRequest | JSONRPCNotification | JSONRPCResponse


class ErrorCodes:
    """Standard JSON-RPC 2.0 error codes plus the MCP/Loamss codes
    the runtime uses. Capsules return these via :class:`RPCError`
    when they want to signal a specific failure mode rather than
    raising a generic ``Exception`` (which becomes -32603).
    """

    PARSE_ERROR: Final[int] = -32700
    INVALID_REQUEST: Final[int] = -32600
    METHOD_NOT_FOUND: Final[int] = -32601
    INVALID_PARAMS: Final[int] = -32602
    INTERNAL_ERROR: Final[int] = -32603

    # Loamss/MCP-specific (mirror `runtime/internal/mcp` codes).
    PERMISSION_DENIED: Final[int] = -32001
    APPROVAL_REQUIRED: Final[int] = -32002
    UNKNOWN_TOOL: Final[int] = -32003
    BACKEND_ERROR: Final[int] = -32099


def parse_message(raw: dict[str, Any]) -> JSONRPCMessage:
    """Sniff which message variant ``raw`` is and return the
    parsed dataclass. The Go runtime + TS SDK use the same
    classification rules: presence-of-``id`` distinguishes
    request from notification; presence-of-``result``/``error``
    distinguishes a response.
    """

    has_id = "id" in raw
    has_method = "method" in raw
    has_result_or_error = "result" in raw or "error" in raw

    if has_id and has_result_or_error:
        return JSONRPCResponse(
            id=raw.get("id"),
            result=raw.get("result"),
            error=(
                JSONRPCError.from_dict(raw["error"]) if "error" in raw else None
            ),
        )
    if has_id and has_method:
        return JSONRPCRequest(
            id=raw["id"], method=raw["method"], params=raw.get("params")
        )
    if has_method:
        return JSONRPCNotification(
            method=raw["method"], params=raw.get("params")
        )
    raise ValueError(f"unrecognised JSON-RPC frame: {raw!r}")


# Field-name re-exports so callers can write ``params=...`` without
# importing the dataclasses just for keyword names. Keeps the
# transport tests readable.
__all__ = [
    "JSONRPC_VERSION",
    "JSONRPCId",
    "JSONRPCError",
    "JSONRPCMessage",
    "JSONRPCNotification",
    "JSONRPCRequest",
    "JSONRPCResponse",
    "RPCError",
    "ErrorCodes",
    "parse_message",
    "field",
]
