"""hello-world — the smallest Loamss capsule, in Python.

Exposes one tool, ``hello``, that echoes a name back in a sentence.
Mirrors ``sdk/typescript/examples/hello-world`` to make the
language-port equivalence visible at a glance.

To run locally (requires the loamss-sdk package installed or on
PYTHONPATH from sdk/python/):

    python main.py

The capsule won't do anything useful on its own — it expects an
MCP client (the Loamss runtime) on stdin/stdout. To actually
install it, point ``loamss capsule install`` at this directory's
``capsule.yaml``.
"""

from __future__ import annotations

import asyncio

from loamss import Capsule, Manifest, content, tool


@tool(
    name="hello",
    description="Say hello to someone.",
    input_schema={
        "type": "object",
        "properties": {
            "name": {
                "type": "string",
                "description": "Who to say hello to. Defaults to 'world'.",
            }
        },
        "additionalProperties": False,
    },
)
async def hello(args, ctx):
    name = (args.get("name") or "world").strip() or "world"
    return content.text(f"Hello, {name}!")


async def main() -> None:
    capsule = Capsule(
        manifest=Manifest(
            name="hello-world-py",
            version="0.1.0",
            description="Reference Python capsule — says hello.",
        ),
        tools=[hello],
    )
    await capsule.start()


if __name__ == "__main__":
    asyncio.run(main())
