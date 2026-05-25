# approval-demo

Reference capsule that exercises the runtime's **user-approval** flow end-to-end. Useful for verifying the approval pipeline works after backend changes, and as a teaching example for capsule authors writing tools that need a human in the loop.

## What it does

The capsule declares one tool — `peek` — that lists the user's recent threads. Its `memory.read` permission is flagged `requires_user_approval: true`, which tells the runtime to queue a pending approval on **every** invocation.

The result: every call to `peek` pauses for a human OK in the dashboard's Approvals pane. The capsule catches the runtime's `-32002` ("user approval required") error and returns a structured `{ status: "pending", approval_id, hint }` so the caller knows what to do.

## Why approve-every-call

For most capabilities — `memory.read` against your own data, `model.call` against your own keys — pre-granting once at install time is the right default. The user already understands what they're enabling.

For consequential capabilities — `email.send`, `payment.transfer`, `social.post` — the runtime supports per-invocation approval. The capsule manifest sets `requires_user_approval: true` and the runtime ensures a human reviews **every single call**. This capsule demonstrates the mechanism using `memory.read` so you can see the workflow without needing actual sensitive capabilities.

## Demo path

The full round-trip uses three of the dashboard's panes: Capsules, Apps, Approvals.

### 0. Prerequisites

You have a running daemon (`loamss start`) with the dashboard open in your browser.

### 1. Install the capsule

In the dashboard's Capsules pane:

1. Click **+ Install capsule**
2. Paste the absolute path to this directory:
   ```
   /path/to/loamss/sdk/typescript/examples/approval-demo
   ```
3. Click **Install**
4. The permission slip shows: `memory.read` · *asks before every use*

The capsule starts immediately under the host's supervision; you'll see it in the Capsules pane as `running`.

### 2. Pair an external client

The capsule's tool is invoked via the MCP surface, which requires a paired client. In the dashboard's Apps pane:

1. Click **+ Pair an app**
2. Name it `approval-test`
3. Click **Generate code**
4. Copy the code

Redeem it directly (this is what an external app would normally do):

```bash
CODE="<paste-the-code-here>"
TOKEN=$(curl -sS -X POST http://127.0.0.1:7777/pair \
  -H 'Content-Type: application/json' \
  -d "{\"code\":\"$CODE\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
echo "token: $TOKEN"
```

Refresh the dashboard — the row should appear in Apps as `active`.

### 3. Grant the client permission to call the capsule's tool

Without this, the client's call to `approval-demo.peek` would be denied (no grant). From a terminal with `loamss` on PATH and `LOAMSS_DATA_DIR` set:

```bash
loamss grant create \
  --principal-kind client \
  --principal-id "<client-id-from-pairing>" \
  --capability "tool.call" \
  --scope-json '{"tool_name":"approval-demo.peek"}'
```

(The client ID is in the `client` field of the `/pair` response above, or visible in the dashboard's Apps pane.)

### 4. Invoke the capsule's tool

This is the part the demo is about. Call `approval-demo.peek`:

```bash
curl -sS -X POST http://127.0.0.1:7777/mcp \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc":"2.0",
    "id":"1",
    "method":"tools/call",
    "params":{"name":"approval-demo.peek","arguments":{"limit":5}}
  }' | python3 -m json.tool
```

The response is a structured `"pending"` payload:

```json
{
  "jsonrpc": "2.0",
  "id": "1",
  "result": {
    "content": [{
      "type": "text",
      "text": "{ \"status\": \"pending\", \"approval_id\": \"apr-...\", \"hint\": \"...\" }"
    }]
  }
}
```

The dashboard's **Approvals pane** is now lit up — refresh and you'll see it:

> `capsule:approval-demo` requests `memory.read`
> *The peek tool lists the user's recent threads...*

### 5. Approve in the dashboard

Click **Approve**. The approval moves to `granted`, the row vanishes from the pane.

### 6. Re-invoke

Run the curl from step 4 again. **It returns `"status": "pending"` with a NEW approval ID** — because the manifest says EVERY invocation requires user approval. That's the contract. Approve. Re-invoke. Pending. Approve. Re-invoke. Pending. Forever.

This is correct. `requires_user_approval: true` means *ask every single call*. The right design for `email.send` or `payment.transfer`. The "wrong" design for `memory.read` — but the point of this demo capsule is to show the queue-and-resolve mechanism in action; we picked `memory.read` because it's the safest capability we have to exercise.

### What this demo doesn't yet do: wait-and-retry

This capsule returns `"pending"` immediately on -32002. It doesn't subscribe to the approval, wait for resolution, and retry the underlying `threads.list` call. That'd be a much nicer UX — the external client makes ONE call and waits for a final answer — but it requires either:

  - **A blocking runtime tool**: `approval.wait(id)` returns when the approval moves out of `pending`. The runtime's `engine.WaitForApproval` already exists; exposing it as an MCP tool is a future commit.
  - **A subscribe-and-stream pattern**: the capsule subscribes to approval state, the dashboard's "approve" click flips the state, the capsule's pending handler resumes. Requires plumbing through SSE.

Until that lands, this demo shows the *queueing*: the approval shows up in the dashboard exactly as designed, the audit log captures everything, and the capsule communicates the pending state cleanly back to the caller.

### Trying the green path

To see what the *successful* path looks like, you can install the `daily-brief` reference capsule (also in `sdk/typescript/examples/`): it declares `memory.read` WITHOUT `requires_user_approval: true`, so its calls go through without an approval prompt. Compare the two capsules' `permissions:` blocks to see the difference.

## Audit trail

Every step lands in the audit log distinguishable forever. Try:

```bash
loamss audit tail -n 12
```

You'll see, in order:
- `capsule.installed` (you installed the capsule)
- two `grant.create` (capsule's permissions)
- `client.pair_code_created`, `client.paired` (you paired the app)
- `grant.create` (you granted tool.call to the client)
- `tool.invoked` → `approval.requested` (first peek)
- `approval.granted` (you clicked Approve, decided_by=console)
- `tool.invoked` → `approval.requested` (second peek triggers a NEW approval — that's the point of `requires_user_approval`)
- `approval.granted` (you'd approve again, or pre-grant via CLI for the "second invocation succeeds without approval" simplification described in the source comments)

`loamss audit verify` confirms the hash chain.

## Source

The capsule is ~80 lines of TypeScript at [`src/index.ts`](src/index.ts). The interesting block is the `try { … } catch (err) { if (err instanceof RPCError && err.code === -32002) … }` pattern — any capsule that wants to handle "approval pending" gracefully follows this shape.
