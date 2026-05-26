package capsule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRegistry is the in-test CapabilityRegistry. It backs the
// "is the capability registered?" check used by Validate without
// dragging in a full permission.Store.
type fakeRegistry struct {
	registered map[string]bool
}

func newFakeRegistry(known ...string) *fakeRegistry {
	r := &fakeRegistry{registered: make(map[string]bool)}
	for _, c := range known {
		r.registered[c] = true
	}
	return r
}

func (r *fakeRegistry) HasCapability(name string) bool { return r.registered[name] }

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

func TestParse_ValidEmailDrafter(t *testing.T) {
	m, err := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Name != "email-drafter" {
		t.Errorf("name: %q", m.Name)
	}
	if m.Version != "1.4.0" {
		t.Errorf("version: %q", m.Version)
	}
	if m.SpecVersion != "0.1" {
		t.Errorf("spec_version: %q", m.SpecVersion)
	}
	if len(m.Permissions) != 2 {
		t.Errorf("permissions count: %d", len(m.Permissions))
	}
	if len(m.Tools) != 2 {
		t.Errorf("tools count: %d", len(m.Tools))
	}
	if m.Runtime.Type != "subprocess" || m.Runtime.Protocol != "mcp" {
		t.Errorf("runtime: %+v", m.Runtime)
	}
	if m.Runtime.Resources.MemoryMB != 256 || m.Runtime.Resources.CPUQuota != 0.5 {
		t.Errorf("resources: %+v", m.Runtime.Resources)
	}
}

func TestParse_ValidTaxOrganizer_WithMemoryExtensions(t *testing.T) {
	m, err := Parse(loadFixture(t, "valid-tax-organizer.yaml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.MemoryExtensions == nil || len(m.MemoryExtensions.EntityTypes) != 1 {
		t.Fatalf("memory_extensions: %+v", m.MemoryExtensions)
	}
	et := m.MemoryExtensions.EntityTypes[0]
	if et.Name != "receipt" || et.Namespace != "com.acme.tax" {
		t.Errorf("entity_type: %+v", et)
	}
	if !et.ProvenanceRequired {
		t.Error("provenance_required should be true")
	}
	if len(et.DataClasses) != 1 || et.DataClasses[0] != "financial" {
		t.Errorf("data_classes: %v", et.DataClasses)
	}
}

func TestParse_RejectsUnknownYAMLKey(t *testing.T) {
	bad := []byte(`
spec_version: "0.1"
name: rogue
version: 1.0.0
author: {name: x}
permissions:
  - capability: memory.read
tools:
  - name: t
    input_schema: {type: object}
runtime:
  type: subprocess
  entrypoint: [a]
  protocol: mcp
model_requirements: {}
mystery_field: surprise
`)
	if _, err := Parse(bad); err == nil {
		t.Error("expected error for unknown YAML key")
	}
}

func TestValidate_HappyPath(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	reg := newFakeRegistry("memory.read", "memory.write")
	if err := m.Validate(reg); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestValidate_RejectsUnsupportedSpecVersion(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.SpecVersion = "9.9"
	err := m.Validate(newFakeRegistry("memory.read", "memory.write"))
	if err == nil || !strings.Contains(err.Error(), "spec_version") {
		t.Errorf("expected spec_version error, got: %v", err)
	}
}

func TestValidate_RejectsBadName(t *testing.T) {
	cases := []string{
		"Email-Drafter", // uppercase
		"1email",        // leading digit
		"email drafter", // space
		"email.drafter", // dot
		"",              // empty
	}
	for _, name := range cases {
		m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
		m.Name = name
		if err := m.Validate(newFakeRegistry("memory.read", "memory.write")); err == nil {
			t.Errorf("name %q should be rejected", name)
		}
	}
}

func TestValidate_RejectsBadVersion(t *testing.T) {
	cases := []string{
		"1",
		"1.2",
		"v1.2.3",
		"1.2.3.4",
		"latest",
		"",
	}
	for _, v := range cases {
		m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
		m.Version = v
		if err := m.Validate(newFakeRegistry("memory.read", "memory.write")); err == nil {
			t.Errorf("version %q should be rejected", v)
		}
	}
}

func TestValidate_RejectsReservedCapability(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.Permissions = append(m.Permissions, PermissionRequest{
		Capability: "audit.write", // reserved
	})
	err := m.Validate(newFakeRegistry("memory.read", "memory.write", "audit.write"))
	if err == nil || !strings.Contains(err.Error(), "reserved namespace") {
		t.Errorf("expected reserved-namespace error, got: %v", err)
	}
}

func TestValidate_AllowsAuditReadException(t *testing.T) {
	// audit.read is on the exceptions list — capsules can declare it.
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.Permissions = append(m.Permissions, PermissionRequest{
		Capability: "audit.read",
		Rationale:  "self-audit",
	})
	if err := m.Validate(newFakeRegistry("memory.read", "memory.write", "audit.read")); err != nil {
		t.Errorf("audit.read should be allowed for capsules, got: %v", err)
	}
}

func TestValidate_RejectsUnknownCapability(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.Permissions[0].Capability = "made.up"
	err := m.Validate(newFakeRegistry("memory.write"))
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Errorf("expected not-registered error, got: %v", err)
	}
}

func TestValidate_SkipsCapabilityChecksWhenRegistryNil(t *testing.T) {
	// Offline mode: validate without a runtime. capability-existence
	// checks are skipped; everything else still runs.
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	if err := m.Validate(nil); err != nil {
		t.Errorf("offline validate should pass for a well-formed manifest, got: %v", err)
	}
}

func TestValidate_RequiresAtLeastOneTool(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.Tools = nil
	if err := m.Validate(newFakeRegistry("memory.read", "memory.write")); err == nil {
		t.Error("expected error when tools is empty")
	}
}

func TestValidate_RejectsDuplicateToolNames(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.Tools = append(m.Tools, ToolDef{
		Name:        "draft_reply",
		InputSchema: map[string]any{"type": "object"},
	})
	err := m.Validate(newFakeRegistry("memory.read", "memory.write"))
	if err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Errorf("expected duplicate-tool error, got: %v", err)
	}
}

func TestValidate_RejectsToolWithoutSchemaType(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.Tools[0].InputSchema = map[string]any{"properties": map[string]any{}}
	err := m.Validate(newFakeRegistry("memory.read", "memory.write"))
	if err == nil || !strings.Contains(err.Error(), `"type"`) {
		t.Errorf("expected type-field error, got: %v", err)
	}
}

func TestValidate_RejectsWasmRuntime(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.Runtime.Type = "wasm"
	err := m.Validate(newFakeRegistry("memory.read", "memory.write"))
	if err == nil || !strings.Contains(err.Error(), "wasm") {
		t.Errorf("expected wasm-rejection error, got: %v", err)
	}
}

func TestValidate_RejectsEmptyEntrypoint(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.Runtime.Entrypoint = nil
	err := m.Validate(newFakeRegistry("memory.read", "memory.write"))
	if err == nil || !strings.Contains(err.Error(), "entrypoint") {
		t.Errorf("expected entrypoint error, got: %v", err)
	}
}

func TestValidate_RejectsBadQualityHint(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.ModelRequirements.PreferredQuality = "ludicrous"
	err := m.Validate(newFakeRegistry("memory.read", "memory.write"))
	if err == nil || !strings.Contains(err.Error(), "preferred_quality") {
		t.Errorf("expected quality-hint error, got: %v", err)
	}
}

func TestValidate_RejectsBadMemoryExtensionNamespace(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-tax-organizer.yaml"))
	m.MemoryExtensions.EntityTypes[0].Namespace = "no-dots"
	err := m.Validate(newFakeRegistry("memory.read", "memory.write"))
	if err == nil || !strings.Contains(err.Error(), "reverse-DNS") {
		t.Errorf("expected reverse-DNS error, got: %v", err)
	}
}

// --- ingestor + oauth manifest blocks ----------------------------

func ingestorFixtureRegistry() *fakeRegistry {
	return newFakeRegistry(
		"memory.write", "credentials.read", "credentials.write", "external.http",
	)
}

func TestParse_ValidCalendarIngestor(t *testing.T) {
	m, err := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Roles) != 1 || m.Roles[0] != "ingestor" {
		t.Errorf("roles: %v", m.Roles)
	}
	if m.Ingestor == nil {
		t.Fatal("ingestor block missing")
	}
	if m.Ingestor.SourceID != "source:calendar" {
		t.Errorf("source_id: %q", m.Ingestor.SourceID)
	}
	if m.Ingestor.Schedule.Interval != "15m" || m.Ingestor.Schedule.Initial != "30s" {
		t.Errorf("schedule: %+v", m.Ingestor.Schedule)
	}
	if m.Ingestor.OnTrigger != "sync" {
		t.Errorf("on_trigger: %q", m.Ingestor.OnTrigger)
	}
	if m.OAuth == nil {
		t.Fatal("oauth block missing")
	}
	if m.OAuth.Provider != "google" {
		t.Errorf("oauth.provider: %q", m.OAuth.Provider)
	}
	if !m.OAuth.PKCE {
		t.Error("oauth.pkce should be true")
	}
	if m.OAuth.ClientIDEnv != "GOOGLE_OAUTH_CLIENT_ID" {
		t.Errorf("oauth.client_id_env: %q", m.OAuth.ClientIDEnv)
	}
}

func TestValidate_IngestorHappyPath(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
	if err := m.Validate(ingestorFixtureRegistry()); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestValidate_RejectsUnknownRole(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
	m.Roles = []string{"ingestor", "wizard"}
	err := m.Validate(ingestorFixtureRegistry())
	if err == nil || !strings.Contains(err.Error(), `"wizard" is not a known role`) {
		t.Errorf("expected unknown-role error: %v", err)
	}
}

func TestValidate_RejectsDuplicateRole(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
	m.Roles = []string{"ingestor", "ingestor"}
	err := m.Validate(ingestorFixtureRegistry())
	if err == nil || !strings.Contains(err.Error(), "listed more than once") {
		t.Errorf("expected duplicate-role error: %v", err)
	}
}

func TestValidate_IngestorRoleRequiresBlock(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
	m.Ingestor = nil
	err := m.Validate(ingestorFixtureRegistry())
	if err == nil || !strings.Contains(err.Error(), "ingestor: block is required") {
		t.Errorf("expected ingestor-required error: %v", err)
	}
}

func TestValidate_IngestorBlockRequiresRole(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
	m.Roles = nil
	err := m.Validate(ingestorFixtureRegistry())
	if err == nil || !strings.Contains(err.Error(), "roles does not contain") {
		t.Errorf("expected role-required error: %v", err)
	}
}

func TestValidate_RejectsBadSourceID(t *testing.T) {
	cases := []string{
		"calendar",         // missing source: prefix
		"source:Calendar",  // uppercase
		"source:1calendar", // leading digit
		"source:cal_endar", // underscore
		"src:calendar",     // wrong namespace
	}
	for _, sid := range cases {
		m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
		m.Ingestor.SourceID = sid
		if err := m.Validate(ingestorFixtureRegistry()); err == nil {
			t.Errorf("source_id %q should be rejected", sid)
		}
	}
}

func TestValidate_RejectsBadInterval(t *testing.T) {
	cases := []struct {
		interval string
		wantMsg  string
	}{
		{"", "interval is required"},
		{"oops", "invalid"},
		{"30s", "< min"},
		{"48h", "> max"},
	}
	for _, c := range cases {
		m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
		m.Ingestor.Schedule.Interval = c.interval
		err := m.Validate(ingestorFixtureRegistry())
		if err == nil || !strings.Contains(err.Error(), c.wantMsg) {
			t.Errorf("interval %q: want %q in err, got %v", c.interval, c.wantMsg, err)
		}
	}
}

func TestValidate_RejectsBadInitial(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
	m.Ingestor.Schedule.Initial = "not-a-duration"
	err := m.Validate(ingestorFixtureRegistry())
	if err == nil || !strings.Contains(err.Error(), "schedule.initial") {
		t.Errorf("expected initial error: %v", err)
	}
}

func TestValidate_RejectsOnTriggerNotInTools(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
	m.Ingestor.OnTrigger = "ghost"
	err := m.Validate(ingestorFixtureRegistry())
	if err == nil || !strings.Contains(err.Error(), "does not reference any declared tool") {
		t.Errorf("expected on_trigger error: %v", err)
	}
}

func TestValidate_RejectsHTTPOAuthEndpoint(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
	m.OAuth.AuthorizationEndpoint = "http://accounts.google.com/o/oauth2/v2/auth"
	err := m.Validate(ingestorFixtureRegistry())
	if err == nil || !strings.Contains(err.Error(), "must use https://") {
		t.Errorf("expected https error: %v", err)
	}
}

func TestValidate_RejectsEmptyOAuthScopes(t *testing.T) {
	m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
	m.OAuth.Scopes = nil
	err := m.Validate(ingestorFixtureRegistry())
	if err == nil || !strings.Contains(err.Error(), "scopes") {
		t.Errorf("expected scopes error: %v", err)
	}
}

func TestValidate_RejectsBadClientIDEnv(t *testing.T) {
	cases := []string{
		"lowercase_var",
		"WITH-DASH",
		"WITH SPACE",
		"123_LEADING_DIGIT",
		"",
	}
	for _, ev := range cases {
		m, _ := Parse(loadFixture(t, "valid-calendar-ingestor.yaml"))
		m.OAuth.ClientIDEnv = ev
		if err := m.Validate(ingestorFixtureRegistry()); err == nil {
			t.Errorf("client_id_env %q should be rejected", ev)
		}
	}
}

func TestValidate_OAuthOptionalForNonIngestors(t *testing.T) {
	// A capsule may declare oauth without being an ingestor — e.g. an
	// actuator that posts to a third-party platform. The two blocks
	// are independent.
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.OAuth = &OAuthSpec{
		Provider:              "github",
		AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
		TokenEndpoint:         "https://github.com/login/oauth/access_token",
		Scopes:                []string{"repo"},
		ClientIDEnv:           "GITHUB_OAUTH_CLIENT_ID",
		PKCE:                  true,
	}
	if err := m.Validate(newFakeRegistry("memory.read", "memory.write")); err != nil {
		t.Errorf("oauth without ingestor role should validate: %v", err)
	}
}

func TestValidate_AggregatesMultipleErrors(t *testing.T) {
	// Give the validator multiple distinct failures and assert the
	// returned error mentions all of them. Authors get a punch list,
	// not one round-trip per fix.
	m, _ := Parse(loadFixture(t, "valid-email-drafter.yaml"))
	m.Name = ""
	m.Version = "not-semver"
	m.Runtime.Protocol = ""
	err := m.Validate(newFakeRegistry("memory.read", "memory.write"))
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{"name", "version", "protocol"} {
		if !strings.Contains(msg, want) {
			t.Errorf("aggregated error should mention %q; got: %v", want, err)
		}
	}
}
