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

func resetApproveFlags() {
	approveListJSON = false
	approveShowJSON = false
	approveGrantNote = ""
	approveDenyNote = ""
}

func runApproveCmd(t *testing.T, dataDir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("LOAMSS_DATA_DIR", dataDir)
	resetApproveFlags()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"approve"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

// seedApproval enqueues a PendingApproval against a fresh runtime.db
// and returns its id. Used by tests that don't need to drive a full
// Check + approval-required flow.
func seedApproval(t *testing.T, dataDir string, a permission.PendingApproval) string {
	t.Helper()
	ctx := context.Background()
	store, err := permission.Open(ctx, filepath.Join(dataDir, "runtime.db"))
	if err != nil {
		t.Fatalf("permission.Open: %v", err)
	}
	defer store.Close()
	enqueued, err := store.EnqueueApproval(ctx, a)
	if err != nil {
		t.Fatalf("EnqueueApproval: %v", err)
	}
	return enqueued.ID
}

func TestApproveList_EmptyShowsNoPending(t *testing.T) {
	dir := t.TempDir()
	out, err := runApproveCmd(t, dir, "list")
	if err != nil {
		t.Fatalf("approve list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no pending approvals") {
		t.Errorf("expected '(no pending approvals)', got:\n%s", out)
	}
}

func TestApproveList_ShowsPending(t *testing.T) {
	dir := t.TempDir()
	seedApproval(t, dir, permission.PendingApproval{
		GrantID:    "grt-fake",
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: "vibez"},
		Capability: "email.send",
		Rationale:  "draft ready for send",
	})

	out, err := runApproveCmd(t, dir, "list")
	if err != nil {
		t.Fatalf("approve list: %v\n%s", err, out)
	}
	for _, want := range []string{
		"client/vibez",
		"email.send",
		"draft ready for send",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestApproveList_JSONMode(t *testing.T) {
	dir := t.TempDir()
	seedApproval(t, dir, permission.PendingApproval{
		GrantID:    "grt-fake",
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: "vibez"},
		Capability: "email.send",
	})

	out, err := runApproveCmd(t, dir, "list", "--json")
	if err != nil {
		t.Fatalf("approve list --json: %v", err)
	}
	var a permission.PendingApproval
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &a); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	if a.Capability != "email.send" {
		t.Errorf("capability: %q", a.Capability)
	}
}

func TestApproveShow_FullDetail(t *testing.T) {
	dir := t.TempDir()
	id := seedApproval(t, dir, permission.PendingApproval{
		GrantID:        "grt-fake",
		Principal:      permission.Principal{Kind: permission.PrincipalClient, ID: "vibez"},
		Capability:     "email.send",
		Rationale:      "draft ready",
		AttemptedScope: map[string]any{"recipient": "sarah@acme.com"},
	})

	out, err := runApproveCmd(t, dir, "show", id)
	if err != nil {
		t.Fatalf("approve show: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Approval " + id,
		"State:",
		"pending",
		"client/vibez",
		"email.send",
		"Rationale:",
		"draft ready",
		"Attempted scope:",
		"sarah@acme.com",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestApproveShow_UnknownErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := runApproveCmd(t, dir, "show", "apr-no-such")
	if !errors.Is(err, permission.ErrApprovalNotFound) {
		t.Errorf("expected ErrApprovalNotFound, got: %v", err)
	}
}

func TestApproveGrant_ResolvesAndEmitsAudit(t *testing.T) {
	dir := t.TempDir()
	id := seedApproval(t, dir, permission.PendingApproval{
		GrantID:    "grt-fake",
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: "vibez"},
		Capability: "email.send",
	})

	out, err := runApproveCmd(t, dir, "grant", id, "--note", "looks good")
	if err != nil {
		t.Fatalf("approve grant: %v\n%s", err, out)
	}
	if !strings.Contains(out, "granted") {
		t.Errorf("expected success message, got:\n%s", out)
	}

	// State updated.
	store, _ := permission.Open(context.Background(), filepath.Join(dir, "runtime.db"))
	defer store.Close()
	a, _ := store.GetApproval(context.Background(), id)
	if a.State != permission.ApprovalGranted {
		t.Errorf("state: got %q, want granted", a.State)
	}

	// Audit entry emitted.
	w, _ := audit.OpenSQLite(context.Background(), filepath.Join(dir, "audit.db"))
	defer w.Close(context.Background())
	entries, _ := w.Query(context.Background(), audit.Filter{Types: []string{"approval.granted"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 approval.granted entry, got %d", len(entries))
	}
}

func TestApproveDeny_ResolvesAndEmitsAudit(t *testing.T) {
	dir := t.TempDir()
	id := seedApproval(t, dir, permission.PendingApproval{
		GrantID:    "grt-fake",
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: "vibez"},
		Capability: "email.send",
	})

	out, err := runApproveCmd(t, dir, "deny", id, "--note", "wrong recipient")
	if err != nil {
		t.Fatalf("approve deny: %v\n%s", err, out)
	}
	if !strings.Contains(out, "denied") {
		t.Errorf("expected denial message, got:\n%s", out)
	}

	store, _ := permission.Open(context.Background(), filepath.Join(dir, "runtime.db"))
	defer store.Close()
	a, _ := store.GetApproval(context.Background(), id)
	if a.State != permission.ApprovalDenied {
		t.Errorf("state: got %q, want denied", a.State)
	}

	w, _ := audit.OpenSQLite(context.Background(), filepath.Join(dir, "audit.db"))
	defer w.Close(context.Background())
	entries, _ := w.Query(context.Background(), audit.Filter{Types: []string{"approval.denied"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 approval.denied entry, got %d", len(entries))
	}
}

func TestApproveGrant_AlreadyResolvedErrors(t *testing.T) {
	dir := t.TempDir()
	id := seedApproval(t, dir, permission.PendingApproval{
		GrantID:    "grt-fake",
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: "x"},
		Capability: "memory.read",
	})

	if _, err := runApproveCmd(t, dir, "grant", id); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	_, err := runApproveCmd(t, dir, "grant", id)
	if !errors.Is(err, permission.ErrApprovalAlreadyResolved) {
		t.Errorf("expected ErrApprovalAlreadyResolved, got: %v", err)
	}
}
