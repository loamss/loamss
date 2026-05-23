# Loamss Scenarios

Concrete end-to-end use cases. These exist to keep the architecture honest: any change to the runtime, MCP surface, capsule spec, or permission model should preserve every scenario here. If a proposed change breaks one, either the change needs rework or this doc needs updating to acknowledge the trade-off.

> **Status: draft.** Add scenarios as they come up. Remove or revise scenarios that no longer reflect the design.

## Scenario 1 — Consumer AI plugs in

A user runs Loamss on their laptop. They want ChatGPT to know who they are without uploading their whole life to OpenAI.

1. User installs the `loamss` binary, runs `loamss init`. Picks storage (local encrypted folder), memory (SQLite + sqlite-vec). Adds a Claude API key for the organizer capsules (optional — can be deferred).
2. User connects Gmail and Calendar via OAuth. Ingestor capsules pull data into storage. Organizer capsules build memory: people, threads, projects.
3. User goes to ChatGPT, adds Loamss as an MCP server using a pairing code from `loamss client pair`.
4. Loamss console shows a permission slip: ChatGPT requests `email.read`, `calendar.read`, `memory.query`. User narrows `memory.query` to `entities: ["people", "projects"], data_classes_excluded: ["health"]`. Approves.
5. In ChatGPT, the user says *"draft a reply to Sarah about the contract."* ChatGPT calls Loamss via MCP for context, drafts locally, returns the draft.
6. User edits, hits send. ChatGPT calls Loamss's `email.send` tool. Loamss pauses for approval (consequential action), user confirms, send happens via Loamss's Gmail OAuth, audit log records the full chain.

**What this stresses**: pairing primitive, scoped grants, consequential-action gating, the fact that the model call happens in the *client*, not Loamss.

## Scenario 2 — Time-boxed specialist client

A patient visits a clinic. The clinic's intake AI has an MCP client.

1. Tablet at the clinic displays a QR code from their MCP client.
2. Patient scans with the Loamss companion app on their phone.
3. Loamss shows a permission slip: clinic requests `memory.query` with `data_classes_included: ["health"], time_range: { since: "12 months ago" }` and `files.read` with `data_classes_included: ["health"]`. Duration: 2 hours. Auto-revoke: yes.
4. Patient approves.
5. For two hours, the clinic AI can query Loamss for symptoms, medications, recent labs. Each query is logged.
6. At 2 hours, the grant expires. The clinic loses access. The audit log retains the full record forever.

**What this stresses**: time-boxed grants, scope narrowing to a data class, mobile approval UX, the same client/pair primitive serving short-lived professional contexts.

## Scenario 3 — Silent organizer capsule

A user wants to do their taxes.

1. User installs `tax-organizer` capsule. Permission slip: `email.read` (scope: `sender: financial-domains-list`), `files.read` (scope: `paths: finance/*`), `files.write` (scope: `paths: finance/tax-2026/*`), `memory.write` (scope: `entities: ["tax-entity"]`, declared by this capsule).
2. User approves. Runs the capsule.
3. The capsule crawls a year of email and files, identifies receipts, contractors, deductions, writes a structured folder. Calls Loamss's model router internally to classify ambiguous items.
4. The capsule never opens a chat window. The user reads the output by opening the resulting folder.

**What this stresses**: capsules-without-chat (most capsules will be this), batch organization as a first-class workflow, the model router as a *capsule-internal* concern that doesn't bother the user.

## Scenario 4 — Cross-surface knowledge

A user uses Cursor for code and ChatGPT for everything else.

1. Both have MCP clients. Both are paired with the same Loamss.
2. Cursor has grants: `files.read` (scope: `paths: notes/engineering/*, code/**`), `memory.query` (scope: `entities: ["project", "decision", "topic"]`), `calendar.read` (scope: `time_range: { today }`).
3. ChatGPT has grants: full set as in Scenario 1.
4. In Cursor, the user asks *"what did I decide about the auth refactor last week?"* The decision was made in a Slack thread, not in code. Cursor queries Loamss's memory, gets the decision back, and shows it.
5. The Slack thread was ingested by the Slack connector (Phase 2 — this scenario is not end-to-end testable until Slack lands). The decision was extracted by an organizer capsule. Cursor never had to see Slack directly.

**What this stresses**: memory as the cross-surface unifier. Two different AI tools, two different scopes, one shared brain. The unifier is the whole point of Loamss.

## Scenario 5 — Creator publishing (the videos scenario)

A user makes video content. A social platform supports MCP and wants to showcase their videos. The user's videos stay in the user's storage; the platform streams from there via Loamss-issued signed URLs; every play is audited; revenue events flow back.

1. User has videos in their own S3 bucket. Loamss has ingested metadata via the filesystem/S3 ingestor (titles, descriptions, durations, thumbnails — extracted from sidecars or `ffprobe`).
2. User installs `content-publisher` capsule. It exposes a new MCP resource type, `content.video`, and a `content.publish` capability.
3. User tags the videos they want available publicly with `tag:public`. Untagged videos are not visible to publishing clients.
4. Platform (call it `vibez.example`) onboards the creator. Creator runs `loamss client pair --name "Vibez"`, pastes the code into Vibez's MCP setup.
5. Loamss shows the permission slip:
   - `content.list`, `content.read` — scope: `tag:public`
   - `content.metrics.write` — to report plays and watch time back
   - `content.revenue.write` — to report revenue events back
   - Framing on the slip: **"Vibez will be able to show these videos to its users."** (Public publish is distinguished from private read.)
6. Creator approves. Vibez calls Loamss via MCP:
   - `GET resources?type=content.video&tag=public` → list of videos
   - `GET resource/video/abc123/metadata` → title, description, thumbnail URI, duration
   - `GET resource/video/abc123/stream` → 307 redirect to a signed URL into the creator's S3 (TTL: 10 min)
   - `POST event/content.metrics { video: abc123, plays: 1, watch_seconds: 47 }`
   - `POST event/content.revenue { video: abc123, cents: 3, source: "ad_split" }`
7. Loamss never proxies the video bytes — Vibez streams directly from the creator's S3 via signed URL. Loamss logs the URL issuance.
8. Vibez writes back metrics and revenue claims. Loamss stores them as Vibez's attestations. The creator can query `memory.query("plays per video this month")` and get unified analytics across every platform that writes back.

**What this stresses**:
- **Content-as-resource (binary blobs)** — different protocol shape than tool-call results. Needs signed-URL/redirect support in the MCP surface spec.
- **Public-publish as a permission concept** — distinct UX from "private read by your AI." Same underlying capability, different framing.
- **Event/metric write-back from external clients** — generalizes beyond content: Spotify writing stream counts, Substack writing subscriber counts, GitHub writing sponsor revenue. New capability namespace.
- **The bandwidth question** — Loamss must not become a video proxy. Signed URLs direct from user storage to consuming platform.
- **The "no SaaS lock-in" pitch made real** — if Vibez dies, the videos and metrics are still in user-owned storage. Point a new platform at the same Loamss, library and history are intact.

## Scenario 6 — Cross-platform analytics consolidation (generalization of Scenario 5)

A user is a creator on multiple platforms: Vibez (video), Resound (audio), Quill (writing). Each platform speaks MCP and writes back metrics.

1. User pairs each platform with their Loamss, granting each `events.write` for its own namespace.
2. Each platform writes events back: plays, listens, reads, revenue claims, subscriber counts.
3. The user's memory now contains a unified timeline of cross-platform reach. They can ask their AI of choice (via Scenario 1): *"which platforms drove the most revenue last quarter, and what content performed best across all of them?"*
4. The AI queries Loamss's memory. Loamss aggregates the platforms' written claims. The AI returns the answer.

**What this stresses**: the write-back surface unlocks value that no single platform can offer (because each only sees its own slice). The user's Loamss becomes the only place where *total* exists.

## Scenario 7 — Federation between two Loamsses (deferred to Phase 3)

A user and their spouse each run their own Loamss. They want to share family calendar and the kids' school folder.

1. User goes to console → Sharing → Invite spouse's Loamss (by URL or pairing code).
2. Grants: `calendar.read` (scope: tag `family`), `files.read` (scope: `family/school/*`).
3. Spouse's Loamss now sees those slices via MCP-over-Tailscale (or whatever transport).
4. Spouse's ChatGPT, paired with the spouse's Loamss, can answer "what's on the family calendar this week" — pulling from the user's Loamss, logged on both sides.

**What this stresses**: federation is "another MCP client, except it's another Loamss." Same primitives, peer relationship. Worth noting: as standards evolve, this is the case where A2A (Agent2Agent Protocol) may be a better fit than MCP. Kept MCP-shaped for now.

## What every scenario shares

All seven scenarios use the same five primitives:

1. **Pairing** — establishing trust with a new client (CLI code, QR, invite link)
2. **Scoped grants** — capability + scope + optional time bound + optional approval requirement
3. **MCP as the wire protocol** — tool calls, resource reads, event writes
4. **User-owned storage** — Loamss never holds the primary copy of anything semantic
5. **Audit log** — every read, every write, every revocation

If a future scenario can't be expressed through these five primitives, that's a signal the model is incomplete — not that we need to special-case the scenario.

## Open questions surfaced by the scenarios

Most of the open questions originally raised here have been resolved across the spec set:

- ✅ **Public-publish vs. private-read UX** (S5): resolved in `permission-model.md` via the `framing` field on grants.
- ✅ **Event/metric write-back schema** (S5, S6): resolved across `mcp-surface.md` (CloudEvents-shaped envelope), `permission-model.md` (`<type>.write` capability declared by exposer capsules), and `audit-spec.md` (`event.write` audit type).
- ✅ **Resource binary streaming** (S5): resolved in `mcp-surface.md` via signed-URL redirection backed by the storage adapter's `SignedURL` operation in `adapter-interface.md`.
- ✅ **Trust in platform-reported metrics** (S5, S6): resolved as **attributed claims** — stored verbatim with source provenance, never silently merged into ground truth.

Remaining open:

- **Federation as MCP vs. A2A** (S7): deferred to Phase 3. The current design treats federation as "another MCP client, where the client is another Loamss" — A2A may be a better fit when Phase 3 work begins.
- ✅ **Capsule-extensible memory entity types** (S3, S4, S6): resolved in `capsule-spec.md` via the `memory_extensions` manifest section. Capsules declare new entity types with JSON schemas under reverse-DNS namespaces; the runtime validates writes against the schema and inherits data-class tags onto entries automatically.
