package mcp

import (
	"fmt"
	"net/http"
	"time"
)

// serveSSE handles GET /mcp, returning a Server-Sent Events stream
// of server→client notifications. v0.1 emits only periodic
// heartbeats so clients can detect connection loss; subscription
// payloads (resources/updated, tools/list_changed, log
// notifications) arrive in Phase 2.
//
// SSE was chosen over WebSocket for symmetry with the upstream MCP
// streamable-HTTP transport: clients POST requests and GET an
// SSE stream from the same endpoint. WebSocket would require a
// distinct framing layer.
func (h *Handler) serveSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by this responder", http.StatusInternalServerError)
		return
	}

	// SSE headers per WHATWG. The X-Accel-Buffering disable hint is
	// for nginx and similar proxies that buffer text/event-stream by
	// default; harmless when no proxy is in front.
	h.deps.Logger.Debug("mcp sse: client connected",
		"remote", r.RemoteAddr,
		"client", clientNameFromCtx(r),
	)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Send a hello so clients know the stream is live without
	// waiting for the first heartbeat interval.
	if err := writeSSEEvent(w, "hello", `{"server":"loamss"}`); err != nil {
		return
	}
	flusher.Flush()

	// Heartbeat at 15s. Long enough that we don't waste bandwidth;
	// short enough that load balancers with idle-connection timeouts
	// (typically 60s) keep the stream open.
	const heartbeatInterval = 15 * time.Second
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			h.deps.Logger.Debug("mcp sse: client disconnected",
				"remote", r.RemoteAddr,
				"err", r.Context().Err(),
			)
			return
		case t := <-ticker.C:
			payload := fmt.Sprintf(`{"timestamp":%q}`, t.UTC().Format(time.RFC3339Nano))
			if err := writeSSEEvent(w, "ping", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSEEvent writes a single SSE event with the given name and
// payload. Event payloads must not contain raw newlines; callers
// emit JSON which is single-line by default. We do not call
// Flusher.Flush here because the caller may want to batch.
func writeSSEEvent(w http.ResponseWriter, event, data string) error {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	return nil
}

// clientNameFromCtx returns the authenticated client's name for log
// breadcrumbs, or empty string if unauthenticated (which shouldn't
// happen for /mcp since auth middleware runs first).
func clientNameFromCtx(r *http.Request) string {
	c := ClientFromContext(r.Context())
	if c == nil {
		return ""
	}
	return c.Name
}
