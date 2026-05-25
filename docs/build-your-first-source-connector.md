# Build your first source connector

This is a hands-on walkthrough for adding a new **source connector** to Loamss — the kind of code that pulls data from somewhere external (an RSS feed, a calendar API, a Slack workspace) into the user's storage and memory.

> **Read this first.** New connectors ship as **capsule ingestors**, not as in-tree Go code under `runtime/internal/source/`. The in-tree path exists only for the two SPI reference implementations (`source:files`, `source:gmail`) that prove the SPI handles the no-auth and OAuth extremes. Everything else — Calendar, Drive, Slack, GitHub, Notion, Linear, RSS — goes through the capsule ingestor role from `capsule-spec.md`. See `sources.md` for the principle and `CLAUDE.md` for the rule.
>
> **This document keeps the in-tree Go walkthrough below because:** (a) it's the closest thing to a literal-form description of what an ingestor must do, and (b) on the rare day someone needs to extend a reference implementation to cover a new SPI gap, they'll need it. **If you're shipping a real connector, follow [`build-your-first-capsule.md`](build-your-first-capsule.md) with the `ingestor` role manifest.**

You'll build `source:hackernews` end-to-end: 90 lines of Go, no auth (HN's API is public), incremental sync via the "last seen item id" cursor pattern, ~5 minutes of typing once you've read this page. The example is hypothetical; we don't intend to land an in-tree HN connector — it's just the smallest possible OAuth-free shape to illustrate the SPI.

If you want to understand the contract abstractly first, read [`sources.md`](../sources.md) — the spec. This page is the tutorial.

---

## What you're building

`source:hackernews` will:

1. Read the user's configured list of HN topic feeds (`config.feeds: ["top", "ask", "show"]`).
2. On each `Sync`, fetch the latest items from each feed.
3. Skip items it's already seen (the cursor is the per-feed last-item-id high watermark).
4. Write each new item into memory as a normalized entry: `{ namespace: "hackernews-<feed>", id: "<item-id>", content: "<title>\n\n<text>", metadata: { url, score, by, time, ... } }`.

That's it. No OAuth, no rate-limit dance, no attachments — the cleanest possible non-trivial connector, suitable as the second example for someone reading the spec.

## File layout

Sources live in `runtime/internal/source/<name>/`. Create the directory:

```bash
mkdir -p runtime/internal/source/hackernews
```

You'll add three files:

| File | What it holds |
|---|---|
| `hackernews.go` | The connector implementation: `init()` registers under `source:hackernews`, the `hackernewsSource` struct + the 8 `source.Source` methods. |
| `client.go` | Thin HTTP client for HN's API. Separate file because it has nothing to do with the SPI. |
| `hackernews_test.go` | Unit tests against an `httptest.Server` that pretends to be HN. |

The existing reference connectors are good shape examples:

- [`internal/source/files/`](../runtime/internal/source/files/) — no network, simplest possible
- [`internal/source/gmail/`](../runtime/internal/source/gmail/) — full OAuth, batched fetch, the works

## The eight methods

Every connector implements `source.Source`. The interface is small; here's what each method has to do.

```go
type Source interface {
    ID() string
    Init(ctx context.Context, deps Deps) error
    AuthStatus(ctx context.Context) (AuthStatus, error)
    BeginAuth(ctx context.Context) (AuthFlow, error)
    CompleteAuth(ctx context.Context, params map[string]string) error
    Sync(ctx context.Context, cursor []byte) (SyncResult, error)
    HealthCheck(ctx context.Context) error
    Close(ctx context.Context) error
}
```

### `ID()`

Return the canonical adapter id, the same string you registered under. Lets the runtime route audit entries + reflect "what kind of source is this" without holding an extra reference.

```go
func (s *hackernewsSource) ID() string { return "source:hackernews" }
```

### `Init(ctx, deps)`

Called once at construction. The runtime hands you everything you're allowed to touch:

```go
type Deps struct {
    SourceName  string             // user's chosen handle ("my-hn")
    Config      map[string]any     // user's per-instance YAML config
    Storage     StorageAdapter     // for raw payloads, per-source credential blob
    Memory      MemoryAdapter      // for normalized entries the memory layer indexes
    Credentials CredentialStore    // per-instance creds (OAuth tokens, API keys)
    Logger      Logger             // already scoped with source_name + source_id
}
```

Validate config; stash refs you'll need; return an error on bad config so the user finds out at `loamss source add` time, not at the first sync. Don't make network calls here — `HealthCheck` is the right place for live validation.

### `AuthStatus(ctx)`

Reports whether the source currently has valid credentials.

```go
type AuthStatus struct {
    Authenticated   bool
    Expires         *time.Time   // for tokens with TTLs
    User            string       // optional display string ("you@gmail.com")
    Description     string       // optional, surfaced verbatim in the UI
}
```

For a no-auth source like HN, `Authenticated: true` always. For OAuth, peek at the credential store + refresh if needed.

### `BeginAuth(ctx)`

Starts an interactive auth flow. Returns an `AuthFlow{Kind: ...}` describing what the user has to do next: open a URL, enter a device code, paste a token back.

For no-auth sources: return `AuthFlow{Kind: AuthFlowNone}`. The runtime treats this as "skip the interactive flow; just call `CompleteAuth` straight away."

### `CompleteAuth(ctx, params)`

Finishes the flow `BeginAuth` started. The runtime passes whatever the user produced (an authorization code, a verifier, a device-grant response, nothing). On success, persist durable credentials via `deps.Credentials` so the next process startup doesn't re-prompt.

For no-auth: a no-op that returns nil.

### `Sync(ctx, cursor)`

The real work. The runtime hands you the cursor from your last successful sync (nil on first call) and expects:

```go
type SyncResult struct {
    Cursor          []byte       // opaque to the runtime; you get it back on the next call
    RecordsAdded    int64
    RecordsUpdated  int64
    BytesIngested   int64
    Started, Finished time.Time
    Errors          []SyncError  // non-fatal per-record failures
}
```

Use the cursor for **incremental sync**. Full re-fetch on every call is a correctness failure for any non-trivial source. For HN: the cursor is a JSON-encoded `map[feed]highwater_item_id`. For Gmail: a Gmail history-id token. For RSS: probably the latest `pubDate` you've seen.

A returned error from `Sync` means the whole pass failed — nothing was persisted. Per-record failures go into `SyncResult.Errors` so the rest of the pass survives.

### `HealthCheck(ctx)`

Cheap, frequently-callable probe. "Can I talk to my backend at all?" Used by `loamss doctor` and the future `/healthz`. For HN: a single `GET /v0/maxitem.json` is enough. For Gmail: a `GET /profile` against the API.

### `Close(ctx)`

Release whatever you opened in `Init`. Most connectors have nothing to release; `return nil`.

---

## Worked example: `source:hackernews`

Open `internal/source/hackernews/hackernews.go`:

```go
package hackernews

import (
    "context"
    "encoding/json"
    "fmt"
    "time"

    "github.com/loamss/loamss/runtime/internal/source"
)

// SourceID is the canonical adapter id under which this source
// registers with the runtime.
const SourceID = "source:hackernews"

func init() {
    source.Register(SourceID, func() source.Source { return New() })
}

func New() source.Source { return &hackernewsSource{} }

type hackernewsSource struct {
    deps   source.Deps
    feeds  []string
    client *Client
}

func (s *hackernewsSource) ID() string { return SourceID }

// Init validates config + stashes the runtime dependencies.
//
// Config:
//   feeds: ["top", "best", "new", "ask", "show", "job"]  (default ["top"])
//   max_items_per_feed: 30                                (default 30)
func (s *hackernewsSource) Init(_ context.Context, deps source.Deps) error {
    s.deps = deps
    feeds, _ := deps.Config["feeds"].([]any)
    s.feeds = []string{"top"}
    if len(feeds) > 0 {
        s.feeds = nil
        for _, f := range feeds {
            if str, ok := f.(string); ok {
                s.feeds = append(s.feeds, str)
            }
        }
    }
    max := 30
    if v, ok := deps.Config["max_items_per_feed"].(int); ok && v > 0 {
        max = v
    }
    s.client = NewClient(max)
    return nil
}

// HackerNews is a public API — no auth.
func (s *hackernewsSource) AuthStatus(_ context.Context) (source.AuthStatus, error) {
    return source.AuthStatus{Authenticated: true}, nil
}

func (s *hackernewsSource) BeginAuth(_ context.Context) (source.AuthFlow, error) {
    return source.AuthFlow{Kind: source.AuthFlowNone}, nil
}

func (s *hackernewsSource) CompleteAuth(_ context.Context, _ map[string]string) error {
    return nil
}
```

That's the lifecycle + config plumbing. Now `Sync` — the part that does real work:

```go
// cursor is per-feed high-water-mark item id.
type cursorPayload struct {
    Highwater map[string]int `json:"highwater"`
}

func (s *hackernewsSource) Sync(ctx context.Context, cursorBytes []byte) (source.SyncResult, error) {
    started := time.Now().UTC()
    cur := cursorPayload{Highwater: map[string]int{}}
    if len(cursorBytes) > 0 {
        if err := json.Unmarshal(cursorBytes, &cur); err != nil {
            // Treat malformed cursor as "start over" — the audit log
            // records the full re-fetch.
            s.deps.Logger.Warn("hackernews: malformed cursor; resyncing from scratch")
            cur.Highwater = map[string]int{}
        }
    }

    var added int64
    var errs []source.SyncError

    for _, feed := range s.feeds {
        ids, err := s.client.FeedItemIDs(ctx, feed)
        if err != nil {
            errs = append(errs, source.SyncError{
                RecordID: feed,
                Message:  fmt.Sprintf("fetch feed %q: %v", feed, err),
            })
            continue
        }

        highwater := cur.Highwater[feed]
        newHigh := highwater

        for _, id := range ids {
            if id <= highwater {
                continue // already seen; cursor wins
            }
            item, err := s.client.Item(ctx, id)
            if err != nil {
                errs = append(errs, source.SyncError{
                    RecordID: fmt.Sprintf("%d", id),
                    Message:  err.Error(),
                })
                continue
            }
            // Write into memory under a per-feed namespace.
            entry := source.MemoryEntry{
                Namespace: fmt.Sprintf("hackernews-%s", feed),
                ID:        fmt.Sprintf("%d", id),
                Content:   item.Title + "\n\n" + item.Text,
                Metadata: map[string]any{
                    "url":    item.URL,
                    "score":  item.Score,
                    "by":     item.By,
                    "time":   item.Time,
                    "kind":   item.Type,
                    "feed":   feed,
                    "source": SourceID,
                },
            }
            if err := s.deps.Memory.Upsert(ctx, entry); err != nil {
                errs = append(errs, source.SyncError{
                    RecordID: entry.ID,
                    Message:  fmt.Sprintf("memory.upsert: %v", err),
                })
                continue
            }
            added++
            if id > newHigh {
                newHigh = id
            }
        }
        cur.Highwater[feed] = newHigh
    }

    nextCursor, _ := json.Marshal(cur)
    return source.SyncResult{
        Cursor:        nextCursor,
        RecordsAdded:  added,
        Started:       started,
        Finished:      time.Now().UTC(),
        Errors:        errs,
    }, nil
}

func (s *hackernewsSource) HealthCheck(ctx context.Context) error {
    _, err := s.client.MaxItem(ctx)
    return err
}

func (s *hackernewsSource) Close(_ context.Context) error { return nil }
```

The HTTP client lives in `client.go`. It's standard `net/http`; nothing in it touches the SPI. (See `internal/source/gmail/api.go` for a real example of a richer client — pagination, retry, rate-limit handling.)

## Registering

Source connectors register themselves on package load via `init()`. For your connector to be picked up by the runtime + CLI, blank-import it from both:

```go
// runtime/internal/cli/start.go
import (
    _ "github.com/loamss/loamss/runtime/internal/source/hackernews"
    // ...
)
```

```go
// runtime/internal/cli/source.go — the source CLI subcommand
import (
    _ "github.com/loamss/loamss/runtime/internal/source/hackernews"
    // ...
)
```

The blank-import triggers `init()`, which calls `source.Register`. After that, `loamss source add source:hackernews --name ...` works.

## Tests

The pattern across the codebase: stand up an `httptest.Server` that speaks just enough of the provider's wire to drive the connector, then exercise `Sync` against it. No live API in CI.

Two example shapes:

```go
// Spin up a fake HN server.
func newFakeHN(t *testing.T, items map[int]Item) *httptest.Server {
    mux := http.NewServeMux()
    mux.HandleFunc("/v0/topstories.json", func(w http.ResponseWriter, _ *http.Request) {
        var ids []int
        for id := range items { ids = append(ids, id) }
        sort.Sort(sort.Reverse(sort.IntSlice(ids)))
        _ = json.NewEncoder(w).Encode(ids)
    })
    mux.HandleFunc("/v0/item/", func(w http.ResponseWriter, r *http.Request) {
        // /v0/item/{id}.json
        id, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(r.URL.Path), ".json"))
        if item, ok := items[id]; ok {
            _ = json.NewEncoder(w).Encode(item)
        } else {
            http.Error(w, "not found", http.StatusNotFound)
        }
    })
    return httptest.NewServer(mux)
}

// Test the round-trip.
func TestSync_FetchesNewItemsOnly(t *testing.T) {
    fake := newFakeHN(t, map[int]Item{
        100: {ID: 100, Title: "first"},
        101: {ID: 101, Title: "second"},
    })
    defer fake.Close()

    src := New().(*hackernewsSource)
    src.Init(ctx, source.Deps{
        Config: map[string]any{"feeds": []any{"top"}},
        Memory: &fakeMemory{},
        Logger: testLogger(t),
    })
    src.client.baseURL = fake.URL  // point at the fake

    // First sync: both items new.
    res, err := src.Sync(ctx, nil)
    require.NoError(t, err)
    assert.Equal(t, int64(2), res.RecordsAdded)

    // Second sync with same cursor: nothing new.
    res2, err := src.Sync(ctx, res.Cursor)
    require.NoError(t, err)
    assert.Equal(t, int64(0), res2.RecordsAdded)
}
```

The `fakeMemory` is a one-line in-memory implementation of `source.MemoryAdapter` that records what got written. Same trick the existing connectors use:

```go
type fakeMemory struct{ entries []source.MemoryEntry }
func (m *fakeMemory) Upsert(_ context.Context, e source.MemoryEntry) error {
    m.entries = append(m.entries, e)
    return nil
}
func (m *fakeMemory) Delete(_ context.Context, _, _ string) error { return nil }
```

`fakeStorage`, `fakeCredentialStore`, and `testLogger` follow the same pattern. See `internal/source/files/files_test.go` for ready-to-copy versions.

## When you need auth

Most real connectors need credentials. The patterns the SPI supports:

- **`AuthFlowNone`** — public APIs (HN, RSS, public webhooks). No-op.
- **API key in config** — return `AuthFlowNone`, validate the key in `CompleteAuth` via a probe call.
- **`AuthFlowBrowser`** — OAuth + loopback HTTP listener (the source captures the callback itself). The Gmail connector is the reference; its OAuth code lives in [`internal/source/gmail/oauth.go`](../runtime/internal/source/gmail/oauth.go).
- **`AuthFlowCodePaste`** — user pastes a code back into the CLI/console after the browser flow. Older OAuth-on-CLI pattern; OK for devices without a loopback option.
- **`AuthFlowDeviceCode`** — OAuth 2.0 device authorization grant (RFC 8628). User opens a URL on another device and types a short code. Best for headless servers.

Once the user completes the flow, persist credentials via `deps.Credentials.Set(...)`. The runtime encrypts that blob via the configured storage adapter — the connector never sees plaintext on disk.

## Provider-specific setup docs

If your connector needs the user to do something at the provider end (create an OAuth client, generate an API key, enable an API in their cloud project), write a sibling doc in this folder following the shape of [`setup-gmail.md`](setup-gmail.md):

- What the user creates
- What scopes / permissions to request
- What to paste into the config
- Common failure modes and what they mean

Skip this when your connector authenticates with nothing (HN, public RSS) or with credentials the user already has (API keys for services they already use).

## Lifecycle, in order

For reference, here's the full sequence the runtime drives a source through:

```
loamss source add ...
  → factory()         (new in-memory instance)
  → Init(deps)        (config validated, refs stashed)

loamss source authenticate ...
  → BeginAuth()       (returns URL or "none")
  → user does the thing
  → CompleteAuth(params)   (persist creds via deps.Credentials)

loamss source sync ...  (or scheduled)
  → AuthStatus()      (skip if not authenticated)
  → Sync(cursor)      (do the work; return new cursor)
  → runtime persists the cursor for next call

loamss source remove ...
  → Close()           (release resources)
  → runtime deletes the credential blob
```

The audit log gets entries for every transition. You don't have to emit those — the runtime does. Per-record errors inside `Sync` show up in `audit tail` under `source.sync.completed.errors`.

## Where to look when stuck

- **The spec**: [`sources.md`](../sources.md) — what the SPI promises, in prose.
- **The Go interface**: [`runtime/internal/source/source.go`](../runtime/internal/source/source.go) — authoritative contract.
- **Simple reference**: [`runtime/internal/source/files/`](../runtime/internal/source/files/) — no network, no auth.
- **Full reference**: [`runtime/internal/source/gmail/`](../runtime/internal/source/gmail/) — OAuth, batched fetch, cursor pattern with provider-issued tokens.
- **Tests in the existing connectors** — best place to copy `httptest`-driven patterns + the fake adapter implementations.

## Submitting your connector

**For 99% of connectors, the answer is: ship it as a capsule.** See [`build-your-first-capsule.md`](build-your-first-capsule.md) and the `ingestor` role in `capsule-spec.md`. The capsule path lets you publish to the marketplace, version independently of the runtime, and update without a Loamss release.

In-tree contribution is reserved for two cases:

1. **Extending an existing reference implementation** to cover a new SPI gap (e.g. push-subscription support, write-back operations). Open a Discussion first — the SPI change is the interesting work; the connector update is downstream.
2. **A new reference implementation for an unsolved auth shape** the existing two don't demonstrate. Open a Discussion first. If you're tempted because "Calendar / Drive / Slack would be useful," that's not a reason — those go in the marketplace.

If after a Discussion the answer is in-tree, the bar is the same as any other runtime change:

1. PR against `main` with `internal/source/<name>/`, blank-imports in `cli/start.go` and `cli/source.go`, and a setup doc under `docs/` if the connector has user-facing provider setup.
2. Tests using `httptest` — no live API hits in CI. The Loamss CI runs `make test-race` so concurrent-Sync correctness matters.
3. Read the [`extensibility.md`](../extensibility.md) anti-patterns; the most common one for source authors is hardcoding provider-specific quirks into the runtime instead of keeping them inside the connector.
