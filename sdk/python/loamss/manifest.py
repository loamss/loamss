"""Manifest types mirroring ``capsule-spec.md``.

Capsules declare themselves to the runtime via ``capsule.yaml`` —
parsed by the runtime, not by this SDK. These dataclasses model
the subset the capsule passes to :func:`create_capsule` for the
MCP ``initialize`` handshake (name, version, …). They are also the
shape used internally to advertise tools to the runtime when it
calls ``tools/list``.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass
class Author:
    name: str
    url: str | None = None


@dataclass
class Manifest:
    """The subset of the capsule manifest a running capsule needs at
    runtime. The full ``capsule.yaml`` is parsed by the runtime's
    installer; this struct is what the capsule constructs in code
    for the ``initialize`` handshake.
    """

    name: str
    version: str
    description: str | None = None
    author: Author | None = None
    # We don't enforce the schema of ``manifest_extra`` here — the
    # SDK passes whatever fields the capsule wanted to surface in
    # the initialize response through verbatim.
    extra: dict[str, Any] = field(default_factory=dict)

    def to_initialize_info(self) -> dict[str, Any]:
        """Render the ``serverInfo`` block of the MCP initialize
        response. Conservative — only name + version are required
        by the spec.
        """

        return {"name": self.name, "version": self.version}


__all__ = ["Manifest", "Author"]
