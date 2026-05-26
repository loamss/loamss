package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/source"
)

// ingestorScheduler drives the scheduled-trigger path for capsule
// ingestors. One ticker goroutine per ingestor capsule; on each
// tick the scheduler invokes the capsule's on_trigger tool, parses
// the SyncResult-shaped return, and writes the result back to
// source.Store (last_sync_*) + the audit log
// (source.sync.started + source.sync.completed).
//
// Implements capsule.LifecycleHook: Host.StartOne fires
// OnCapsuleStarted (schedule) and StopOne fires OnCapsuleStopped
// (unschedule). The scheduler's lifecycle is bounded by Host's —
// daemon shutdown causes the Host to fire OnCapsuleStopped for
// every running capsule, draining all tickers.
//
// Tick policy: skip on overrun (a sync taking longer than the
// interval doesn't queue a backlog of ticks; the next tick fires
// at the next aligned boundary after the previous one returns).
// Matches cron behavior and avoids piling up work when a provider
// API gets slow.
type ingestorScheduler struct {
	host    *capsule.Host
	sources *source.Store
	audit   audit.Writer
	logger  *slog.Logger

	mu      sync.Mutex
	tickers map[string]*ingestorTicker
}

// ingestorTicker is one capsule's scheduled state.
type ingestorTicker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func newIngestorScheduler(
	host *capsule.Host,
	sources *source.Store,
	aud audit.Writer,
	logger *slog.Logger,
) *ingestorScheduler {
	return &ingestorScheduler{
		host:    host,
		sources: sources,
		audit:   aud,
		logger:  logger,
		tickers: make(map[string]*ingestorTicker),
	}
}

// OnCapsuleStarted implements capsule.LifecycleHook. No-op for
// non-ingestor capsules; schedules a ticker for ingestors.
func (s *ingestorScheduler) OnCapsuleStarted(_ context.Context, c capsule.Installed) {
	if c.Manifest == nil || c.Manifest.Ingestor == nil {
		return
	}
	if !hasRole(c.Manifest.Roles, "ingestor") {
		return
	}
	spec := c.Manifest.Ingestor
	interval, err := time.ParseDuration(spec.Schedule.Interval)
	if err != nil {
		s.logger.Warn("ingestor scheduler: invalid interval, capsule will not be scheduled",
			"capsule", c.Name, "interval", spec.Schedule.Interval, "err", err)
		return
	}
	initial := interval
	if spec.Schedule.Initial != "" {
		if d, err := time.ParseDuration(spec.Schedule.Initial); err == nil {
			initial = d
		}
	}

	s.mu.Lock()
	if existing, ok := s.tickers[c.Name]; ok {
		// Idempotent: cancel the prior ticker before starting a new
		// one. Happens at capsule upgrade-in-place if/when that lands.
		existing.cancel()
		<-existing.done
	}
	tickCtx, cancel := context.WithCancel(context.Background())
	t := &ingestorTicker{cancel: cancel, done: make(chan struct{})}
	s.tickers[c.Name] = t
	s.mu.Unlock()

	go s.tickerLoop(tickCtx, c.Name, spec.SourceID, spec.OnTrigger, initial, interval, t.done)
	s.logger.Info("ingestor scheduler: scheduled",
		"capsule", c.Name, "source_id", spec.SourceID,
		"initial", initial, "interval", interval, "on_trigger", spec.OnTrigger)
}

// OnCapsuleStopped implements capsule.LifecycleHook.
func (s *ingestorScheduler) OnCapsuleStopped(_ context.Context, name string) {
	s.mu.Lock()
	t, ok := s.tickers[name]
	if ok {
		delete(s.tickers, name)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	t.cancel()
	<-t.done
	s.logger.Info("ingestor scheduler: unscheduled", "capsule", name)
}

// tickerLoop fires the on_trigger tool on each cadence boundary.
// Sleeps for `initial` first (so the first sync happens quickly
// after install, not after a full Interval), then loops on Interval.
//
// Overrun policy: skip. If runSync takes longer than `interval`,
// the next tick fires at the next interval after runSync returns
// — we do NOT queue ticks. Matches cron semantics.
func (s *ingestorScheduler) tickerLoop(
	ctx context.Context, capsuleName, sourceID, toolName string,
	initial, interval time.Duration, done chan struct{},
) {
	defer close(done)

	// Initial delay.
	select {
	case <-ctx.Done():
		return
	case <-time.After(initial):
	}

	for {
		s.runOneSync(ctx, capsuleName, sourceID, toolName)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// runOneSync invokes the capsule's on_trigger tool and updates the
// source store + audit log with the result. Failures (transport
// errors, capsule returns isError, malformed result) are surfaced
// in the same audit shape as in-tree source.RunSync so consumers
// see one uniform sync-history surface.
func (s *ingestorScheduler) runOneSync(
	ctx context.Context, capsuleName, sourceID, toolName string,
) {
	started := time.Now().UTC()

	// source.sync.started, matching in-tree RunSync.
	_, _ = s.audit.Append(ctx, audit.Entry{
		Type:    "source.sync.started",
		Actor:   audit.Actor{Kind: audit.ActorRuntime, ID: "scheduler"},
		Subject: &audit.Subject{Kind: audit.SubjectSource, ID: capsuleName},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"adapter_id": sourceID,
			"via":        "capsule_ingestor",
			"capsule":    capsuleName,
		},
	})

	client := s.host.Client(capsuleName)
	if client == nil {
		s.recordFailed(ctx, capsuleName, sourceID, started, fmt.Errorf("capsule %s is not running", capsuleName))
		return
	}

	// Bound the tool call; we don't want a hung sync to deadlock the
	// ticker forever. 10 minutes is generous for sync work that has
	// to talk to a remote provider; longer than that and the user
	// should be using a streaming/push subscription, not poll-sync.
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	resp, err := client.CallTool(callCtx, toolName, json.RawMessage(`{}`))
	if err != nil {
		s.recordFailed(ctx, capsuleName, sourceID, started, fmt.Errorf("capsule call: %w", err))
		return
	}
	if resp.Error != nil {
		s.recordFailed(ctx, capsuleName, sourceID, started,
			fmt.Errorf("capsule returned RPC error: %s", resp.Error.Message))
		return
	}

	counters, isErr, parseErr := parseSyncToolResult(resp.Result)
	if parseErr != nil {
		s.recordFailed(ctx, capsuleName, sourceID, started, parseErr)
		return
	}
	finished := time.Now().UTC()

	status := "success"
	if isErr {
		status = "error"
	}
	summary := map[string]any{
		"records_added":   counters.RecordsAdded,
		"records_updated": counters.RecordsUpdated,
		"bytes_ingested":  counters.BytesIngested,
		"errors":          counters.Errors,
		"started":         started.Format(time.RFC3339Nano),
		"finished":        finished.Format(time.RFC3339Nano),
		"via":             "capsule_ingestor",
	}
	_ = s.sources.SetLastSync(ctx, capsuleName, status, summary, finished)

	outcome := audit.OutcomeSuccess
	if isErr {
		outcome = audit.OutcomeError
	}
	_, _ = s.audit.Append(ctx, audit.Entry{
		Type:    "source.sync.completed",
		Actor:   audit.Actor{Kind: audit.ActorRuntime, ID: "scheduler"},
		Subject: &audit.Subject{Kind: audit.SubjectSource, ID: capsuleName},
		Outcome: outcome,
		Data:    summary,
	})
}

func (s *ingestorScheduler) recordFailed(
	ctx context.Context, capsuleName, sourceID string, started time.Time, err error,
) {
	finished := time.Now().UTC()
	summary := map[string]any{
		"records_added":   int64(0),
		"records_updated": int64(0),
		"bytes_ingested":  int64(0),
		"errors":          1,
		"started":         started.Format(time.RFC3339Nano),
		"finished":        finished.Format(time.RFC3339Nano),
		"error_message":   err.Error(),
		"via":             "capsule_ingestor",
	}
	_ = s.sources.SetLastSync(ctx, capsuleName, "error", summary, finished)
	_, _ = s.audit.Append(ctx, audit.Entry{
		Type:    "source.sync.completed",
		Actor:   audit.Actor{Kind: audit.ActorRuntime, ID: "scheduler"},
		Subject: &audit.Subject{Kind: audit.SubjectSource, ID: capsuleName},
		Outcome: audit.OutcomeError,
		Data:    summary,
	})
	s.logger.Warn("ingestor scheduler: sync failed",
		"capsule", capsuleName, "source_id", sourceID, "err", err)
}

// syncCounters mirrors the shape an ingestor capsule's sync tool
// is expected to return — see docs/capsule-ingestor-primitives.md
// "Returned shape." A subset of source.SyncResult; the runtime
// fills started/finished itself (the capsule can't lie about
// timing).
type syncCounters struct {
	RecordsAdded   int64 `json:"records_added"`
	RecordsUpdated int64 `json:"records_updated"`
	BytesIngested  int64 `json:"bytes_ingested"`
	Errors         int   `json:"errors"`
}

// parseSyncToolResult decodes the MCP tool-result envelope an
// ingestor's sync tool returns and pulls the syncCounters out of
// the first text content block.
//
// Acceptable shapes (in order tried):
//   - {content:[{type:"text",text:"<JSON-encoded counters>"}], isError?}
//   - direct {records_added: ...} object (capsule returned without
//     the MCP envelope; we accept it for resilience)
//
// Returns (counters, isError, err). `err` is non-nil only when the
// result is structurally unparseable.
func parseSyncToolResult(result any) (syncCounters, bool, error) {
	if result == nil {
		return syncCounters{}, false, fmt.Errorf("sync tool returned nil result")
	}
	rb, err := json.Marshal(result)
	if err != nil {
		return syncCounters{}, false, fmt.Errorf("re-encoding result: %w", err)
	}
	// Envelope shape: { content: [...], isError: bool }
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(rb, &env); err == nil && len(env.Content) > 0 {
		var c syncCounters
		if jerr := json.Unmarshal([]byte(env.Content[0].Text), &c); jerr == nil {
			return c, env.IsError, nil
		}
	}
	// Direct shape: capsule returned the counters object without
	// the envelope. Be tolerant.
	var direct syncCounters
	if err := json.Unmarshal(rb, &direct); err == nil {
		return direct, false, nil
	}
	return syncCounters{}, false, fmt.Errorf("sync tool result did not match counters shape")
}

// stop cancels every active ticker. Best-effort — used in tests;
// production drains via Host.Stop firing OnCapsuleStopped for each
// running capsule.
func (s *ingestorScheduler) stop() {
	s.mu.Lock()
	tickers := s.tickers
	s.tickers = make(map[string]*ingestorTicker)
	s.mu.Unlock()
	for _, t := range tickers {
		t.cancel()
		<-t.done
	}
}

// hasRole reports whether the role list contains the given name.
func hasRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}
