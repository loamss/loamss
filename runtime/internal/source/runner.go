package source

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
)

// `runner.go` hosts the build-source + run-sync helpers shared by
// the `loamss source` CLI commands and the /console/sources HTTP
// endpoints. Without this extraction, both call sites would
// duplicate (and slowly drift): adapter registration check, init,
// audit-event shape, last-sync persistence, cursor write-back.
//
// Design choices that aren't load-bearing but help readers:
//
//   - The "build env" is a separate type from source.Deps. Deps is
//     what a Source sees on Init (storage/memory/credentials/logger
//     for ITS own use). BuildEnv is what the runtime needs to
//     construct that Deps for a given Configured row. Splitting
//     keeps the SPI surface stable when the runtime gains new
//     subsystems.
//
//   - RunSync persists last_sync metadata + cursor BEFORE returning,
//     even on failure. The audit trail and the source's own
//     last-attempt view should agree. Callers that need the raw
//     result still get it back.
//
//   - Audit events use a `runner_actor` callback rather than a
//     baked-in ActorKind. The CLI is user:cli, the HTTP path is
//     user:console. Tests can also pass user:test. Hardcoding here
//     would make those distinctions impossible.

// BuildEnv is the runtime's bag of adapters used to construct a
// configured Source instance. Callers populate it from the live
// daemon's adapters (the daemon case) or per-invocation
// construction (the CLI case).
type BuildEnv struct {
	// Storage is the user's storage adapter. Sources write raw
	// payloads and read/write their per-instance credential blob
	// through here.
	Storage StorageAdapter

	// Memory is the narrow memory adapter the source writes into.
	// The runtime's memory layer satisfies this interface via a
	// bridge constructed by the caller; the source package doesn't
	// import the layer.
	Memory MemoryAdapter

	// Logger is the runtime's structured logger. Build() scopes it
	// per source instance (adding source_name + source_id) before
	// handing it to the source.
	Logger Logger
}

// Build constructs and initialises a Source for the given
// Configured row. The returned Source has had its Init called and
// holds open whatever resources its adapter needs; the caller
// MUST call Close when done.
//
// Errors are wrapped with the adapter id so callers can distinguish
// "you have no source:foo registered in this binary" from "the
// source:foo init failed validating your config."
func Build(ctx context.Context, env BuildEnv, c *Configured) (Source, error) {
	if c == nil {
		return nil, errors.New("source: Build called with nil Configured")
	}
	if !isRegistered(c.AdapterID) {
		return nil, fmt.Errorf("source adapter %q is not registered in this binary", c.AdapterID)
	}
	src, err := New(c.AdapterID)
	if err != nil {
		return nil, err
	}
	creds := NewStorageCredentialStore(env.Storage, c.Name)
	if err := src.Init(ctx, Deps{
		SourceName:  c.Name,
		Config:      c.Config,
		Storage:     env.Storage,
		Memory:      env.Memory,
		Credentials: creds,
		// Per-source scoping (source_name + source_id attributes)
		// is the caller's responsibility — the source.Logger
		// interface is intentionally narrow (Info/Warn/Error/Debug)
		// to avoid coupling the SPI to slog. The CLI / server wrap
		// their *slog.Logger and apply .With() before constructing
		// the env.
		Logger: env.Logger,
	}); err != nil {
		return nil, fmt.Errorf("initializing source %s: %w", c.AdapterID, err)
	}
	return src, nil
}

// RunSyncActor identifies which subsystem drove a sync. Used in
// the audit log so "the user clicked sync in the console" and
// "the user ran loamss source sync in a terminal" are
// distinguishable forever.
type RunSyncActor struct {
	Kind audit.ActorKind
	ID   string
}

// RunSyncResult bundles the SyncResult with the human-readable
// summary the store + audit log both consume. Centralising the
// summary shape here keeps the dashboard JSON, the CLI text
// output, and audit Data on a single field set.
type RunSyncResult struct {
	Started        time.Time
	Finished       time.Time
	RecordsAdded   int64
	RecordsUpdated int64
	BytesIngested  int64
	Errors         int
	ErrorMessage   string // empty on success
	Status         string // "success" | "error"
	Summary        map[string]any
}

// RunSync executes one Sync() pass for an already-built source,
// persists the resulting last_sync metadata + cursor, and emits the
// two canonical audit events ("source.sync.started" before, then
// "source.sync.completed" after).
//
// The src must already be Init'd via Build. The function does NOT
// Close it — that's the caller's job; the CLI closes per command,
// the HTTP path closes after the goroutine that ran the sync.
//
// On a sync-level error the function returns the wrapped error AND
// the populated result/summary. Callers that want to surface the
// summary even on failure (CLI verbose, console dashboard) read it
// regardless of the error.
func RunSync(
	ctx context.Context,
	src Source,
	store *Store,
	auditor audit.Writer,
	c *Configured,
	actor RunSyncActor,
) (*RunSyncResult, error) {
	if src == nil || store == nil || auditor == nil || c == nil {
		return nil, errors.New("source: RunSync requires non-nil src, store, auditor, configured")
	}

	_, _ = auditor.Append(ctx, audit.Entry{
		Type:    "source.sync.started",
		Actor:   audit.Actor{Kind: actor.Kind, ID: actor.ID},
		Subject: &audit.Subject{Kind: audit.SubjectSource, ID: c.Name},
		Outcome: audit.OutcomeSuccess,
		Data:    map[string]any{"adapter_id": c.AdapterID},
	})

	started := time.Now().UTC()
	syncResult, syncErr := src.Sync(ctx, c.Cursor)
	finished := time.Now().UTC()

	status := "success"
	if syncErr != nil {
		status = "error"
	}
	summary := map[string]any{
		"records_added":   syncResult.RecordsAdded,
		"records_updated": syncResult.RecordsUpdated,
		"bytes_ingested":  syncResult.BytesIngested,
		"errors":          len(syncResult.Errors),
		"started":         started.Format(time.RFC3339Nano),
		"finished":        finished.Format(time.RFC3339Nano),
	}
	if syncErr != nil {
		summary["error_message"] = syncErr.Error()
	}
	// Persist last_sync state EVEN on failure so the dashboard's
	// next /console/state poll shows the attempt (and the error).
	_ = store.SetLastSync(ctx, c.Name, status, summary, finished)
	if syncErr == nil && len(syncResult.Cursor) > 0 {
		_ = store.UpdateCursor(ctx, c.Name, syncResult.Cursor)
	}

	_, _ = auditor.Append(ctx, audit.Entry{
		Type:    "source.sync.completed",
		Actor:   audit.Actor{Kind: actor.Kind, ID: actor.ID},
		Subject: &audit.Subject{Kind: audit.SubjectSource, ID: c.Name},
		Outcome: outcomeFromError(syncErr),
		Data:    summary,
	})

	out := &RunSyncResult{
		Started:        started,
		Finished:       finished,
		RecordsAdded:   syncResult.RecordsAdded,
		RecordsUpdated: syncResult.RecordsUpdated,
		BytesIngested:  syncResult.BytesIngested,
		Errors:         len(syncResult.Errors),
		Status:         status,
		Summary:        summary,
	}
	if syncErr != nil {
		out.ErrorMessage = syncErr.Error()
		return out, syncErr
	}
	return out, nil
}

// MarkSyncRunning flips the source's last_sync_status to "running"
// before kickoff. Used by the async HTTP path so the dashboard's
// next /console/state poll shows "syncing now" while the goroutine
// works. The status will be flipped back to success/error by
// RunSync's SetLastSync call.
func MarkSyncRunning(ctx context.Context, store *Store, name string) error {
	now := time.Now().UTC()
	return store.SetLastSync(ctx, name, "running",
		map[string]any{"started": now.Format(time.RFC3339Nano)}, now)
}

// outcomeFromError mirrors the CLI's helper. Kept private to this
// package so callers don't accidentally use a different mapping —
// the audit log's Outcome field has to be consistent across all
// sync writers.
func outcomeFromError(err error) audit.Outcome {
	if err == nil {
		return audit.OutcomeSuccess
	}
	if errors.Is(err, ErrAuthRequired) {
		return audit.OutcomeDenied
	}
	return audit.OutcomeError
}

// isRegistered checks the package registry for an adapter id.
// Same logic the CLI used (sourceRegistered) but lives here so the
// HTTP path can use it without importing CLI.
func isRegistered(id string) bool {
	for _, r := range Registered() {
		if r == id {
			return true
		}
	}
	return false
}
