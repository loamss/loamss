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
