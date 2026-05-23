# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Loamss — context

Loaded automatically by Claude Code at session start. Keep it concise and current. Detailed information lives in `ARCHITECTURE.md`, `ROADMAP.md`, `capsule-spec.md`, `mcp-surface.md`, `permission-model.md`, `adapter-interface.md`, `cli.md`, and `scenarios.md` (planned to move under `docs/` once that directory lands).

**Current state**: Phase 0 spec set is complete. The repo is specs only — `README.md`, `ARCHITECTURE.md`, `ROADMAP.md`, `capsule-spec.md`, `mcp-surface.md`, `permission-model.md`, `adapter-interface.md`, `cli.md`, `scenarios.md`, this file. None of the directories shown under "Repo layout" exist yet; treat that section as the planned shape, not the current shape. **`scenarios.md` is the design's correctness check** — every architecture change should preserve every scenario or explicitly acknowledge breaking one.

## Canonical URLs

- Domain: `loamss.com` (registered, no site yet)
- GitHub org: `github.com/loamss`
- Canonical repo: `github.com/loamss/loamss`
- npm org scope: `@loamss` — **reserved, not published to.** The SDK ships in Phase 2; the org reservation is purely defensive (prevents squatting).
- License: Apache-2.0 (see `LICENSE` at repo root)

## What Loamss is, in one paragraph

Loamss is open-source personal data infrastructure. The user brings storage, identity, and compute. Loamss ingests data from connected sources into user-owned storage, builds a durable memory layer (entity resolution, vector index, knowledge graph) on top of it, and exposes governed views via MCP to whatever AI tools the user connects — Claude, ChatGPT, Cursor, peer Loamss instances, content platforms, scripts. Every external read and every external write-back (metrics, revenue events, etc.) passes through the permission framework and the audit log. The tools are interchangeable; the brain is permanent and belongs to the user.

## Core principles (these override convenience)

1. **The user owns their data.** Loamss never holds primary copies. Storage and memory backends are user-configured at deploy. If we ever feel tempted to "just keep a copy for convenience," stop.
2. **Capabilities are explicit and revocable.** Every data access goes through the permission framework. No back doors, no implicit grants, no "trusted" capsules that bypass it.
3. **Models are pluggable.** Never assume a specific model. Routing rules and model identity belong to the user.
4. **Open by default.** Specs, runtime, SDKs, adapters — all open source. Closed code is a smell.
5. **Boring is good.** This is infrastructure. Prefer stable, well-understood tools over novel ones. Stability is a feature.
6. **Audit everything.** Every data access, every model call, every external action gets logged. The audit log is a first-class user-facing surface, not a debug artifact.

## Repo layout

```
loamss/
├── README.md                Public intro
├── CLAUDE.md                This file
├── ARCHITECTURE.md          Full technical picture
├── ROADMAP.md               Phased build plan
├── docs/
│   ├── capsule-spec.md      The open capsule standard
│   ├── permission-model.md  Capability framework details
│   └── adapter-interface.md Storage / memory / model adapter contracts
├── runtime/                 The butler — Go
├── sdk/
│   ├── typescript/          Capsule SDK for TS authors
│   └── python/              Capsule SDK for Python authors
├── adapters/
│   ├── storage/             Drivers for storage backends
│   ├── memory/              Drivers for vector / graph DBs
│   └── model/               Drivers for model providers
├── connectors/              Data ingestion adapters (Gmail, Calendar, etc.)
├── console/                 User-facing UI — Next.js
└── registry/                Capsule marketplace backend
```

Each top-level directory may have its own CLAUDE.md with subsystem-specific context (loaded lazily by Claude Code when working in that subtree).

## Stack (recommended starting point — revisit if a directory has reason to differ)

- **Runtime**: Go. Single binary, strong daemon ecosystem, cross-platform. Standard library plus minimal deps.
- **SDKs**: TypeScript first, Python second.
- **Console**: Next.js + React + TypeScript + Tailwind.
- **Registry backend**: Go.
- **Local storage for runtime state**: SQLite (the user's *data* storage is separate and user-configured).
- **Capsule manifests**: YAML.
- **Inter-process protocol**: MCP over stdio or HTTP, depending on capsule type.

## Common commands

The full CLI surface lives in `cli.md`. None of it is implemented yet. The Phase 1 MVP cut is roughly:

```bash
loamss init                          # First-run setup
loamss start / stop / status / doctor
loamss source add / list / sync      # Connect data sources (Gmail, etc.)
loamss capsule install / list / new / validate / dev
loamss client pair / list / revoke   # External MCP clients
loamss memory query / forget
loamss audit tail / log
loamss export                        # The walkaway promise
```

Build tooling (planned, not yet present):

```bash
# make build         Build the runtime binary
# make test          Run all tests
# make lint          Run linters
```

## Code style

- **Go**: standard `gofmt`, `golangci-lint` on default settings. Errors are values; wrap with context (`fmt.Errorf("...: %w", err)`). Avoid panics outside main. Small interfaces. No global state in the runtime.
- **TypeScript**: strict mode on. ES modules. Prefer named exports. No implicit `any`.
- **Python**: 3.11+. Type hints required on public APIs. `ruff` for lint and format.
- **Commits**: imperative mood, scoped prefix (`runtime:`, `sdk-ts:`, `docs:`).
- **Tests**: behavior-first. No mocks unless necessary. Integration tests against a real (containerized) backend whenever feasible.

## What to prioritize when in doubt

1. **User trust** > developer convenience.
2. **Spec stability** > shipping a feature. Once the capsule spec is public, breaking it is expensive. Get it right before locking it.
3. **Audit completeness** > performance. If you can't log it, don't ship it.
4. **Adapter portability** > built-in features. If a feature locks users into a specific backend, redesign.

## What to avoid

- Hardcoded model providers. Use the model router.
- Direct storage access from anywhere except the storage adapter layer.
- Capsules with implicit permissions. Every grant must be visible in the audit log.
- Telemetry that phones home. If we ever want it, it's opt-in and goes through the same consent system as everything else.
- Anything that makes the runtime hard to self-host. Single binary, no required cloud services.

## Where things are heading

We're currently in **Phase 0** (foundations: specs and skeletons). See `ROADMAP.md`. Don't optimize for Phase 2 problems while Phase 0 is incomplete.
