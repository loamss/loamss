package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/audit"
)

// seedAuditDB constructs a fresh audit DB at <dataDir>/audit.db with
// the supplied entries appended in order. Returns the path.
func seedAuditDB(t *testing.T, dataDir string, entries []audit.Entry) {
	t.Helper()
	w, err := audit.OpenSQLite(context.Background(), filepath.Join(dataDir, "audit.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = w.Close(context.Background()) }()
	for _, e := range entries {
		if _, err := w.Append(context.Background(), e); err != nil {
			t.Fatalf("seed Append: %v", err)
		}
	}
}

// runAuditCmd executes the audit subcommand via rootCmd.SetArgs.
// Returns stdout + stderr combined.
func runAuditCmd(t *testing.T, dataDir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("LOAMSS_DATA_DIR", dataDir)
	resetAuditFlags()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"audit"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

// resetAuditFlags zeroes the package-level flag variables so each
// test starts from a clean state (cobra reuses globals across runs).
func resetAuditFlags() {
	auditTailLimit = 50
	auditTailJSON = false
	auditLogSince = ""
	auditLogUntil = ""
	auditLogTypes = nil
	auditLogActorKind = ""
	auditLogActorID = ""
	auditLogSubject = ""
	auditLogOutcomes = nil
	auditLogLimit = 100
	auditLogJSON = false
	auditVerifyJSON = false
	auditExportLimit = 1000000
}

func basicAuditEntries() []audit.Entry {
	return []audit.Entry{
		{Type: "grant.create", Actor: audit.Actor{Kind: audit.ActorUser, ID: "fortunatus"}, Outcome: audit.OutcomeSuccess},
		{Type: "check.allow", Actor: audit.Actor{Kind: audit.ActorClient, ID: "vibez"}, Outcome: audit.OutcomeSuccess},
		{Type: "check.deny", Actor: audit.Actor{Kind: audit.ActorClient, ID: "chatgpt"}, Outcome: audit.OutcomeDenied},
		{Type: "email.send", Actor: audit.Actor{Kind: audit.ActorCapsule, ID: "email-drafter"}, Outcome: audit.OutcomeSuccess},
	}
}

func TestAuditTail_ShowsAllEntries(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	out, err := runAuditCmd(t, dir, "tail")
	if err != nil {
		t.Fatalf("tail: %v\n%s", err, out)
	}
	for _, want := range []string{"grant.create", "check.allow", "check.deny", "email.send"} {
		if !strings.Contains(out, want) {
			t.Errorf("tail output missing %q\n%s", want, out)
		}
	}
}

func TestAuditTail_LimitClipsOutput(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	out, err := runAuditCmd(t, dir, "tail", "--limit", "2")
	if err != nil {
		t.Fatalf("tail --limit: %v\n%s", err, out)
	}
	// With --limit=2, we expect the last two entries only.
	if !strings.Contains(out, "check.deny") || !strings.Contains(out, "email.send") {
		t.Errorf("expected last two entries (check.deny + email.send), got:\n%s", out)
	}
	if strings.Contains(out, "grant.create") {
		t.Errorf("limit=2 should not include the first entry; got:\n%s", out)
	}
}

func TestAuditTail_EmptyLogReportsNoEntries(t *testing.T) {
	dir := t.TempDir()
	// No seed; the audit DB will be created empty by openAuditWriter.

	out, err := runAuditCmd(t, dir, "tail")
	if err != nil {
		t.Fatalf("tail (empty): %v\n%s", err, out)
	}
	if !strings.Contains(out, "no entries") {
		t.Errorf("expected '(no entries)' for empty log, got:\n%s", out)
	}
}

func TestAuditTail_JSONMode(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	out, err := runAuditCmd(t, dir, "tail", "--json")
	if err != nil {
		t.Fatalf("tail --json: %v\n%s", err, out)
	}
	// Output should be 4 JSON lines; each should parse as an Entry.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Errorf("expected 4 JSON lines, got %d:\n%s", len(lines), out)
	}
	for i, line := range lines {
		var e audit.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d not valid JSON: %v\nline: %s", i, err, line)
		}
	}
}

func TestAuditLog_FiltersByOutcome(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	out, err := runAuditCmd(t, dir, "log", "--outcome", "denied")
	if err != nil {
		t.Fatalf("log --outcome: %v\n%s", err, out)
	}
	if !strings.Contains(out, "check.deny") {
		t.Errorf("expected the denied entry, got:\n%s", out)
	}
	if strings.Contains(out, "check.allow") || strings.Contains(out, "grant.create") {
		t.Errorf("expected only denied entries, got:\n%s", out)
	}
}

func TestAuditLog_FiltersByActorKindAndID(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	out, err := runAuditCmd(t, dir, "log", "--actor-kind", "client", "--actor-id", "vibez")
	if err != nil {
		t.Fatalf("log --actor: %v\n%s", err, out)
	}
	if !strings.Contains(out, "check.allow") {
		t.Errorf("expected vibez's check.allow, got:\n%s", out)
	}
	if strings.Contains(out, "chatgpt") || strings.Contains(out, "fortunatus") {
		t.Errorf("expected only vibez entries, got:\n%s", out)
	}
}

func TestAuditLog_FiltersByType(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	out, err := runAuditCmd(t, dir, "log", "--type", "grant.create", "--type", "email.send")
	if err != nil {
		t.Fatalf("log --type: %v\n%s", err, out)
	}
	if !strings.Contains(out, "grant.create") || !strings.Contains(out, "email.send") {
		t.Errorf("expected the two specified types, got:\n%s", out)
	}
	if strings.Contains(out, "check.allow") || strings.Contains(out, "check.deny") {
		t.Errorf("expected only specified types, got:\n%s", out)
	}
}

func TestAuditLog_SinceRelativeDuration(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	// Anything within the last hour should include everything we
	// just appended.
	out, err := runAuditCmd(t, dir, "log", "--since", "1h")
	if err != nil {
		t.Fatalf("log --since 1h: %v\n%s", err, out)
	}
	if !strings.Contains(out, "grant.create") {
		t.Errorf("expected recent entries with --since 1h, got:\n%s", out)
	}
}

func TestAuditLog_SinceRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	out, err := runAuditCmd(t, dir, "log", "--since", "not-a-time")
	if err == nil {
		t.Fatalf("expected error for bad --since, got nil\n%s", out)
	}
	if !strings.Contains(err.Error(), "since") {
		t.Errorf("error should mention --since, got: %v", err)
	}
}

func TestAuditVerify_CleanChain(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	out, err := runAuditCmd(t, dir, "verify")
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓") || !strings.Contains(out, "Chain integrity verified") {
		t.Errorf("expected success line, got:\n%s", out)
	}
}

func TestAuditVerify_EmptyChain(t *testing.T) {
	dir := t.TempDir()
	out, err := runAuditCmd(t, dir, "verify")
	if err != nil {
		t.Fatalf("verify (empty): %v\n%s", err, out)
	}
	if !strings.Contains(out, "0 entries") {
		t.Errorf("expected 0-entry success line, got:\n%s", out)
	}
}

func TestAuditVerify_JSONMode(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	out, err := runAuditCmd(t, dir, "verify", "--json")
	if err != nil {
		t.Fatalf("verify --json: %v\n%s", err, out)
	}
	var r audit.VerifyResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &r); err != nil {
		t.Fatalf("verify --json output not valid JSON: %v\noutput: %s", err, out)
	}
	if !r.Valid {
		t.Errorf("expected Valid=true, got %+v", r)
	}
	if r.EntriesChecked != 4 {
		t.Errorf("expected 4 entries checked, got %d", r.EntriesChecked)
	}
}

func TestAuditExport_StreamsJSONL(t *testing.T) {
	dir := t.TempDir()
	seedAuditDB(t, dir, basicAuditEntries())

	out, err := runAuditCmd(t, dir, "export")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Errorf("expected 4 JSONL lines, got %d:\n%s", len(lines), out)
	}
	for i, line := range lines {
		var e audit.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d not valid JSON: %v", i, err)
		}
		if !strings.HasPrefix(e.ID, "aud-") {
			t.Errorf("line %d ID lacks aud- prefix: %s", i, e.ID)
		}
	}
}

func TestAuditExport_Empty(t *testing.T) {
	dir := t.TempDir()
	out, err := runAuditCmd(t, dir, "export")
	if err != nil {
		t.Fatalf("export empty: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("export of empty log should produce no output, got: %q", out)
	}
}

func TestParseTimeFlag_AcceptsRFC3339(t *testing.T) {
	got, err := parseTimeFlag("2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("RFC3339: %v", err)
	}
	if got.Year() != 2026 {
		t.Errorf("year: %d", got.Year())
	}
}

func TestParseTimeFlag_AcceptsDuration(t *testing.T) {
	got, err := parseTimeFlag("24h")
	if err != nil {
		t.Fatalf("24h: %v", err)
	}
	if got.IsZero() {
		t.Error("expected non-zero time")
	}
}

func TestParseTimeFlag_AcceptsDays(t *testing.T) {
	got, err := parseTimeFlag("7d")
	if err != nil {
		t.Fatalf("7d: %v", err)
	}
	if got.IsZero() {
		t.Error("expected non-zero time")
	}
}

func TestParseTimeFlag_RejectsGibberish(t *testing.T) {
	if _, err := parseTimeFlag("blah"); err == nil {
		t.Error("expected error for gibberish")
	}
}
