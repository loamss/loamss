package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// Reset just the create-specific flags. The other grant-flag reset
// in grant_test.go does not touch these; we extend it here so the
// create tests start from a known state regardless of test order.
func resetGrantCreateFlags() {
	grantCreatePrincipalKind = ""
	grantCreatePrincipalID = ""
	grantCreateCapability = ""
	grantCreateScopeJSON = ""
	grantCreateRationale = ""
	grantCreateUserNote = ""
	grantCreateFraming = "private_read"
	grantCreateExpiresIn = 0
	grantCreateRequiresApprove = false
	grantCreateJSON = false
}

func TestGrantCreate_HappyPath(t *testing.T) {
	dir := t.TempDir()
	defer resetGrantCreateFlags()
	resetGrantCreateFlags()

	out, err := runGrantCmd(t, dir, "create",
		"--principal-kind", "client",
		"--principal-id", "vibez",
		"--capability", "content.read",
		"--rationale", "for the demo",
		"--scope-json", `{"tag":"public"}`,
	)
	if err != nil {
		t.Fatalf("grant create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Issued grant") {
		t.Errorf("expected 'Issued grant' line, got:\n%s", out)
	}
	if !strings.Contains(out, "content.read") {
		t.Errorf("expected capability in output, got:\n%s", out)
	}

	// Audit entry emitted.
	w, _ := audit.OpenSQLite(context.Background(), filepath.Join(dir, "audit.db"))
	defer w.Close(context.Background())
	entries, _ := w.Query(context.Background(), audit.Filter{Types: []string{"grant.create"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 grant.create audit entry, got %d", len(entries))
	}
}

func TestGrantCreate_RejectsBadPrincipalKind(t *testing.T) {
	dir := t.TempDir()
	defer resetGrantCreateFlags()
	resetGrantCreateFlags()

	_, err := runGrantCmd(t, dir, "create",
		"--principal-kind", "stranger",
		"--principal-id", "x",
		"--capability", "memory.read",
	)
	if err == nil {
		t.Error("expected error for bad principal kind")
	}
}

func TestGrantCreate_RejectsUnknownCapability(t *testing.T) {
	dir := t.TempDir()
	defer resetGrantCreateFlags()
	resetGrantCreateFlags()

	_, err := runGrantCmd(t, dir, "create",
		"--principal-kind", "client",
		"--principal-id", "x",
		"--capability", "made.up",
	)
	if err == nil {
		t.Error("expected error for unknown capability")
	}
}

func TestGrantCreate_RejectsBadScopeJSON(t *testing.T) {
	dir := t.TempDir()
	defer resetGrantCreateFlags()
	resetGrantCreateFlags()

	_, err := runGrantCmd(t, dir, "create",
		"--principal-kind", "client",
		"--principal-id", "x",
		"--capability", "memory.read",
		"--scope-json", "not json",
	)
	if err == nil {
		t.Error("expected error for malformed scope-json")
	}
}

func TestGrantCreate_PersistedAndListable(t *testing.T) {
	dir := t.TempDir()
	defer resetGrantCreateFlags()
	resetGrantCreateFlags()

	if _, err := runGrantCmd(t, dir, "create",
		"--principal-kind", "client",
		"--principal-id", "claude",
		"--capability", "memory.read",
	); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Verify directly via the store.
	store, _ := permission.Open(context.Background(), filepath.Join(dir, "runtime.db"))
	defer store.Close()
	gs, err := store.ListGrantsByPrincipal(context.Background(), permission.PrincipalClient, "claude")
	if err != nil {
		t.Fatalf("ListGrantsByPrincipal: %v", err)
	}
	if len(gs) != 1 {
		t.Fatalf("expected 1 grant, got %d", len(gs))
	}
	if gs[0].Capability != "memory.read" {
		t.Errorf("capability: %q", gs[0].Capability)
	}
}
