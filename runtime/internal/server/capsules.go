package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/capsule"
)

// Capsules CRUD + lifecycle under /console/capsules.
//
//   POST   /console/capsules              — install from a path
//   DELETE /console/capsules/{name}       — uninstall
//   POST   /console/capsules/{name}/start — bring online
//   POST   /console/capsules/{name}/stop  — take offline
//
// Same unauthenticated-localhost contract as the rest of /console/*.
// Mutations require both the installer + the host; reads (already
// in /console/state) only need the store.
//
// Path-based install: today the request body is { path: string }
// pointing at a directory or .yaml on the same machine as the
// runtime. That matches the CLI's `loamss capsule install <path>`
// and works for the common desktop case. Uploading a tarball /
// directory through the browser is a future commit when the
// capsule marketplace exists; the runtime layer would be the same
// (Installer.Install operates on a filesystem path either way).
//
// Permission-slip preview: the install response carries the full
// list of declared permissions so the dashboard can render its
// "this capsule is about to receive these capabilities" review
// before the grants land. The grants ARE issued by Install — we
// don't gate them — but the UI shows them on the post-install
// screen so the user always sees what changed.

// --- POST /console/capsules ------------------------------------------------

type capsuleInstallRequest struct {
	Path string `json:"path"`
}

type capsuleInstallResponse struct {
	OK       bool                        `json:"ok"`
	Capsule  consoleCapsule              `json:"capsule"`
	Grants   []string                    `json:"grants"`
	Manifest capsuleInstallManifestBlock `json:"manifest"`
}

// capsuleInstallManifestBlock surfaces the parsed manifest's
// human-meaningful pieces so the dashboard can render the
// permission slip + capsule-card right after install. We don't
// echo the whole manifest because most of it (tool schemas,
// model requirements) is noise on the post-install screen.
type capsuleInstallManifestBlock struct {
	Name        string                       `json:"name"`
	Version     string                       `json:"version"`
	Description string                       `json:"description,omitempty"`
	Author      string                       `json:"author,omitempty"`
	Permissions []capsuleManifestPermission  `json:"permissions"`
	Tools       []capsuleManifestToolSummary `json:"tools,omitempty"`
}

type capsuleManifestPermission struct {
	Capability           string         `json:"capability"`
	Scope                map[string]any `json:"scope,omitempty"`
	Rationale            string         `json:"rationale,omitempty"`
	RequiresUserApproval bool           `json:"requires_user_approval"`
}

type capsuleManifestToolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (s *Server) handleCapsuleInstall(w http.ResponseWriter, r *http.Request) {
	if s.capsuleInstaller == nil || s.capsules == nil {
		writeJSONError(w, "capsule mutations are not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeJSONError(w, "request body too large or unreachable", http.StatusBadRequest)
		return
	}
	var req capsuleInstallRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		writeJSONError(w, "path is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	result, err := s.capsuleInstaller.Install(ctx, req.Path, "console")
	switch {
	case errors.Is(err, capsule.ErrCapsuleAlreadyInstalled):
		writeJSONError(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		// Install validates the manifest before doing any work;
		// validation errors come back as plain Go errors. Surfacing
		// them as 400 ("you gave us a bad capsule") is more useful
		// than 500 ("we crashed").
		writeJSONError(w, "install failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Best-effort start. The host's StartOne is idempotent — if the
	// runtime is configured to auto-start on Host.Start, the row
	// might already be running; either way, post-install starts
	// match user expectation (you installed it to use it).
	//
	// IMPORTANT: we MUST NOT pass the request ctx through to
	// StartOne. The capsule subprocess is spawned with
	// exec.CommandContext(ctx, ...) — its lifetime would be tied to
	// the HTTP request and the subprocess would be killed the
	// moment we returned a response. Use context.Background() so
	// the capsule's lifetime tracks the daemon's, matching the
	// invariant Host.Start (batch) already relies on.
	startedNote := ""
	if s.host != nil {
		if err := s.host.StartOne(context.Background(), *result.Capsule); err != nil {
			s.logger.Warn("console capsule install: post-install start failed",
				"err", err, "name", result.Capsule.Name)
			startedNote = "installed; auto-start failed (" + err.Error() + ")"
		}
	}

	s.logger.Info("console capsule install",
		"name", result.Capsule.Name,
		"version", result.Capsule.Version,
		"grants", len(result.GrantIDs))

	running := false
	if s.host != nil {
		for _, n := range s.host.Running() {
			if n == result.Capsule.Name {
				running = true
				break
			}
		}
	}

	resp := capsuleInstallResponse{
		OK:       true,
		Capsule:  capsuleToConsole(*result.Capsule, running),
		Grants:   result.GrantIDs,
		Manifest: manifestToInstallBlock(result.Capsule),
	}
	if startedNote != "" {
		writeJSON(w, http.StatusCreated, struct {
			capsuleInstallResponse
			Note string `json:"note,omitempty"`
		}{resp, startedNote})
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func manifestToInstallBlock(c *capsule.Installed) capsuleInstallManifestBlock {
	if c == nil || c.Manifest == nil {
		return capsuleInstallManifestBlock{}
	}
	m := c.Manifest
	perms := make([]capsuleManifestPermission, 0, len(m.Permissions))
	for _, p := range m.Permissions {
		perms = append(perms, capsuleManifestPermission{
			Capability:           p.Capability,
			Scope:                p.Scope,
			Rationale:            p.Rationale,
			RequiresUserApproval: p.RequiresUserApproval,
		})
	}
	tools := make([]capsuleManifestToolSummary, 0, len(m.Tools))
	for _, t := range m.Tools {
		tools = append(tools, capsuleManifestToolSummary{
			Name:        t.Name,
			Description: t.Description,
		})
	}
	return capsuleInstallManifestBlock{
		Name:        m.Name,
		Version:     m.Version,
		Description: m.Description,
		Author:      m.Author.Name,
		Permissions: perms,
		Tools:       tools,
	}
}

// --- DELETE /console/capsules/{name} ---------------------------------------

func (s *Server) handleCapsuleUninstall(w http.ResponseWriter, r *http.Request) {
	if s.capsuleInstaller == nil {
		writeJSONError(w, "capsule mutations are not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, "capsule name required in URL", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	// Stop first so the subprocess is gone before its files / grants
	// are removed. StopOne is idempotent — no error if not running.
	if s.host != nil {
		if err := s.host.StopOne(ctx, name); err != nil {
			s.logger.Warn("console capsule uninstall: stop failed (continuing)",
				"err", err, "name", name)
		}
	}

	err := s.capsuleInstaller.Uninstall(ctx, name, "console", "removed via dashboard")
	switch {
	case errors.Is(err, capsule.ErrCapsuleNotFound):
		writeJSONError(w, "no capsule with that name", http.StatusNotFound)
		return
	case err != nil:
		writeJSONError(w, "uninstall failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.logger.Info("console capsule uninstall", "name", name)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"name": name,
	})
}

// --- POST /console/capsules/{name}/start -----------------------------------

func (s *Server) handleCapsuleStart(w http.ResponseWriter, r *http.Request) {
	if s.capsules == nil || s.host == nil {
		writeJSONError(w, "capsule lifecycle is not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, "capsule name required in URL", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	c, err := s.capsules.Get(ctx, name)
	switch {
	case errors.Is(err, capsule.ErrCapsuleNotFound):
		writeJSONError(w, "no capsule with that name", http.StatusNotFound)
		return
	case err != nil:
		writeJSONError(w, "failed to load capsule: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Same context-lifetime concern as in install: passing the
	// request ctx would tie the spawned subprocess's lifetime to
	// the HTTP request and kill it on response. Use background().
	if err := s.host.StartOne(context.Background(), *c); err != nil {
		// StartOne is idempotent on already-running capsules — any
		// error here is a real failure (subprocess spawn, MCP
		// handshake). Surface verbatim; the dashboard renders it
		// inline on the row.
		s.logger.Warn("console capsule start failed", "err", err, "name", name)
		writeJSONError(w, "start failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_, _ = s.audit.Append(ctx, audit.Entry{
		Type:    "capsule.started",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: "console"},
		Subject: &audit.Subject{Kind: audit.SubjectCapsule, ID: c.Name},
		Outcome: audit.OutcomeSuccess,
		Data:    map[string]any{"version": c.Version},
	})

	s.logger.Info("console capsule start", "name", c.Name)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"name":    c.Name,
		"running": true,
	})
}

// --- POST /console/capsules/{name}/stop ------------------------------------

func (s *Server) handleCapsuleStop(w http.ResponseWriter, r *http.Request) {
	if s.host == nil {
		writeJSONError(w, "capsule lifecycle is not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, "capsule name required in URL", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	if err := s.host.StopOne(ctx, name); err != nil {
		s.logger.Warn("console capsule stop failed", "err", err, "name", name)
		writeJSONError(w, "stop failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_, _ = s.audit.Append(ctx, audit.Entry{
		Type:    "capsule.stopped",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: "console"},
		Subject: &audit.Subject{Kind: audit.SubjectCapsule, ID: name},
		Outcome: audit.OutcomeSuccess,
	})

	s.logger.Info("console capsule stop", "name", name)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"name":    name,
		"running": false,
	})
}
