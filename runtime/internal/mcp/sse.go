package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// serveSSE handles GET /mcp, returning a Server-Sent Events stream
// of server→client notifications. v0.1 emits a `hello` event on
// connect and a `ping` every 15 seconds — enough for clients to
// detect dropped connections. Subscription payloads
// (resources/updated, tools/list_changed, log notifications)
// arrive in Phase 2; the wire shape is already in place so
// publishing into the stream becomes a one-line addition.
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

	h.deps.Logger.Debug("mcp sse: client connected",
		"remote", r.RemoteAddr,
		"client", clientNameFromCtx(r),
	)
	// SSE headers per WHATWG. The X-Accel-Buffering disable hint is
	// for nginx and similar proxies that buffer text/event-stream by
	// default; harmless when no proxy is in front.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Hello event — gives clients a definitive "the stream is live"
	// signal without waiting for the first heartbeat.
	if err := writeSSEEvent(w, "hello", helloPayload{
		Server:          h.deps.ServerName,
		Version:         h.deps.ServerVersion,
		ProtocolVersion: mcpProtocolVersion,
	}); err != nil {
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
			if err := writeSSEEvent(w, "ping", pingPayload{
				Timestamp: t.UTC().Format(time.RFC3339Nano),
			}); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// helloPayload is the JSON body of the SSE `hello` event. Surface
// the server identity early so clients can pin against a specific
// build version on first byte.
type helloPayload struct {
	Server          string `json:"server"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocolVersion"`
}

// pingPayload is the JSON body of the SSE `ping` heartbeat.
type pingPayload struct {
	Timestamp string `json:"timestamp"`
}

// writeSSEEvent writes one SSE event with the given event name and
// a JSON-serialized payload. SSE's data field forbids unescaped
// newlines; json.Encoder emits one-line JSON by default and we
// post-process to strip its trailing newline.
//
// Callers do not flush — batching is the caller's choice. (In
// practice every Loamss SSE event is followed by a Flush call.)
func writeSSEEvent(w io.Writer, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sse: encoding payload: %w", err)
	}
	// Defensive: if payload contains an explicit \n (e.g., a string
	// field holding a multi-line value), split into multiple `data:`
	// lines per the WHATWG SSE spec.
	body := strings.ReplaceAll(string(data), "\n", "\ndata: ")
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
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
