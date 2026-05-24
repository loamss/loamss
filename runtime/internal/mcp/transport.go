package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
)

// MCP-over-stdio transport. Bidirectional JSON-RPC 2.0 with one
// connection (a pair of pipes: stdin we write to, stdout we read
// from). Used by the capsule host: the runtime spawns a capsule
// subprocess, attaches a Transport to its stdin/stdout, and from
// then on either side can issue requests at any time.
//
// Why bidirectional matters: MCP capsules don't just receive
// tools/call from the runtime — they also call BACK into the
// runtime for memory.query, files.read, model.call. The runtime
// is simultaneously a "client" of the capsule's MCP server AND a
// "server" for the capsule's runtime-side calls. A single
// JSON-RPC multiplexer over the same connection handles both.
//
// Framing: newline-delimited JSON (each message on its own line,
// terminated by '\n'). Matches the convention used by most MCP
// stdio SDKs; simpler than the LSP-style Content-Length headers
// and friendlier to debug with `cat` / `grep`.

// TransportHandler is the callback invoked for incoming requests and
// notifications. method + params are forwarded verbatim; the
// handler returns either a result value (will be JSON-encoded) or
// an error (will be surfaced as a JSON-RPC error response,
// notifications discard both).
//
// Notifications carry no id; the transport calls TransportHandler in a
// goroutine and ignores its return value. Requests block on the
// handler before the transport writes the response.
//
// TransportHandler implementations should respect ctx cancellation — when
// the transport shuts down it cancels every in-flight handler so
// long-running operations release.
type TransportHandler func(ctx context.Context, method string, params json.RawMessage) (result any, err error)

// Transport multiplexes JSON-RPC over an io.Reader + io.Writer
// pair. Safe for concurrent use: Request and Notify can be called
// from any goroutine.
//
// Lifecycle: NewTransport creates a Transport in the not-yet-
// started state. Start launches the read loop and returns
// immediately. Close shuts down the read loop, drains any
// pending requests with ErrTransportClosed, and waits for the
// read goroutine to exit.
type Transport struct {
	r       io.Reader
	w       io.Writer
	handler TransportHandler
	logger  *slog.Logger

	// writeMu serializes writes to w. JSON-RPC messages can
	// interleave on the wire, but the underlying io.Writer
	// (typically a pipe to a subprocess) demands atomic line
	// writes — partial messages from concurrent writers would
	// corrupt the framing.
	writeMu sync.Mutex

	// nextID monotonically generates outbound request ids.
	// Atomic so Request can be called from any goroutine without
	// taking writeMu just to generate the id.
	nextID atomic.Int64

	// pending maps outbound request id → channel the caller is
	// waiting on. Guarded by pendingMu.
	pendingMu sync.Mutex
	pending   map[string]chan *Response

	// startCh closes when Start has been called. Close before
	// Start is a no-op rather than an error.
	startOnce sync.Once
	closeOnce sync.Once

	// stopOnce gates the close(stopCh) call so both Close (local
	// shutdown) and the read loop's peer-EOF path can safely
	// attempt to signal stopCh without risking a double-close
	// panic.
	stopOnce sync.Once

	// doneCh closes when the read loop has exited. Close blocks
	// on this.
	doneCh chan struct{}

	// stopCh is closed when the transport should stop — either by
	// explicit Close or by the read loop detecting peer EOF.
	// Signals in-flight handler goroutines to abandon work and
	// signals waiting Request callers to fail with
	// ErrTransportClosed.
	stopCh chan struct{}
}

// NewTransport constructs a Transport bound to the given reader,
// writer, and handler. handler may be nil — incoming requests are
// then rejected with -32601 (method not found). logger is used for
// framing-level diagnostics; pass slog.Default() if you don't have
// a more specific one.
func NewTransport(r io.Reader, w io.Writer, handler TransportHandler, logger *slog.Logger) *Transport {
	if logger == nil {
		logger = slog.Default()
	}
	return &Transport{
		r:       r,
		w:       w,
		handler: handler,
		logger:  logger,
		pending: make(map[string]chan *Response),
		doneCh:  make(chan struct{}),
		stopCh:  make(chan struct{}),
	}
}

// ErrTransportClosed is returned by Request when the transport
// closes before the response arrives.
var ErrTransportClosed = errors.New("mcp: transport closed")

// Done returns a channel that closes when the transport's read
// loop has exited — either because Close was called or the
// underlying reader returned EOF (peer closed its end). Useful
// for goroutines that want to react to a peer disconnecting
// without polling.
func (t *Transport) Done() <-chan struct{} { return t.doneCh }

// Start launches the read loop in a goroutine and returns. Safe to
// call multiple times — second and subsequent calls are no-ops.
// The read loop runs until Close is called or the underlying
// reader returns an error (typically EOF on subprocess exit).
func (t *Transport) Start() {
	t.startOnce.Do(func() {
		go t.readLoop()
	})
}

// Close stops the read loop and drains any pending requests with
// ErrTransportClosed. Blocks until the read goroutine exits.
// Idempotent.
//
// The stopCh-close races with the read loop's own stopCh-close on
// peer EOF; whichever runs first wins via the select guard. Once
// doneCh closes, draining has already happened in readLoop's
// deferred cleanup, so Close just waits for the goroutine.
func (t *Transport) Close() error {
	t.closeOnce.Do(func() {
		t.signalStop()
		// Closing the underlying reader (if it supports it) makes
		// the read loop's scanner exit immediately rather than
		// waiting on stdin. Without this, the read loop would block
		// until the subprocess closes its stdout, which may never
		// happen for a misbehaving capsule.
		if c, ok := t.r.(io.Closer); ok {
			_ = c.Close()
		}
	})
	<-t.doneCh
	return nil
}

// signalStop closes stopCh exactly once across all callers
// (Close, readLoop on peer EOF). Subsequent calls are no-ops.
func (t *Transport) signalStop() {
	t.stopOnce.Do(func() { close(t.stopCh) })
}

// readLoop reads messages from r line-by-line, decodes each as a
// JSON-RPC message, and dispatches.
//
// Decoding strategy: each line is one JSON object. We peek at the
// fields to discriminate request vs response:
//   - has "method" + "id" → request (call handler, write response)
//   - has "method", no "id" → notification (call handler, discard)
//   - has "result" or "error", has "id" → response (route to
//     pending channel)
//   - anything else → malformed; log and skip.
//
// When the read loop exits (peer EOF, error, or local Close), it
// drains any pending Requests with ErrTransportClosed and signals
// done. Without that drain, a Request waiting for a response from
// a peer that just died would block until its ctx deadline.
func (t *Transport) readLoop() {
	defer func() {
		// Mark the transport as stopped so any Request issued
		// after the read loop exits returns immediately, and
		// drain currently-pending Requests with ErrTransportClosed.
		// signalStop is sync.Once-guarded so it's safe even if
		// Close already ran.
		t.signalStop()
		t.drainPending()
		close(t.doneCh)
	}()

	scanner := bufio.NewScanner(t.r)
	// MCP messages can be substantial — tool results may include
	// embedded JSON Schema fragments, content arrays, etc. 1 MiB
	// is a comfortable ceiling for stdio framing.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-t.stopCh:
			return
		default:
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Copy the bytes because scanner.Bytes() is only valid
		// until the next Scan call, and we hand off to goroutines.
		buf := make([]byte, len(line))
		copy(buf, line)
		t.dispatch(buf)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		t.logger.Warn("mcp transport: read error", "err", err)
	}
}

// dispatch handles one decoded line. Discriminates message kind
// and routes to the appropriate path.
func (t *Transport) dispatch(line []byte) {
	// First pass: try to decode as a generic JSON-RPC envelope.
	// We peek at the field set to discriminate request vs response.
	var probe struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   *RPCError       `json:"error"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		t.logger.Warn("mcp transport: malformed JSON", "line", string(line), "err", err)
		return
	}

	hasID := len(probe.ID) > 0 && string(probe.ID) != "null"
	hasMethod := probe.Method != ""
	hasResultOrError := len(probe.Result) > 0 || probe.Error != nil

	switch {
	case hasResultOrError && hasID:
		// Inbound response — route to pending.
		t.routeResponse(probe.ID, &Response{
			JSONRPC: probe.JSONRPC,
			ID:      probe.ID,
			Result:  rawToAny(probe.Result),
			Error:   probe.Error,
		})
	case hasMethod && hasID:
		// Inbound request — dispatch to handler, write response.
		go t.handleRequest(probe.ID, probe.Method, probe.Params)
	case hasMethod && !hasID:
		// Inbound notification — dispatch to handler, ignore result.
		go t.handleNotification(probe.Method, probe.Params)
	default:
		t.logger.Warn("mcp transport: unrecognized message shape",
			"line", string(line))
	}
}

// routeResponse delivers a response to the goroutine waiting on
// the request with the matching id. If no pending entry matches
// (caller timed out, ids collided, peer is misbehaving), the
// response is dropped with a debug log line.
func (t *Transport) routeResponse(id json.RawMessage, resp *Response) {
	key := string(id)
	t.pendingMu.Lock()
	ch, ok := t.pending[key]
	if ok {
		delete(t.pending, key)
	}
	t.pendingMu.Unlock()
	if !ok {
		t.logger.Debug("mcp transport: response for unknown id", "id", key)
		return
	}
	// Non-blocking send: the channel is buffered (size 1), so this
	// always succeeds. We protect against double-routing via the
	// delete above.
	ch <- resp
}

// handleRequest runs the user-supplied handler for an incoming
// request and writes the response. TransportHandler nil → -32601.
func (t *Transport) handleRequest(id json.RawMessage, method string, params json.RawMessage) {
	ctx, cancel := t.handlerContext()
	defer cancel()

	if t.handler == nil {
		_ = t.writeMessage(methodNotFoundResponse(id, method))
		return
	}
	result, err := t.handler(ctx, method, params)
	if err != nil {
		// A handler returning an error becomes an internal_error
		// response by default; if the handler returned an *RPCError
		// directly we preserve its code.
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) {
			_ = t.writeMessage(Response{
				JSONRPC: jsonRPCVersion,
				ID:      id,
				Error:   rpcErr,
			})
			return
		}
		_ = t.writeMessage(internalErrorResponse(id, err.Error()))
		return
	}
	_ = t.writeMessage(successResponse(id, result))
}

// handleNotification runs the handler for a notification (no id,
// no response). Errors are logged at debug; per JSON-RPC 2.0 the
// peer never sees them.
func (t *Transport) handleNotification(method string, params json.RawMessage) {
	if t.handler == nil {
		return
	}
	ctx, cancel := t.handlerContext()
	defer cancel()
	if _, err := t.handler(ctx, method, params); err != nil {
		t.logger.Debug("mcp transport: notification handler error",
			"method", method, "err", err)
	}
}

// handlerContext returns a context that cancels when the
// transport stops (Close was called). TransportHandlers should respect it
// so we can shut down cleanly.
func (t *Transport) handlerContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-t.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// Request sends a JSON-RPC request, blocks until the response
// arrives (or ctx is cancelled, or the transport closes), and
// returns the decoded Response. The caller distinguishes success
// from error by inspecting Response.Error.
func (t *Transport) Request(ctx context.Context, method string, params any) (*Response, error) {
	id := t.nextID.Add(1)
	idJSON, _ := json.Marshal(id)
	idStr := string(idJSON)

	ch := make(chan *Response, 1)
	t.pendingMu.Lock()
	t.pending[idStr] = ch
	t.pendingMu.Unlock()
	defer func() {
		t.pendingMu.Lock()
		delete(t.pending, idStr)
		t.pendingMu.Unlock()
	}()

	paramsJSON, err := encodeParams(params)
	if err != nil {
		return nil, fmt.Errorf("mcp transport: encoding params: %w", err)
	}
	req := Request{
		JSONRPC: jsonRPCVersion,
		ID:      idJSON,
		Method:  method,
		Params:  paramsJSON,
	}
	if err := t.writeMessage(req); err != nil {
		return nil, fmt.Errorf("mcp transport: writing request: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.stopCh:
		return nil, ErrTransportClosed
	}
}

// Notify sends a JSON-RPC notification (request without id, no
// response expected). Returns the write error if the underlying
// pipe is broken; nil otherwise.
func (t *Transport) Notify(ctx context.Context, method string, params any) error {
	_ = ctx // notifications don't wait, but accepting ctx keeps the API uniform
	paramsJSON, err := encodeParams(params)
	if err != nil {
		return fmt.Errorf("mcp transport: encoding params: %w", err)
	}
	return t.writeMessage(Request{
		JSONRPC: jsonRPCVersion,
		Method:  method,
		Params:  paramsJSON,
	})
}

// writeMessage encodes v as one newline-terminated JSON object and
// writes it to the underlying writer under writeMu. Concurrent
// callers serialize here.
func (t *Transport) writeMessage(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.w.Write(data); err != nil {
		return err
	}
	if _, err := t.w.Write([]byte{'\n'}); err != nil {
		return err
	}
	return nil
}

// drainPending fails every in-flight Request with
// ErrTransportClosed. Called by Close after the read loop has
// exited; the pending map is no longer being written to.
func (t *Transport) drainPending() {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()
	for id, ch := range t.pending {
		ch <- &Response{
			JSONRPC: jsonRPCVersion,
			ID:      json.RawMessage(id),
			Error: &RPCError{
				Code:    codeInternalError,
				Message: ErrTransportClosed.Error(),
			},
		}
		delete(t.pending, id)
	}
}

// encodeParams marshals a params value into json.RawMessage.
// Nil produces nil (omitted from the wire); already-encoded
// json.RawMessage is passed through.
func encodeParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(params)
}

// rawToAny converts a json.RawMessage result back into a
// generic Go value (map[string]any, []any, primitives). Callers
// who need a typed result re-encode and Unmarshal.
func rawToAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

// idAsString returns a stable string form of a JSON id (number or
// string). Used by tests asserting against specific outbound ids.
//
//nolint:unused // exported helper for future test scaffolding
func idAsString(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	s := string(id)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return strconv.FormatInt(n, 10)
	}
	// Strip surrounding quotes for string ids.
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
