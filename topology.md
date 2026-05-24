# Loamss Topology & Data Flows

A deployment-level walkthrough of the **front-facing-app scenario**: a creator self-hosts Loamss in front of multi-region object storage, a third-party platform (referred to here as *Vibez*) presents content to fans, and fans stream the bytes **directly from the creator's storage** through a CDN — never through the platform, never through Loamss's hot path.

This document is the design's correctness check for **Scenario 5** (creator publishing) in [`scenarios.md`](./scenarios.md). Every architecture change that touches pairing, signed URLs, the storage adapter SPI, or the audit surface should preserve every flow described here, or explicitly acknowledge breaking one.

> **Status: living document.** Update when the trust boundaries, identity systems, or canonical flows change. The five views below are the contract; the failure-mode table is the safety net.

---

## TL;DR

A fan watching a creator's video never sends a single byte through Vibez's servers or the creator's Loamss instance. Vibez gets a *signed URL* from Loamss; the fan's browser fetches the bytes from the CDN edge in front of the creator's S3 region; Loamss only sees the URL-mint event and the metric write-back. The CDN is a delivery extension of the creator's storage, not a Loamss-layer cache.

---

## View 1 — Topology

```
┌─────────────┐                   ┌────────────────────────────┐
│             │   1. browse       │                            │
│    Fan      │ ─────────────►    │   Vibez (platform)         │
│  (browser)  │ ◄─────────────    │   - catalog UI             │
│             │   2. HTML page    │   - billing / fan auth     │
└──────┬──────┘                   │   - holds NO content bytes │
       │                          └──────────────┬─────────────┘
       │                                         │
       │ 5. GET bytes via signed URL             │ 3. ask for stream URL
       │    (no Loamss / Vibez in path)          │    (MCP over HTTPS,
       │                                         │     paired client cred)
       │                                         ▼
       │                          ┌────────────────────────────┐
       │                          │  Loamss runtime (creator)  │
       │                          │  - MCP surface             │
       │                          │  - permission engine       │
       │                          │  - audit log (chain)       │
       │                          │  - signed-URL minter       │
       │                          │  - capsule host            │
       │                          └──────────────┬─────────────┘
       │                                         │
       │                                         │ 4. PutObject ACLs /
       │                                         │    signing keys
       │                                         ▼
       │                          ┌────────────────────────────┐
       │  6. (edge) ─────────►    │  CDN  (CloudFront / CF)    │
       └─────────────────────►    │  - signature verify        │
                                  │  - (optional) fan binding  │
                                  └──────────────┬─────────────┘
                                                 │ 7. origin fetch
                                                 ▼
                                  ┌────────────────────────────┐
                                  │  Object storage (creator-  │
                                  │  owned, multi-region S3,   │
                                  │  e.g. us-east-1 / eu-west) │
                                  └────────────────────────────┘

         ┌─────────────────────────────┐
         │  Creator's personal AI      │ ◄── separate MCP client,
         │  (Claude / ChatGPT / etc.)  │     paired with creator's
         └─────────────────────────────┘     own Loamss session
```

**Layers, by who owns them:**

| Layer | Owner | What it holds |
| --- | --- | --- |
| L0 — Fan device | Fan | Browser session, Vibez cookie |
| L1 — Platform (Vibez) | Vibez | Catalog, fan accounts, signed-URL handle (no bytes) |
| L2 — Loamss runtime | Creator | Identity, grants, audit log, URL minting |
| L3 — CDN | Creator's CDN account | Edge cache, signature verifier |
| L4 — Object storage | Creator | Canonical bytes, multi-region |

Vibez is **never** in the byte path. Loamss is **never** in the byte path. The fan talks to the CDN; the CDN talks to S3.

---

## View 2 — Auth (three independent identity systems)

The topology has *three* identity boundaries that don't share credentials. Mixing them is a security bug.

### A. Fan ↔ Vibez (platform-local)

- Fan signs into Vibez with whatever Vibez offers (email/password, OAuth, etc.).
- Vibez stores a session cookie in the fan's browser.
- **Loamss never sees the fan's credentials.** Loamss does not know who individual fans are.
- From Loamss's perspective, Vibez is a single paired MCP client; everything Vibez asks for is attributed to *Vibez-as-platform*, not to specific fans.

### B. Vibez ↔ Loamss (paired MCP client)

- One-time pairing: creator runs `loamss client pair` and hands Vibez a `lck_<client-id>_<base64url(32 bytes)>` bearer token (the same format the runtime issues today).
- Vibez stores the token server-side. **Never** ships it to the browser.
- Every MCP call Vibez makes carries `Authorization: Bearer lck_…`.
- Loamss attributes each call to the Vibez client ID, runs `permission.Check`, and writes an audit entry with `actor.kind=client, actor.id=<vibez-client-id>`.
- The pairing can be revoked instantly from the Loamss console; the audit log retains the history.

### C. Fan binding inside the signed URL (cryptographic, opaque)

- When Vibez asks Loamss for `media.stream_url(asset_id, fan_session)`, Loamss mints a signed URL that the CDN can verify.
- The signature payload optionally encodes a **fan-binding tuple**: short-lived TTL + hash of the fan's IP / session ID. The CDN edge worker re-derives the tuple from the inbound request and rejects mismatches.
- The fan never sees a Loamss credential. The fan's *only* claim is "I am the same browser that Vibez issued this URL to."
- Trade-off: stricter binding (IP-bound) breaks on mobile networks. Looser binding (session-only) is the safer default; pick per asset class.

### D. Creator ↔ Loamss (out of band, not part of fan flow)

- Creator's personal AI (Claude / ChatGPT / a peer Loamss) pairs with the creator's Loamss instance the same way Vibez does — separate client credential, separate grant set.
- This client typically has **broader** grants than Vibez (e.g. `memory.write`, `capsule.invoke`) but is *never* in the fan's byte path.

**Key invariant:** the four credential systems above never cross. A Vibez token cannot stream bytes; a signed CDN URL cannot call MCP; a fan cookie cannot reach Loamss at all.

---

## View 3 — Read flow: fan watches a video

Every numbered step matches a step in View 1. Audit entries written by Loamss are flagged with **`[audit]`**.

```
Fan                Vibez               Loamss              CDN              S3
 │                  │                   │                   │                │
 │  1. browse       │                   │                   │                │
 │ ───────────────► │                   │                   │                │
 │  2. HTML +       │                   │                   │                │
 │     <video> tag  │                   │                   │                │
 │ ◄─────────────── │                   │                   │                │
 │                  │  3. MCP           │                   │                │
 │                  │  tools/call       │                   │                │
 │                  │  media.stream_url │                   │                │
 │                  │  (asset, fan_sid) │                   │                │
 │                  │ ────────────────► │                   │                │
 │                  │                   │ permission.Check  │                │
 │                  │                   │ → grant=media.read│                │
 │                  │                   │ → scope match     │                │
 │                  │                   │ [audit] url.mint  │                │
 │                  │                   │ mint signed URL   │                │
 │                  │                   │ (TTL=10m,         │                │
 │                  │                   │  fan_sid binding) │                │
 │                  │  4. {url}         │                   │                │
 │                  │ ◄──────────────── │                   │                │
 │  5. inject URL   │                   │                   │                │
 │     into <video> │                   │                   │                │
 │ ◄─────────────── │                   │                   │                │
 │  6. GET <url>    │                   │                   │                │
 │ ────────────────────────────────────────────────────────►│                │
 │                  │                   │                   │ verify sig +   │
 │                  │                   │                   │ fan binding    │
 │                  │                   │                   │ ─────────────► │
 │                  │                   │                   │ ◄───── bytes ──│
 │  7. ◄── bytes ──────────────────────────────────────────────              │
 │     (range-streamed, cached at edge)                     │                │
```

**What's in the audit log after step 3:**

```json
{
  "type": "media.stream_url.minted",
  "actor": { "kind": "client", "id": "vibez-prod" },
  "subject": { "kind": "asset", "id": "asset_01H…" },
  "data": {
    "ttl_seconds": 600,
    "fan_binding": "sha256:9f…",
    "region_hint": "us-east-1"
  },
  "outcome": "success"
}
```

**What's NOT in the audit log:** the fan's IP, the fan's Vibez user ID, the fan's name. Loamss never received those.

**What the CDN logs:** the CDN account belongs to the creator. The creator can run separate analytics on those logs (or not). Vibez sees only "URL handed off; user pressed play in our player."

---

## View 4 — Write flow: Vibez reports plays and revenue back to the creator

Reads are the easy half. Writes are where the trust model earns its keep — Vibez is making *attributed claims* against the creator's storage, and the audit log has to preserve which claims came from whom.

```
Vibez (back end)                Loamss                          Audit log
   │                              │                                │
   │ 1. MCP tools/call            │                                │
   │    metrics.report            │                                │
   │    {asset_id, plays:14213,   │                                │
   │     window: "2026-05-23"}    │                                │
   │ ───────────────────────────► │                                │
   │                              │ permission.Check               │
   │                              │   grant=metrics.write          │
   │                              │   scope.asset_ids ⊇ asset_id   │
   │                              │   DefaultApproval=true         │
   │                              │   (no consequential gate)      │
   │                              │ ─────────────────────────────► │
   │                              │ [audit] metrics.report         │
   │                              │   actor.kind=client            │
   │                              │   actor.id=vibez-prod          │
   │                              │   data.claim={plays,window}    │
   │                              │   outcome=success              │
   │                              │ ─────────────────────────────► │
   │                              │ memory.write entry             │
   │                              │   namespace=metrics            │
   │                              │   attribution=vibez-prod       │
   │ 2. {accepted: true,          │                                │
   │     entry_id: "01H…"}        │                                │
   │ ◄─────────────────────────── │                                │
   │                              │                                │
   │ 3. MCP tools/call            │                                │
   │    revenue.report            │                                │
   │    {amount_cents: 4280,      │                                │
   │     currency: "USD",         │                                │
   │     period: "2026-05"}       │                                │
   │ ───────────────────────────► │                                │
   │                              │ permission.Check               │
   │                              │   grant=revenue.write          │
   │                              │   DefaultApproval=false  ◄──── consequential
   │                              │ pause → console approval slip  │
   │                              │ (creator approves)             │
   │                              │ [audit] revenue.report         │
   │                              │   approved_by=creator          │
   │                              │ ─────────────────────────────► │
```

Two things to notice:

1. **Metrics writes are auto-approved**, revenue writes are not. This is the `DefaultApproval` flag on the canonical capability (see `permission-model.md`). The creator picks the gate per capability when granting; metrics typically auto, money typically manual.
2. **Every write is attributed**. The memory entry stores `attribution=vibez-prod`. If Vibez ever inflates a play count, the creator can `loamss audit log --actor=vibez-prod --since=…` and see exactly which claims came in.

---

## View 5 — Trust boundaries

Read row-by-row: what does this party see, what can they do, what's stopping them.

| Party | Can see | Can do | Cannot do | Enforcement |
| --- | --- | --- | --- | --- |
| **Fan** | Vibez UI, signed URL strings, video bytes | Stream content for which they have a valid URL | See other fans' URLs; bypass binding; access raw S3; call Loamss | CDN signature + fan-binding hash; no direct Loamss exposure |
| **Vibez (platform)** | Catalog metadata, signed-URL handles, MCP grants | Mint URLs (via Loamss), report metrics/revenue, fetch content metadata | Hold content bytes; learn creator's S3 credentials; call tools outside its grants | Bearer-token auth + per-call `permission.Check` + scoped grants + audit |
| **Creator** | Everything in their Loamss + audit log + S3 console + CDN dashboard | Revoke clients, narrow grants, rotate keys, export everything | (No restriction — they own the substrate) | They are the root of trust |
| **Capsule (running locally)** | Only what its manifest declares; only what the runtime hands it via MCP-over-stdio | Call back into runtime tools per `runtime.tools` grants | Reach the network unsandboxed; share state with other capsules; persist outside its scratch dir | Subprocess sandbox + `RuntimeHandler` permission check on every callback + cross-capsule call rejection |
| **CDN edge** | Signed URL contents, request IP, byte ranges | Verify signatures, fetch from origin, cache, deliver | See plaintext storage credentials; bypass signature; serve unsigned requests | Creator-owned CDN account; origin auth via short-lived signing keys |
| **Object storage (S3)** | Encrypted-at-rest bytes | Serve to authenticated origin pulls | Talk to anyone but the CDN's signed-fetcher role | IAM bucket policy restricts origin access to CDN role only |

---

## Failure modes (and what each costs)

| Failure | Blast radius | Mitigation in place | Worst case |
| --- | --- | --- | --- |
| Vibez token leaked | All grants Vibez holds | Token-scoped grants; revoke via console; audit shows abnormal call mix | Creator revokes within minutes; audit log reveals scope of abuse |
| Signed URL leaked (e.g. posted publicly) | One asset, until TTL expires | Short TTL (5–15 min); fan-binding hash | Strangers may stream a single asset for a few minutes |
| Loamss instance down | New URL mints + write-backs blocked | CDN keeps serving previously-issued URLs until they expire; storage stays up | Vibez sees errors on `media.stream_url`; falls back per platform UX (cached URLs / retry) |
| S3 region outage | Reads from that region | Multi-region replication; CDN failover origin | Latency spike + partial cache misses; no data loss |
| CDN signing key compromise | Could mint unauthorized URLs against creator's origin | Rotate key in Loamss + CDN; old key invalidated on rotation; audit shows the rotation event | Window between compromise and rotation; size of window depends on rotation cadence |
| Capsule misbehaves | Bounded by capsule's grants + capability checks | `RuntimeHandler` checks every callback; capsule cannot talk to network or other capsules | Capsule's grants get burned; revoke and reinstall |
| Creator's machine compromised | Total | Out of scope for runtime; the substrate root-of-trust is the user's machine | Same as any other self-hosted system |

---

## Summary

> Fans get bytes from the creator's storage via a CDN, verified by signed URLs that Loamss mints on Vibez's behalf. Vibez never holds content. Loamss never sees fan identities. The creator's audit log is the durable, hash-chained record of every URL mint and every attributed write-back. Three independent credential systems (fan↔platform, platform↔Loamss, fan-binding↔CDN) keep the trust boundaries from collapsing into each other.

---

## Related specs

- [`scenarios.md`](./scenarios.md) — Scenario 5 (creator publishing) is the textual version of this picture
- [`permission-model.md`](./permission-model.md) — `DefaultApproval`, scope match primitives, grant lifecycle
- [`audit-spec.md`](./audit-spec.md) — entry shape, hash chain, verify semantics
- [`adapter-interface.md`](./adapter-interface.md) — storage adapter SPI (the contract S3, fs-encrypted, etc. implement)
- [`mcp-surface.md`](./mcp-surface.md) — `media.stream_url`, `metrics.report`, `revenue.report` tool shapes
- [`native-apps.md`](./native-apps.md) — what Vibez has to do on its side to integrate cleanly
