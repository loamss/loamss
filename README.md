# Loamss

Loamss is open-source **personal data infrastructure**.

It is the place your data and memory live — under your control, in storage you own — and the governed interface that lets the AI tools you use plug in to see who you are. **MCP is the primary interface**: any MCP-speaking client (Claude, ChatGPT, Cursor, an agent, your own scripts) connects to your Loamss and gets scoped, permissioned, audited access to the slices of your life you've chosen to expose.

## The idea

Every AI tool you use today keeps its own siloed copy of you. ChatGPT remembers what ChatGPT has seen. Gemini knows what Google knows. Copilot knows what's in your editor. None of them actually know *you* — they each see a slice of your life and that slice belongs to them.

Loamss inverts that. Your data lives where you put it. Your memory — the durable, entity-resolved understanding of your work, your people, your projects — accumulates over years in storage you own. Then any AI tool plugs into the same brain via MCP and instantly knows who you are. Switch tools, switch models, switch decades — the brain persists.

**The tools are interchangeable. The brain is permanent. The brain is yours.**

## What Loamss is

- An open-source **runtime** that owns the lifecycle of your personal data and memory
- An **MCP server surface** that exposes governed views of that data to whatever AI tools you connect
- An open **capsule specification** for packaging the pieces that ingest, organize, expose, and act on your data
- A **registry** where capsule developers publish and users discover
- A **permission framework** with auditable capability-based consent — for both capsules acting on data and external clients reading it
- **Adapter layers** that let users plug in their own storage and memory backends
- A **console** for managing data sources, capsules, connected clients, permissions, and the audit log

## What Loamss is not

- Not a chat app. The chat surfaces are whatever you already use; Loamss is what they connect to.
- Not a model. We don't train one. We don't call one on your behalf except as needed to organize your data.
- Not a data host. You bring storage. We don't operate the database your data sits in.
- Not a walled garden. Capsules from anywhere, clients from anywhere, storage anywhere.
- Not a SaaS lock-in. If we stop being useful, you point another runtime at your data and walk away.

## Status

Early. Specifications first, reference implementations following. See `ROADMAP.md`.

## Where to look next

- `ARCHITECTURE.md` — the full technical picture
- `CLAUDE.md` — context for Claude Code agents working on this repo
- `ROADMAP.md` — what we're building in what order
- `capsule-spec.md` — the capsule format
- `cli.md` — the `loamss` CLI surface
- `scenarios.md` — end-to-end use cases the design must support
