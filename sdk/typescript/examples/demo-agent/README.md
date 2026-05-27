# demo-agent — an external MCP client with a local Ollama brain

This example is the **end-to-end story for the 90-second demo**. It
shows what a typical AI app — Claude Desktop, ChatGPT, Cursor, or
anything else — looks like talking to a user's Loamss runtime.

The agent has no privileged relationship to Loamss. It speaks the
same MCP-over-HTTP+SSE wire protocol any external tool would speak.
Every call is checked against the user's capability grants; allowed
calls return data, denied calls return a clean JSON-RPC error.

## What it shows

Two scenarios, picked to make the contract visible:

| Scenario              | Tool          | Outcome              | Why                                                       |
| --------------------- | ------------- | -------------------- | --------------------------------------------------------- |
| `bun src/agent.ts "what did Sarah want?"` | `memory.query` | **ALLOWED** | The user granted `memory.read` after pairing.             |
| `bun src/agent.ts --write "buy milk"`     | `memory.upsert` | **DENIED**  | No `memory.write` grant — the runtime refuses, the agent reports the denial. |

The Loamss audit log records every attempt with the same hash chain
either way. The agent doesn't audit itself; the substrate does.

## Why this matters for the demo

When viewers see the model "remember" something across sessions, the
obvious next question is: *why should I let some random AI tool
touch my data?* This agent is the answer:

1. **You** ran `loamss client pair`.
2. **You** chose which capabilities to grant.
3. The runtime gates every request.
4. The audit log is yours to read.

The agent has only what you handed it. Swap it for a different
agent tomorrow — pair, grant, go. The brain stays the same.

## Prerequisites

```bash
# 1. Loamss running on http://127.0.0.1:7777
loamss start

# 2. Ollama running with both models
brew services start ollama         # or `ollama serve` in a shell
ollama pull nomic-embed-text       # 274 MB — Loamss uses this for embedding
ollama pull llama3.2:1b            # 1.3 GB — the agent's brain

# 3. Some content in memory so memory.query returns hits.
# The agent has memory.write granted via the same flow below, so
# you can also just run it once in --write mode to seed entries.
# If you have legacy notes already, the transitional files
# connector is a quick way to get them in:
loamss source add source:files --name notes \
  --config root=/path/to/your/notes \
  --config namespace=notes
loamss source sync notes
```

## Install the agent

This example lives in the Loamss repo. On a fresh machine:

```bash
git clone https://github.com/loamss/loamss
cd loamss/sdk/typescript/examples/demo-agent
bun install   # pulls @loamss/sdk from npm
```

## Pair the agent (one-time)

The agent and the runtime are separate processes, so we exchange a
one-time code:

```bash
# Terminal A — generate a pairing code
loamss client pair --name "Demo Agent" --json | jq -r .code
#  → e.g. 6DK5-8XE2

# Terminal B — redeem it
cd sdk/typescript/examples/demo-agent
bun src/pair.ts http://127.0.0.1:7777 6DK5-8XE2
#  → writes ./demo-agent.token
```

Then grant the read capability — without this, every memory.query
call comes back DENIED:

```bash
CID=$(jq -r .clientId ./demo-agent.token)
loamss grant create \
  --principal-kind client --principal-id "$CID" \
  --capability memory.read --scope-json '{}' \
  --rationale "let the demo agent read memory"
```

## Run

### Allowed path

```bash
bun src/agent.ts "what did Sarah want?"
```

Expected output (colorized in your terminal):

```
[agent] Connecting as client cli-01K... (Demo Agent)
[agent] Brain: Ollama llama3.2:1b at http://localhost:11434
[agent] Discovering tools the user granted me...
[loamss]   client.info — ...
[loamss]   memory.query — ...
[loamss]   memory.upsert — ...
[loamss]   ...

[agent] Question: "what did Sarah want?"
[agent] Calling memory.query(query="what did Sarah want?", limit=3) ...
[loamss] ✓ ALLOWED  memory.query returned 3 hits
[loamss]   notes:2026-05-20-sarah-contract.md (distance 0.274)
[loamss]   notes:2026-05-22-auth-refactor.md  (distance 0.513)
[loamss]   notes:2026-05-24-hn-list.md        (distance 0.531)

[agent] Asking llama3.2:1b to summarize...

[ollama] Sarah called about renewing the Q3 consulting contract and
         wanted to raise her rate from $180 to $220/hr. You decided
         to counter at $200 and lock the SOW before end of June.
```

### Denied path

```bash
bun src/agent.ts --write "I should buy milk"
```

Expected output:

```
[agent] Note to remember: "I should buy milk"
[agent] Calling memory.upsert(...) ...
[loamss] ✗ DENIED  memory.upsert blocked
[loamss]          capability: memory.write
[loamss]          reason:     no matching grant for capability memory.write

[agent] I can't store anything without your permission. To let me
        write, run:
[agent]   loamss grant create --principal-kind client --principal-id cli-... \
[agent]     --capability memory.write --scope-json '{}' --rationale "agent notes"
```

Then visit the audit pane in the console — both the allowed and the
denied calls show up, with the matching grant ID for the first and
`grant=none` for the second.

## Environment overrides

| Variable          | Default                     | Purpose                                    |
| ----------------- | --------------------------- | ------------------------------------------ |
| `OLLAMA_MODEL`    | `llama3.2:1b`               | Any model installed via `ollama pull`      |
| `OLLAMA_ENDPOINT` | `http://localhost:11434`    | Remote Ollama (over Tailscale, etc.)       |

## What this example is NOT

- **Not a production agent framework.** No retries, no streaming,
  no multi-turn memory of its own. It's a transparency tool.
- **Not Ollama-specific.** The MCP client half works the same way
  with Claude (via the Anthropic SDK), OpenAI, or any caller —
  Ollama is just the cheapest local brain for a demo.
- **Not the right shape for app-specific UX.** A real inbox app
  would render results in a UI; here we stream everything to the
  terminal so the trust contract is visible at a glance.
