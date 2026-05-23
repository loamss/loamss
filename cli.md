# CLI Specification v0.1 (draft)

The `loamss` binary is the primary scriptable surface for the runtime. The console covers the same ground with a GUI for interactive use; the CLI exists for bootstrap (before the console is running), headless deployments (servers, NAS, CI), automation (cron, scripts), and capsule development.

> **Status: draft.** Surface and verbs will change before v1.0. This document captures intent — the actual commands land progressively across Phases 0–2.

## Conventions

- Single binary, git-style subcommands (`loamss <noun> <verb> [args]`).
- All commands are non-interactive by default, with the exception of `loamss init` and any flow that requires OAuth or user approval. Interactive prompts can be suppressed with `--yes` or equivalent flags; required input then comes from flags or stdin.
- Output is human-readable by default; `--json` (or `--format=json`) is supported on every read command for scripting.
- Exit codes: `0` success, non-zero failure. Reserved codes documented per command.
- Every command that touches data or external services emits an audit log entry, same as the runtime itself.

## Command groups

The CLI groups by noun, not by phase. Phase tags below indicate when each verb is expected to land.

### Runtime lifecycle

The minimum surface to run Loamss. Phase 0 / Phase 1.

```bash
loamss init                     # First-run wizard: storage, memory, MCP endpoint, console
loamss start                    # Start the runtime daemon (foreground by default)
loamss start --detach           # Background
loamss stop                     # Stop the daemon
loamss status                   # Running? Connected sources? Connected clients?
loamss version
loamss doctor                   # Health check: storage reachable? memory? OAuth tokens valid?
```

`loamss init` is the only command that is interactive by default. It writes a config file the user can later edit by hand or via `loamss config`.

### Capsules

The user side and the developer side share the noun. Phase 1 for installation verbs; `new`, `validate`, `dev`, `sign`, `publish` are Phase 1–2 as the registry comes online.

```bash
# User-facing
loamss capsule list                              # Installed capsules
loamss capsule install <name>[@version]          # From registry
loamss capsule install ./path/to/capsule         # Local
loamss capsule uninstall <name>
loamss capsule info <name>                       # Manifest, permissions, version, status
loamss capsule update [<name>]                   # All or one
loamss capsule run <name> <tool> [args...]       # One-shot invocation (mostly testing)

# Developer-facing
loamss capsule new <name>                        # Scaffold (TS or Python? type? permissions?)
loamss capsule validate [path]                   # Lint manifest + signature + entrypoint
loamss capsule dev [path]                        # Local harness against fixture data
loamss capsule sign [path]                       # Sign for publishing
loamss capsule publish [path]                    # Push to registry
```

`loamss capsule new` is the on-ramp for the entire third-party capsule ecosystem. It is a Phase 0 priority: the SDK ergonomics rest on it.

### Data sources

Connectors that pull data from external services into user storage. Phase 1.

```bash
loamss source list                               # Connected sources, sync status
loamss source add <type>                         # gmail, gcal, drive, slack, github, fs, ...
                                                # Opens OAuth flow if needed
loamss source remove <name>
loamss source sync [<name>]                      # Manually trigger ingestion
loamss source status <name>                      # Last sync, error rate, items pulled
```

Sources are distinct from capsules in the CLI even if they share infrastructure: users think of "I connected Gmail" differently from "I installed a capsule."

### Clients and permissions

External MCP clients (ChatGPT, Cursor, peer Loamsses, etc.) connecting to read scoped views. Phase 1, alongside the MCP surface.

```bash
loamss client list                               # Connected clients
loamss client info <id>                          # Scopes, last access, access count
loamss client revoke <id>                        # Immediate
loamss client pair                               # Generate a one-time pairing code
loamss client pair --name "ChatGPT laptop"       # Named pairing

loamss grant list                                # All grants across capsules and clients
loamss grant show <id>                           # Full scope detail
loamss grant revoke <id>
```

**Pairing** is the bootstrapping primitive for new clients. The CLI version prints a one-time code that the user pastes into the client's MCP configuration. The console will offer a QR-code version. This primitive is not yet specified elsewhere and should be captured in the MCP surface spec when written.

### Memory

User-facing operations on the memory layer. Phase 1, lean set; richer queries arrive with the Phase 2 memory features (entity resolution, knowledge graph).

```bash
loamss memory query "<text>"                     # Search memory, dump matches
loamss memory show <entity-id>                   # Dump a specific entity
loamss memory forget <entity-id>                 # Remove (and log)
loamss memory rebuild [--since=<time>]           # Re-process from source data
loamss memory stats                              # Entities, relationships, size on disk
```

`memory forget` is the user-facing "delete me" primitive. Surfacing it as a first-class verb matches the "user owns their data" principle.

### Audit

The audit log is a first-class user surface. The CLI must make it script-friendly. Phase 1.

```bash
loamss audit tail                                # Live stream
loamss audit tail --client=chatgpt               # Filter
loamss audit log [--since=<time>] [--client=<>] [--capsule=<>] [--capability=<>]
loamss audit export --format=jsonl > audit.jsonl # For long-term retention or analysis
```

### Data ownership and portability

The walkaway promise made concrete. Phase 1 — shipping early is a credibility signal even if the polish lags.

```bash
loamss export                                    # Full dump: storage + memory + audit
loamss export --memory-only
loamss import <path>                             # Restore into a fresh Loamss
loamss backup                                    # Incremental, to configured target
loamss restore <backup>
```

If `export` / `import` cannot be written, the user-ownership claim is not real. These verbs are the test.

### Config

For headless deployments and edge cases the console doesn't cover. Phase 1, low priority.

```bash
loamss config get <key>
loamss config set <key> <value>
loamss config edit                               # Opens $EDITOR on the config file
```

### Federation (deferred)

Peer-to-peer Loamss sharing. Phase 3.

```bash
loamss peer list
loamss peer add <loamss-url>
loamss peer share <scope> --to=<peer>
loamss peer revoke <peer>
```

Don't build until Phase 3. Naming the surface now keeps it consistent when it lands.

## Phase 1 MVP cut

The smallest CLI that constitutes a usable Loamss:

```bash
loamss init
loamss start / stop / status / version / doctor
loamss source add / list / sync
loamss capsule install / list / uninstall / new / validate / dev
loamss client pair / list / revoke
loamss grant list / revoke
loamss memory query / forget
loamss audit tail / log
loamss export
```

That is ~20 commands. Enough to: install Loamss, connect a data source, install a capsule, pair an external MCP client, watch the audit log, and walk away with all your data.

## What this surface is telling us about the model

Writing the CLI exposed five things about the runtime that the architecture doc does not yet name explicitly:

1. **`loamss client` is a real noun.** External MCP clients are first-class — they need their own listing, info, revoke. The current ARCHITECTURE.md does not name "MCP surface" as a top-level component; it should.
2. **`loamss source` vs. `loamss capsule` is a real split.** Even if sources are implemented as a capsule subtype, users think about them as a separate category. The CLI honors that.
3. **`loamss memory` is a noun, not a side effect.** Memory is the product; the verbs prove it.
4. **Pairing is an unspecified primitive.** How a new external client first establishes trust with an Loamss is not in any current spec. The CLI forces it out: one-time codes (CLI), QR (console), or invite link (federation, later).
5. **`loamss export` is the test of the ownership claim.** If it cannot be written, the architecture is wrong.

These observations feed back into the next pass of ARCHITECTURE.md and into a future `mcp-surface.md`.

## Open questions

- **`capsule new` UX**: wizard (interactive) or flags (scriptable)? Probably both, with sane defaults.
- **`loamss start` as daemon vs. foreground**: which is default? Initial leaning: foreground, with `--detach` for daemon. Match `caddy run` / `tailscaled` conventions.
- **`loamss pair` vs. `loamss client pair`**: shorter alias for the common case? Or keep the noun structure pure?
- **Output format**: support `--format=table|json|yaml` uniformly, or only `--json`? Initial leaning: just `--json` + default human-readable. Yaml is rarely needed for CLI output.
- **Plugin commands**: should capsules be able to contribute CLI subcommands (e.g., `loamss gmail search "..."`)? Probably no for v1 — keeps the surface predictable. Revisit in Phase 2.
- **Telemetry / phone-home flags**: explicitly none for v1, in line with the trust model. Worth stating in `--help` so users see the absence.
