package capsule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/loamss/loamss/runtime/internal/mcp"
)

// Client is the runtime-side handle to a running capsule. It owns
// the subprocess (Process) and the JSON-RPC multiplexer (mcp.Transport)
// and exposes a small API the rest of the runtime needs:
//
//   - Start: spawn the subprocess, start the transport, run the
//     initialize + tools/list handshake, cache the advertised tool set.
//   - Tools: return the capsule's advertised tools (with input schemas).
//   - CallTool: forward a tool invocation to the capsule, return the
//     result. The runtime's MCP tool dispatcher calls this when an
//     external client invokes one of the capsule's tools.
//   - Stop: graceful shutdown.
//
// Callbacks (capsule → runtime): the caller of NewClient supplies a
// RuntimeHandler that the transport invokes whenever the capsule calls
// a runtime method (memory.query, files.read, ...). The runtime's
// Engine.Check + audit machinery sits inside that handler.
type Client struct {
	capsule Installed
	logger  *slog.Logger
	process *Process
	tr      *mcp.Transport

	// onCapsuleCall is the runtime-side handler invoked when the
	// capsule sends a request to us. The transport calls this for
	// every inbound request; the caller wires it to the runtime's
	// permission.Engine + adapter dispatch.
	onCapsuleCall RuntimeHandler

	// tools is the cached capsule tool advertisement, populated by
	// the initialize/tools/list handshake. Read-only after Start
	// returns; safe for concurrent readers without a lock.
	tools []ToolAdvertisement

	// info captures the capsule's serverInfo from initialize.
	// Stored mainly for logs and diagnostics.
	info ServerInfo

	startOnce sync.Once
	startErr  error
	closeOnce sync.Once
}

// ServerInfo mirrors the MCP serverInfo + protocol version the
// capsule reports during initialize.
type ServerInfo struct {
	Name            string
	Version         string
	ProtocolVersion string
}

// ToolAdvertisement is one tool the capsule advertises via tools/list.
// Mirrors the wire shape so we can pass it through to the runtime's
// mcp.Registry without re-shaping. The runtime stamps the capsule's
// principal onto the registered tool so permission checks resolve
// correctly when an external client invokes it.
type ToolAdvertisement struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// RuntimeHandler is the callback the transport invokes for inbound
// capsule→runtime requests. Signature mirrors mcp.TransportHandler
// for direct passthrough. The implementation is wired by the caller
// (typically capsule.Host) and routes the call through the
// permission engine + relevant adapter (memory, files, model, ...).
type RuntimeHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// NewClient constructs a Client. Does NOT spawn the subprocess —
// call Start. The runtimeHandler may be nil for tests that only
// want to exercise the runtime → capsule direction; in production
// the runtime always supplies a handler so capsule callbacks work.
func NewClient(installed Installed, logger *slog.Logger, runtimeHandler RuntimeHandler) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		capsule:       installed,
		logger:        logger,
		onCapsuleCall: runtimeHandler,
	}
}

// Capsule returns the installed record this client manages.
func (c *Client) Capsule() Installed { return c.capsule }

// Tools returns the capsule's advertised tool set. Valid only after
// Start has completed successfully; returns nil otherwise. Callers
// iterate without locking — the slice is immutable after Start.
func (c *Client) Tools() []ToolAdvertisement { return c.tools }

// Info returns the server identity the capsule reported during
// initialize. Empty until Start completes.
func (c *Client) Info() ServerInfo { return c.info }

// Start spawns the subprocess, attaches the transport, and runs the
// initialize + tools/list handshake. Returns the first error
// encountered; on failure the subprocess is stopped before returning.
//
// Idempotent: second call returns the cached startErr (typically
// nil on success). Concurrent Start callers serialize on startOnce.
func (c *Client) Start(ctx context.Context) error {
	c.startOnce.Do(func() {
		c.startErr = c.startLocked(ctx)
		if c.startErr != nil {
			// Best-effort cleanup; never block the caller.
			if c.process != nil {
				stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = c.process.Stop(stopCtx)
				cancel()
			}
		}
	})
	return c.startErr
}

func (c *Client) startLocked(ctx context.Context) error {
	// --- spawn subprocess --------------------------------------
	c.process = NewProcess(c.capsule, c.logger)
	if err := c.process.Start(ctx); err != nil {
		return fmt.Errorf("capsule: start process for %s: %w", c.capsule.Name, err)
	}

	// --- attach transport --------------------------------------
	// The transport reads from the subprocess's stdout and writes
	// to its stdin. The runtime is the "client" of the capsule's
	// MCP server, but the same transport also serves inbound
	// requests from the capsule (callbacks).
	c.tr = mcp.NewTransport(c.process.Stdout(), c.process.Stdin(),
		c.wrapHandler(), c.logger)
	c.tr.Start()

	// --- initialize handshake ----------------------------------
	if err := c.doInitialize(ctx); err != nil {
		return fmt.Errorf("capsule %s initialize: %w", c.capsule.Name, err)
	}

	// --- tools/list --------------------------------------------
	if err := c.doToolsList(ctx); err != nil {
		return fmt.Errorf("capsule %s tools/list: %w", c.capsule.Name, err)
	}

	c.logger.Info("capsule: client ready",
		"name", c.capsule.Name,
		"version", c.capsule.Version,
		"tools", len(c.tools),
	)
	return nil
}

// wrapHandler returns a TransportHandler that delegates to the
// caller-supplied RuntimeHandler. If the caller passed nil, the
// transport's default method-not-found behavior takes effect
// (we return a typed RPCError with codeMethodNotFound).
func (c *Client) wrapHandler() mcp.TransportHandler {
	if c.onCapsuleCall == nil {
		return nil
	}
	h := c.onCapsuleCall
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return h(ctx, method, params)
	}
}

// doInitialize sends the MCP initialize request and decodes the
// reply into c.info. Bounded by a 10-second timeout — a capsule
// that doesn't respond within that window is broken and we want
// Start to fail fast.
func (c *Client) doInitialize(ctx context.Context) error {
	initCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := c.tr.Request(initCtx, "initialize", map[string]any{
		"protocolVersion": mcpClientProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "loamss-runtime",
			"version": "v0.1",
		},
	})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("rpc %d: %s", resp.Error.Code, resp.Error.Message)
	}

	// Decode the result. resp.Result is map[string]any post-decode;
	// re-encode and unmarshal into a typed shape.
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("re-encoding initialize result: %w", err)
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decoding initialize result: %w", err)
	}
	c.info = ServerInfo{
		Name:            result.ServerInfo.Name,
		Version:         result.ServerInfo.Version,
		ProtocolVersion: result.ProtocolVersion,
	}
	return nil
}

// doToolsList sends tools/list and caches the advertised tool set.
// A capsule with zero tools is unusual but valid; we accept the
// empty list.
func (c *Client) doToolsList(ctx context.Context) error {
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := c.tr.Request(listCtx, "tools/list", nil)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("rpc %d: %s", resp.Error.Code, resp.Error.Message)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("re-encoding tools/list result: %w", err)
	}
	var result struct {
		Tools []ToolAdvertisement `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decoding tools/list result: %w", err)
	}
	c.tools = result.Tools
	return nil
}

// CallTool forwards a tool invocation to the capsule. Returns the
// MCP-shaped result on success or an *mcp.RPCError on failure.
// The caller is the runtime's tool dispatcher; permission checks
// happen before CallTool is invoked, so we trust the call here.
//
// The dispatcher provides args as already-decoded JSON
// (json.RawMessage); we forward verbatim.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.Response, error) {
	if c.tr == nil {
		return nil, errors.New("capsule: client not started")
	}
	return c.tr.Request(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
}

// Stop gracefully shuts down the client. Closes the transport
// (drains pending requests with ErrTransportClosed, cancels in-
// flight handlers) and then the subprocess. Bounded by ctx; if
// the deadline elapses, the subprocess is SIGKILLed. Idempotent.
func (c *Client) Stop(ctx context.Context) error {
	var err error
	c.closeOnce.Do(func() {
		if c.tr != nil {
			_ = c.tr.Close()
		}
		if c.process != nil {
			err = c.process.Stop(ctx)
		}
	})
	return err
}

// ExitedCh returns a channel that closes when the subprocess
// exits — either gracefully via Stop or abruptly (crash, OOM,
// external SIGKILL). The host supervisor selects on this to
// react to crashes.
func (c *Client) ExitedCh() <-chan struct{} {
	if c.process == nil {
		// Closed channel — caller's select fires immediately,
		// which is the right thing for "client never started"
		// scenarios.
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return c.process.ExitedCh()
}

// mcpClientProtocolVersion is the MCP protocol version this client
// announces in initialize. Currently a string mirroring the
// runtime-side server version; bumped when we adopt a newer
// upstream MCP spec.
const mcpClientProtocolVersion = "2025-03-26"
