package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// `/console/init` accepts the first-run wizard's collected
// configuration. The endpoint is intentionally narrow:
//
//   - POST only
//   - body: a JSON object describing storage / memory / models /
//     source_intents
//   - response: 200 with the echoed payload and a small "received_at"
//     stamp on success, 4xx with a JSON-shaped error otherwise
//
// v0.1 ships as a STUB. The endpoint exists so the console wizard
// has a real target to POST to; the runtime simply echoes the
// payload back and writes nothing. Production behavior (writing
// the config file, restarting subsystems, creating a console-local
// client) lands in a follow-up commit when the config-rewrite
// machinery is in place.
//
// Auth: none. The runtime defaults to binding 127.0.0.1 only, so
// this endpoint is unreachable from off-host. The wizard runs before
// any client is paired — there's no token to use anyway.

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
	OK         bool                `json:"ok"`
	ReceivedAt string              `json:"received_at"`
	Echo       consoleInitRequest  `json:"echo"`
	Note       string              `json:"note,omitempty"`
	Capability consoleInitCapacity `json:"capability"`
}

// consoleInitCapacity tells the console what /console/init can
// actually do in this build. v0.1 returns implemented:false for
// every field — the wizard renders a "what we would write" preview
// instead of claiming success. Later builds flip these to true as
// the corresponding logic lands.
type consoleInitCapacity struct {
	WritesConfigFile      bool `json:"writes_config_file"`
	RestartsRuntime       bool `json:"restarts_runtime"`
	CreatesPairedConsole  bool `json:"creates_paired_console"`
	AddsConfiguredSources bool `json:"adds_configured_sources"`
}

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

	s.logger.Info("console init received",
		"storage", req.Storage.Adapter,
		"memory", req.Memory.Adapter,
		"models", len(req.Models),
		"source_intents", len(req.SourceIntents),
	)

	resp := consoleInitResponse{
		OK:         true,
		ReceivedAt: nowRFC3339(),
		Echo:       req,
		Note: "v0.1 stub: the runtime received this config but did " +
			"not write it. A follow-up commit ships the config writer.",
		Capability: consoleInitCapacity{
			WritesConfigFile:      false,
			RestartsRuntime:       false,
			CreatesPairedConsole:  false,
			AddsConfiguredSources: false,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// nowRFC3339 is a tiny helper kept here rather than reaching for
// time.Now().UTC().Format(time.RFC3339Nano) inline — makes the
// handler easier to read and mockable in tests if we ever need to.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
