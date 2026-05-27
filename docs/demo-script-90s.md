# 90-second demo — fresh-machine recording script

Everything below assumes a clean macOS or Linux box. No prior Loamss
state, no clones of this repo, no Node project. Brew installs the
runtime; `bun` installs the SDK from npm; Ollama runs the local
embedding model and the external agent's brain. Two terminals total.

The story we're telling in 90 seconds: **the user owns the brain.
The AI tool is interchangeable.** We prove it by showing an external
agent with its own model getting allowed-then-denied through Loamss,
with the audit log catching both.

The flow has been validated end-to-end on darwin/arm64 against
Loamss v0.1.5+ and `@loamss/sdk@0.1.4` on npm.

---

## Part A — One-time setup on the demo machine

Everything in Part A happens **before** you hit record. Once Part A
is done, all of Part B and Part C are filmable as-is.

### A1. Install the substrate

```bash
# Bun — TypeScript runtime for the external agent
curl -fsSL https://bun.sh/install | bash
source ~/.zshrc    # or ~/.bashrc — whichever shell you use

# Ollama — local model server
brew install ollama
brew services start ollama         # macOS
# (Linux: curl -fsSL https://ollama.com/install.sh | sh && sudo systemctl start ollama)

# Loamss — the substrate
brew tap loamss/loamss
brew install loamss
```

Verify each:

```bash
bun --version       # 1.x
ollama --version    # 0.x
loamss version      # 0.1.5 or later
```

### A2. Pull the two Ollama models

Two separate roles, two separate models:

```bash
ollama pull nomic-embed-text   # 274 MB — used INSIDE Loamss for embedding
ollama pull llama3.2:1b        # 1.3 GB — used INSIDE the external agent
```

This is the part where the audience needs to register that Loamss
and the agent each pick their own model. Independent.

### A3. Make sample notes

The demo ingests three short notes. Pick a directory that lives
*outside* `~/.loamss` so it looks like the user's normal filesystem:

```bash
mkdir -p ~/Documents/loamss-demo/notes

cat > ~/Documents/loamss-demo/notes/2026-05-20-sarah-contract.md <<'EOF'
# Sarah Chen — Q3 contract talk

Sarah called about renewing the consulting contract for Q3.
Wants to push the rate from $180/hr to $220/hr. Says the scope
is widening: now includes the rollout review, not just the
implementation. I should counter at $200 and lock the SOW
before end of June.
EOF

cat > ~/Documents/loamss-demo/notes/2026-05-22-auth-refactor.md <<'EOF'
# Auth refactor — decision

Decided to move JWT validation into the gateway layer rather
than per-service. Cuts duplication, keeps rotation simple.
Risk: gateway becomes a bigger blast radius. Mitigation:
red-team it before the cutover; keep the old per-service
path behind a feature flag for 30 days.
EOF

cat > ~/Documents/loamss-demo/notes/2026-05-24-hn-list.md <<'EOF'
# Show HN — shortlist for next launch

- Loamss (this thing)
- The terminal recorder
- The bandit dashboard

Loamss first; the recorder is a tool we use internally and
isn't ready for a launch narrative yet.
EOF
```

### A4. Drop the demo-agent project into your home directory

The external agent lives in this repo as an example. Two paths to
get it on a fresh machine; pick one.

**Path 1 (filmable as `git clone`):**

```bash
cd ~
git clone https://github.com/loamss/loamss
cd loamss/sdk/typescript/examples/demo-agent
bun install
```

**Path 2 (no clone — type the script):** copy the three files
under `sdk/typescript/examples/demo-agent/` into a fresh directory,
then `bun install`. Path 1 is shorter on screen; prefer that.

### A5. Sanity-check Ollama is responsive

```bash
curl -s http://localhost:11434/api/tags | jq '.models[].name'
```

Should print both `nomic-embed-text:latest` and `llama3.2:1b`.

If Ollama is sluggish on the first inference, fire a warm-up call
to load the weights into RAM:

```bash
ollama run llama3.2:1b "hi" >/dev/null
```

---

## Part B — Pre-flight checklist (run immediately before record)

```bash
# Clean state — no prior daemon, no leftover config
pkill -f "loamss start" 2>/dev/null
rm -rf ~/.loamss

# Make sure Ollama is up
curl -fs http://localhost:11434/api/tags >/dev/null || \
  { echo "Ollama not running"; exit 1; }

# Terminal: 100×30, dark theme, font ≥ 16 pt
# DND: ON. Slack/Discord/Mail: closed
# Screen recorder: 1080p / 60fps, mic gain checked
# Browser: fresh window, no extensions visible, zoom 110%
```

---

## Part C — The 90 seconds, shot by shot

Two terminals open side by side. **Left** is labeled `LOAMSS`,
**right** is labeled `EXTERNAL AGENT`. The browser sits on a
third virtual desktop, swiped in when needed.

| t (s)  | Where    | Action                                                                                       | Voice-over                                                                                                                                |
| ------ | -------- | -------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| 0–6    | Title    | Wordmark `loamss` on dark, tagline underneath                                                | *"Your data is everywhere. Your AI tools change every six months.* ***Your brain shouldn't.****"*                                         |
| 6–14   | LEFT     | `loamss start` — first-run banner appears with the console URL on `127.0.0.1:7777`           | "Install with Homebrew. Run it. It bootstraps on your machine; nothing leaves localhost until you grant something."                       |
| 14–28  | Browser  | Open the console URL. Wizard: storage → fs-encrypted, memory → sqlite, models → Ollama (auto-detected). Click through. | "The wizard picks the substrate: your filesystem, a local vector store, your local Ollama for embedding."                                 |
| 28–42  | Browser → Sources | Click **Add source** → `source:files`, root `~/Documents/loamss-demo/notes`, namespace `notes`. **Sync**.       | "Add a data source. Files today; Gmail, Calendar, GitHub — all ship as capsules from the marketplace, same shape."                        |
| 42–48  | LEFT     | `loamss source list` — `notes` shows `success` last-sync                                     | "Three notes embedded into the user's own vector store. The agent doesn't get to see any of this yet."                                    |
| 48–55  | RIGHT    | (pre-paired off-camera) `bun src/agent.ts "what did Sarah want?"`                            | "Now a separate process: an external agent with its own Ollama model. It pairs in like Claude Desktop or ChatGPT would."                  |
| 55–67  | RIGHT    | Agent prints `✓ ALLOWED memory.query → 3 hits`, then `llama3.2:1b` answers in one sentence    | "It asks the memory layer. The user granted memory.read, so Loamss returns the hits. The agent's own model summarizes."                   |
| 67–77  | RIGHT    | `bun src/agent.ts --write "remember the milk"` → agent prints `✗ DENIED capability=memory.write` + the missing-grant explainer | "Same agent tries to write. No grant — Loamss refuses. The agent doesn't crash; it tells the user exactly what would unblock it."         |
| 77–85  | Browser → Audit | The two rows from above light up live: `check.allow → grant/grt-...` for the read, `check.deny` for the write    | "Every allowed read. Every denied write. Hashed and chained. The audit log is a user-facing surface."                                     |
| 85–90  | Title    | `loamss.com` + tagline                                                                       | *"Your tools change. Your brain doesn't."*                                                                                                |

---

## Off-camera before record: pair the agent

Pairing is plumbing — keep it off screen. Run these in a third
hidden terminal, then dismiss it.

```bash
# 1. Mint a one-time code
loamss client pair --name "Demo Agent" --json | jq -r .code
#   → for example, AB12-CD34

# 2. Redeem it in the demo-agent directory (writes ./demo-agent.token)
cd ~/loamss/sdk/typescript/examples/demo-agent
bun src/pair.ts http://127.0.0.1:7777 AB12-CD34

# 3. Grant ONLY memory.read — leaving memory.write unset is intentional;
#    that's what the on-camera DENIED row will demonstrate.
CID=$(jq -r .clientId ./demo-agent.token)
loamss grant create \
  --principal-kind client --principal-id "$CID" \
  --capability memory.read --scope-json '{}' \
  --rationale "let the demo agent read memory"
```

The right terminal stays in `~/loamss/sdk/typescript/examples/demo-agent`
for the whole recording, so the on-camera commands are just
`bun src/agent.ts …`.

---

## What the camera sees

### Allowed path (verified output)

```
[agent] Connecting as client cli-01K... (Demo Agent)
[agent] Loamss endpoint: http://127.0.0.1:7777/mcp
[agent] Brain: Ollama llama3.2:1b at http://localhost:11434

[agent] Discovering tools the user granted me...
[loamss]   audit.read — ...
[loamss]   memory.query — Search the semantic memory layer ...
[loamss]   memory.upsert — Write an entry into the memory layer ...
[loamss]   ... and 10 more

[agent] Question: "what did Sarah want?"
[agent] Calling memory.query(query="what did Sarah want?", limit=3) ...
[loamss] ✓ ALLOWED  memory.query returned 3 hits
[loamss]   notes:2026-05-20-sarah-contract.md (distance 0.483)
[loamss]   notes:2026-05-24-hn-list.md (distance 0.552)
[loamss]   notes:2026-05-22-auth-refactor.md (distance 0.559)

[agent] Asking llama3.2:1b to summarize...

[ollama] Sarah Chen wanted the SLA bumped to 99.95% and was open
         to keeping the existing pricing tier if we added a
         quarterly review clause.
```

The Sarah note's 0.48 vs the others' 0.55 is the visible proof that
the embedding pipeline is doing semantic ranking, not keyword match.

### Denied path (verified output)

```
[agent] Note to remember: "remember the milk"
[agent] Calling memory.upsert(...) ...
[loamss] ✗ DENIED  memory.upsert blocked
[loamss]          capability: memory.write
[loamss]          reason:     no matching grant for capability memory.write

[agent] I can't store anything without your permission. To let me
        write, run:
[agent]   loamss grant create --principal-kind client --principal-id cli-... \
[agent]     --capability memory.write --scope-json '{}' --rationale "agent notes"
```

### Audit log (verified)

```
tool.invoked  client/cli-...  success  → tool/memory.query
check.allow   client/cli-...  success  → grant/grt-...
check.deny    client/cli-...  denied
```

The deny row carries the same shape as the allow row — capability,
principal, scope. Refusal is a first-class event.

---

## Why two terminals + a browser

The trust story collapses without three vantage points:

1. **LEFT (Loamss)** — proves the runtime is just `loamss start`,
   no daemon spaghetti.
2. **RIGHT (External agent)** — proves the AI tool is a separate
   process speaking the same wire protocol any vendor would speak.
3. **Browser (console)** — proves there's a single user-facing
   pane where allow/deny lights up in real time.

If you cut to a single terminal, viewers infer the agent is
"inside" Loamss. The split is the whole point.

---

## Editing notes

- Hold the `✓ ALLOWED` line on screen for ~1.2 seconds — viewers
  need to read both `memory.query` and the distance numbers.
- Hold the `✗ DENIED` line for ~1.5 seconds — slightly longer,
  because the audience hasn't seen this shape before.
- When the audit pane shows up, cut to it *as the latest row
  appears* (use the SSE-driven live updates). Two-frame buffer max.
- Voice-over hits "your brain shouldn't" right as the last title
  card crossfades in. Sync to the cut, not the clock.

---

## What this demo deliberately leaves out

A 90-second cut can't show everything. The follow-up videos cover:

- **Capsule installation** — daily-briefing or RSS ingestor (60s
  cut on its own).
- **OAuth ingestor** — calendar-ingestor walks through Google
  consent without leaking client_id (60s cut).
- **Approvals workflow** — `requires_user_approval` grants and
  the permission slip (45s cut).
- **Federation** — peer Loamss instances, Phase 3.

This demo's job is the **substrate**: user-owned storage, embedded
memory, a permission engine that gates every external request, and
an audit log that records the decision either way. The rest of the
ecosystem rides on these four things.
