package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

func resetClientFlags() {
	clientPairName = ""
	clientPairTTL = 0
	clientPairJSON = false
	clientPairCompleteJSON = false
	clientListStatus = "active"
	clientListLimit = 100
	clientListJSON = false
	clientShowJSON = false
	clientRevokeReason = ""
	clientRevokeYes = false
}

func runClientCmd(t *testing.T, dataDir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("LOAMSS_DATA_DIR", dataDir)
	resetClientFlags()
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"client"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

// extractCode returns the pairing code from the output of
// `loamss client pair`. The format is fixed enough that we can
// scan for the line that contains a dash-bearing token.
func extractCode(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 9 && line[4] == '-' {
			return line
		}
	}
	t.Fatalf("no pairing code found in output:\n%s", out)
	return ""
}

func TestClientPair_HappyPath(t *testing.T) {
	dir := t.TempDir()
	out, err := runClientCmd(t, dir, "pair", "--name", "ChatGPT laptop")
	if err != nil {
		t.Fatalf("client pair: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Pairing code for",
		"ChatGPT laptop",
		"loamss client pair complete",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	code := extractCode(t, out)
	if code == "" {
		t.Fatal("no code emitted")
	}
}

func TestClientPair_RequiresName(t *testing.T) {
	dir := t.TempDir()
	_, err := runClientCmd(t, dir, "pair")
	if err == nil {
		t.Error("expected error for missing --name")
	}
}

func TestClientPair_JSONMode(t *testing.T) {
	dir := t.TempDir()
	out, err := runClientCmd(t, dir, "pair", "--name", "claude", "--json")
	if err != nil {
		t.Fatalf("client pair --json: %v", err)
	}
	var p permission.PairingCode
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &p); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	if p.ClientName != "claude" {
		t.Errorf("client_name: %q", p.ClientName)
	}
	if !strings.Contains(p.Code, "-") {
		t.Errorf("code shape: %q", p.Code)
	}
}

func TestClientPairComplete_HappyPath(t *testing.T) {
	dir := t.TempDir()
	out, _ := runClientCmd(t, dir, "pair", "--name", "claude")
	code := extractCode(t, out)

	out, err := runClientCmd(t, dir, "pair", "complete", code)
	if err != nil {
		t.Fatalf("pair complete: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Paired client",
		"claude",
		"lck_cli-",
		"save this now",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestClientPairComplete_JSONIncludesToken(t *testing.T) {
	dir := t.TempDir()
	out, _ := runClientCmd(t, dir, "pair", "--name", "claude")
	code := extractCode(t, out)

	out, err := runClientCmd(t, dir, "pair", "complete", code, "--json")
	if err != nil {
		t.Fatalf("pair complete --json: %v", err)
	}
	var payload struct {
		Client *permission.Client `json:"client"`
		Token  string             `json:"token"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, out)
	}
	if payload.Token == "" || !strings.HasPrefix(payload.Token, "lck_") {
		t.Errorf("bad token: %q", payload.Token)
	}
	if payload.Client == nil || payload.Client.Name != "claude" {
		t.Errorf("client: %+v", payload.Client)
	}
}

func TestClientPairComplete_UnknownCodeErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := runClientCmd(t, dir, "pair", "complete", "NOPE-NOPE")
	if err == nil {
		t.Error("expected error for unknown code")
	}
}

func TestClientList_EmptyShowsNoClients(t *testing.T) {
	dir := t.TempDir()
	out, err := runClientCmd(t, dir, "list")
	if err != nil {
		t.Fatalf("client list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no clients") {
		t.Errorf("expected '(no clients)', got:\n%s", out)
	}
}

func TestClientList_ShowsPaired(t *testing.T) {
	dir := t.TempDir()
	out, _ := runClientCmd(t, dir, "pair", "--name", "claude")
	code := extractCode(t, out)
	_, err := runClientCmd(t, dir, "pair", "complete", code)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	out, err = runClientCmd(t, dir, "list")
	if err != nil {
		t.Fatalf("client list: %v", err)
	}
	if !strings.Contains(out, "claude") || !strings.Contains(out, "cli-") {
		t.Errorf("expected claude + cli-... in output:\n%s", out)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("expected 'active' status:\n%s", out)
	}
}

func TestClientShow_OmitsCredentialHash(t *testing.T) {
	dir := t.TempDir()
	out, _ := runClientCmd(t, dir, "pair", "--name", "claude")
	code := extractCode(t, out)
	completeOut, _ := runClientCmd(t, dir, "pair", "complete", code, "--json")

	var payload struct {
		Client *permission.Client `json:"client"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(completeOut)), &payload); err != nil {
		t.Fatalf("decoding complete output: %v", err)
	}

	out, err := runClientCmd(t, dir, "show", payload.Client.ID)
	if err != nil {
		t.Fatalf("client show: %v", err)
	}
	for _, want := range []string{
		"Client " + payload.Client.ID,
		"claude",
		"Status:",
		"active",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Hash must never appear in human output.
	if strings.Contains(out, "credential_hash") {
		t.Errorf("credential_hash leaked to human output:\n%s", out)
	}
}

func TestClientShow_Unknown(t *testing.T) {
	dir := t.TempDir()
	_, err := runClientCmd(t, dir, "show", "cli-no-such")
	if err == nil {
		t.Error("expected error for unknown client")
	}
}

func TestClientRevoke_CascadesGrantsAndEmitsAudit(t *testing.T) {
	dir := t.TempDir()
	// Pair → complete → revoke.
	out, _ := runClientCmd(t, dir, "pair", "--name", "doomed")
	code := extractCode(t, out)
	completeOut, _ := runClientCmd(t, dir, "pair", "complete", code, "--json")
	var payload struct {
		Client *permission.Client `json:"client"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(completeOut)), &payload)
	id := payload.Client.ID

	// Seed a grant directly via the store.
	ctx := context.Background()
	store, err := permission.Open(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.IssueGrant(ctx, permission.Grant{
		Principal:  permission.Principal{Kind: permission.PrincipalClient, ID: id},
		Capability: "memory.read",
	}); err != nil {
		t.Fatalf("IssueGrant: %v", err)
	}
	_ = store.Close()

	out, err = runClientCmd(t, dir, "revoke", "--yes", id)
	if err != nil {
		t.Fatalf("client revoke: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Revoked client") {
		t.Errorf("expected success line, got:\n%s", out)
	}

	// Confirm cascade + audit.
	w, _ := audit.OpenSQLite(ctx, filepath.Join(dir, "audit.db"))
	defer w.Close(ctx)
	cr, _ := w.Query(ctx, audit.Filter{Types: []string{"client.revoked"}})
	if len(cr) != 1 {
		t.Errorf("expected 1 client.revoked, got %d", len(cr))
	}
	gr, _ := w.Query(ctx, audit.Filter{Types: []string{"grant.revoke"}})
	if len(gr) != 1 {
		t.Errorf("expected 1 grant.revoke (cascade), got %d", len(gr))
	}
}

func TestClientRevoke_Idempotent(t *testing.T) {
	dir := t.TempDir()
	out, _ := runClientCmd(t, dir, "pair", "--name", "doomed")
	code := extractCode(t, out)
	completeOut, _ := runClientCmd(t, dir, "pair", "complete", code, "--json")
	var payload struct {
		Client *permission.Client `json:"client"`
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(completeOut)), &payload)

	if _, err := runClientCmd(t, dir, "revoke", "--yes", payload.Client.ID); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	out, err := runClientCmd(t, dir, "revoke", "--yes", payload.Client.ID)
	if err != nil {
		t.Errorf("second revoke should be idempotent, got: %v", err)
	}
	if !strings.Contains(out, "already revoked") {
		t.Errorf("expected 'already revoked' message, got:\n%s", out)
	}
}

func TestClientRevoke_UnknownErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := runClientCmd(t, dir, "revoke", "--yes", "cli-no-such")
	if err == nil {
		t.Error("expected error for unknown client")
	}
}
