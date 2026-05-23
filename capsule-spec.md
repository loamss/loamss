# Capsule Specification v0.1 (draft)

A **capsule** is a packaged Loamss agent. This document defines the format. Once stable, this spec is what any compliant Loamss runtime must support and what any capsule author must produce.

> **Status: draft.** This spec will change before v1.0. Breaking changes after v1.0 will be expensive — review accordingly.

## Anatomy

A capsule is a directory (or a signed tarball of a directory) with the following layout:

```
my-capsule/
├── capsule.yaml          Manifest (required)
├── signature.sig         Detached signature of capsule.yaml + code hash (required for registry)
├── README.md             Human-readable docs (recommended)
├── code/                 Capsule implementation
│   ├── server.{ts|py|…}  MCP server entry point
│   └── …                 Any supporting code
└── assets/               Optional prompts, examples, schemas
```

## The manifest (`capsule.yaml`)

```yaml
# Required
spec_version: "0.1"
name: email-drafter
version: 1.4.0                  # semver
author:
  name: Acme Capsules Inc.
  url: https://acme.example
  key_id: acme-2026-01           # public key the signature verifies against

# Required: what this capsule needs
permissions:
  - capability: email.read
    scope:
      sender: "*"                # narrow: e.g. "sarah@acme.com"
      folder: "inbox"
    rationale: "Read recent emails to draft replies."
  - capability: email.send
    scope:
      requires_user_approval: true
    rationale: "Send the drafted reply after you review it."
  - capability: files.read
    scope:
      paths: ["files/contracts/*"]
    rationale: "Reference contract documents when drafting."
  - capability: memory.read
    scope:
      entities: ["people", "projects"]
    rationale: "Recall context about correspondents and topics."
  - capability: model.call
    scope:
      tasks: ["drafting", "summarization"]
    rationale: "Generate the reply text."

# Required: what this capsule exposes
tools:
  - name: draft_reply
    description: "Draft a reply to a specific email thread."
    input_schema:
      type: object
      properties:
        thread_id: { type: string }
        instructions: { type: string }
      required: [thread_id]
  - name: list_pending_replies
    description: "List emails that look like they need a reply."

# Required: what models this capsule works with
model_requirements:
  capabilities: ["text", "long_context"]
  min_context_tokens: 32000
  preferred_quality: "high"     # high | balanced | fast
  forbidden_data_classes: []     # e.g. ["health"] to forbid sensitive routing

# Required: runtime
runtime:
  type: subprocess               # subprocess | wasm (future)
  entrypoint: ["node", "code/server.js"]
  protocol: mcp
  resources:
    memory_mb: 256
    cpu_quota: 0.5

# Optional
homepage: https://acme.example/capsules/email-drafter
repository: https://github.com/acme/email-drafter
license: Apache-2.0
description: "Drafts email replies with full context from your inbox and notes."
tags: [email, productivity]
```

## Permissions

Every data access a capsule attempts at runtime must correspond to a declared capability in the manifest. The runtime rejects undeclared accesses outright.

Capabilities are namespaced. The MVP set:

| Namespace | Examples |
|---|---|
| `email` | `email.read`, `email.send`, `email.draft` |
| `calendar` | `calendar.read`, `calendar.write` |
| `files` | `files.read`, `files.write` |
| `messages` | `messages.read`, `messages.send` |
| `memory` | `memory.read`, `memory.write` |
| `model` | `model.call` |
| `external` | `external.http` (whitelisted hosts only) |

Each capability has a `scope` schema specific to it. Scopes narrow the grant — paths, senders, folders, tags, time windows. The user sees the scope on the permission slip at install time and can narrow it further or revoke later.

`requires_user_approval: true` on any capability makes every invocation interactive — the runtime pauses and asks the user before proceeding. This is required for consequential actions like sending email, spending money, or deleting data.

## Tools

A capsule exposes its functionality through MCP tools declared in the manifest. The runtime mounts these tools into the user's agent context. Tool inputs are validated against `input_schema` before invocation.

## Model requirements

A capsule declares what kind of model it needs, not which one. The model router picks the actual model based on the capsule's requirements, the user's routing rules, and the task at hand. A capsule that hardcodes a specific provider is non-compliant.

`forbidden_data_classes` is the safety valve: a health-data capsule can declare that its outputs must never be sent to a hosted model, forcing the router to use a local one.

## The runtime contract

The runtime invokes the capsule as a subprocess speaking MCP over stdio (or HTTP for long-running capsules). On startup, the capsule:

1. Announces its MCP capabilities (matching the manifest's `tools`).
2. Receives a session handle from the runtime.
3. For each tool invocation: receives input, may call back into the runtime for `memory.query`, `files.read`, `model.call`, etc. (each subject to permission checks), returns output.
4. Exits cleanly on session end.

The capsule never directly accesses storage, memory, models, or external services. It always goes through the runtime, which mediates and logs.

## Signing

Capsules published to the registry must be signed. The signature covers:

- The full content of `capsule.yaml`
- A SHA-256 hash tree of the `code/` and `assets/` directories

The author's public key is published with their registry profile. The runtime verifies on install and again on each update.

## Versioning

Semver. The runtime allows users to pin to exact versions, follow a channel (`stable`, `beta`), or auto-update within a major version. Breaking changes to the manifest schema or tool signatures require a major version bump.

## Validation

A capsule is valid if:

1. The manifest parses and conforms to the schema.
2. The manifest version (`spec_version`) is one this runtime supports.
3. Every declared capability is recognized by the runtime.
4. Every declared tool has a valid input schema.
5. The signature (if present) verifies against the author's published key.
6. The runtime can execute the entrypoint with the declared resources.

The runtime ships a `loamss capsule validate <path>` command for authors.

## Open questions for v0.2

- Inter-capsule communication: can one capsule call another's tools? Currently no. May need it for orchestration capsules in Phase 2.
- Capsule-provided UI surfaces: should capsules be able to contribute panels to the console? Currently no.
- WASM runtime: cleaner sandboxing than subprocesses. Deferred.
- Capsule-declared data schemas: should capsules be able to extend the memory schema with their own entity types? Probably yes, with care.
