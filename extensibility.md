# Extensibility Specification v0.1 (draft)

This document captures the architectural principle that keeps Loamss from being limited to the use cases its authors imagined. It defines what's stable, what's open for extension, who can extend each surface, and the anti-patterns that erode extensibility if left unchecked.

> **Status: draft.** This doc is more principle than mechanism. The specific extension mechanisms live in `capsule-spec.md`, `mcp-surface.md`, `permission-model.md`, `adapter-interface.md`, and `audit-spec.md`. This doc points at them and names the discipline that holds them together.

## What this document is and is not

This spec defines:

- The principle the architecture honors (and the reasoning behind it)
- The eight extension points where the system grows without core changes
- The contract for what's stable vs. extensible
- Worked examples of unanticipated scenarios that should "just work"
- Risks that erode extensibility, and how to mitigate them
- Anti-patterns to catch in code review
- The rare process for introducing a genuinely new primitive

This spec does **not** define:

- The mechanics of any specific extension surface (those live in the respective specs)
- A capability catalog (that's emergent, community-maintained)
- A type registry (resource types, entity types, event types are declared by capsules, not centrally cataloged)

## The principle

**The runtime supports verbs and shapes, not specific use cases.**

Every scenario Loamss handles — including ones nobody on the team has thought of yet — should be expressible as a composition of the architecture's primitives:

- A **capability namespace** with a **scope schema**
- A **resource type** or **event type** declared by a capsule
- A **kind of consumer** that speaks MCP

If a new scenario fits this shape, it extends the system without changing the runtime. If a scenario doesn't fit, the answer is almost never "modify the runtime to handle this case" — it's "decompose the scenario into the primitives, or identify a genuinely missing primitive."

The corollary: **the seven scenarios in `scenarios.md` are demonstrations of the primitives, not the universe of features.** They are checked against the design to keep it honest, not used to define it.

## What's stable

These do not change without a major version bump and a migration path. Capsules, clients, and adapters can rely on them:

1. **The five primitives**: pairing, scoped grants, MCP wire protocol, user-owned storage, audit chain
2. **The capsule taxonomy roles**: ingestor / organizer / exposer / actuator (a capsule can wear multiple roles; the roles themselves are closed)
3. **The trust model**: user > runtime > adapters > capsules > external clients (orthogonal: models)
4. **The audit-as-user-surface invariant**: every gated operation produces an entry; entries are append-only; the chain is integrity-checkable
5. **The export contract**: `loamss export` produces a self-contained archive that another runtime can import
6. **The permission framework's structure**: capabilities are namespaced strings, scopes are typed JSON, grants are user-issued and revocable

## What's extensible

The runtime accepts these additions without code changes:

### Capsule authors can introduce

| Surface | How | Spec reference |
|---|---|---|
| New capability namespace | Declared in capsule manifest with scope schema | `capsule-spec.md`, `permission-model.md` |
| New scope field for an existing capability they own | Same | Same |
| New resource type | Declared by an exposer capsule | `mcp-surface.md`, `capsule-spec.md` |
| New event type (for client write-back) | Declared by an exposer capsule, generates a `<type>.write` capability | `mcp-surface.md`, `permission-model.md` |
| New memory entity type | Declared in `memory_extensions` in the capsule manifest | `capsule-spec.md` |
| New MCP tools | Standard MCP tool declaration, mounted by the runtime | `mcp-surface.md` |

### Adapter authors can introduce

| Surface | How | Spec reference |
|---|---|---|
| New storage backend | Implement the storage adapter interface | `adapter-interface.md` |
| New memory (vector) backend | Implement the memory adapter interface | `adapter-interface.md` |
| New model provider | Implement the model adapter interface | `adapter-interface.md` |

### External-client developers can introduce

| Surface | How | Spec reference |
|---|---|---|
| A new kind of consumer (AI tool, content platform, hardware device, peer Loamss) | Speak MCP, follow the pairing flow | `mcp-surface.md` |

That last row is the most powerful: **anything that speaks MCP and follows the pairing flow works.** No Loamss-specific integration code on the consumer side. No runtime changes on the Loamss side. The consumer just shows up and asks.

### Runtime maintainers handle the rare cases

| Surface | When | Spec reference |
|---|---|---|
| New transport for the MCP surface (BLE, NFC, gRPC) | Rare, orthogonal to the surface | `mcp-surface.md` |
| New approval handler interface (hardware wallet, federated multi-party) | Rare, when default approval flows don't cover the case | `permission-model.md` open questions |
| New audit subscription mechanisms (e.g., a transparency-log gateway) | Rare | `audit-spec.md` open questions |

These are the only changes that require runtime code, and they're explicitly designed to be additive — orthogonal to the existing surfaces, not modifications of them.

## What's expensive to change

These changes require a major version bump, a migration story, and (in practice) a deprecation period. Listed here so design changes around them are weighed honestly:

- The five primitives themselves
- The audit chain hash algorithm (would invalidate existing chains)
- The capsule taxonomy roles (would require capsule rewrites)
- The MCP wire format itself (mostly defer to upstream MCP)
- The capability/scope schema validation model (would require grant migration)
- The export archive format (would break older exports being importable)

The goal is to never need to change these. Get them right in v0.1, evolve them carefully, deprecate before removing.

## Worked examples — scenarios the design must handle without runtime changes

These are not in `scenarios.md` because they haven't shipped or been demanded. They're here to demonstrate extensibility by checking that the architecture supports them through the existing primitives.

### Smart home control

A community-maintained `home-control` capsule:

- Capability namespace: `home.*` — `home.thermostat.read`, `home.thermostat.write`, `home.lights.write`, `home.lock.read`, `home.camera.read`, etc.
- Exposes `loamss://home/device/<id>` resources for state inspection
- Actuator role for commands; `home.lock.write` requires user approval
- Connects to a Home Assistant or Matter hub via `external.http` capability with whitelisted local hosts

**Runtime changes needed**: none. **Spec changes needed**: none. **What the user sees**: a permission slip with capabilities described in the capsule's rationale, scoped to specific devices or rooms.

### Research collaboration

A geneticist shares de-identified data with a consortium:

- New resource type `loamss://genomic/dataset/<id>` declared by an exposer capsule
- Standard `files.read` with `data_classes_included: ["research_consent"]`
- Time-bounded grant for the study duration
- The consortium's MCP system is just another paired client

**Runtime changes needed**: none. **What's reused**: pairing, scoped grants, data classes (with the include-set scope from the recent audit), the standard `files.read` capability.

### Vehicle telemetry

Car writes location and diagnostic events to a personal Loamss via an OBD-II MCP bridge:

- New event types `vehicle.location`, `vehicle.diagnostic`, `vehicle.fuel`
- Each declared by an exposer capsule, generating corresponding `<type>.write` capabilities
- Stored as attributed claims (same mechanism as platform write-backs in scenario 6)
- Memory query "where was I last Tuesday" works because the same memory layer handles vehicle events as everything else

**Runtime changes needed**: none. **What's reused**: event write-back, attributed claims, memory query.

### Government attestation

A government service requests proof of address for a benefits application:

- Capability namespace declared by an `attestation` exposer capsule: `attestation.address`, `attestation.identity`, `attestation.income`
- Each capability returns a signed attestation document, not the raw underlying data
- Time-bounded grant (15 minutes)
- The audit entry is itself the proof-of-disclosure document the user retains

**Runtime changes needed**: none. **What's reused**: capabilities, scoped grants, time bounds, audit log as user-facing record.

### Health-data-only specialist

A school nurse needs read access to a student's allergy and medication info during a school trip:

- Standard `memory.query` + `files.read` with `data_classes_included: ["health"]` and `entities: ["allergy", "medication"]`
- Time-bounded for the trip duration
- Auto-revoke

**Runtime changes needed**: none. **What's reused**: the same primitives that power the clinic scenario in `scenarios.md` §2, with different scope.

All five examples land entirely as capsule + manifest + scope work. None require touching the runtime, the MCP surface contract, the permission framework, the adapter interfaces, or the audit chain.

## Risks and mitigations

Three things could erode extensibility if left unaddressed:

### 1. Capability name proliferation

**Risk**: every capsule invents its own vocabulary; permission slips become unintelligible; users grant blindly because the words mean nothing.

**Mitigations**:

- Every capability declaration **must include a human-readable rationale** that survives the manifest into the permission slip
- The capsule registry encourages canonical capability names through review; a "common capability catalog" can emerge as a community resource without being enforced
- The console offers a "what does this mean" affordance for any capability the user hasn't seen before — pulling the rationale, registry reputation, and any community documentation
- Capability namespaces conventionally use **reverse-DNS prefixes** for non-canonical extensions (`com.acme.tax.*`) until they earn canonical status

### 2. Special-case code creep

**Risk**: someone lands a runtime commit titled "fix: handle Vibez differently." Every such commit is a tax on the architecture that will eventually demand a rewrite.

**Mitigations**:

- Code review rule: **no string-match logic against specific capsule names, client names, capability names, or consumer identifiers** anywhere in the runtime. All such matches must go through declared metadata (capsule manifests, capability rationales, scope fields).
- Anti-pattern list in this doc (below) used as a code review checklist
- A "special-case scan" CI check that flags suspicious patterns in PRs — names like `vibez`, `chatgpt`, `cursor` appearing in identifier positions is an automatic review trigger

### 3. Scenario lock-in

**Risk**: people treating the seven scenarios in `scenarios.md` as a contract; refusing valid changes because "scenario 4 would break."

**Mitigations**:

- `scenarios.md` says explicitly: *"Remove or revise scenarios that no longer reflect the design."*
- Scenarios are **demonstrations of the primitives**, not the spec
- New primitives must be justified on their own merits, not by "the scenarios don't cover X"

## Anti-patterns (for code review)

Catch these in pull requests:

1. **String matching against named entities**: `if capsule.name == "tax-organizer"` in any runtime code. The runtime doesn't know capsules by name; it knows them by their manifest.
2. **Hardcoded capability lists**: a switch statement enumerating known capabilities. Capabilities are declared, not enumerated.
3. **Hardcoded entity type lists in the memory layer**: `if entity.type in ["person", "project", "topic"]`. Entity types are open.
4. **Hardcoded resource type lists in the MCP surface**: `if resource_type == "content.video"`. Resource types are declared by exposer capsules.
5. **Hardcoded transports**: a function that assumes HTTP and won't work for stdio or future transports. Transport choice belongs to the transport layer, not request handling.
6. **Hardcoded approval UX**: an approval flow that only works through the console and won't accept a phone-companion or hardware-wallet handler. Approval handlers should be pluggable.
7. **Hardcoded model providers**: `if model_provider == "anthropic"`. Provider choice belongs to the model adapter, not capsule or client code.
8. **Hardcoded scope field interpretations** outside the capability's owning code: the runtime validates scope-shape via the schema; it doesn't know what `cost_ceiling` means unless the `model.call` capability's owner tells it.

If a feature seems to require any of these, it's almost always a sign that the feature should be expressed differently — usually as a manifest declaration, a capability, or a new adapter.

## How to introduce a genuinely new primitive

Rare. Most new requirements are extensions of existing primitives, not new ones. But sometimes a real new primitive is needed (e.g., the original "external clients write events back into memory" was a new primitive in scenario 5 that hadn't been considered before).

The process:

1. **Articulate why the existing primitives fail** to express the requirement. If they don't, decompose into them and stop.
2. **Write a design doc** proposing the new primitive: its shape, its interaction with the existing primitives, its scope/permission semantics, its audit signature.
3. **Validate against `scenarios.md`** — does the new primitive preserve every scenario? Does it enable scenarios that previously needed runtime changes?
4. **Identify spec impact**: which of the v0.1 specs need additions (additive) vs. modifications (breaking). Modifications require a deprecation plan.
5. **Implement, then update specs** — the order matters; the spec writes itself once the implementation forces clarity.

Skipping any of these steps produces architectural debt that survives years.

## Open questions

- **Canonical capability catalog**: should the project maintain a public list of "well-known" capability names with stable semantics, or let conventions emerge purely from the registry? Initial leaning: emergent, with the registry offering "verified canonical" badges for capabilities that have community-validated shapes.
- **Capability namespace conflicts**: two capsules both claim `tax.*` with incompatible scope schemas. Initial leaning: reverse-DNS prefixes (`com.acme.tax.*`) for unverified namespaces; canonical short names (`tax.*`) only after community validation.
- **Resource type collision**: same problem at the resource-URI level. Same mitigation.
- **When MCP itself evolves**: if upstream MCP introduces a feature that overlaps with one of our extensibility surfaces (e.g., a typed subscription mechanism), do we drop our version? Initial leaning: yes, defer to upstream when it stabilizes.
- **Adapter ecosystem governance**: as third-party adapters grow, how does the project signal trust levels? Initial leaning: canonical adapters in the main repo; community adapters in a sibling repo with a vetting badge.
- **The plug-in registry as a single point of trust**: if the canonical registry becomes too gatekeeperly, capsule authors fork their own registries — a healthy outcome we should make easy, not fight.
