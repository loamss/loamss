package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite" // registers memory:sqlite
	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// toolFixture is testFixture extended with a memory adapter and the
// three runtime tools registered.
type toolFixture struct {
	*testFixture
	mem memory.Adapter
}

func newToolFixture(t *testing.T) *toolFixture {
	t.Helper()
	f := newTestHandler(t)
	mem, err := memory.New("memory:sqlite")
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := mem.Init(context.Background(), map[string]any{
		"path": filepath.Join(t.TempDir(), "memory.db"),
	}); err != nil {
		t.Fatalf("mem.Init: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close(context.Background()) })
	for _, tool := range []Tool{
		NewClientInfoTool(),
		NewAuditReadTool(f.audit),
		NewMemoryShowTool(mem),
	} {
		if err := f.h.deps.Tools.Register(tool); err != nil {
			t.Fatalf("Register %s: %v", tool.Name, err)
		}
	}
	return &toolFixture{testFixture: f, mem: mem}
}

func TestToolsList_ReturnsRegisteredTools(t *testing.T) {
	f := newToolFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var lr listToolsResult
	if err := json.Unmarshal(rb, &lr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	names := make(map[string]bool)
	for _, t := range lr.Tools {
		names[t.Name] = true
	}
	for _, want := range []string{"client.info", "audit.read", "memory.show"} {
		if !names[want] {
			t.Errorf("missing tool %q in list", want)
		}
	}
	for _, lt := range lr.Tools {
		if len(lt.InputSchema) == 0 {
			t.Errorf("tool %q has no inputSchema", lt.Name)
		}
	}
}

func TestToolsCall_UnknownToolErrors(t *testing.T) {
	f := newToolFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "no.such"},
	})
	if resp.Error == nil || resp.Error.Code != codeUnknownTool {
		t.Errorf("expected codeUnknownTool, got %+v", resp.Error)
	}
}

func TestToolsCall_MissingName(t *testing.T) {
	f := newToolFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{},
	})
	if resp.Error == nil || resp.Error.Code != codeInvalidParams {
		t.Errorf("expected codeInvalidParams, got %+v", resp.Error)
	}
}

func TestClientInfo_HappyPath(t *testing.T) {
	f := newToolFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "client.info", "arguments": map[string]any{}},
	})
	if resp.Error != nil {
		t.Fatalf("client.info error: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var cr callToolResult
	_ = json.Unmarshal(rb, &cr)
	if len(cr.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(cr.Content))
	}
	if !strings.Contains(cr.Content[0].Text, f.client.ID) {
		t.Errorf("content missing client id, got: %s", cr.Content[0].Text)
	}
	if !strings.Contains(cr.Content[0].Text, f.client.Name) {
		t.Errorf("content missing client name, got: %s", cr.Content[0].Text)
	}
	if strings.Contains(cr.Content[0].Text, "credential_hash") {
		t.Errorf("credential_hash leaked: %s", cr.Content[0].Text)
	}
}

func TestAuditRead_DeniedWithoutGrant(t *testing.T) {
	f := newToolFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{"name": "audit.read", "arguments": map[string]any{"limit": 5}},
	})
	if resp.Error == nil || resp.Error.Code != codePermissionDenied {
		t.Errorf("expected codePermissionDenied, got %+v", resp.Error)
	}
}

func TestAuditRead_AllowedWithGrant(t *testing.T) {
	f := newToolFixture(t)
	ctx := context.Background()
	if _, err := f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "audit.read",
	}); err != nil {
		t.Fatalf("IssueGrant: %v", err)
	}
	if _, err := f.audit.Append(ctx, audit.Entry{
		Type:    "grant.create",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: "x"},
		Outcome: audit.OutcomeSuccess,
	}); err != nil {
		t.Fatalf("Append seed: %v", err)
	}

	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "tools/call",
		"params": map[string]any{"name": "audit.read", "arguments": map[string]any{"limit": 50}},
	})
	if resp.Error != nil {
		t.Fatalf("audit.read error: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var cr callToolResult
	_ = json.Unmarshal(rb, &cr)
	if !strings.Contains(cr.Content[0].Text, `"count":`) {
		t.Errorf("expected count field, got: %s", cr.Content[0].Text)
	}
	if !strings.Contains(cr.Content[0].Text, "grant.create") {
		t.Errorf("expected seeded entry in result, got: %s", cr.Content[0].Text)
	}
}

func TestAuditRead_RejectsBadTimestamp(t *testing.T) {
	f := newToolFixture(t)
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "audit.read",
	})
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{
			"name":      "audit.read",
			"arguments": map[string]any{"since": "yesterday"},
		},
	})
	if resp.Error == nil || resp.Error.Code != codeBackendError {
		t.Errorf("expected codeBackendError, got %+v", resp.Error)
	}
}

func TestMemoryShow_DeniedWithoutGrant(t *testing.T) {
	f := newToolFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 8, "method": "tools/call",
		"params": map[string]any{"name": "memory.show", "arguments": map[string]any{"id": "anything"}},
	})
	if resp.Error == nil || resp.Error.Code != codePermissionDenied {
		t.Errorf("expected codePermissionDenied, got %+v", resp.Error)
	}
}

func TestMemoryShow_NotFoundReturnsIsError(t *testing.T) {
	f := newToolFixture(t)
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "memory.read",
	})
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "tools/call",
		"params": map[string]any{"name": "memory.show", "arguments": map[string]any{"id": "cli-no-such"}},
	})
	if resp.Error != nil {
		t.Fatalf("not-found should return ToolResult{IsError:true}, not RPC error: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var cr callToolResult
	_ = json.Unmarshal(rb, &cr)
	if !cr.IsError {
		t.Errorf("expected isError=true on not-found")
	}
}

func TestMemoryShow_HappyPath(t *testing.T) {
	f := newToolFixture(t)
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "memory.read",
	})
	if err := f.mem.Upsert(ctx, "mem-001", []float32{0.1, 0.2, 0.3, 0.4},
		map[string]any{"type": "person", "name": "Sarah"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 10, "method": "tools/call",
		"params": map[string]any{"name": "memory.show", "arguments": map[string]any{"id": "mem-001"}},
	})
	if resp.Error != nil {
		t.Fatalf("memory.show: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var cr callToolResult
	_ = json.Unmarshal(rb, &cr)
	if !strings.Contains(cr.Content[0].Text, "mem-001") || !strings.Contains(cr.Content[0].Text, "Sarah") {
		t.Errorf("expected entry data in result, got: %s", cr.Content[0].Text)
	}
	if strings.Contains(cr.Content[0].Text, "0.1") {
		t.Errorf("vector should not be inlined in result, got: %s", cr.Content[0].Text)
	}
	if !strings.Contains(cr.Content[0].Text, `"vector_size": 4`) {
		t.Errorf("expected vector_size: 4, got: %s", cr.Content[0].Text)
	}
}

func TestApprovalRequired_FlowsThrough(t *testing.T) {
	f := newToolFixture(t)
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:            permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability:           "audit.read",
		RequiresUserApproval: true,
	})
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 11, "method": "tools/call",
		"params": map[string]any{"name": "audit.read", "arguments": map[string]any{}},
	})
	if resp.Error == nil || resp.Error.Code != codeApprovalRequired {
		t.Fatalf("expected codeApprovalRequired, got %+v", resp.Error)
	}
	data, _ := resp.Error.Data.(map[string]any)
	if data == nil || data["approval_id"] == nil {
		t.Errorf("approval_id missing in error data: %v", resp.Error.Data)
	}
}

func TestToolInvoked_EmitsAudit(t *testing.T) {
	f := newToolFixture(t)
	ctx := context.Background()
	_ = doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 12, "method": "tools/call",
		"params": map[string]any{"name": "client.info"},
	})
	entries, err := f.audit.Query(ctx, audit.Filter{Types: []string{"tool.invoked"}})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 tool.invoked entry, got %d", len(entries))
	}
}
