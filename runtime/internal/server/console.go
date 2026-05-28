package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/config"
)

// `/console/init` accepts the first-run wizard's collected
// configuration and persists it to disk.
//
// Contract:
//
//   - POST only
//   - body: a JSON object describing storage / memory / models /
//     source_intents
//   - on success: 200 with a small response describing what was
//     written and a hint that the runtime needs a restart to pick
//     up the new config
//   - on collision (config file already exists): 409 with a
//     machine-readable code so the console can offer "overwrite" or
//     "back up + overwrite". The caller retries with ?overwrite=1.
//   - on validation failure: 400 with the validator's message
//
// Auth: none. The runtime defaults to binding 127.0.0.1 only, so
// this endpoint is unreachable from off-host. The wizard runs before
// any client is paired — there's no token to use anyway.
//
// Atomicity: the write goes through config.WriteAtomic — temp file
// in the same directory, rename into place. The file is never
// observable in a half-written state.

type consoleInitRequest struct {
	Storage struct {
		Adapter string         `json:"adapter"`
		Config  map[string]any `json:"config"`
	} `json:"storage"`
	Memory struct {
		Adapter string         `json:"adapter"`
		Config  map[string]any `json:"config"`
	} `json:"memory"`
	Models []struct {
		Adapter string         `json:"adapter"`
		Config  map[string]any `json:"config"`
	} `json:"models"`
	SourceIntents []struct {
		Adapter string `json:"adapter"`
		Name    string `json:"name"`
	} `json:"source_intents,omitempty"`
}

type consoleInitResponse struct {
	OK         bool               `json:"ok"`
	ReceivedAt string             `json:"received_at"`
	Echo       consoleInitRequest `json:"echo"`
	// WrittenTo is the absolute path the config landed at. Empty if
	// the write was a dry run (none currently — kept for future).
	WrittenTo string `json:"written_to,omitempty"`
	// NextStep is a short, human-readable hint the console renders on
	// the Done screen. Today: "restart `loamss start` to apply" when
	// restart-required fields changed; "applied" when only
	// hot-swappable fields changed; defaults appropriately.
	NextStep string `json:"next_step,omitempty"`
	Note     string `json:"note,omitempty"`
	// Applied lists schema paths whose change took effect immediately
	// (e.g. log.level rebuilds the daemon's slog handler). Empty when
	// nothing was hot-swappable.
	Applied []config.FieldChange `json:"applied,omitempty"`
	// RestartRequired lists schema paths whose change won't take effect
	// until `loamss start` is restarted. The wizard's Done page
	// renders a banner when this is non-empty.
	RestartRequired []config.FieldChange `json:"restart_required,omitempty"`
	// PairedConsole is the bearer-credential handoff that closes the
	// cloud-bootstrap gap: after /console/init burns the setup token,
	// the wizard JS needs *some* credential to keep talking to
	// /console/* through the gate. We mint a paired "Loamss Console"
	// client as part of the same request and return its bearer here.
	// The console persists it to localStorage and uses it from this
	// point onward; the setup token is dropped.
	//
	// On laptop installs the gate is inactive, but we mint the client
	// anyway so the dashboard's Apps pane shows a real row from day
	// one (and so future features that DO require a bearer have one
	// available).
	PairedConsole *consoleInitPairedConsole `json:"paired_console,omitempty"`
	Capability    consoleInitCapacity       `json:"capability"`
}

// consoleInitPairedConsole carries the auto-paired client's
// credentials. Surfaced once to the wizard; the runtime cannot
// re-emit the bearer (same one-shot rule as `loamss client pair
// complete`).
type consoleInitPairedConsole struct {
	ClientID string `json:"client_id"`
	Token    string `json:"token"`
}

// consoleInitConflictResponse is returned with HTTP 409 when a config
// file already exists at the target path. The console distinguishes
// this from other 4xx errors via the "code" field so it can offer
// the user a one-click "overwrite" affordance.
type consoleInitConflictResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	Path  string `json:"path"`
	Hint  string `json:"hint"`
}

// consoleInitCapacity tells the console what /console/init can
// actually do in this build. Now that the writer is wired,
// writes_config_file=true. The remaining capabilities (restart,
// paired-console, configured-sources) still wait for follow-up work.
type consoleInitCapacity struct {
	WritesConfigFile      bool `json:"writes_config_file"`
	RestartsRuntime       bool `json:"restarts_runtime"`
	CreatesPairedConsole  bool `json:"creates_paired_console"`
	AddsConfiguredSources bool `json:"adds_configured_sources"`
}

// consoleInitHeader is the comment block emitted at the top of the
// generated config file. It tells future-them (or a confused power
// user reading the file by hand) where the file came from and that
// editing it directly is fine — the wizard isn't watching.
const consoleInitHeader = `Generated by the Loamss console wizard.

This file is YAML and safe to hand-edit. The runtime re-reads it on
restart (` + "`loamss start`" + `). If you re-run the wizard, the
existing file is kept unless you explicitly choose "overwrite" — no
configuration is lost without consent.

Schema reference: docs/adapter-interface.md`

func (s *Server) handleConsoleInit(w http.ResponseWriter, r *http.Request) {
	// Bound the read so a misbehaving client can't push gigabytes.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeJSONError(w, "request body too large or unreachable", http.StatusBadRequest)
		return
	}
	var req consoleInitRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Storage.Adapter == "" || req.Memory.Adapter == "" {
		writeJSONError(w, "storage.adapter and memory.adapter are required", http.StatusBadRequest)
		return
	}

	// Build a Config from the running daemon's snapshot (or library
	// defaults when no snapshot is wired), then overlay the wizard's
	// storage/memory/models choices. Carrying forward the running
	// config means runtime.data_dir, listen_addr, audit.*, and log.*
	// reflect what's actually live — not whatever Default() returns
	// on this host.
	cfg := buildConfigFromConsoleInit(s.baseConfig, req)

	// Validate at the request boundary so bad input surfaces as a
	// 400 (it's the client's fault) rather than the 500 that
	// WriteAtomic's defense-in-depth check would otherwise produce.
	// WriteAtomic still re-validates so file-on-disk callers stay
	// covered.
	if err := config.Validate(cfg); err != nil {
		writeJSONError(w, "invalid configuration: "+err.Error(), http.StatusBadRequest)
		return
	}

	target := s.configPath
	if target == "" {
		target = config.DefaultPath()
	}

	overwrite := r.URL.Query().Get("overwrite") == "1"

	err = config.WriteAtomic(target, cfg, config.WriteOptions{
		Overwrite:    overwrite,
		BackupSuffix: backupSuffixForOverwrite(overwrite),
		Header:       consoleInitHeader,
	})
	switch {
	case errors.Is(err, config.ErrAlreadyExists):
		s.logger.Info("console init refused: file already exists", "path", target)
		writeJSON(w, http.StatusConflict, consoleInitConflictResponse{
			Error: "a config file already exists at the destination",
			Code:  "config_already_exists",
			Path:  target,
			Hint:  "POST again with ?overwrite=1 to replace it (the existing file will be renamed with a timestamped .bak suffix)",
		})
		return
	case err != nil:
		s.logger.Error("console init: writing config", "err", err, "path", target)
		writeJSONError(w, "failed to write config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.logger.Info("console init wrote config",
		"path", target,
		"storage", req.Storage.Adapter,
		"memory", req.Memory.Adapter,
		"models", len(req.Models),
		"source_intents", len(req.SourceIntents),
	)

	// Burn the setup token. From here on the gate enforces
	// paired-client auth only; the bearer-token-as-setup-token path
	// dies for this instance and (via the sentinel file) for any
	// future restarts against the same data_dir.
	//
	// Errors on the sentinel write are logged but not surfaced: the
	// in-memory flip already happened, and the next /console/init
	// attempt under this process will be rejected regardless. The
	// only thing the sentinel buys us is restart-safety.
	//
	// Audit: emit setup_token.consumed exactly once across concurrent
	// init attempts. Consume returns firstCall=true only on the call
	// that actually flipped the atomic state, so two simultaneous
	// /console/init requests both succeed at writing config but only
	// one writes the audit row. The principal in the request context,
	// when present, is the paired-client that completed init —
	// recorded as the actor. When the consumer used the raw setup
	// token, no principal exists and we record actor=system.
	if s.setupToken != nil {
		firstCall, err := s.setupToken.Consume()
		if err != nil {
			s.logger.Warn("console init: persisting setup-token consumption failed",
				"err", err,
				"hint", "in-memory state is correct; on restart the gate would re-open until /console/init is rerun",
			)
		}
		if firstCall && s.audit != nil {
			actor := audit.Actor{Kind: audit.ActorSystem, ID: "setup-token"}
			if p := PrincipalFromContext(r.Context()); p != nil {
				actor = audit.Actor{Kind: audit.ActorKind(p.Kind), ID: p.ID}
			}
			_, _ = s.audit.Append(r.Context(), audit.Entry{
				Type:    "setup_token.consumed",
				Actor:   actor,
				Subject: &audit.Subject{Kind: audit.SubjectSetupToken, ID: "active"},
				Outcome: audit.OutcomeSuccess,
				Data: map[string]any{
					"origin":      s.setupToken.Origin(),
					"config_path": target,
				},
			})
		}
	}

	// Auto-pair a "Loamss Console" client and hand the bearer back
	// to the wizard. Closes the cloud-bootstrap gap: post-init the
	// gate accepts only paired-client credentials, and prior to this
	// fix the operator had no way to obtain one without psql
	// surgery against runtime.db.
	//
	// We use the same code-mint + code-redeem flow the CLI offers
	// (loamss client pair / loamss client pair complete), just
	// in-process so no HTTP round trip is required. The TTL is
	// short (10 seconds) because the redeem is the very next call.
	//
	// Failures are logged but don't fail the init — config was
	// already written, and the operator can fall back to the psql
	// workaround documented in docs/deploying.md. Worst case is
	// degraded UX, not lost work.
	var paired *consoleInitPairedConsole
	if s.engine != nil {
		paired = s.mintConsoleClient(r.Context())
	}

	// Diff against the live config. Apply what's hot-swappable
	// (currently only log config), report the rest as
	// restart-required so the dashboard can render a clear banner
	// without lying about what just happened.
	diff := config.Diff(s.baseConfig, cfg)
	applied := s.applyHotSwap(cfg, diff)

	// Update baseConfig pointer so subsequent /console/state diffs
	// reflect the new on-disk file as the live config (modulo the
	// fields still needing restart — those are reported separately).
	s.baseConfig = cfg

	nextStep := "Configuration written. No changes required a restart."
	if len(diff.RestartRequired) > 0 {
		nextStep = "Restart the runtime (`loamss start`) for the changed sections to take effect."
	} else if len(applied) > 0 {
		nextStep = "Configuration applied — no restart needed."
	}

	writeJSON(w, http.StatusOK, consoleInitResponse{
		OK:              true,
		ReceivedAt:      nowRFC3339(),
		Echo:            req,
		WrittenTo:       target,
		NextStep:        nextStep,
		Applied:         applied,
		RestartRequired: diff.RestartRequired,
		PairedConsole:   paired,
		Capability: consoleInitCapacity{
			WritesConfigFile: true,
			// Partial hot-reload now lives in the daemon: log.level
			// + log.format swap without a restart. Storage, memory,
			// models, listen_addr still require `loamss start` —
			// honestly reported via restart_required above.
			RestartsRuntime:       len(applied) > 0,
			CreatesPairedConsole:  paired != nil,
			AddsConfiguredSources: false,
		},
	})
}

// applyHotSwap calls the runtime's reload callbacks for fields the
// daemon can adopt without a restart. Returns the FieldChange list
// for what was actually applied; the caller surfaces this in the
// HTTP response so the dashboard can show "X took effect".
//
// Log-config reload is the only one wired today. Future:
// audit.redaction_level (when the writer accepts runtime tuning),
// model adapter list (when the MCP tools route through a swappable
// ModelRouter), per-adapter config maps (when adapters expose
// Reinit). Storage and memory adapters are unlikely candidates —
// they hold open SQLite handles and changing them mid-flight is a
// recipe for corruption.
func (s *Server) applyHotSwap(newCfg *config.Config, diff config.DiffResult) []config.FieldChange {
	applied := make([]config.FieldChange, 0, len(diff.HotSwapped))
	for _, fc := range diff.HotSwapped {
		switch fc.Path {
		case "log.level", "log.format":
			if s.reloadLog == nil {
				// No reload callback wired (test fixture, stripped
				// build). Report as restart-required instead by
				// pushing back into the restart list — but we don't
				// have access to it here; the simplest signal is to
				// log and skip.
				s.logger.Info("console init: log config changed but no reload callback wired",
					"path", fc.Path)
				continue
			}
			if err := s.reloadLog(newCfg.Log); err != nil {
				s.logger.Warn("console init: log reload failed",
					"err", err, "path", fc.Path)
				continue
			}
			applied = append(applied, fc)
		default:
			// HotSwapped contains a path the server doesn't know
			// how to apply. Programming error: a field landed in
			// the HotSwapped bucket without a matching case here.
			s.logger.Warn("console init: hot-swap requested for unknown path",
				"path", fc.Path)
		}
	}
	return applied
}

// buildConfigFromConsoleInit translates the wizard's payload into a
// runtime.Config. It starts from `base` — typically the running
// daemon's live config — so all the fields the wizard doesn't
// collect (runtime dirs, audit, log) preserve the daemon's actual
// state. When base is nil it falls back to config.Default() so the
// function stays usable from tests that don't wire a running config.
//
// The function does NOT validate; WriteAtomic re-validates before
// touching disk, and surfacing that error there keeps the failure
// shape consistent with file-driven config edits.
func buildConfigFromConsoleInit(base *config.Config, req consoleInitRequest) *config.Config {
	var cfg *config.Config
	if base != nil {
		// Shallow copy is enough: every section is either a value
		// (Runtime, Audit, Log) or a fresh slice/map we're about to
		// replace (Storage, Memory, Models). The Routing slice from
		// the running config carries forward by reference, which is
		// fine — the wizard doesn't touch it and the file write only
		// reads it.
		clone := *base
		cfg = &clone
	} else {
		cfg = config.Default()
	}

	cfg.Storage = config.AdapterConfig{
		Adapter: req.Storage.Adapter,
		Config:  req.Storage.Config,
	}
	cfg.Memory = config.AdapterConfig{
		Adapter: req.Memory.Adapter,
		Config:  req.Memory.Config,
	}

	// Models: the wizard's slice fully replaces whatever was running.
	// If the wizard sent zero models, that's a deliberate "no models"
	// choice — clear the list rather than keeping the old one.
	cfg.Models = make([]config.AdapterConfig, 0, len(req.Models))
	for _, m := range req.Models {
		cfg.Models = append(cfg.Models, config.AdapterConfig{
			Adapter: m.Adapter,
			Config:  m.Config,
		})
	}

	// Source intents from the wizard are not yet persisted into the
	// config file — sources live in runtime.db (see
	// internal/source/), and provisioning them needs OAuth callbacks
	// for connectors like Gmail. The wizard captures the intent so
	// the console can present a "set up your sources" next-step list
	// after restart, but we don't try to forge an entry in
	// runtime.db from here.

	return cfg
}

// backupSuffixForOverwrite picks the backup-suffix when overwriting.
// We use a timestamped suffix so multiple re-runs of the wizard
// don't clobber each other's backups. Empty when not overwriting
// (the path is unused in that case).
func backupSuffixForOverwrite(overwrite bool) string {
	if !overwrite {
		return ""
	}
	return ".%s.bak"
}

// nowRFC3339 is a tiny helper kept here rather than reaching for
// time.Now().UTC().Format(time.RFC3339Nano) inline — makes the
// handler easier to read and mockable in tests if we ever need to.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// mintConsoleClient runs the create-pairing-code + redeem dance
// internally so /console/init can hand the wizard a paired-client
// bearer alongside the config-written response. Without this, after
// the gate consumes the setup token, the operator has no path to
// authenticate any subsequent /console/* request without restarting
// the runtime.
//
// Returns nil on any failure — the init request itself still
// succeeds, just without the bearer. The operator can fall back to
// the documented psql workaround in docs/deploying.md, which is
// strictly less convenient but doesn't lose work.
//
// The "created_by" actor is "system:console-init" so the audit row
// reflects the runtime-initiated origin (vs. a human operator
// running `loamss client pair --name X`). The metadata field
// records the same.
func (s *Server) mintConsoleClient(ctx context.Context) *consoleInitPairedConsole {
	const (
		name      = "Loamss Console"
		createdBy = "system:console-init"
		ttl       = 10 * time.Second // we redeem in the next line; no race window
	)
	code, err := s.engine.CreatePairingCode(ctx, name, createdBy, ttl)
	if err != nil {
		s.logger.Warn("console init: minting paired-console pairing code failed",
			"err", err,
			"hint", "operator can fall back to the psql workaround in docs/deploying.md",
		)
		return nil
	}
	client, token, err := s.engine.RedeemPairingCode(ctx, code.Code, map[string]any{
		"paired_via": "console_init",
		"origin":     "auto",
	})
	if err != nil {
		s.logger.Warn("console init: redeeming paired-console code failed",
			"err", err,
			"hint", "operator can fall back to the psql workaround in docs/deploying.md",
		)
		return nil
	}
	s.logger.Info("console init: auto-paired console client",
		"client_id", client.ID,
		"name", client.Name,
	)
	return &consoleInitPairedConsole{
		ClientID: client.ID,
		Token:    token,
	}
}
