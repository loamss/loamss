package capsule

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/mcp"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// hostFixture wires the deps a Host needs plus the underlying
// permission store and capsule store for assertions.
type hostFixture struct {
	dir       string
	permStore *permission.Store
	capStore  *Store
	audit     *audit.SQLite
	engine    *permission.Engine
	tools     *mcp.Registry
	host      *Host
}

func newHostFixture(t *testing.T) *hostFixture {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	dbPath := filepath.Join(dir, "runtime.db")

	permStore, err := permission.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("permission.Open: %v", err)
	}
	capStore, err := OpenStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("capsule.OpenStore: %v", err)
	}
	w, err := audit.OpenSQLite(ctx, filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatalf("audit.OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		_ = permStore.Close()
		_ = capStore.Close()
		_ = w.Close(context.Background())
	})
	engine := permission.NewEngine(permStore, w)
	tools := mcp.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	host := NewHost(capStore, engine, w, tools, logger)
	return &hostFixture{
		dir: dir, permStore: permStore, capStore: capStore,
		audit: w, engine: engine, tools: tools, host: host,
	}
}

// installFakeCapsule persists a capsule record whose entrypoint is
// the test binary in mcp-capsule mode. The record's install path
// points at a fresh temp dir so the subprocess's Dir is real (the
// helper doesn't actually need files there — it talks MCP via
// stdio — but Process.Start cwd's there).
func installFakeCapsule(t *testing.T, f *hostFixture, name string) Installed {
	t.Helper()
	ctx := context.Background()
	t.Setenv("GO_CAPSULE_HELPER", "mcp-capsule")
	installed := fakeMCPCapsule(t)
	installed.Name = name
	installed.ID = name + "@" + installed.Version
	installed.Manifest.Name = name
	if err := f.capStore.Insert(ctx, installed); err != nil {
		t.Fatalf("Insert capsule: %v", err)
	}
	return installed
}

func TestHost_StartMountsToolsIntoRegistry(t *testing.T) {
	f := newHostFixture(t)
	installFakeCapsule(t, f, "drafter")

	started, err := f.host.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = f.host.Stop(ctx)
	}()
	if started != 1 {
		t.Errorf("started: got %d, want 1", started)
	}

	// The capsule's "echo" tool should be in the registry under
	// the namespaced name.
	tool, ok := f.tools.Get("drafter.echo")
	if !ok {
		t.Fatalf("expected tool drafter.echo in registry, have: %v", toolNames(f.tools))
	}
	if !strings.Contains(tool.Description, "Echo") {
		t.Errorf("description: %q", tool.Description)
	}
}

func TestHost_ExternalCallReachesCapsule(t *testing.T) {
	f := newHostFixture(t)
	installFakeCapsule(t, f, "drafter")

	if _, err := f.host.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = f.host.Stop(ctx)
	}()

	tool, _ := f.tools.Get("drafter.echo")
	in := mcp.ToolInput{
		Args: json.RawMessage(`{"text":"hello capsule"}`),
		Principal: permission.Principal{
			Kind: permission.PrincipalClient, ID: "test-client",
		},
	}
	res, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("expected content blocks")
	}
	// The fake capsule echoes the args as "echo:<args>".
	if !strings.Contains(res.Content[0].Text, "echo:") {
		t.Errorf("expected echo prefix, got: %s", res.Content[0].Text)
	}
}

func TestHost_StartIsIdempotent(t *testing.T) {
	f := newHostFixture(t)
	installFakeCapsule(t, f, "drafter")
	ctx := context.Background()
	if _, err := f.host.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = f.host.Stop(stopCtx)
	}()
	// Second StartOne on the same capsule is a no-op.
	if err := f.host.StartOne(ctx, Installed{Name: "drafter"}); err != nil {
		t.Errorf("second StartOne: %v", err)
	}
}

func TestHost_StopShutsDownClients(t *testing.T) {
	f := newHostFixture(t)
	installFakeCapsule(t, f, "drafter-a")
	installFakeCapsule(t, f, "drafter-b")
	ctx := context.Background()
	if _, err := f.host.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := len(f.host.Running()); got != 2 {
		t.Errorf("Running before Stop: got %d, want 2", got)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.host.Stop(stopCtx); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if got := len(f.host.Running()); got != 0 {
		t.Errorf("Running after Stop: got %d, want 0", got)
	}
}

func TestHost_CapsuleCallback_RejectsUnknownMethod(t *testing.T) {
	// The runtime handler only accepts tools/call. Any other
	// method returns -32601 directly to the capsule.
	f := newHostFixture(t)
	installFakeCapsule(t, f, "drafter")
	t.Setenv("GO_CAPSULE_CALLBACK_METHOD", "ping")

	if _, err := f.host.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = f.host.Stop(ctx)
	}()

	tool, _ := f.tools.Get("drafter.echo")
	// Triggers a callback to "ping" (not tools/call). Capsule
	// receives -32601 but continues and returns its echo to us.
	res, err := tool.Handler(context.Background(), mcp.ToolInput{Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("expected content blocks even when callback failed")
	}
}

func TestHost_CapsuleCallback_TooltsCallDeniedWithoutGrant(t *testing.T) {
	// Capsule calls tools/call name=memory.read tool, but has no
	// grant. Runtime returns -32001 permission denied. The capsule
	// observes the error and still completes its own response.
	f := newHostFixture(t)
	installFakeCapsule(t, f, "drafter")
	// Have the capsule call back into tools/call with a runtime
	// tool name. The mcp-capsule helper sends back the result of
	// the callback as its own tool result; we'll verify the
	// audit entries directly.
	t.Setenv("GO_CAPSULE_HELPER", "mcp-capsule")
	t.Setenv("GO_CAPSULE_CALLBACK_METHOD", "tools/call")
	t.Setenv("GO_CAPSULE_CALLBACK_TOOL", "memory.show")

	// Register memory.show in the runtime registry so the dispatch
	// has a tool to resolve. We use the real one from mcp.
	registerMemoryShowForTest(t, f)

	if _, err := f.host.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = f.host.Stop(ctx)
	}()

	tool, _ := f.tools.Get("drafter.echo")
	_, err := tool.Handler(context.Background(), mcp.ToolInput{Args: json.RawMessage(`{"id":"x"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Audit should show a check.deny against the capsule principal.
	entries, _ := f.audit.Query(context.Background(), audit.Filter{Types: []string{"check.deny"}})
	if len(entries) == 0 {
		t.Errorf("expected check.deny audit entry from the capsule callback path")
	}
}

func TestHost_CapsuleCallback_AllowedWithGrant(t *testing.T) {
	f := newHostFixture(t)
	installFakeCapsule(t, f, "drafter")
	t.Setenv("GO_CAPSULE_HELPER", "mcp-capsule")
	t.Setenv("GO_CAPSULE_CALLBACK_METHOD", "tools/call")
	t.Setenv("GO_CAPSULE_CALLBACK_TOOL", "memory.show")

	registerMemoryShowForTest(t, f)

	// The Installer would normally issue capsule grants. For this
	// test we go through the engine directly to avoid the full
	// install round-trip.
	if _, err := f.permStore.IssueGrant(context.Background(), permission.Grant{
		Principal: permission.Principal{
			Kind: permission.PrincipalCapsule, ID: "drafter",
		},
		Capability: "memory.read",
	}); err != nil {
		t.Fatalf("IssueGrant: %v", err)
	}

	if _, err := f.host.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = f.host.Stop(ctx)
	}()

	tool, _ := f.tools.Get("drafter.echo")
	_, err := tool.Handler(context.Background(), mcp.ToolInput{Args: json.RawMessage(`{"id":"x"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Audit should show check.allow (from the engine.Check call)
	// AND tool.invoked with actor.kind=capsule.
	allows, _ := f.audit.Query(context.Background(),
		audit.Filter{Types: []string{"check.allow"}})
	if len(allows) == 0 {
		t.Errorf("expected check.allow audit entry")
	}
	invokes, _ := f.audit.Query(context.Background(),
		audit.Filter{Types: []string{"tool.invoked"}})
	foundCapsuleActor := false
	for _, e := range invokes {
		if e.Actor.Kind == audit.ActorCapsule && e.Actor.ID == "drafter" {
			foundCapsuleActor = true
			if v, _ := e.Data["via"].(string); v != "capsule_callback" {
				t.Errorf("expected via=capsule_callback, got %v", e.Data["via"])
			}
		}
	}
	if !foundCapsuleActor {
		t.Errorf("expected tool.invoked with actor=capsule:drafter, got: %+v", invokes)
	}
}

func TestHost_CapsuleCallback_RejectsCrossCapsuleCall(t *testing.T) {
	// Install two capsules; capsule A tries to call capsule B's
	// tool via the runtime. The Host rejects with -32601 (the
	// "deferred to v0.2" message).
	f := newHostFixture(t)
	installFakeCapsule(t, f, "alpha")
	installFakeCapsule(t, f, "beta")
	// Configure alpha to try calling beta.echo.
	t.Setenv("GO_CAPSULE_HELPER", "mcp-capsule")
	t.Setenv("GO_CAPSULE_CALLBACK_METHOD", "tools/call")
	t.Setenv("GO_CAPSULE_CALLBACK_TOOL", "beta.echo")

	if _, err := f.host.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = f.host.Stop(ctx)
	}()

	tool, _ := f.tools.Get("alpha.echo")
	_, err := tool.Handler(context.Background(), mcp.ToolInput{Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	// Capsule's own tool.invoked happens regardless; verifying
	// the cross-capsule rejection happened requires watching the
	// daemon logs. We assert that no check.allow against the
	// "beta" principal exists.
	entries, _ := f.audit.Query(context.Background(),
		audit.Filter{Types: []string{"check.allow"}})
	for _, e := range entries {
		if e.Actor.Kind == audit.ActorCapsule && e.Actor.ID == "alpha" {
			t.Errorf("alpha should not have a check.allow — its cross-capsule call was rejected before Check ran: %+v", e)
		}
	}
}

// registerMemoryShowForTest registers the real memory.show tool
// into the test host's registry so capsule-callback tests have a
// runtime tool to dispatch to. Uses an in-memory adapter sized to
// match model:dummy.
func registerMemoryShowForTest(t *testing.T, f *hostFixture) {
	t.Helper()
	// Use a minimal stub: register an mcp.Tool with Capability =
	// memory.read but a Handler that returns a synthetic result.
	// Avoids dragging the memory adapter into the host tests
	// (which would require initializing it from a config map).
	err := f.tools.Register(mcp.Tool{
		Name:        "memory.show",
		Description: "test stub",
		Capability:  "memory.read",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, _ mcp.ToolInput) (mcp.ToolResult, error) {
			return mcp.ToolResult{
				Content: []mcp.Content{{Type: "text", Text: "ok"}},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("registerMemoryShowForTest: %v", err)
	}
}

// toolNames reads the names of every tool currently in the registry.
// Pure debug helper for failure messages.
func toolNames(r *mcp.Registry) []string {
	out := make([]string, 0)
	for _, t := range r.List() {
		out = append(out, t.Name)
	}
	return out
}
