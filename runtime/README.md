# loamss runtime

The Go runtime for Loamss. Single binary, OS-level daemon. The repo-root [`README.md`](../README.md) covers the project at large; this directory holds the reference implementation.

## Status

**Phase 1+ working runtime.** Boots end-to-end, hosts capsules, talks MCP to external clients, ships an embedded dashboard. ~33k LOC of Go (plus ~24k of tests). See [`../ROADMAP.md`](../ROADMAP.md) for what's planned beyond.

What's in the binary today:

- **MCP surface** over HTTP+SSE (JSON-RPC 2.0, bearer-token auth) at `/mcp`
- **Permission engine** — capability + scope + `requires_user_approval` framework
- **Hash-chained audit log** on SQLite (WAL + `BEGIN IMMEDIATE`); `Verify` pass
- **Capsule host** — subprocess + MCP-over-stdio + permission-gated callbacks
- **Memory layer** — entity + thread derivation above the memory adapter
- **Source connector framework** — `source:files` (no-auth) + `source:gmail` (OAuth) as SPI reference implementations; the catalogue grows in the marketplace as capsule ingestors
- **Ingestor primitives** — `credentials.*`, `cursor.*`, `oauth.access_token`, `memory.upsert` MCP tools, plus a per-capsule scheduler driven by the manifest
- **OAuth orchestrator** — well-known provider registry (google, github), per-user `client_id` store, PKCE, ephemeral loopback listener, transparent refresh
- **Embedded console** — Next.js static export, served from the binary

Adapter set:

| Surface | Adapters |
|---|---|
| Storage | `fs-encrypted` (AES-256-GCM), `s3` (AWS / R2 / B2 / MinIO / Wasabi), `gcs` (Workload Identity) |
| Memory | `sqlite-vec`, `pgvector` (with optional Cloud SQL IAM), `chroma`, `qdrant` |
| Model | `anthropic`, `openai` (chat + embeddings), `ollama`, plus `none` / `dummy` |

## Requirements

- Go 1.25+
- `bun` to build the embedded console (`make build` runs the console build first; if you only touch Go, `make build-go` skips it)
- `make` (GNU or BSD)
- `golangci-lint` for `make lint` (CI installs automatically; `brew install golangci-lint` locally)
- `git` for version metadata baking

## Build

```bash
make build       # console (bun) + go build → bin/loamss
make build-go    # Go-only, when iterating without console changes
make test        # unit tests
make test-race   # tests with the race detector
make lint        # golangci-lint
make fmt         # gofmt -s
make vet         # go vet
make tidy        # go mod tidy
make install     # install to /usr/local/bin (override with PREFIX=...)
make clean
make help        # list all targets
```

Try it:

```bash
make build
./bin/loamss start --open
```

The first run opens the embedded console's setup wizard; subsequent runs land on the dashboard. The full CLI surface (`init`, `doctor`, `start`, `open`, `status`, `version`, `config`, `capsule`, `source`, `client`, `grant`, `audit`, `approve`, `export`) is documented in [`../cli.md`](../cli.md).

## Layout

```
runtime/
├── cmd/loamss/                binary entry point
├── internal/
│   ├── cli/                   cobra command tree + daemon wiring
│   ├── server/                HTTP listener (mounts /mcp + /console/*)
│   ├── mcp/                   MCP protocol — tools, resources, dispatch
│   ├── permission/            capability framework + grant + approval queue
│   ├── audit/                 hash-chained audit log writer
│   ├── memory/                memory layer (entities, threads) above adapter
│   ├── source/                Source SPI + registry + in-tree connectors
│   ├── capsule/               capsule manifest + installer + host (subprocess)
│   ├── oauth/                 OAuth orchestrator + per-user client store
│   ├── config/                config schema + loader + hot-reload
│   ├── console/               embed.FS wrapper for the Next.js static export
│   └── adapter/
│       ├── storage/           fs-encrypted, s3, gcs
│       ├── memory/            sqlite-vec, pgvector, chroma, qdrant
│       └── model/             anthropic, openai, ollama, none, dummy
├── scripts/
│   ├── loadtest_mcp/          MCP-surface load harness
│   └── smoke_seed/            deterministic seed for the e2e smoke test
├── Makefile
└── .golangci.yml
```

## Conventions

The load-bearing ones (full digest in [`CLAUDE.md`](CLAUDE.md)):

- **Errors are values** — wrap with context: `fmt.Errorf("opening config: %w", err)`. No panics outside `main`.
- **No global state** in the runtime (one exception: the cobra command tree, which cobra requires).
- **Small interfaces, defined where consumed.** The Source SPI lives in `internal/source/source.go` next to the runtime that calls it; the storage SPI lives in `internal/adapter/storage/` next to the consumers.
- **No string-matching on names** — no `if capsule.Name == "..."`, no hardcoded adapter ID lists. Anti-patterns enumerated in [`../extensibility.md`](../extensibility.md).
- **`log/slog` everywhere** — no `log.Println`, no `fmt.Fprintf(os.Stderr, ...)` from runtime code.
- **Race detector must pass** — `make test-race` is part of CI.

## Where to start reading

| If you want to… | Start with |
|---|---|
| Add a runtime tool (new `tools/call` target) | `internal/mcp/tools_client_info.go` as the canonical small example, then `tools_memory_query.go` for the adapter-using shape |
| Add a storage / memory / model adapter | `internal/adapter/storage/adapter.go` for the SPI; `internal/adapter/memory/sqlite/sqlite.go` as a complete worked example |
| Add an in-tree source connector | The two existing ones (`source:files` no-auth, `source:gmail` OAuth) are the references. Most ingestion goes via capsules instead — see [`../docs/capsule-ingestor-primitives.md`](../docs/capsule-ingestor-primitives.md). |
| Trace a request end-to-end | Start in `internal/server/server.go` → `internal/mcp/handler.go` → `internal/mcp/tools.go` (dispatch) |
| Understand the permission flow | `internal/permission/engine.go` (Check + ResolveApproval), then `internal/permission/canonical.go` for the capability catalogue |
| Read a capsule's full lifecycle | `internal/capsule/installer.go` (install) → `internal/capsule/host.go` (run) → `internal/capsule/lifecycle_hook.go` (scheduler integration) |

## Releases

Each tag triggers `.github/workflows/release.yml` which produces `loamss-darwin-{arm64,amd64}` and `loamss-linux-{arm64,amd64}` binaries against the embedded console. CI (`.github/workflows/ci.yml`) runs `make build && make test && make test-race && make vet && make lint && golangci-lint run` on every push to `main` and every PR.

## Contributing

See [`../CONTRIBUTING.md`](../CONTRIBUTING.md). Big architectural changes should be discussed in an Issue before opening a PR; small fixes and adapter additions are welcome as direct PRs.
