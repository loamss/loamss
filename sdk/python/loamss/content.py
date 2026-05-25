"""Content-block helpers.

MCP tool results are sequences of "content blocks" — text, image,
JSON, etc. The runtime + spec mandate a structured block list
rather than a free-form payload so clients can render mixed
content cleanly. This module gives capsule authors the same
ergonomic helpers the TypeScript SDK exposes.

Capsules can always emit raw dicts; these helpers just save
typing.
"""

from __future__ import annotations

import json as _json
from typing import Any


def text(content: str) -> dict[str, Any]:
    """A text-typed content block."""

    return {"type": "text", "text": content}


def json(payload: Any) -> dict[str, Any]:
    """A text block carrying a JSON-encoded payload.

    Useful when the result is structured data but the client
    expects the standard text-block shape. The TS SDK's ``json()``
    helper produces an identical payload.
    """

    return {"type": "text", "text": _json.dumps(payload)}


def image(data_url: str) -> dict[str, Any]:
    """An image content block. ``data_url`` should be a full
    ``data:image/...;base64,...`` URL.
    """

    return {"type": "image", "data": data_url}


def result(*content: dict[str, Any]) -> dict[str, Any]:
    """Wrap content blocks in the runtime's ToolResult shape."""

    return {"content": list(content)}


def error_result(message: str, *, details: Any = None) -> dict[str, Any]:
    """A tool result flagged ``isError: true``. The runtime surfaces
    this to the calling client as a non-fatal tool error (distinct
    from a JSON-RPC -32xxx, which is a transport-level failure).
    """

    out: dict[str, Any] = {
        "content": [text(message)],
        "isError": True,
    }
    if details is not None:
        out["content"].append(json(details))
    return out


__all__ = ["text", "json", "image", "result", "error_result"]
