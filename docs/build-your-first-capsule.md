# Build Your First Capsule

This tutorial walks you through writing, installing, and invoking a Loamss capsule from scratch. By the end you'll have a working capsule that runs as a subprocess inside someone's Loamss runtime, exposes a tool, and (in the second half) calls back into the runtime to read memory.

Estimated time: 15 minutes. Prerequisites: Bun ≥ 1.1 and a working `loamss` binary in your PATH (`loamss version` should print).

> **What a capsule is.** A subprocess the runtime spawns, talks to over MCP-over-stdio (newline-delimited JSON-RPC 2.0), and gates by per-capability permission checks. From the user's POV, a capsule is one box in their console where they decide what it can read and do. From your POV, it's a small program that returns answers when called.

## Part 1 — Hello, capsule

### 1.1 Project skeleton

```bash
mkdir hello-capsule && cd hello-capsule
bun init -y                     # creates package.json, tsconfig.json
bun add @loamss/sdk             # ~one dep, no transitive bloat
```

Add `"type": "module"` to `package.json` if `bun init` didn't.

### 1.2 The capsule code

Create `src/index.ts`:

```ts
import { createCapsule, defineTool } from "@loamss/sdk";

const greet = defineTool({
  name: "greet",
  description: "Greet someone by name.",
  inputSchema: {
    type: "object",
    properties: {
      who: { type: "string", description: "Who to greet." },
    },
    required: ["who"],
  },
  handler: (input: { who: string }) => `Hello, ${input.who}!`,
});

await createCapsule({
  manifest: {
    name: "com.example.hello-capsule",
    version: "0.1.0",
    author: { name: "you" },
  },
  tools: [greet],
}).start();
```

That's the whole capsule. Three observations:

- The `handler` is a plain async function. Whatever it returns is auto-wrapped into the MCP content shape — strings become text blocks, objects become JSON-encoded text blocks.
- `defineTool` is just a typed builder. You could skip it and pass the object directly — `defineTool` exists for IDE ergonomics.
- `createCapsule(...).start()` blocks. It returns when the runtime closes the subprocess.

### 1.3 The manifest

The runtime installs capsules from on-disk packages, not from npm yet. Create `capsule.yaml` alongside `src/`:

```yaml
spec_version: "0.1"

name: com.example.hello-capsule
version: 0.1.0
description: My first capsule — exposes a `greet` tool.

author:
  name: you

permissions:
  requires: []   # this capsule reads nothing, calls nothing

tools:
  - name: greet
    description: Greet someone by name.
    input_schema:
      type: object
      properties:
        who:
          type: string

runtime:
  type: subprocess
  entrypoint: bun
  args:
    - run
    - src/index.ts
```

The `tools` block in `capsule.yaml` and the `defineTool(...)` calls in your code MUST match — the runtime validates against the manifest at install time and rejects mismatches.

> Full manifest schema: see [`capsule-spec.md`](../capsule-spec.md).

### 1.4 Validate before installing

```bash
loamss capsule validate .
```

Expected:

```
✓ capsule.yaml is valid (spec_version 0.1)
  name:    com.example.hello-capsule
  version: 0.1.0
  tools:   1 (greet)
```

If validation fails, fix the manifest and re-run. The error list points at every problem the runtime would have rejected later.

### 1.5 Install + invoke

Make sure the runtime is initialized and running in a second terminal:

```bash
# Terminal 2 — leave running
loamss init                     # one-time
loamss start
```

Install the capsule:

```bash
loamss capsule install . --yes
```

Expected:

```
✓ Installed com.example.hello-capsule@0.1.0
  install_path: /Users/you/.loamss/capsules/com.example.hello-capsule
  grants issued: 0
```

(0 grants because we requested 0 capabilities — `permissions.requires` was empty.)

Verify the capsule is loaded by the host:

```bash
loamss capsule list
```

You should see `com.example.hello-capsule` with status `running`.

### 1.6 Invoke the tool

The runtime mounts capsule tools under `<capsule-name>.<tool-name>`. To invoke `greet`, we need a paired MCP client. Use the example app from the SDK repo:

```bash
# Generate a one-time pairing code
loamss client pair --name "tutorial app"
# → 5QUK-5EPE  (yours will differ)

# Pair the example client
cd /path/to/loamss/sdk/typescript/examples/inbox-app
bun src/pair.ts http://127.0.0.1:7777 5QUK-5EPE

# Now use a tiny inline script to call the tool:
bun -e '
  const { createClient } = await import("@loamss/sdk");
  const t = JSON.parse(await Bun.file("inbox-app.token").text());
  const client = createClient({ endpoint: t.endpointUrl, token: t.token });
  const result = await client.tools.call(
    "com.example.hello-capsule.greet",
    { who: "Loamss" }
  );
  console.log(result.content[0].text);
'
```

Expected:

```
Hello, Loamss!
```

🎉 The capsule received the call from the runtime via MCP-over-stdio, ran your handler, and returned a result. The runtime mediated everything: bearer-token auth on the paired client, permission check (none required for `greet`), audit emission, response routing.

### 1.7 Confirm the audit log captured it

```bash
loamss audit log --type tool.invoked --limit 5
```

You should see an entry with `subject.id = com.example.hello-capsule.greet`, the actor your paired client, and `outcome = success`.

## Part 2 — A capsule that uses memory

The hello-capsule didn't actually do anything Loamss-y. Let's build one that queries the user's memory. This exercises the *runtime callback* path: capsule → runtime → memory adapter, with the permission engine in between.

### 2.1 Update the code

```ts
// src/index.ts
import { createCapsule, defineTool } from "@loamss/sdk";

const recentEntries = defineTool({
  name: "recent",
  description: "Return the N most recent memory entries.",
  inputSchema: {
    type: "object",
    properties: {
      limit: { type: "integer", minimum: 1, maximum: 50 },
    },
  },
  handler: async (input: { limit?: number }, { runtime }) => {
    const limit = input.limit ?? 5;
    const result = await runtime.tools.call("memory.query", {
      query: "",       // empty query → recency-ordered
      limit,
    });
    return result;     // pass the MCP content through verbatim
  },
});

await createCapsule({
  manifest: {
    name: "com.example.hello-capsule",
    version: "0.2.0",
    author: { name: "you" },
  },
  tools: [recentEntries],
}).start();
```

The key new line is `await runtime.tools.call(...)`. That's a *runtime callback* — the capsule's request goes back over MCP-over-stdio to the runtime, the runtime checks the capsule's grants (does it have `memory.read`?), and either returns the result or rejects with `-32001 permission denied`.

### 2.2 Update the manifest

The capsule now needs `memory.read`:

```yaml
spec_version: "0.1"

name: com.example.hello-capsule
version: 0.2.0
description: Query recent memory entries.

author:
  name: you

permissions:
  requires:
    - capability: memory.read
      rationale: Needed to surface recent entries to the caller.

tools:
  - name: recent
    description: Return the N most recent memory entries.
    input_schema:
      type: object
      properties:
        limit: { type: integer, minimum: 1, maximum: 50 }

runtime:
  type: subprocess
  entrypoint: bun
  args:
    - run
    - src/index.ts
```

`permissions.requires` declares what the capsule needs to function. When the user runs `loamss capsule install`, they get a permission slip listing every capability — they can narrow or reject before approving.

### 2.3 Reinstall

```bash
loamss capsule uninstall com.example.hello-capsule --yes
loamss capsule install . --yes
```

(Today the runtime requires uninstall+install for upgrades. Hot upgrade is a Phase 2 feature.)

Expected:

```
✓ Installed com.example.hello-capsule@0.2.0
  grants issued: 1
  - memory.read (scope: any)
```

### 2.4 Invoke against (possibly empty) memory

Memory is populated by sources (Gmail, Calendar, …) and by capsules with `memory.write` grants. If you've followed [`docs/setup-gmail.md`](./setup-gmail.md) on this runtime, your Gmail messages are already in memory. If not, the query below returns an empty result — that's fine; it still proves the call chain works.

Invoke the tool:

```bash
bun -e '
  const { createClient } = await import("@loamss/sdk");
  const t = JSON.parse(await Bun.file("inbox-app.token").text());
  const client = createClient({ endpoint: t.endpointUrl, token: t.token });
  const result = await client.tools.call(
    "com.example.hello-capsule.recent",
    { limit: 3 }
  );
  console.log(JSON.stringify(JSON.parse(result.content[0].text), null, 2));
'
```

Expected (shape; results vary):

```json
{
  "results": []
}
```

(or, on a runtime with seeded memory, an array of entries with `namespace`, `id`, `content`, and `metadata`.)

The full chain just worked:

```
your inline bun script
       │
       ▼ tools/call com.example.hello-capsule.recent
loamss runtime  ──  permission check: tool exists, paired client allowed
       │
       ▼ tools/call recent (forwarded to capsule subprocess)
hello-capsule
       │
       ▼ runtime.tools.call("memory.query", ...)
loamss runtime  ──  permission check: capsule has memory.read grant
       │
       ▼ memory adapter
SQLite
       │
       ▼ results
       ⤴ result content rolls back up the same chain
```

Every hop emits an audit entry. Run `loamss audit log --since 5m --limit 20` to see them.

## Part 3 — What can go wrong

| Symptom | Cause | Fix |
| --- | --- | --- |
| `permission denied: capability "memory.read"` from the capsule's call | Capsule was installed without the grant (e.g., you narrowed it at install). | `loamss grant list --principal-kind capsule --principal-id com.example.hello-capsule` to inspect; `loamss grant create` to add. |
| `unknown tool: com.example.hello-capsule.greet` | Capsule isn't loaded by the host (it crashed, or the runtime needs a restart after install). | `loamss capsule list` — should show the capsule. If the runtime is running but the tool isn't visible, restart `loamss start` and re-check. Subprocess stderr currently flows through the runtime's slog; check the daemon's log output. |
| Capsule starts then immediately exits | Stray `console.log` in your code. Stdout is reserved for MCP frames — any extra bytes corrupt the JSON-RPC stream and the runtime closes the connection. | Use `console.error(...)` for diagnostics, or `runtime.log("info", ...)` once you've got a runtime client. |
| `unknown method: tools/foo` reaches your capsule | The runtime sent a method `@loamss/sdk` doesn't handle. Probably a protocol-version mismatch. | Confirm `loamss version` and the SDK are both on `2025-03-26`. |
| Capsule installs but `tools/list` returns empty | `defineTool(...)` was called but the result wasn't passed to `createCapsule`. | Check the `tools: [...]` array in the `createCapsule` call. |

## Next steps

- **Add a write tool**: `runtime.tools.call("memory.upsert", { ... })` if the capsule has `memory.write`.
- **Read resources**: `runtime.resources.read("memory://entries/<id>")` for direct fetches.
- **Subscribe**: capsules don't subscribe yet (that's a Path-B-app surface), but the wire shape will land later this phase.
- **Publish your capsule**: the canonical registry arrives in Phase 2/3. For now, share the repo URL — installation works from any directory.

## Related

- [`capsule-spec.md`](../capsule-spec.md) — full manifest schema and the host runtime's expectations
- [`permission-model.md`](../permission-model.md) — how grants get scoped, narrowed, and revoked
- [`audit-spec.md`](../audit-spec.md) — what gets logged on every callback
- [`mcp-surface.md`](../mcp-surface.md) — the wire protocol your capsule speaks
- [`sdk/typescript/`](../sdk/typescript/) — SDK source + the `examples/hello-world/` capsule the tutorial maps to
- [`connect-your-app.md`](./connect-your-app.md) — the companion guide for Path-B apps (the other side of MCP)
