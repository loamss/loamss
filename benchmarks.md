# Loamss Benchmarks & Performance Baselines

Baseline performance numbers for the hot paths in the runtime. Captured at the commit named in each table; rerun on every release and on any change to the SQLite schema, audit chain semantics, permission engine, MCP dispatch, or memory adapter.

> **Status: living document.** Numbers age; methodology shouldn't. The headline tables below have a `commit` column — update both the value and the commit when you re-run.

## What this document is and is not

This doc captures:

- The benchmarks we run, where their source lives, and how to reproduce them
- Baseline numbers from the canonical hardware target
- Interpretation: where the bottlenecks are, why, and when to worry

This doc does **not**:

- Track every micro-optimization. Small numbers wobble — chase the bottlenecks named in §[Where the bottlenecks are](#where-the-bottlenecks-are), not the leaf measurements
- Cover wall-clock cost of capsule subprocess spawn (covered loosely in process lifecycle tests; not a hot path)
- Cover real-network latency for model adapters (each adapter doc has its own perf notes)

## Reference hardware

Apple M4 Pro, macOS 26, Go 1.25, SQLite via `modernc.org/sqlite` (pure-Go, no CGO). All numbers below are from this target unless otherwise stated.

CI runs benchmarks on `ubuntu-latest` and `macos-latest`; CI numbers will be lower (smaller CPUs, more virtualization overhead) but the ratios between operations should track within ~20%.

## How to reproduce

### Go benchmarks (subsystem-level)

```bash
cd runtime

# Audit log: append + verify + cross-instance
go test ./internal/audit/ -bench=. -benchtime=2s -run=^$ -timeout 5m

# Permission framework: Check (the hot path)
go test ./internal/permission/ -bench=BenchmarkCheck -benchtime=2s -run=^$ -timeout 5m

# Memory adapter: Search at varying corpus sizes (slow; -benchtime=1x)
go test ./internal/adapter/memory/sqlite/ -bench=. -benchtime=1x -run=^$ -timeout 10m
```

Source: `internal/{audit,permission,adapter/memory/sqlite}/bench_test.go`.

### HTTP load test (end-to-end)

Start a daemon configured with `model:dummy`, pair a client, grant `memory.read`, seed memory, then run the load tool:

```bash
# Setup (see runtime/scripts/smoke_seed/main.go for the seed contents)
./bin/loamss --config $CFG start &
TOKEN=$(./bin/loamss --config $CFG client pair complete <code> --json | jq -r .token)
./bin/loamss --config $CFG grant create \
    --principal-kind client --principal-id <client-id> \
    --capability memory.read
go run ./scripts/smoke_seed/main.go $DATA_DIR/memory.db

# Sweep concurrency
for c in 1 4 8 16 32; do
  go run ./scripts/loadtest_mcp/main.go \
      --token="$TOKEN" --method=memory.show --id=mem-seed-0 \
      --concurrency=$c --requests=$((c*200))
done
```

Source: `scripts/loadtest_mcp/main.go`.

## Baseline numbers

### Hot-path latency (Go benchmarks)

Commit: `44dbcaf` (post the audit ULID-monotonic fix and the MCP-over-stdio work).

| Operation | Single-thread p50 | Parallel p50 | Bottleneck |
|---|---|---|---|
| `audit.Append` | **71 µs** | **76 µs** | SQLite write lock (BEGIN IMMEDIATE serializes) |
| `permission.Check` allow | **93 µs** | **89 µs** | Includes audit emission; WAL readers fan out |
| `permission.Check` deny | **91 µs** | — | Same lookup; marginally less audit data |
| `permission.Check` w/ scope | **97 µs** | — | 4 µs overhead for match-primitive dispatch |
| `memory:sqlite` Upsert | **82 µs** | — | One SQLite INSERT/UPDATE |

**What "parallel" means**: `b.RunParallel` with `runtime.GOMAXPROCS(0)` workers (= 14 on the reference machine). For audit append, parallel is no faster — the SQLite write lock serializes. For permission Check, parallel is marginally faster because WAL readers don't block each other; the bottleneck shifts to the audit emission inside each Check.

### Audit chain Verify scaling

Linear in N, ~6.3 µs/entry. Pure CPU (no concurrent writers to contend with):

| Chain size | Verify time | Per entry |
|---|---|---|
| 100 entries | 0.64 ms | 6.4 µs |
| 1,000 entries | 6.3 ms | 6.3 µs |
| 10,000 entries | 63 ms | 6.3 µs |

Extrapolation: 1M-entry chain takes ~6 s. Acceptable for `loamss audit verify` on a daily/weekly cadence. At 10M+ entries, build a checkpoint scheme (verify is currently O(N); checkpoints make it O(N - last_checkpoint)).

### Memory adapter search (brute-force k-NN at 384 dims)

Vectors generated from `math/rand` with fixed seed for reproducibility. k=10 nearest:

| Corpus size | Search latency |
|---|---|
| 100 entries | 0.37 ms |
| 1,000 entries | 3.3 ms |
| 10,000 entries | 27 ms |
| **100,000 entries** | **273 ms** |

O(N·D) as designed for `memory:sqlite`. The break point is around 10k entries for interactive use (sub-50 ms). Beyond that, swap to `memory:sqlite-vec` (sqlite-vec extension, indexed k-NN) or `memory:pgvector`. Both implementations land under the same SPI — no caller changes.

### End-to-end MCP `tools/call` over HTTP

`memory.show` exercises the full stack: HTTP → bearer auth → JSON-RPC dispatch → permission.Check → tool handler → memory adapter Get → JSON-RPC response. Two audit entries per call (`check.allow` + `tool.invoked`).

| Concurrency | qps | p50 | p95 | p99 |
|---|---|---|---|---|
| 1 | 2,835 | 287 µs | 623 µs | 1.27 ms |
| 4 | 5,340 | 624 µs | 1.45 ms | 2.05 ms |
| **8** | **5,923** | **1.26 ms** | **2.25 ms** | **2.57 ms** |
| 16 | 5,697 | 2.61 ms | 3.93 ms | 6.50 ms |
| 32 | 5,011 | 5.73 ms | 8.41 ms | 14.20 ms |

`client.info` skips the permission check (auth-only) and writes one audit entry per call:

| Concurrency | qps | p50 | p95 | p99 |
|---|---|---|---|---|
| 16 | 9,356 | 1.25 ms | 4.86 ms | 5.58 ms |

The 1.6× gap between `client.info` and `memory.show` at the same concurrency confirms the audit write-lock is the bottleneck, not HTTP framing or JSON-RPC dispatch.

### Chain integrity under sustained load

The HTTP load sweep above wrote **27,603 audit entries** across 5 concurrent runs (each in a separate process: `go run ./scripts/loadtest_mcp/...`) plus pairing + grant + tool-invocation entries. After the sweep finished:

```
$ loamss audit verify
✓ Chain integrity verified (27,603 entries)
```

This is the test that previously surfaced the ULID-monotonic-across-processes bug. The fix in `internal/audit/writer.go` (re-read head + bump timestamp if needed) holds under realistic concurrent load.

## Where the bottlenecks are

In rough order of impact:

1. **SQLite write lock for audit append.** Every external call that produces an audit entry serializes here. The lock is short (~70 µs/append, dominated by the WAL fsync via `synchronous=NORMAL`), but it caps total external-write throughput at ~14k entries/sec. Realistic external-client load runs at ~5-6k qps because each call emits 2 audit entries.

   **When to worry**: if a real workload sustains >5k qps for >5 minutes. Mitigations: batch entries within one tx, switch to async append with bounded backlog, or shard the audit log by actor kind.

2. **Memory adapter linear scan.** `memory:sqlite` reads every row on every Search. Fine up to 10k entries; obviously broken at 1M.

   **When to worry**: when interactive queries cross 100 ms. Mitigation: install `memory:sqlite-vec` (planned), `memory:pgvector`, `memory:qdrant`, or `memory:chroma`. SPI is stable.

3. **Capsule subprocess cold start.** Spawn time depends on the entrypoint (Node ~50ms, Python ~100ms, Go binary ~5ms). Currently the host spawns every installed capsule at daemon start. Cold-start cost is incurred once per daemon lifecycle.

   **When to worry**: if a user installs 50+ capsules and daemon start crosses 5 s. Mitigation: lazy-spawn (start the capsule on first tool call) or pool common runtimes.

## What we don't have to worry about

The benchmarks confirmed several things are NOT bottlenecks worth tuning today:

- **JSON-RPC framing**: `client.info` at 9,356 qps shows the framing stack is cheap. The HTTP MCP layer is not on the critical path.
- **Permission Check itself**: 93 µs including audit. The match-primitive dispatch adds only 4 µs even with a non-trivial scope.
- **Bearer auth**: O(1) lookup + constant-time SHA-256 compare. Negligible vs the audit write that follows.
- **Hash chain computation**: 6 µs/entry on Verify; on Append it's dominated by the SQL commit, not the SHA-256.
- **MCP transport for capsule callbacks**: in-memory pipes; the multiplexer adds <10 µs over the underlying I/O.

## Regression tracking

CI runs the unit tests + race detector on every push (see `.github/workflows/ci.yml`). It does **not** currently run the benchmarks — benchmark numbers vary across runner generations and would produce noisy diffs.

For now, the discipline is:

1. **On any change to** `internal/audit/writer.go`, `internal/permission/engine.go`, `internal/mcp/handler.go`, `internal/mcp/tools.go`, or the `Adapter` interfaces, **re-run the benchmarks locally and update the tables above** with the new commit hash.
2. **If a number regresses >2x** without a corresponding feature reason, that's a stop-the-line moment. Investigate before merging.
3. **At each tagged release** (when those exist), capture a snapshot in this doc with the version tag.

The HTTP load test isn't part of CI either; it requires a running daemon and a configured client. Run it manually before tagged releases against the runtime binary on the target machine.

## Open performance questions

These are deferred — capture here so future work can pick them up:

- **Cold-store rotator latency.** Audit hot store currently grows unbounded. The rotator (when it lands) will need to verify chain integrity before archiving + start a new chain segment.
- **Model adapter network latency.** Once real adapters (anthropic, openai, ollama) exist, measure embedding RTT and add it to this doc.
- **Concurrent capsule callback fan-out.** A capsule could make many parallel callbacks (multi-query embeddings). The current MCP transport handles concurrent Request calls; the runtime side's audit-write lock will dominate at high fan-out.
- **Memory: provenance write amplification.** Once `provenance_required` capsule entity types land, every memory.write adds a provenance audit entry on top of the storage write. Re-measure.
