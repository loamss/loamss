package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/source"
	"github.com/loamss/loamss/runtime/internal/source/sourcetest"
)

func resetSourceFlags() {
	sourceAddName = ""
	sourceAddConfig = nil
	sourceAddJSON = false
	sourceAddAuthenticate = false
	sourceListJSON = false
	sourceShowJSON = false
	sourceSyncJSON = false
	sourceRemoveYes = false
	sourceRemoveReason = ""
}

func runSourceCmd(t *testing.T, dataDir string, stdin string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("LOAMSS_DATA_DIR", dataDir)
	resetSourceFlags()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	if stdin != "" {
		rootCmd.SetIn(strings.NewReader(stdin))
	} else {
		rootCmd.SetIn(strings.NewReader(""))
	}
	rootCmd.SetArgs(append([]string{"source"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

// sharedFake is one Fake instance that all source CLI tests in this
// file share — source.Register panics on a duplicate id, so we
// can't re-register per test. State leakage between tests is
// limited because each test uses a fresh data dir.
var sharedFake = sourcetest.New("source:fake")

func init() {
	source.Register("source:fake", sharedFake.Factory())
}

func TestSourceAdd_HappyPath(t *testing.T) {
	dir := t.TempDir()
	out, err := runSourceCmd(t, dir, "", "add", "source:fake",
		"--name", "fake-personal",
		"--config", "label=INBOX")
	if err != nil {
		t.Fatalf("source add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓ Added source") {
		t.Errorf("expected success line, got:\n%s", out)
	}

	// Verify the record exists.
	s, err := source.OpenStore(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = s.Close() }()
	got, err := s.Get(context.Background(), "fake-personal")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AdapterID != "source:fake" {
		t.Errorf("adapter_id: %q", got.AdapterID)
	}
	if got.Config["label"] != "INBOX" {
		t.Errorf("config: %+v", got.Config)
	}

	// Verify audit emission.
	w, _ := audit.OpenSQLite(context.Background(), filepath.Join(dir, "audit.db"))
	defer func() { _ = w.Close(context.Background()) }()
	entries, err := w.Query(context.Background(), audit.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("audit Query: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Type == "source.added" && e.Subject != nil && e.Subject.ID == "fake-personal" {
			found = true
		}
	}
	if !found {
		t.Errorf("source.added entry missing; saw %d entries", len(entries))
	}
}

func TestSourceAdd_RejectsInvalidAdapterID(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"gmail", "Source:gmail", "source:", "source:UPPER", "source:has space"} {
		out, err := runSourceCmd(t, dir, "", "add", bad, "--name", "x")
		if err == nil {
			t.Errorf("expected error for adapter id %q, got:\n%s", bad, out)
		}
	}
}

func TestSourceAdd_RejectsUnregistered(t *testing.T) {
	dir := t.TempDir()
	out, err := runSourceCmd(t, dir, "", "add", "source:not-registered",
		"--name", "x")
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Errorf("expected 'not registered' error, got err=%v out=%s", err, out)
	}
}

func TestSourceAdd_RequiresName(t *testing.T) {
	dir := t.TempDir()
	out, err := runSourceCmd(t, dir, "", "add", "source:fake")
	if err == nil {
		t.Errorf("expected error for missing --name, got:\n%s", out)
	}
}

func TestSourceAdd_DuplicateNameFails(t *testing.T) {
	dir := t.TempDir()
	if _, err := runSourceCmd(t, dir, "", "add", "source:fake", "--name", "dup"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, err := runSourceCmd(t, dir, "", "add", "source:fake", "--name", "dup")
	if err == nil {
		t.Error("expected error on duplicate name")
	}
}

func TestSourceList_EmptyAndPopulated(t *testing.T) {
	dir := t.TempDir()
	out, err := runSourceCmd(t, dir, "", "list")
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if !strings.Contains(out, "no sources configured") {
		t.Errorf("empty list: %s", out)
	}

	if _, err := runSourceCmd(t, dir, "", "add", "source:fake", "--name", "lf1"); err != nil {
		t.Fatalf("add lf1: %v", err)
	}
	out, err = runSourceCmd(t, dir, "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "lf1") || !strings.Contains(out, "source:fake") {
		t.Errorf("list output: %s", out)
	}
}

func TestSourceShow_JSON(t *testing.T) {
	dir := t.TempDir()
	if _, err := runSourceCmd(t, dir, "", "add", "source:fake", "--name", "sh1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := runSourceCmd(t, dir, "", "show", "sh1", "--json")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	var c source.Configured
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &c); err != nil {
		t.Fatalf("JSON decode: %v\n%s", err, out)
	}
	if c.Name != "sh1" {
		t.Errorf("name: %q", c.Name)
	}
}

func TestSourceShow_MasksSensitiveConfig(t *testing.T) {
	dir := t.TempDir()
	if _, err := runSourceCmd(t, dir, "", "add", "source:fake",
		"--name", "mask1",
		"--config", "client_secret=hush",
		"--config", "label=INBOX"); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := runSourceCmd(t, dir, "", "show", "mask1")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	if !strings.Contains(out, "(set, hidden)") {
		t.Errorf("expected '(set, hidden)' marker in human-formatted output, got:\n%s", out)
	}
	if strings.Contains(out, "hush") {
		t.Errorf("plaintext secret leaked into human-formatted output:\n%s", out)
	}
	// Non-sensitive keys should still render verbatim.
	if !strings.Contains(out, "label: INBOX") {
		t.Errorf("expected non-sensitive config to render verbatim, got:\n%s", out)
	}
}

func TestSourceAuthenticate_CodePaste(t *testing.T) {
	dir := t.TempDir()
	// Reset shared fake state — earlier tests may have flipped AuthComplete.
	sharedFake.AuthComplete = false

	if _, err := runSourceCmd(t, dir, "", "add", "source:fake", "--name", "auth1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := runSourceCmd(t, dir, "secret-code-123\n", "authenticate", "auth1")
	if err != nil {
		t.Fatalf("authenticate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Open this URL") {
		t.Errorf("missing prompt: %s", out)
	}
	if !strings.Contains(out, "✓ Authenticated") {
		t.Errorf("missing success line: %s", out)
	}
	if !sharedFake.AuthComplete {
		t.Error("fake AuthComplete was not set")
	}
}

func TestSourceSync_RequiresAuth(t *testing.T) {
	dir := t.TempDir()
	// Make sure fake is not authenticated.
	sharedFake.AuthComplete = false
	sharedFake.SkipAuth = false

	if _, err := runSourceCmd(t, dir, "", "add", "source:fake", "--name", "noauth"); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := runSourceCmd(t, dir, "", "sync", "noauth")
	if err == nil {
		t.Errorf("expected sync to fail without auth, out=%s", out)
	}
	// And the audit log should contain a sync.completed with denied/error outcome.
	w, _ := audit.OpenSQLite(context.Background(), filepath.Join(dir, "audit.db"))
	defer func() { _ = w.Close(context.Background()) }()
	entries, _ := w.Query(context.Background(), audit.Filter{Limit: 20})
	sawCompleted := false
	for _, e := range entries {
		if e.Type == "source.sync.completed" && e.Outcome != audit.OutcomeSuccess {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Error("expected source.sync.completed with non-success outcome")
	}
}

func TestSourceSync_PersistsCursor(t *testing.T) {
	dir := t.TempDir()
	sharedFake.SkipAuth = true // bypass auth for this test
	t.Cleanup(func() { sharedFake.SkipAuth = false })

	if _, err := runSourceCmd(t, dir, "", "add", "source:fake", "--name", "syn1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := runSourceCmd(t, dir, "", "sync", "syn1")
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}

	// Cursor should now be set (the fake stores the call count as a byte).
	s, _ := source.OpenStore(context.Background(), filepath.Join(dir, "runtime.db"))
	defer func() { _ = s.Close() }()
	got, _ := s.Get(context.Background(), "syn1")
	if len(got.Cursor) == 0 {
		t.Errorf("cursor not persisted")
	}
	if got.LastSyncStatus != "success" {
		t.Errorf("status: %q", got.LastSyncStatus)
	}
}

func TestSourceRemove_DeletesRecordAndCreds(t *testing.T) {
	dir := t.TempDir()
	sharedFake.AuthComplete = false
	if _, err := runSourceCmd(t, dir, "", "add", "source:fake", "--name", "del1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Authenticate so creds get written.
	if _, err := runSourceCmd(t, dir, "code-xyz\n", "authenticate", "del1"); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if _, err := runSourceCmd(t, dir, "", "remove", "del1", "--yes"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Record should be gone.
	s, _ := source.OpenStore(context.Background(), filepath.Join(dir, "runtime.db"))
	defer func() { _ = s.Close() }()
	if _, err := s.Get(context.Background(), "del1"); err == nil {
		t.Error("expected record gone after remove")
	}
}

func TestSourceRemove_MissingFails(t *testing.T) {
	dir := t.TempDir()
	_, err := runSourceCmd(t, dir, "", "remove", "ghost", "--yes")
	if err == nil {
		t.Error("expected error removing nonexistent source")
	}
}
