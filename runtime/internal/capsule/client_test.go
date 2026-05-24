package capsule

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMCPCapsule builds an Installed record whose entrypoint
// re-execs the test binary in mcp-capsule mode (defined in
// process_test.go). The helper speaks real MCP-over-stdio.
func fakeMCPCapsule(t *testing.T) Installed {
	t.Helper()
	t.Setenv("GO_CAPSULE_HELPER", "mcp-capsule")
	return fakeCapsule(t)
}

func TestClient_StartHandshake(t *testing.T) {
	installed := fakeMCPCapsule(t)
	c := NewClient(installed, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Stop(ctx)
	}()

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// initialize result captured.
	info := c.Info()
	if info.Name != "test-capsule" || info.Version != "0.0.1" {
		t.Errorf("info: %+v", info)
	}
	if info.ProtocolVersion != "2025-03-26" {
		t.Errorf("protocol version: %q", info.ProtocolVersion)
	}

	// tools/list result captured.
	tools := c.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("tool name: %q", tools[0].Name)
	}
	if len(tools[0].InputSchema) == 0 {
		t.Error("input_schema should be non-empty")
	}
}

func TestClient_CallTool(t *testing.T) {
	c := NewClient(fakeMCPCapsule(t), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Stop(ctx)
	}()
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	args := json.RawMessage(`{"text":"hi"}`)
	resp, err := c.CallTool(context.Background(), "echo", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	// The helper returns {content: [{type: text, text: "echo:<args>"}]}.
	rb, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(rb), `"echo:`) {
		t.Errorf("expected echo-prefixed result, got: %s", rb)
	}
}

func TestClient_CapsuleCallback(t *testing.T) {
	// Configure the helper to call BACK to the runtime during
	// tools/call. The runtime-side handler we register here
	// records the inbound method and returns a structured result.
	// This exercises the bidirectional flow capsule callbacks need.
	var callbackReceived atomic.Bool
	rt := func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "memory.query" {
			callbackReceived.Store(true)
		}
		return map[string]any{"acknowledged": true}, nil
	}
	installed := fakeMCPCapsule(t)
	t.Setenv("GO_CAPSULE_CALLBACK_METHOD", "memory.query")

	c := NewClient(installed, slog.New(slog.NewTextHandler(io.Discard, nil)), rt)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = c.Stop(ctx)
	}()
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Invoke the tool. The helper will call back to the runtime
	// (memory.query) before responding.
	_, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !callbackReceived.Load() {
		t.Error("expected capsule → runtime callback for memory.query")
	}
}

func TestClient_CallToolBeforeStart(t *testing.T) {
	c := NewClient(fakeMCPCapsule(t), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	_, err := c.CallTool(context.Background(), "anything", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "not started") {
		t.Errorf("expected 'not started' error, got: %v", err)
	}
}

func TestClient_StopClean(t *testing.T) {
	c := NewClient(fakeMCPCapsule(t), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}

	// Second Stop is idempotent.
	if err := c.Stop(context.Background()); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestClient_ExitedChClosesOnStop(t *testing.T) {
	c := NewClient(fakeMCPCapsule(t), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.Stop(ctx)

	select {
	case <-c.ExitedCh():
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("ExitedCh should close after Stop")
	}
}
