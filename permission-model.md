# Permission Model Specification v0.1 (draft)

This document defines the permission framework that gates every read, write, and external action in Loamss. It is the contract that capsule authors and external clients honor, and the source of truth for how the runtime decides what's allowed.

> **Status: draft.** Capability namespaces and scope schemas will change before v1.0. Breaking changes after v1.0 will require a migration path for existing grants.

## What this document is and is not

This spec defines:

- The conceptual model (capabilities, scopes, grants, principals)
- The capability namespaces and what each one allows
- The scope schemas attached to each capability
- The grant lifecycle (issuance, modification, revocation, expiry)
- The user-approval primitive for consequential actions
- The cross-cutting data-class restrictions
- How everything ties to the audit log

This spec does **not** define wire-level details of permission checks (those live in `mcp-surface.md` for external clients and `capsule-spec.md` for capsules), the audit log schema itself (`audit-spec.md`), or specific UX for the permission slip (the console's concern).

## The framework, in one paragraph

Loamss uses **capability-based access control**: a principal (a capsule or an external client) holds zero or more **grants**, each tying a **capability** to a **scope** and optional **expiry** and **approval flag**. Every operation the runtime performs on the user's behalf is checked against the requesting principal's grants before any storage, memory, or external adapter is touched. There is no implicit access, no "trusted" principal, and no back door. The user is the only entity that can issue, modify, or revoke grants.

## Why capability-based (not RBAC, not ACLs)

Roles and ACLs both decay over time as the system grows. New resource types appear, new principals get role assignments by analogy, and the union of "what does Alice's role actually permit?" becomes opaque. Capability-based systems force the same question to be answerable directly — "show me Alice's grants" — without joining across role tables or resource lists.

For a personal data substrate where the user needs to actually understand and audit what each AI tool can see, capabilities are the right primitive. The cost (more grants per principal) is paid by the user only at install/pair time, mediated by a good permission slip UI.

## Principals

Loamss recognizes two kinds of principals that can hold grants:

| Principal | Lifecycle | Created via | Identifier |
|---|---|---|---|
| **Capsule** | Installed → uninstalled | `loamss capsule install` | Capsule manifest `name` + signed key |
| **Client** | Paired → revoked | `loamss client pair` + console approval | Per-client credential + opaque client ID |

Both kinds of principals hold grants of the same shape, expressed in the same capability namespace. The runtime enforces them identically. The difference is purely how they entered the system (installed code vs. external connection).

A third actor — the **user** — is not a principal in this sense. The user *issues* and *revokes* grants but never holds them. The user is the only entity that can do this; the framework has no mechanism for one principal to grant capabilities to another.

## Capabilities

A capability is a **namespaced verb**: `<domain>.<action>`.

Each capability:

- Is registered with the runtime (the runtime rejects grants for unknown capabilities)
- Has a **scope schema** — the type-specific shape of how the grant can be narrowed
- May default to requiring user approval per invocation (e.g., `email.send`)
- May be marked as belonging to one or more **data classes** (see below)

### The MVP capability set

| Namespace | Capability | Direction | Default approval | Common scope fields |
|---|---|---|---|---|
| `email` | `email.read` | inbound | no | `sender`, `folder`, `time_range`, `thread_id` |
| `email` | `email.send` | outbound | **yes** | `recipient` (allow/deny lists) |
| `email` | `email.draft` | internal | no | (none — writes to local draft store) |
| `calendar` | `calendar.read` | inbound | no | `tag`, `time_range` |
| `calendar` | `calendar.write` | outbound | **yes** | `tag`, `time_range` |
| `files` | `files.read` | inbound | no | `paths` (glob list), `time_range`, `data_classes_included`, `data_classes_excluded` |
| `files` | `files.write` | outbound | optional | `paths` (glob list) |
| `messages` | `messages.read` | inbound | no | `channel`, `time_range` |
| `messages` | `messages.send` | outbound | **yes** | `channel`, `recipient` |
| `memory` | `memory.read` | inbound | no | `entities` (type list), `data_classes_included`, `data_classes_excluded` |
| `memory` | `memory.query` | inbound | no | `entities`, `data_classes_included`, `data_classes_excluded`, `time_range` |
| `memory` | `memory.write` | internal | no | `entities`, `provenance_required` |
| `memory` | `memory.forget` | internal | **yes** | `entity_id` |
| `model` | `model.call` | internal | no | `tasks` (allow list), `cost_ceiling`, `forbidden_data_classes` |
| `content` | `content.list` | inbound | no | `tag`, `type` |
| `content` | `content.read` | inbound | no | `tag`, `type`, `resource_id` |
| `content` | `content.publish` | outbound | optional | `tag` (effective public exposure surface) |
| `events` | `<type>.write` | inbound | no | `subject_pattern`, `source` (auto-set from credential) — capability name follows the event type (e.g., `content.metrics.write`, `vehicle.telemetry.write`); declared by the exposer capsule that defines the event type |
| `audit` | `audit.read` | inbound | no | `time_range`, `event_types` |
| `external` | `external.http` | outbound | optional | `hosts` (allow list), `methods` |

Directions:

- **inbound**: principal is reading user data
- **outbound**: principal is asking the runtime to take action affecting the outside world
- **internal**: principal is operating on Loamss's internal state (memory, drafts, etc.) without external effect

### Reserved namespaces

The following namespaces are reserved for the runtime and cannot be claimed by capsules:

- `runtime.*`
- `loamss.*`
- `audit.*` (except `audit.read`)
- `permission.*`
- `pairing.*`

Capsules attempting to declare capabilities in these namespaces fail validation at install time.

## Scopes

A scope is a **structured narrowing** of a capability, expressed as a JSON object matching the capability's scope schema. Scopes are validated when a grant is created and re-checked at every use.

### Scope examples

```yaml
# A capsule that reads email from a specific sender only
- capability: email.read
  scope:
    sender: "sarah@acme.com"
    folder: "inbox"
    time_range: { since: "2026-01-01" }

# A client reading memory but excluding health
- capability: memory.query
  scope:
    entities: ["people", "projects", "topics"]
    data_classes_excluded: ["health"]

# A content platform pulling public-tagged videos
- capability: content.read
  scope:
    tag: "public"
    type: "video"

# A capsule that can spend up to $0.10 per model call on drafting
- capability: model.call
  scope:
    tasks: ["drafting", "summarization"]
    cost_ceiling: 0.10
    forbidden_data_classes: ["health"]
```

### Scope semantics

- An **empty scope** (`{}`) means **maximum granted** — every field at its widest. This is rare and discouraged; the permission slip warns when a grant is requested with no scope.
- A **populated field** narrows that dimension; unpopulated fields stay at maximum.
- **Multiple grants** for the same capability are **union'd** — the principal sees the broader of any matching grant.
- Scope checks are evaluated **at the time of access**, not the time the grant was issued — so a grant with `time_range: { since: "2026-01-01" }` continues to be valid as time advances.

## Grants

A grant ties everything together:

```yaml
grant:
  id: grt-01HVZ...                    # stable, opaque, used in audit log
  principal:
    type: capsule | client
    id: "email-drafter" | "client-abc123"
  capability: email.read
  scope: { sender: "sarah@acme.com", folder: "inbox" }
  issued_at: 2026-05-23T15:00:00Z
  expires_at: null | <ISO>
  requires_user_approval: false       # overrides the capability default if set
  rationale: "Read Sarah's emails to draft replies"   # capsule/client-supplied
  user_note: ""                       # optional user-added context
  framing: "private_read" | "public_publish"   # see below
```

### Grant lifecycle

| Event | When | Effect | Audit |
|---|---|---|---|
| **Create** | Capsule install / client pairing approval | New grant added to principal | `grant.create` |
| **Modify** | User narrows scope or sets expiry via console | Existing grant updated (new `id`, old retained for audit) | `grant.modify` |
| **Revoke** | User explicitly revokes; or capsule uninstall / client revocation | Grant removed; future checks deny | `grant.revoke` |
| **Auto-expire** | `expires_at` passes | Grant becomes inactive; runtime removes on next sweep | `grant.expire` |

Revocations take effect **immediately** — any in-flight operation by the affected principal sees the revocation on its next permission check. There is no grace period.

### Management surfaces

Grants can be managed two ways. Both operate on the same underlying store; one is not authoritative over the other:

- **CLI** (always available): `loamss grant list / show / revoke` for direct, scriptable management. Documented in `cli.md`.
- **Console** (opt-in via config — see `ARCHITECTURE.md` §The Console): a web UI for issuing grants from a permission slip, modifying scopes, setting expiry, revoking, and reviewing the audit-log entries each grant has generated. Enabled by setting `console.enabled: true` in the runtime config.

Headless deployments rely on the CLI; users who want a GUI flip the console flag. The check engine is unaware of which surface produced a grant change — both write through the same `grants` table.

## The user-approval primitive

When a grant has `requires_user_approval: true` (either by capability default or explicit grant flag), every invocation of that capability is **interactive**:

1. Principal calls the capability (tool, action, write).
2. Runtime resolves the grant, finds the approval flag set.
3. Runtime returns a `loamss.approval_pending` response to the principal.
4. Runtime emits an approval request to the console + phone companion + any other notification surface the user has enabled.
5. User reviews (including the full intended action, target, content) and approves or denies.
6. Runtime resumes the call (or returns `loamss.approval_denied`).

This is the bright line between **an AI helped me** (autonomous within scope) and **an AI did something to my world** (consequential, gated by an explicit human "yes"). It is required for `email.send`, `messages.send`, `memory.forget`, most `*.write` capabilities on outbound resources, and any payment-related capability that will eventually exist.

Approvals are **single-use** — approving one send does not approve the next. Bulk approval is a UX problem for the console, not a permission-model concession.

## Data classes

Some data carries cross-cutting sensitivity that overrides ordinary scope checks. Loamss defines **data classes** as opt-in tags that can be applied to entities, files, or memory entries:

- `health` — medical records, symptoms, prescriptions, lab results
- `financial` — account balances, transactions, tax records
- `legal` — communications under attorney-client privilege, contracts in negotiation
- `intimate` — relationships, personal correspondence not intended for any third-party
- (user-extensible)

Data classes interact with permissions in three ways:

1. **`forbidden_data_classes` in `model.call` scope**: routing rule that prevents content tagged in those classes from being sent to a model. A health-data capsule can declare `forbidden_data_classes: ["health"]` on its model calls to ensure no hosted model ever sees the data.

2. **`data_classes_excluded` in `memory.read` / `memory.query` / `files.read` scope**: a grant can globally exclude classes from view. ChatGPT may be granted `memory.query` with `data_classes_excluded: ["health"]` — even health-tagged memory entries are invisible, not just redacted.

3. **`data_classes_included` in `memory.read` / `memory.query` / `files.read` scope**: the inverse — the principal sees **only** data tagged in those classes, and nothing else. A clinic AI may be granted `memory.query` with `data_classes_included: ["health"]` — every memory entry that lacks the `health` tag is invisible to the clinic, even if entities and time range would otherwise match. This is how specialist contexts are scoped: the clinic does not need (and must not see) anything outside their domain.

When both `data_classes_included` and `data_classes_excluded` are specified on the same scope, `data_classes_included` is applied first (positive filter), then `data_classes_excluded` is applied to the remainder (negative filter on top). An entry must match the include set AND not match the exclude set.

Data classes are **declared on data**, not on capabilities. The same `files.read` capability can be granted with or without `data_classes_excluded` — the protection is scope-level, not capability-level.

## Public-publish vs private-read

Two grants of the same capability can mean very different things in user-impact terms. Consider `content.read` granted to two different clients:

- **ChatGPT** with `content.read` scoped to `tag:family` — ChatGPT's user (you) sees family photos in their chats. Effective audience: you.
- **Vibez** with `content.read` scoped to `tag:public` — Vibez's millions of viewers see the videos through the platform's app. Effective audience: the public.

Same capability, same shape of grant. Dramatically different consequences.

The permission slip distinguishes these via the `framing` field on the grant:

- `private_read` — the default. The principal reads on the user's behalf.
- `public_publish` — the principal will broadcast to its own audience.

Capsules declare expected framing in their permission requests; clients declare it during pairing. The runtime enforces nothing different between them — the distinction is **UX only**, ensuring the user sees an appropriately worded slip ("Vibez will be able to show these videos to its users") rather than a misleadingly mild one ("Vibez can read these videos").

## Forbidden combinations

The framework rejects combinations that violate invariants:

| Combination | Why rejected |
|---|---|
| `memory.write` without `provenance_required` for external clients | Loss of attribution makes claims unverifiable |
| Any `*.send` capability without `requires_user_approval` or explicit per-grant bypass for trusted automation | Avoids silent send-on-behalf scenarios |
| `external.http` with `hosts: ["*"]` for any client | Effective unrestricted outbound; never granted in one slip |
| Data-class grant in a public-publish framing without explicit slip warning | UX guardrail; prevents accidental exposure of sensitive class on a public surface |

These rejections happen at grant-creation time. The user can override via `loamss grant create --force` for advanced cases, but the console UI does not surface that path.

## Audit integration

Every permission-framework event produces an audit entry:

| Event | Audit type |
|---|---|
| Grant created | `grant.create` |
| Grant modified | `grant.modify` |
| Grant revoked | `grant.revoke` |
| Grant auto-expired | `grant.expire` |
| Capability check (allow) | `check.allow` (sampled at high rate; full payload kept) |
| Capability check (deny) | `check.deny` (always full payload) |
| Approval requested | `approval.requested` |
| Approval granted by user | `approval.granted` |
| Approval denied by user | `approval.denied` |
| Approval timed out | `approval.timeout` |

Denials are **never silent**. Every denied request produces an explicit audit entry the user can review — even if the principal interpreted the denial as a normal error.

## Open questions

- **Capability composition**: should a capsule be able to declare derived capabilities (e.g., `email.summarize = email.read + model.call + memory.write` as a single user-facing slip)? Initial leaning: no in v0.1, revisit when capsule UX matures.
- **Time-bounded session escalation**: should the user be able to grant a temporary higher scope (e.g., "for the next hour, ChatGPT can see health data")? Probably yes; not specified in v0.1.
- **Group grants**: granting the same scope across multiple principals (a "ChatGPT family" of three paired devices, all with identical scopes)? Probably yes in Phase 2 to avoid permission-slip fatigue.
- **Negative scopes (deny lists)**: currently scopes are positive narrowings ("only this sender"). Adding "everything except this sender" is awkward. Either add explicit deny lists or document the workaround. Defer to v0.2.
- **Default approval threshold**: should the user be able to set "approve all my own actions implicitly when initiated via the console" without losing the audit trail? Probably yes; UX-only, not a permission model change.
- **Cross-principal trust**: a capsule cannot grant capabilities to another capsule, but capsules in Phase 2+ may collaborate. Will need an inter-capsule capability shape — likely "this orchestration capsule may invoke these specific tools on these specific capsules" rather than blanket capability transfer.
- **Programmatic permission requests**: can a paired client request additional scope mid-session via a tool call, prompting a new permission slip? Initial leaning: yes, with the user always in the loop.
- **Storage of grant history**: how long do we keep revoked grants in the database (vs. moved to audit-only)? Affects the "modify" semantics. Initial leaning: retain forever for audit, with a query API that defaults to active-only.
