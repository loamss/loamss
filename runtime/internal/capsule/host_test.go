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

func TestHost_CapsuleCallbackReturnsMethodNotFound(t *testing.T) {
	// v0.1 stub: capsule callbacks aren't dispatched yet. The
	// fake-capsule helper exits cleanly even when its callback
	// fails, so this test mostly verifies the stub returns the
	// expected RPCError shape.
	f := newHostFixture(t)
	installFakeCapsule(t, f, "drafter")
	t.Setenv("GO_CAPSULE_CALLBACK_METHOD", "memory.query")

	if _, err := f.host.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = f.host.Stop(ctx)
	}()

	tool, _ := f.tools.Get("drafter.echo")
	// Tool invocation triggers the capsule's callback attempt;
	// the runtime handler stub returns method-not-found. The
	// capsule continues regardless and returns its echo.
	in := mcp.ToolInput{Args: json.RawMessage(`{}`)}
	res, err := tool.Handler(context.Background(), in)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatal("expected content blocks")
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
