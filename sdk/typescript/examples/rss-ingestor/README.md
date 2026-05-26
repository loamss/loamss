# `rss-ingestor` — reference ingestor capsule

The canonical ingestor-role capsule for Loamss. Pulls RSS / Atom
feeds into memory on the runtime's scheduler. No OAuth — RSS is
public, so this is the cleanest demonstration of the ingestor
primitives end-to-end.

## What it exercises

| Primitive | This capsule uses it for |
|---|---|
| Manifest `roles: [ingestor]` + `ingestor:` block | Declares cadence + the tool the scheduler invokes |
| `cursor.get` / `cursor.set` MCP tools | Per-feed "last-seen item id + pubDate" so re-sync doesn't re-ingest |
| `memory.upsert` MCP tool | Writes one entry per feed item under `namespace=rss-<host>` |
| `external.http` capability | Fetches the feed XML over HTTPS |
| Scheduled trigger | Runtime ticks the capsule's `sync` tool every 5 minutes |

What it does **not** exercise: OAuth (no provider auth needed). See
the planned `calendar-ingestor` example for the OAuth variant.

## Install

```bash
# Build
cd sdk/typescript/examples/rss-ingestor
bun install   # (only needed if running from source; the build target
              #  has no runtime deps beyond bun itself)
bun run build

# Install into a running Loamss daemon
loamss capsule install ./
```

At install the runtime:

1. Validates the manifest (`capsule.yaml`).
2. Grants the `memory.write` + `external.http` capabilities to the
   capsule principal.
3. Inserts a row in the `sources` table with `name=rss-ingestor` and
   `adapter_id=source:rss` — the dashboard's Sources pane now shows
   the ingestor alongside any in-tree connectors.
4. Starts the capsule subprocess and the scheduler.

After ~10 seconds (the `initial` delay in the manifest), the
scheduler invokes `sync` for the first time. Every 5 minutes
thereafter it ticks again.

## Configure which feeds to fetch

The reference build ships with one feed (Hacker News' main feed).
Override at run time via env var:

```bash
LOAMSS_RSS_FEEDS="https://feeds.bbci.co.uk/news/world/rss.xml,https://lobste.rs/rss" \
  loamss start
```

A per-capsule config surface (so each installation can carry its
own feed list without env vars) is on the roadmap.

## Inspect what landed

```bash
# Show the most recent sync result.
loamss source show rss-ingestor

# Surface a few stored items via the memory:// resource.
loamss memory query "ai" --k 5

# Or watch the audit log.
loamss audit tail --type source.sync.completed
```

## How the loop looks at runtime

```
scheduler tick (every 5m)
  → host.Client("rss-ingestor").CallTool("sync", {})
      → capsule fetches each feed (external.http)
      → capsule calls cursor.get → "last-seen highwater per feed"
      → for each new item:
          capsule calls memory.upsert
              → memory.Layer.Upsert → entity + thread derivation,
                adapter write
      → capsule calls cursor.set with the new highwater
      → capsule returns { records_added, records_updated,
                           bytes_ingested, errors, per_feed: [...] }
  → scheduler writes the result to source.Store.last_sync_summary
  → scheduler emits source.sync.completed audit entry
```

## Anti-patterns this avoids

- **No OAuth boilerplate.** Public feeds don't need auth; the
  capsule doesn't import the OAuth surface at all.
- **No hardcoded paths.** All persistence goes through MCP tools.
  Replacing the storage backend or moving the runtime to another
  machine doesn't break this capsule.
- **No in-process state.** Cursor lives in `cursor.set`-backed
  blobs. A capsule restart loses no progress.

## Tests

The `parseRSS` helper is exported so a follow-up commit can add a
unit test against a captured RSS XML fixture. The ingestor's
network-driven sync loop is integration-territory and is exercised
via the runtime's e2e suite.
