package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// seedPermissionDB creates a runtime.db at <dataDir>/runtime.db,
// issues the provided grants, and closes. Returns nothing because
// the CLI tests open their own connections.
func seedPermissionDB(t *testing.T, dataDir string, grants []permission.Grant) []permission.Grant {
	t.Helper()
	ctx := context.Background()
	store, err := permission.Open(ctx, filepath.Join(dataDir, "runtime.db"))
	if err != nil {
		t.Fatalf("permission.Open: %v", err)
	}
	defer store.Close()

	out := make([]permission.Grant, 0, len(grants))
	for _, g := range grants {
		issued, err := store.IssueGrant(ctx, g)
		if err != nil {
			t.Fatalf("IssueGrant: %v", err)
		}
		out = append(out, *issued)
	}
	return out
}

func resetGrantFlags() {
	grantListPrincipalKind = ""
	grantListPrincipalID = ""
	grantListCapability = ""
	grantListStatus = "active"
	grantListLimit = 100
	grantListJSON = false
	grantShowJSON = false
	grantRevokeReason = ""
	grantRevokeYes = false
}

func runGrantCmd(t *testing.T, dataDir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("LOAMSS_DATA_DIR", dataDir)
	resetGrantFlags()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"grant"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

func TestGrantList_EmptyShowsNoGrants(t *testing.T) {
	dir := t.TempDir()
	out, err := runGrantCmd(t, dir, "list")
	if err != nil {
		t.Fatalf("grant list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no grants") {
		t.Errorf("expected '(no grants)', got:\n%s", out)
	}
}

func TestGrantList_ShowsActive(t *testing.T) {
	dir := t.TempDir()
	seedPermissionDB(t, dir, []permission.Grant{
		{Principal: permission.Principal{Kind: permission.PrincipalClient, ID: "vibez"}, Capability: "content.read"},
		{Principal: permission.Principal{Kind: permission.PrincipalCapsule, ID: "tax@1.0"}, Capability: "files.read"},
	})

	out, err := runGrantCmd(t, dir, "list")
	if err != nil {
		t.Fatalf("grant list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "content.read") || !strings.Contains(out, "files.read") {
		t.Errorf("expected both grants in output, got:\n%s", out)
	}
}

func TestGrantList_FilterByCapability(t *testing.T) {
	dir := t.TempDir()
	seedPermissionDB(t, dir, []permission.Grant{
		{Principal: permission.Principal{Kind: permission.PrincipalClient, ID: "vibez"}, Capability: "content.read"},
		{Principal: permission.Principal{Kind: permission.PrincipalCapsule, ID: "tax@1.0"}, Capability: "files.read"},
	})

	out, err := runGrantCmd(t, dir, "list", "--capability", "content.read")
	if err != nil {
		t.Fatalf("grant list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "content.read") {
		t.Errorf("expected content.read, got:\n%s", out)
	}
	if strings.Contains(out, "files.read") {
		t.Errorf("filter leaked: files.read should not appear\n%s", out)
	}
}

func TestGrantList_FilterByPrincipal(t *testing.T) {
	dir := t.TempDir()
	seedPermissionDB(t, dir, []permission.Grant{
		{Principal: permission.Principal{Kind: permission.PrincipalClient, ID: "vibez"}, Capability: "content.read"},
		{Principal: permission.Principal{Kind: permission.PrincipalClient, ID: "chatgpt"}, Capability: "memory.read"},
	})

	out, err := runGrantCmd(t, dir, "list", "--principal-id", "vibez")
	if err != nil {
		t.Fatalf("grant list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "vibez") {
		t.Errorf("expected vibez grant, got:\n%s", out)
	}
	if strings.Contains(out, "chatgpt") {
		t.Errorf("filter leaked: chatgpt should not appear\n%s", out)
	}
}

func TestGrantList_StatusAllIncludesRevoked(t *testing.T) {
	dir := t.TempDir()
	gs := seedPermissionDB(t, dir, []permission.Grant{
		{Principal: permission.Principal{Kind: permission.PrincipalClient, ID: "x"}, Capability: "memory.read"},
	})

	// Revoke directly via store.
	store, _ := permission.Open(context.Background(), filepath.Join(dir, "runtime.db"))
	_ = store.RevokeGrant(context.Background(), gs[0].ID)
	_ = store.Close()

	// Default (active) should hide it.
	out, _ := runGrantCmd(t, dir, "list")
	if strings.Contains(out, gs[0].ID) {
		t.Errorf("default list should hide revoked: %s", out)
	}

	// --status all should show it.
	out, _ = runGrantCmd(t, dir, "list", "--status", "all")
	if !strings.Contains(out, gs[0].ID) {
		t.Errorf("--status all should include revoked: %s", out)
	}
	if !strings.Contains(out, "revoked") {
		t.Errorf("expected 'revoked' status in output:\n%s", out)
	}
}

func TestGrantList_JSONMode(t *testing.T) {
	dir := t.TempDir()
	seedPermissionDB(t, dir, []permission.Grant{
		{Principal: permission.Principal{Kind: permission.PrincipalClient, ID: "vibez"}, Capability: "content.read"},
	})
	out, err := runGrantCmd(t, dir, "list", "--json")
	if err != nil {
		t.Fatalf("grant list --json: %v", err)
	}
	var g permission.Grant
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &g); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	if g.Capability != "content.read" {
		t.Errorf("capability: %q", g.Capability)
	}
}

func TestGrantShow_FullDetail(t *testing.T) {
	dir := t.TempDir()
	gs := seedPermissionDB(t, dir, []permission.Grant{
		{
			Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: "vibez"},
			Capability: "content.read",
			Scope:      map[string]any{"tag": "public"},
			Rationale:  "show content to viewers",
		},
	})

	out, err := runGrantCmd(t, dir, "show", gs[0].ID)
	if err != nil {
		t.Fatalf("grant show: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Grant " + gs[0].ID,
		"Principal:",
		"client/vibez",
		"Capability:",
		"content.read",
		"Scope:",
		"public",
		"Rationale:",
		"show content to viewers",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestGrantShow_UnknownErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := runGrantCmd(t, dir, "show", "grt-no-such")
	if err == nil {
		t.Error("expected error for unknown grant")
	}
}

func TestGrantRevoke_MarksRevokedAndEmitsAudit(t *testing.T) {
	dir := t.TempDir()
	gs := seedPermissionDB(t, dir, []permission.Grant{
		{Principal: permission.Principal{Kind: permission.PrincipalClient, ID: "x"}, Capability: "memory.read"},
	})

	out, err := runGrantCmd(t, dir, "revoke", "--yes", gs[0].ID)
	if err != nil {
		t.Fatalf("grant revoke: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Revoked") {
		t.Errorf("expected success line, got:\n%s", out)
	}

	// Verify state in store.
	store, _ := permission.Open(context.Background(), filepath.Join(dir, "runtime.db"))
	defer store.Close()
	g, _ := store.GetGrant(context.Background(), gs[0].ID)
	if g.RevokedAt == nil {
		t.Error("grant not marked revoked")
	}

	// Verify audit entry.
	w, _ := audit.OpenSQLite(context.Background(), filepath.Join(dir, "audit.db"))
	defer w.Close(context.Background())
	entries, _ := w.Query(context.Background(), audit.Filter{Types: []string{"grant.revoke"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 grant.revoke audit entry, got %d", len(entries))
	}
}

func TestGrantRevoke_IdempotentOnRevokedGrant(t *testing.T) {
	dir := t.TempDir()
	gs := seedPermissionDB(t, dir, []permission.Grant{
		{Principal: permission.Principal{Kind: permission.PrincipalClient, ID: "x"}, Capability: "memory.read"},
	})
	// First revoke.
	if _, err := runGrantCmd(t, dir, "revoke", "--yes", gs[0].ID); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	// Second revoke should not error.
	out, err := runGrantCmd(t, dir, "revoke", "--yes", gs[0].ID)
	if err != nil {
		t.Errorf("second revoke should be idempotent, got: %v", err)
	}
	if !strings.Contains(out, "already revoked") {
		t.Errorf("expected 'already revoked' message, got:\n%s", out)
	}
}

func TestGrantRevoke_UnknownErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := runGrantCmd(t, dir, "revoke", "--yes", "grt-no-such")
	if err == nil {
		t.Error("expected error for unknown grant")
	}
	if !errors.Is(err, permission.ErrGrantNotFound) {
		t.Errorf("expected ErrGrantNotFound, got: %v", err)
	}
}
