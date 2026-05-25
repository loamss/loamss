package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/permission"
	"github.com/loamss/loamss/runtime/internal/source"
)

// `/console/state` returns a single atomic snapshot of everything
// the dashboard needs to render: runtime status, the resolved
// config, the configured sources, installed/running capsules,
// paired clients, pending approvals, and the most recent N audit
// events.
//
// Design choices:
//
//   - ONE endpoint instead of five. The dashboard renders the
//     whole page at once; fanning out to /sources, /capsules,
//     /clients, /audit, /approvals would be 5x the round-trips
//     for no benefit and would also serialise inconsistently
//     (sources from t1, capsules from t2, etc.). One snapshot at
//     one moment is correct and faster.
//
//   - Graceful degradation. The Server holds optional refs to the
//     source store, capsule store, capsule host. When any is nil
//     (because the daemon was started in a stripped-down mode, or
//     a test wired only part of the surface), the corresponding
//     pane in the response is marked "available: false" rather
//     than crashing. The dashboard tile renders an "unavailable
//     in this build" placeholder instead of pretending to be
//     loading forever.
//
//   - Read-only. No mutation here, no side effects. The endpoint
//     is poll-safe and the dashboard can refresh it every few
//     seconds without thinking about idempotency.
//
//   - Same unauthenticated-localhost contract as /console/init.
//     The runtime defaults to 127.0.0.1 binding; the dashboard
//     runs in the browser the user just used to write the
//     config; no token to negotiate. If we ever expose the
//     runtime off-host we'll need a bearer-auth retrofit on
//     everything under /console/* — explicit in the design.

// consoleStateResponse is the JSON shape returned by GET
// /console/state. Fields are pointers / "available" flags wherever
// a subsystem might not be wired so the console can tell "no data
// yet" from "this build doesn't expose this".
type consoleStateResponse struct {
	GeneratedAt string                `json:"generated_at"`
	Runtime     consoleRuntimeBlock   `json:"runtime"`
	Config      consoleConfigSummary  `json:"config"`
	Sources     consoleSourcesBlock   `json:"sources"`
	Capsules    consoleCapsulesBlock  `json:"capsules"`
	Clients     consoleClientsBlock   `json:"clients"`
	Approvals   consoleApprovalsBlock `json:"approvals_pending"`
	Activity    consoleActivityBlock  `json:"activity"`
}

type consoleRuntimeBlock struct {
	Version       string `json:"version"`
	ListenAddr    string `json:"listen_addr"`
	DataDir       string `json:"data_dir"`
	StartedAt     string `json:"started_at"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

type consoleConfigSummary struct {
	Available      bool     `json:"available"`
	StorageAdapter string   `json:"storage_adapter,omitempty"`
	MemoryAdapter  string   `json:"memory_adapter,omitempty"`
	ModelAdapters  []string `json:"model_adapters,omitempty"`

	// WizardComplete reports whether the configured path has an
	// actual file on disk. False means the runtime is running
	// against library defaults (or env overrides) but no user
	// has accepted the setup yet. The console uses this to decide
	// whether to land on the dashboard or the wizard — `available`
	// + populated adapter fields are always true once BaseConfig
	// is wired (defaults populate them), so they're a poor "first
	// run completed" signal on their own.
	WizardComplete bool   `json:"wizard_complete"`
	WizardPath     string `json:"wizard_path,omitempty"`

	// RestartRequired lists the schema paths whose value on disk
	// differs from the live in-memory config. Populated by diffing
	// the file at WizardPath against baseConfig on every request.
	// Non-empty means: someone (the wizard, a hand-edit of the
	// YAML) changed config since the daemon started, and the
	// daemon hasn't picked it up. The dashboard renders a
	// "restart needed" banner in that case.
	RestartRequired []config.FieldChange `json:"restart_required,omitempty"`
}

type consoleSourcesBlock struct {
	Available bool            `json:"available"`
	Items     []consoleSource `json:"items"`
	Error     string          `json:"error,omitempty"`
}

type consoleSource struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Adapter        string         `json:"adapter"`
	LastSyncAt     string         `json:"last_sync_at,omitempty"`
	LastSyncStatus string         `json:"last_sync_status"` // "success", "error", "running", or "" (never synced)
	Summary        map[string]any `json:"summary,omitempty"`
	AddedAt        string         `json:"added_at"`
}

type consoleCapsulesBlock struct {
	Available bool             `json:"available"`
	Items     []consoleCapsule `json:"items"`
	Error     string           `json:"error,omitempty"`
}

type consoleCapsule struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Author      string   `json:"author,omitempty"`
	Permissions []string `json:"permissions"`
	InstalledAt string   `json:"installed_at"`
	Running     bool     `json:"running"`
}

type consoleClientsBlock struct {
	Available bool            `json:"available"`
	Items     []consoleClient `json:"items"`
	Error     string          `json:"error,omitempty"`
}

type consoleClient struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PairedAt   string `json:"paired_at"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
	Active     bool   `json:"active"`
}

type consoleApprovalsBlock struct {
	Available bool              `json:"available"`
	Items     []consoleApproval `json:"items"`
	Error     string            `json:"error,omitempty"`
}

type consoleApproval struct {
	ID            string         `json:"id"`
	PrincipalKind string         `json:"principal_kind"`
	PrincipalID   string         `json:"principal_id"`
	Capability    string         `json:"capability"`
	Rationale     string         `json:"rationale,omitempty"`
	Scope         map[string]any `json:"scope,omitempty"`
	RequestedAt   string         `json:"requested_at"`
}

type consoleActivityBlock struct {
	Available bool              `json:"available"`
	Items     []consoleActivity `json:"items"`
	Error     string            `json:"error,omitempty"`
}

type consoleActivity struct {
	ID          string `json:"id"`
	At          string `json:"at"`
	Type        string `json:"type"`
	ActorKind   string `json:"actor_kind"`
	ActorID     string `json:"actor_id"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectID   string `json:"subject_id,omitempty"`
	Outcome     string `json:"outcome"`
}

// consoleConfigPath resolves where the wizard would write — either
// the path the daemon was launched with (via --config) or the
// library default. Centralised here so wizardConfigPresent and the
// /console/init handler can't drift.
func (s *Server) consoleConfigPath() string {
	if s.configPath != "" {
		return s.configPath
	}
	return config.DefaultPath()
}

// detectRestartRequired diffs the file on disk against the live
// baseConfig and returns the fields that need a restart to take
// effect. Empty when no file exists, no baseConfig is wired, or
// the file matches the live config.
//
// Errors reading the file are logged at debug level and treated
// as "no diff" — a missing/unreadable file shouldn't pollute the
// dashboard's state response. The wizard's wizard_complete
// signal handles the "no file" case separately.
func (s *Server) detectRestartRequired() []config.FieldChange {
	if s.baseConfig == nil {
		return nil
	}
	path := s.consoleConfigPath()
	if _, err := os.Stat(path); err != nil {
		return nil // no file → nothing to diff against
	}
	onDisk, err := config.Load(path)
	if err != nil {
		s.logger.Debug("console state: config load for diff failed", "err", err, "path", path)
		return nil
	}
	diff := config.Diff(s.baseConfig, onDisk)
	if len(diff.RestartRequired) == 0 {
		return nil
	}
	return diff.RestartRequired
}

// wizardConfigPresent reports whether an actual file exists at the
// wizard's target path. This is the honest signal for "has the
// user completed setup?" — distinct from "do we have a working
// config?", which is true the moment the runtime starts (defaults
// populate every required field).
func (s *Server) wizardConfigPresent() bool {
	_, err := os.Stat(s.consoleConfigPath())
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// activityFetchLimit caps how many recent audit events we surface
// to the dashboard. Twenty is enough to fill the pane without
// pulling so many rows that the SQLite query becomes a footgun
// during high-volume bursts (model-call storms, sync runs).
const activityFetchLimit = 20

func (s *Server) handleConsoleState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	resp := consoleStateResponse{
		GeneratedAt: nowRFC3339(),
		Runtime: consoleRuntimeBlock{
			Version:       s.version,
			ListenAddr:    s.addr,
			StartedAt:     s.startedAt.Format(time.RFC3339Nano),
			UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		},
	}

	// Config — always available when baseConfig is wired. The
	// wizard's "what did I just set up?" recap reads this.
	if s.baseConfig != nil {
		resp.Runtime.DataDir = s.baseConfig.Runtime.DataDir
		resp.Config = consoleConfigSummary{
			Available:       true,
			StorageAdapter:  s.baseConfig.Storage.Adapter,
			MemoryAdapter:   s.baseConfig.Memory.Adapter,
			WizardComplete:  s.wizardConfigPresent(),
			WizardPath:      s.consoleConfigPath(),
			RestartRequired: s.detectRestartRequired(),
		}
		for _, m := range s.baseConfig.Models {
			resp.Config.ModelAdapters = append(resp.Config.ModelAdapters, m.Adapter)
		}
	}

	resp.Sources = s.collectSources(ctx)
	resp.Capsules = s.collectCapsules(ctx)
	resp.Clients = s.collectClients(ctx)
	resp.Approvals = s.collectApprovals(ctx)
	resp.Activity = s.collectActivity(ctx)

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) collectSources(ctx context.Context) consoleSourcesBlock {
	if s.sources == nil {
		return consoleSourcesBlock{Available: false, Items: []consoleSource{}}
	}
	list, err := s.sources.List(ctx)
	if err != nil {
		s.logger.Warn("console state: listing sources", "err", err)
		return consoleSourcesBlock{Available: true, Items: []consoleSource{}, Error: err.Error()}
	}
	items := make([]consoleSource, 0, len(list))
	for _, c := range list {
		items = append(items, sourceToConsole(c))
	}
	return consoleSourcesBlock{Available: true, Items: items}
}

func sourceToConsole(c source.Configured) consoleSource {
	cs := consoleSource{
		ID:             c.ID,
		Name:           c.Name,
		Adapter:        c.AdapterID,
		LastSyncStatus: c.LastSyncStatus,
		Summary:        c.LastSyncSummary,
		AddedAt:        c.AddedAt.Format(time.RFC3339Nano),
	}
	if !c.LastSyncAt.IsZero() {
		cs.LastSyncAt = c.LastSyncAt.Format(time.RFC3339Nano)
	}
	return cs
}

func (s *Server) collectCapsules(ctx context.Context) consoleCapsulesBlock {
	if s.capsules == nil {
		return consoleCapsulesBlock{Available: false, Items: []consoleCapsule{}}
	}
	list, err := s.capsules.List(ctx)
	if err != nil {
		s.logger.Warn("console state: listing capsules", "err", err)
		return consoleCapsulesBlock{Available: true, Items: []consoleCapsule{}, Error: err.Error()}
	}
	// Build a name → running set so we can stamp each row with its
	// live state. The host can be nil if the runtime started in a
	// no-subprocess mode (none ship today, but the option exists);
	// when it is, every capsule is reported running:false.
	running := map[string]bool{}
	if s.host != nil {
		for _, name := range s.host.Running() {
			running[name] = true
		}
	}
	items := make([]consoleCapsule, 0, len(list))
	for _, c := range list {
		items = append(items, capsuleToConsole(c, running[c.Name]))
	}
	return consoleCapsulesBlock{Available: true, Items: items}
}

func capsuleToConsole(c capsule.Installed, isRunning bool) consoleCapsule {
	cc := consoleCapsule{
		ID:          c.ID,
		Name:        c.Name,
		Version:     c.Version,
		Author:      c.AuthorName,
		InstalledAt: c.InstalledAt.Format(time.RFC3339Nano),
		Running:     isRunning,
		Permissions: []string{},
	}
	if c.Manifest != nil {
		for _, p := range c.Manifest.Permissions {
			cc.Permissions = append(cc.Permissions, p.Capability)
		}
	}
	return cc
}

func (s *Server) collectClients(ctx context.Context) consoleClientsBlock {
	if s.engine == nil {
		return consoleClientsBlock{Available: false, Items: []consoleClient{}}
	}
	store := s.engine.Store()
	if store == nil {
		return consoleClientsBlock{Available: false, Items: []consoleClient{}}
	}
	clients, err := store.ListClients(ctx, permission.ClientFilter{})
	if err != nil {
		s.logger.Warn("console state: listing clients", "err", err)
		return consoleClientsBlock{Available: true, Items: []consoleClient{}, Error: err.Error()}
	}
	items := make([]consoleClient, 0, len(clients))
	for _, c := range clients {
		items = append(items, clientToConsole(c))
	}
	return consoleClientsBlock{Available: true, Items: items}
}

func clientToConsole(c permission.Client) consoleClient {
	cc := consoleClient{
		ID:       c.ID,
		Name:     c.Name,
		PairedAt: c.PairedAt.Format(time.RFC3339Nano),
		Active:   c.Active(),
	}
	if c.LastSeenAt != nil {
		cc.LastSeenAt = c.LastSeenAt.Format(time.RFC3339Nano)
	}
	return cc
}

func (s *Server) collectApprovals(ctx context.Context) consoleApprovalsBlock {
	if s.engine == nil {
		return consoleApprovalsBlock{Available: false, Items: []consoleApproval{}}
	}
	store := s.engine.Store()
	if store == nil {
		return consoleApprovalsBlock{Available: false, Items: []consoleApproval{}}
	}
	pending, err := store.ListPendingApprovals(ctx)
	if err != nil {
		s.logger.Warn("console state: listing approvals", "err", err)
		return consoleApprovalsBlock{Available: true, Items: []consoleApproval{}, Error: err.Error()}
	}
	items := make([]consoleApproval, 0, len(pending))
	for _, p := range pending {
		items = append(items, approvalToConsole(p))
	}
	return consoleApprovalsBlock{Available: true, Items: items}
}

func approvalToConsole(p permission.PendingApproval) consoleApproval {
	return consoleApproval{
		ID:            p.ID,
		PrincipalKind: string(p.Principal.Kind),
		PrincipalID:   p.Principal.ID,
		Capability:    p.Capability,
		Rationale:     p.Rationale,
		Scope:         p.AttemptedScope,
		RequestedAt:   p.RequestedAt.Format(time.RFC3339Nano),
	}
}

func (s *Server) collectActivity(ctx context.Context) consoleActivityBlock {
	if s.audit == nil {
		return consoleActivityBlock{Available: false, Items: []consoleActivity{}}
	}
	entries, err := s.audit.Query(ctx, audit.Filter{
		Limit:   activityFetchLimit,
		Reverse: true, // newest first
	})
	if err != nil {
		s.logger.Warn("console state: querying audit", "err", err)
		return consoleActivityBlock{Available: true, Items: []consoleActivity{}, Error: err.Error()}
	}
	items := make([]consoleActivity, 0, len(entries))
	for _, e := range entries {
		items = append(items, entryToConsole(e))
	}
	return consoleActivityBlock{Available: true, Items: items}
}

func entryToConsole(e audit.Entry) consoleActivity {
	ca := consoleActivity{
		ID:        e.ID,
		At:        e.Timestamp.Format(time.RFC3339Nano),
		Type:      e.Type,
		ActorKind: string(e.Actor.Kind),
		ActorID:   e.Actor.ID,
		Outcome:   string(e.Outcome),
	}
	if e.Subject != nil {
		ca.SubjectKind = string(e.Subject.Kind)
		ca.SubjectID = e.Subject.ID
	}
	return ca
}
