---
name: Feature request
about: Suggest a change that fits within the existing design
title: "[feat] "
labels: enhancement
assignees: ''
---

> **Before opening**: read [`extensibility.md`](../../extensibility.md). Most features for Loamss are extensions (new capsules, new capabilities, new adapter implementations) that don't require runtime changes. If yours fits there, you don't need this issue — you need to build the extension.
>
> This template is for changes to the runtime, the specs, or the core surfaces.

## What's the change?

## Why does it matter?

<!-- What use case is currently impossible or awkward? Cite a scenario from scenarios.md, or describe a new one. -->

## How does it fit the existing design?

<!-- Which extension point would it use, or which primitive would it modify? If it modifies a primitive, what's the migration story? -->

## Alternatives considered

<!-- Did you consider expressing this as a capsule, an adapter, or a new client? Why isn't that sufficient? -->

## Have you checked?

- [ ] `extensibility.md` — this isn't already a documented extension point
- [ ] `scenarios.md` — this isn't covered by an existing scenario
- [ ] `ROADMAP.md` — this isn't deferred to a later phase
- [ ] Other Issues and Discussions — this hasn't been raised already
