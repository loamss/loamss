# Contributing to Loamss

Thanks for considering contributing. Loamss is open-source personal data infrastructure, built in public, on a deliberate cadence. This document tells you how to get involved.

## Where to discuss

- **Questions, ideas, open-ended discussion** → [GitHub Discussions](https://github.com/loamss/loamss/discussions)
- **Concrete bugs, design defects, or work items** → [GitHub Issues](https://github.com/loamss/loamss/issues)
- **Security vulnerabilities** → see [SECURITY.md](SECURITY.md), not the public issue tracker

If you're not sure which fits, default to Discussions. Issues are for things with clear scope.

## Current state

The project is **pre-Phase-1**. The specs are complete and reviewed; the Go runtime hasn't been written yet. That means:

- Spec-level feedback (gaps, contradictions, edge cases the design doesn't cover) is the most valuable contribution right now.
- Code contributions to a `runtime/` directory will be welcome once that directory exists.
- Capsule and adapter development is a Phase 2 concern; the SDKs aren't published yet.

Read [`ROADMAP.md`](ROADMAP.md) for what's expected in each phase.

## How to contribute

### Spec-level feedback (Phase 0)

This is the highest-leverage contribution today.

1. Read the spec set — `ARCHITECTURE.md`, `capsule-spec.md`, `mcp-surface.md`, `permission-model.md`, `adapter-interface.md`, `audit-spec.md`, `extensibility.md`, `cli.md`, `scenarios.md`.
2. Try to design something concrete against it on paper — a capsule, a client integration, an adapter.
3. Open a Discussion with what you found: where the specs disagree with themselves, where you got stuck, where a scenario you care about isn't expressible.

### Documentation improvements

Typos, clarifications, broken links, missing cross-references — open a PR directly.

### Code contributions

Will be welcome once Phase 1 begins. Until then, please discuss before writing code; the spec-vs-implementation contract isn't finalized.

When code contributions open:

- **Match the project conventions** documented in [CLAUDE.md](CLAUDE.md) under "Code style"
- **Tests required** — behavior-first, no mocks unless necessary, integration tests against real (containerized) backends when feasible
- **Commit messages** — imperative mood, scoped prefix (`runtime:`, `sdk-ts:`, `docs:`)
- **Keep PRs focused** — one concern per PR; large refactors warrant a prior Discussion

## Licensing

By submitting a contribution to this project, you agree to license it under the project's existing license ([Apache-2.0](LICENSE)). The Apache-2.0 license includes a patent grant from contributors to the project, which is part of why we chose it.

No CLA. No additional paperwork. The act of submitting a PR is the agreement.

## Conduct

We follow the [Contributor Covenant 2.1](CODE_OF_CONDUCT.md). Read it. Treat other contributors well. The project is small enough that any conflict will be addressed personally; the goal is a community where capsule developers, adapter authors, and integrators can build without friction.

## What we won't accept

- Code that hardcodes a specific AI provider, storage backend, or model — the adapter layers exist for a reason
- Capabilities or scopes that bypass the permission framework
- Telemetry or phone-home features without explicit user opt-in
- Closed-source dependencies in the core runtime path
- "Helpful" features that erode the user's data ownership

See [extensibility.md](extensibility.md) for the broader anti-pattern list.

## Recognition

Contributors are credited in commit history (the canonical record) and, on request, named in release notes. We don't maintain a separate `CONTRIBUTORS` file — GitHub's contributor view is the source of truth.

## Questions?

Open a Discussion. We respond within a few days for non-urgent items, faster for security or blocker reports.
