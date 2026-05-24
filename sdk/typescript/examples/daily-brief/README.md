# daily-brief capsule

The canonical "full loop" reference capsule. Generates a daily briefing from recent memory.

## What it demonstrates

This is the smallest interesting capsule that touches every layer of the substrate:

```
sources  →  memory layer  →  this capsule  →  model.call  →  brief
```

Concretely, when a paired client invokes `daily-brief.brief`:

1. The capsule calls `threads.list` to find recent conversations
2. For each thread it calls `threads.entries` to get reading order
3. For each entry it calls `memory.show` to pull content + metadata
4. It assembles the context and calls `model.call` to summarize
5. It returns a structured brief: summary text + threads considered + entities seen + token counts

Every one of those calls goes through the runtime's permission engine. The capsule declares `memory.read` and `model.call` in its manifest; the runtime issues those grants at install time; the audit log records every callback.

If `model.call` fails (no generation-capable adapter configured), the capsule **degrades gracefully**: it still returns the read-side facts (thread + entity counts) without a prose summary, and surfaces a `graceful_degradation` field explaining what's missing.

## Install + run

You need:

- A running `loamss` runtime (`loamss start` in another terminal)
- A configured source with some data — e.g., `source:files` pointing at a directory of markdown notes
- A configured model adapter, or be OK with the graceful-degradation path

```bash
# From the repo root
loamss capsule install ./sdk/typescript/examples/daily-brief --yes
loamss capsule list                                 # confirm it's loaded

# Pair an MCP client (or use an existing one), then invoke:
#   tools/call daily-brief.brief { "thread_limit": 5 }
```

The simplest way to invoke without writing a client is the existing inbox-app example:

```bash
cd sdk/typescript/examples/inbox-app
bun src/pair.ts http://127.0.0.1:7777 <pair-code-from-loamss-client-pair>
bun -e '
  const { createClient } = await import("../../src/index.ts");
  const t = JSON.parse(await Bun.file("inbox-app.token").text());
  const c = createClient({ endpoint: t.endpointUrl, token: t.token });
  const r = await c.tools.call("daily-brief.brief", {
    thread_limit: 5,
    entries_per_thread: 5,
  });
  console.log(r.content[0].text);
'
```

## Output shape

```jsonc
{
  "generated_at": "2026-05-24T19:30:00Z",
  "summary": "Three short paragraphs from the model...",
  "source_notes": "based on 4 thread(s) and 7 entity/entities",
  "threads_considered": [
    {
      "id": "thr_01H...",
      "subject": "Project Alpha kickoff",
      "namespace": "notes",
      "entry_count": 3,
      "last_seen": "2026-05-22T11:00:00Z",
      "participants": ["Sarah Smith", "Bob Lee"]
    }
  ],
  "entities_seen": [
    { "canonical": "Sarah Smith", "entry_count": 8 },
    { "canonical": "Bob Lee", "entry_count": 4 }
  ],
  "tokens_used": { "input": 412, "output": 184 }
}
```

## What this catches

If any of the following break, this capsule notices first:

- The memory layer's entity/thread derivation
- The capsule subprocess host (process supervisor, MCP-over-stdio framing)
- Runtime callbacks (capsule → runtime tool calls)
- The permission engine (`memory.read` + `model.call` grants must be issued correctly at install)
- Audit emission on capsule callbacks
- `model.call` dispatch + graceful degradation

The other example capsule (`hello-world`) verifies the basic capsule lifecycle. This one verifies the substrate as a system.

## Customizing

The interesting knobs:

- `namespace` — restrict to a single source's namespace (e.g., `"notes"` to brief only on file source)
- `thread_limit` / `entries_per_thread` — controls how much context the model receives
- `model_id` — pin a specific model; useful if you have several configured

The brief's prompt lives inline in `src/index.ts` near the `model.call` invocation. The default produces a three-paragraph "calm assistant" tone; rewrite it for your taste.

## Related

- [`capsule-spec.md`](../../../../capsule-spec.md) — the manifest contract
- [`memory-layer.md`](../../../../memory-layer.md) — what `threads.list`, `entities.list`, `memory.show` return
- [`mcp-surface.md`](../../../../mcp-surface.md) — the MCP wire protocol
- [`hello-world`](../hello-world/) — the simpler capsule reference
- [`inbox-app`](../inbox-app/) — the client side; pair + call this capsule
