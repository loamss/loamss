# runtime/CLAUDE.md

Subsystem context for Claude Code sessions working in `runtime/`. Loaded lazily by Claude Code when files in this subtree are edited.

The repo-level `CLAUDE.md` covers the project's principles, the spec set, and the trust model. This file adds the conventions that only apply to the Go runtime code.

## What lives here

The reference implementation of the Loamss runtime. Single binary, OS-level daemon, written in Go.

Components (current):

- `cmd/loamss/` — binary entry point
- `internal/cli/` — cobra command tree (`version`, `init`, `config`, `doctor`, `status`, `start`)
- `internal/config/` — schema, defaults, file + env loader, context plumbing
- `internal/server/` — HTTP listener with `/healthz` and `/version`

Components (planned, not yet present):

- `internal/runtime/` — top-level orchestration (lifecycle of adapters, MCP surface, audit writer, capsule host)
- `internal/mcp/` — MCP surface implementation
- `internal/permission/` — capability framework + grant store
- `internal/audit/` — audit log writer, hash chain, hot store
- `internal/memory/` — memory layer (entities, graph, episodic); sits *above* the memory adapter
- `internal/adapter/` — adapter SPI + concrete implementations
  - `internal/adapter/storage/` — storage adapters (fs-encrypted, sqlite, s3, postgres)
  - `internal/adapter/memory/` — memory adapters (sqlite-vec, pgvector, chroma, qdrant)
  - `internal/adapter/model/` — model adapters (anthropic, openai, mistral, ollama)
- `internal/capsule/` — capsule host (subprocess + MCP)
- `internal/source/` — source/connector framework (Gmail, Calendar, etc.)
- `pkg/` — if/when we expose anything for external Go users

`pkg/` is empty today and may stay empty. The runtime is a single binary; embedding it as a library is not a goal in v0.1.

## Conventions specific to the runtime

### Errors

- Errors are values. Wrap with context using `%w`: `fmt.Errorf("opening config: %w", err)`.
- Never panic outside `main`. Cobra `RunE` handlers return errors; `main` exits non-zero.
- For error types with stable identity (sentinel checks, `errors.Is`), define them as package-level vars: `var ErrNotFound = errors.New(...)`.

### Logging

- Use `log/slog` (standard library). No third-party logger.
- The runtime builds a single root logger from `config.LogConfig` (see `internal/cli/start.go::newLogger`) and threads it through subsystems via constructor injection.
- Levels: `debug`, `info`, `warn`, `error`. Default is `info`.
- Format: `text` for humans, `json` for production. Configurable.
- No `log.Println`, no `fmt.Fprintf(os.Stderr, ...)` from runtime code. (CLI command output to `cmd.OutOrStdout()` is fine — that's user-facing, not logging.)

### State

- **No global state in the runtime** other than:
  - The cobra command tree (cobra requires it)
  - Build-time variables in `cli/version.go` (set via `-ldflags`)
- Subsystems receive their dependencies via constructors. Tests pass test doubles directly.

### Concurrency

- The standard library is sufficient: `sync`, `context`, channels, `errgroup` from `golang.org/x/sync` only when its semantics actually fit. No new dependencies for concurrency without a strong reason.
- Every long-running goroutine accepts a `context.Context` and exits when it's done.
- Shutdown is bounded: graceful first, then forced. The `start` command's pattern is the reference.

### Interfaces

- Define interfaces where they're consumed, not where they're implemented (Go idiom). E.g., the storage adapter interface lives near the runtime code that uses it, not in the storage adapter package — though for v0.1 we keep both in `internal/adapter/storage/` for cohesion.
- Keep interfaces small. The runtime should not consume an interface with twelve methods if six are all it uses.
- Concrete implementations belong in `internal/`; if v1 ever exposes an SPI for third-party adapters, that's the move from `internal/` to `pkg/`.

### Tests

- Behavior-first. Test what the code does, not how it does it.
- No mocking framework. Hand-rolled fakes when needed; otherwise test against the real thing (containerized backends in the long term).
- Tests live alongside the code: `foo.go` + `foo_test.go`.
- `t.TempDir()` for filesystem state. `t.Setenv()` for env vars. Both clean up automatically.
- Race detector must pass: `make test-race`.
- Integration tests that need a server start one on `127.0.0.1:0` (kernel-assigned port) and tear down via `t.Cleanup`.

### Dependencies

- Direct deps so far: `github.com/spf13/cobra` (CLI), `gopkg.in/yaml.v3` (config parsing). That's it.
- Adding a dep requires a real reason; "convenient" is not enough.
- Standard library before third party. Always check `pkg.go.dev/std` first.

## Anti-patterns specific to runtime code

These come from `extensibility.md` but apply specifically to Go code in this subtree:

1. **No string-matching against specific capsule names, client names, or capability names** in runtime code. The runtime knows capsules and clients by their manifests and per-client credentials respectively — not by hardcoded literal names. Any `if capsule.Name == "..."` in non-test code is a code-review block.
2. **No hardcoded adapter ID lists**. The runtime validates the *shape* of adapter IDs (`namespace:name`), not the specific names.
3. **No hardcoded transport assumptions**. The HTTP listener is one transport; the MCP surface eventually supports others. Request-handling code accepts MCP framing, not raw HTTP semantics, beyond the listener boundary.
4. **No hardcoded model providers**. Model adapter selection goes through the (future) model router; nothing in the runtime calls a provider API directly.

## Build, test, lint

See `runtime/README.md` for the developer-facing commands. The Makefile is the source of truth; CI runs the same targets.

Standard development loop:

```bash
make tidy && make build && make test && make lint
```

Race detector should be part of pre-commit hygiene for any concurrency change: `make test-race`.

## What's expected to land next (heads-up for sessions)

The order is partial; check `ROADMAP.md` for the canonical sequence.

1. Storage adapter SPI (`internal/adapter/storage/`) — interfaces matching `adapter-interface.md`, plus an adapter registry.
2. First storage adapter: `storage:fs-encrypted` — local filesystem with at-rest encryption.
3. Memory adapter SPI + `memory:sqlite-vec`.
4. Audit log writer (`internal/audit/`) — using the storage adapter for the cold store.
5. Permission framework (`internal/permission/`) — grant store + capability checks.
6. MCP surface implementation (`internal/mcp/`) — mounts onto the existing HTTP listener.
7. Pairing primitive — `loamss client pair` + the credential exchange protocol.
8. Capsule host (`internal/capsule/`) — subprocess + MCP for capsule execution.

Adapter implementations for s3-compat, postgres, pgvector, chroma, qdrant follow after the SPIs are stable.

## Where to ask questions

- Open a Discussion on the repo for design questions
- Open an Issue for concrete bugs or work items
- See `CONTRIBUTING.md` (repo root) for the full guide
