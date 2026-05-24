package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
	_ "github.com/loamss/loamss/runtime/internal/adapter/memory/sqlite"
	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// newResourceFixture extends the base test fixture with a memory
// adapter and the MemoryResourceProvider registered. Mirrors
// newToolFixture's shape so resources_test.go follows the same
// conventions as tools_test.go.
type resourceFixture struct {
	*testFixture
	mem memory.Adapter
}

func newResourceFixture(t *testing.T) *resourceFixture {
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
	if err := f.h.deps.Resources.Register(NewMemoryResourceProvider(mem)); err != nil {
		t.Fatalf("Register memory provider: %v", err)
	}
	return &resourceFixture{testFixture: f, mem: mem}
}

func TestResourcesTemplates_ReturnsRegisteredTemplates(t *testing.T) {
	f := newResourceFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "resources/templates/list",
	})
	if resp.Error != nil {
		t.Fatalf("resources/templates/list error: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var tr templatesResult
	if err := json.Unmarshal(rb, &tr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(tr.ResourceTemplates) == 0 {
		t.Fatal("expected at least one template")
	}
	found := false
	for _, tmpl := range tr.ResourceTemplates {
		if tmpl.URITemplate == "memory://entry/{id}" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected memory://entry/{id} template, got: %+v", tr.ResourceTemplates)
	}
}

func TestResourcesList_EmptyByDefault(t *testing.T) {
	// The memory provider deliberately returns nothing from List().
	// This test pins that contract.
	f := newResourceFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "resources/list",
	})
	if resp.Error != nil {
		t.Fatalf("resources/list error: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var lr ListResult
	_ = json.Unmarshal(rb, &lr)
	if len(lr.Resources) != 0 {
		t.Errorf("expected empty list, got %d entries", len(lr.Resources))
	}
}

func TestResourcesRead_MissingURI(t *testing.T) {
	f := newResourceFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "resources/read",
		"params": map[string]any{},
	})
	if resp.Error == nil || resp.Error.Code != codeInvalidParams {
		t.Errorf("expected codeInvalidParams, got %+v", resp.Error)
	}
}

func TestResourcesRead_UnknownScheme(t *testing.T) {
	f := newResourceFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "resources/read",
		"params": map[string]any{"uri": "nopes://anything"},
	})
	if resp.Error == nil || resp.Error.Code != codeUnknownResource {
		t.Errorf("expected codeUnknownResource, got %+v", resp.Error)
	}
}

func TestResourcesRead_URIWithoutScheme(t *testing.T) {
	f := newResourceFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "resources/read",
		"params": map[string]any{"uri": "bare-string"},
	})
	if resp.Error == nil || resp.Error.Code != codeUnknownResource {
		t.Errorf("expected codeUnknownResource for missing scheme, got %+v", resp.Error)
	}
}

func TestResourcesRead_DeniedWithoutGrant(t *testing.T) {
	f := newResourceFixture(t)
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "resources/read",
		"params": map[string]any{"uri": "memory://entry/abc"},
	})
	if resp.Error == nil || resp.Error.Code != codePermissionDenied {
		t.Errorf("expected codePermissionDenied, got %+v", resp.Error)
	}
}

func TestResourcesRead_AllowedWithGrant_NotFound(t *testing.T) {
	f := newResourceFixture(t)
	ctx := context.Background()
	if _, err := f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "memory.read",
	}); err != nil {
		t.Fatalf("IssueGrant: %v", err)
	}
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "resources/read",
		"params": map[string]any{"uri": "memory://entry/does-not-exist"},
	})
	if resp.Error == nil || resp.Error.Code != codeUnknownResource {
		t.Errorf("expected codeUnknownResource for missing entry, got %+v", resp.Error)
	}
}

func TestResourcesRead_HappyPath(t *testing.T) {
	f := newResourceFixture(t)
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "memory.read",
	})
	if err := f.mem.Upsert(ctx, "mem-007", []float32{1, 2, 3, 4},
		map[string]any{"type": "decision", "summary": "use Postgres"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	resp := doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 8, "method": "resources/read",
		"params": map[string]any{"uri": "memory://entry/mem-007"},
	})
	if resp.Error != nil {
		t.Fatalf("resources/read error: %+v", resp.Error)
	}
	rb, _ := json.Marshal(resp.Result)
	var rr readResourceResult
	if err := json.Unmarshal(rb, &rr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rr.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(rr.Contents))
	}
	c := rr.Contents[0]
	if c.URI != "memory://entry/mem-007" {
		t.Errorf("uri round-trip: %q", c.URI)
	}
	if c.MIMEType != "application/json" {
		t.Errorf("mimeType: %q", c.MIMEType)
	}
	if !strings.Contains(c.Text, "mem-007") || !strings.Contains(c.Text, "use Postgres") {
		t.Errorf("text missing fields: %s", c.Text)
	}
	if strings.Contains(c.Text, `"1"`) || strings.Contains(c.Text, `"2"`) {
		// Sanity: vector elements shouldn't be inlined as strings.
		// (We're checking the well-formed JSON path; floats stringify
		// as "1" only if we're emitting the vector, which we aren't.)
		t.Logf("vector bleed into output: %s", c.Text)
	}
	if !strings.Contains(c.Text, `"vector_size": 4`) {
		t.Errorf("expected vector_size: 4, got: %s", c.Text)
	}
}

func TestResourceRead_EmitsAudit(t *testing.T) {
	f := newResourceFixture(t)
	ctx := context.Background()
	_, _ = f.store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: f.client.ID},
		Capability: "memory.read",
	})
	_ = f.mem.Upsert(ctx, "mem-audit", []float32{0, 0, 0, 1}, map[string]any{"x": 1})
	_ = doRPC(t, f.h, f.client, map[string]any{
		"jsonrpc": "2.0", "id": 9, "method": "resources/read",
		"params": map[string]any{"uri": "memory://entry/mem-audit"},
	})
	entries, _ := f.audit.Query(ctx, audit.Filter{Types: []string{"resource.read"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 resource.read entry, got %d", len(entries))
	}
}

func TestResourceRegistry_DuplicateSchemeRejected(t *testing.T) {
	r := NewResourceRegistry()
	prov := &fakeProvider{scheme: "x"}
	if err := r.Register(prov); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register(prov); err == nil {
		t.Error("expected error on duplicate scheme registration")
	}
}

func TestResourceRegistry_RejectsEmptyScheme(t *testing.T) {
	r := NewResourceRegistry()
	if err := r.Register(&fakeProvider{scheme: ""}); err == nil {
		t.Error("expected error on empty scheme")
	}
	if err := r.Register(nil); err == nil {
		t.Error("expected error on nil provider")
	}
}

// fakeProvider implements ResourceProvider for tests that need to
// inspect the registry surface without dragging the memory adapter in.
type fakeProvider struct {
	scheme string
}

func (f *fakeProvider) Scheme() string                                   { return f.scheme }
func (f *fakeProvider) Templates() []ResourceTemplate                    { return nil }
func (f *fakeProvider) Capability() string                               { return "" }
func (f *fakeProvider) List(context.Context, string) (ListResult, error) { return ListResult{}, nil }
func (f *fakeProvider) Read(context.Context, string) (ResourceContent, error) {
	return ResourceContent{}, nil
}

func TestSchemeOf_Cases(t *testing.T) {
	cases := []struct {
		uri, want string
	}{
		{"memory://entry/abc", "memory"},
		{"file:///etc/hosts", "file"},
		{"vibez.content://video/123", "vibez.content"},
		{"bare-string", ""},
		{"://no-scheme", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := schemeOf(tc.uri)
		if got != tc.want {
			t.Errorf("schemeOf(%q): got %q, want %q", tc.uri, got, tc.want)
		}
	}
}
