package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/source"
)

// Sources CRUD under /console/sources.
//
// The dashboard's Sources pane needs three mutations beyond the
// read-only snapshot /console/state provides:
//
//   POST   /console/sources              — add a new source
//   POST   /console/sources/{name}/sync  — trigger a sync (async)
//   DELETE /console/sources/{name}       — disconnect / remove
//
// Same unauthenticated-localhost contract as /console/init. The
// dashboard and the CLI both reach into the same SQLite store, so
// adding a source from either path shows up in both views on the
// next /console/state poll.
//
// All three handlers require s.sources (the persistence layer) AND
// s.sourceBuildEnv (the runtime adapter bag — needed to validate
// config by actually constructing a source.Source). Without the
// build env we can't tell "user typed a valid adapter id" from
// "the config will actually init" — and validating only at sync
// time would mean the dashboard happily accepts garbage and only
// reveals the truth when the user clicks Sync.
//
// 503 when either is missing. Tests run with the real subsystems
// so the 503 path stays exercised but doesn't dominate.

// --- POST /console/sources -------------------------------------------------

type sourceAddRequest struct {
	Adapter string         `json:"adapter"`
	Name    string         `json:"name"`
	Config  map[string]any `json:"config,omitempty"`
}

type sourceAddResponse struct {
	OK     bool          `json:"ok"`
	Source consoleSource `json:"source"`
}

func (s *Server) handleSourceAdd(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil || s.sourceBuildEnv == nil {
		writeJSONError(w, "source mutations are not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeJSONError(w, "request body too large or unreachable", http.StatusBadRequest)
		return
	}
	var req sourceAddRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Adapter == "" || req.Name == "" {
		writeJSONError(w, "adapter and name are required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	added, err := s.sources.Insert(ctx, source.Configured{
		Name:      req.Name,
		AdapterID: req.Adapter,
		Config:    req.Config,
	})
	switch {
	case errors.Is(err, source.ErrSourceNameTaken):
		writeJSONError(w, "a source with that name already exists", http.StatusConflict)
		return
	case err != nil:
		s.logger.Warn("console source add: insert failed", "err", err, "adapter", req.Adapter, "name", req.Name)
		writeJSONError(w, "failed to add source: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Validate that the source can actually be constructed with
	// this config. If Build fails we surface it as 400 (the
	// caller's input is the cause) and DELETE the row we just
	// inserted — otherwise the dashboard ends up with a row that
	// can never sync, which is worse than a clean rejection.
	src, buildErr := source.Build(ctx, *s.sourceBuildEnv, added)
	if buildErr != nil {
		_ = s.sources.Delete(ctx, added.Name)
		s.logger.Warn("console source add: build failed, rolled back",
			"err", buildErr, "adapter", req.Adapter, "name", req.Name)
		writeJSONError(w,
			"source rejected by adapter: "+buildErr.Error(),
			http.StatusBadRequest)
		return
	}
	_ = src.Close(ctx)

	// Audit the add. Same shape the CLI emits so dashboard-added
	// sources show up identically in `loamss audit tail`.
	_, _ = s.audit.Append(ctx, audit.Entry{
		Type:    "source.added",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: "console"},
		Subject: &audit.Subject{Kind: audit.SubjectSource, ID: added.Name},
		Outcome: audit.OutcomeSuccess,
		Data:    map[string]any{"adapter_id": added.AdapterID},
	})

	s.logger.Info("console source add", "name", added.Name, "adapter", added.AdapterID)
	writeJSON(w, http.StatusCreated, sourceAddResponse{
		OK:     true,
		Source: sourceToConsole(*added),
	})
}

// --- POST /console/sources/{name}/sync -------------------------------------

type sourceSyncResponse struct {
	OK   bool   `json:"ok"`
	Name string `json:"name"`
	Note string `json:"note"`
}

func (s *Server) handleSourceSync(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil || s.sourceBuildEnv == nil {
		writeJSONError(w, "source mutations are not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, "source name required in URL", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	c, err := s.sources.Get(ctx, name)
	switch {
	case errors.Is(err, source.ErrSourceNotFound):
		writeJSONError(w, "no source with that name", http.StatusNotFound)
		return
	case err != nil:
		writeJSONError(w, "failed to load source: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Refuse if a sync is already in flight. The SQLite row IS the
	// lock — we don't need an in-memory mutex map. Without this
	// guard, a double-click on the Sync button would launch two
	// concurrent syncs against the same source, racing each other's
	// cursor writes.
	if c.LastSyncStatus == "running" {
		writeJSONError(w, "a sync is already in progress for this source", http.StatusConflict)
		return
	}

	// Mark the row "running" synchronously so the next
	// /console/state poll (potentially before the goroutine even
	// starts working) shows the UI in the right state.
	if err := source.MarkSyncRunning(ctx, s.sources, name); err != nil {
		s.logger.Warn("console source sync: mark running failed", "err", err, "name", name)
		writeJSONError(w, "failed to mark sync running: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Fire and forget. The goroutine runs with a fresh background
	// context — we explicitly DON'T inherit the request context,
	// because the HTTP request returns immediately and its ctx
	// would be cancelled before the goroutine finishes its work.
	//
	// Errors land in the audit log + the source's last_sync_status.
	// Logged here at warn level so they show up in the daemon log
	// too; the dashboard will surface them via /console/state.
	env := *s.sourceBuildEnv
	store := s.sources
	auditor := s.audit
	logger := s.logger
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		src, buildErr := source.Build(bgCtx, env, c)
		if buildErr != nil {
			logger.Warn("console source sync: build failed", "err", buildErr, "name", name)
			_ = store.SetLastSync(bgCtx, name, "error",
				map[string]any{"error_message": buildErr.Error()}, time.Now().UTC())
			_, _ = auditor.Append(bgCtx, audit.Entry{
				Type:    "source.sync.completed",
				Actor:   audit.Actor{Kind: audit.ActorUser, ID: "console"},
				Subject: &audit.Subject{Kind: audit.SubjectSource, ID: name},
				Outcome: audit.OutcomeError,
				Data:    map[string]any{"error_message": buildErr.Error()},
			})
			return
		}
		defer func() { _ = src.Close(bgCtx) }()

		_, syncErr := source.RunSync(bgCtx, src, store, auditor, c,
			source.RunSyncActor{Kind: audit.ActorUser, ID: "console"})
		if syncErr != nil {
			logger.Warn("console source sync: failed", "err", syncErr, "name", name)
		} else {
			logger.Info("console source sync: completed", "name", name)
		}
	}()

	// 202 Accepted: we've accepted the work, it's in progress, the
	// final outcome lives in /console/state's next poll.
	writeJSON(w, http.StatusAccepted, sourceSyncResponse{
		OK:   true,
		Name: name,
		Note: "sync started; watch /console/state for the outcome",
	})
}

// --- DELETE /console/sources/{name} ----------------------------------------

func (s *Server) handleSourceDelete(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		writeJSONError(w, "source mutations are not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, "source name required in URL", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	// Confirm existence before deleting so we can return 404 cleanly.
	// The store.Delete is otherwise idempotent and silent — fine for
	// CLI but the dashboard's UX benefits from a real signal.
	c, err := s.sources.Get(ctx, name)
	switch {
	case errors.Is(err, source.ErrSourceNotFound):
		writeJSONError(w, "no source with that name", http.StatusNotFound)
		return
	case err != nil:
		writeJSONError(w, "failed to load source: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Drop credentials first when we have the build env to do so.
	// The CLI does this via a credential store; we can call into
	// the same one. Failures here log but don't block the delete —
	// orphaned credentials in storage are a minor cleanup issue,
	// while a half-deleted source is a worse problem.
	if s.sourceBuildEnv != nil {
		creds := source.NewStorageCredentialStore(s.sourceBuildEnv.Storage, c.Name)
		if err := creds.Delete(ctx); err != nil {
			s.logger.Warn("console source delete: credential clear failed",
				"err", err, "name", c.Name)
		}
	}

	if err := s.sources.Delete(ctx, c.Name); err != nil {
		writeJSONError(w, "failed to delete source: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_, _ = s.audit.Append(ctx, audit.Entry{
		Type:    "source.removed",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: "console"},
		Subject: &audit.Subject{Kind: audit.SubjectSource, ID: c.Name},
		Outcome: audit.OutcomeSuccess,
		Data:    map[string]any{"adapter_id": c.AdapterID},
	})

	s.logger.Info("console source delete", "name", c.Name)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"name": c.Name,
	})
}
