package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCapsuleCmd(t *testing.T, dataDir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("LOAMSS_DATA_DIR", dataDir)
	capsuleValidateOffline = false
	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(append([]string{"capsule"}, args...))
	err := rootCmd.Execute()
	return buf.String(), err
}

// writeManifest writes the supplied YAML body to a fresh temp dir,
// either at <dir>/capsule.yaml (when asDir is true) or at
// <dir>/raw.yaml. Returns the path the test should pass to
// `loamss capsule validate`.
func writeManifest(t *testing.T, body string, asDir bool) string {
	t.Helper()
	dir := t.TempDir()
	var path string
	if asDir {
		path = filepath.Join(dir, "capsule.yaml")
	} else {
		path = filepath.Join(dir, "raw.yaml")
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if asDir {
		return dir
	}
	return path
}

const validYAML = `
spec_version: "0.1"
name: smoke
version: 0.0.1
author:
  name: Tester
permissions:
  - capability: memory.read
    rationale: read
tools:
  - name: hello
    input_schema:
      type: object
model_requirements:
  capabilities: ["text"]
runtime:
  type: subprocess
  entrypoint: [echo]
  protocol: mcp
`

func TestCapsuleValidate_HappyPath_DirectoryPath(t *testing.T) {
	path := writeManifest(t, validYAML, true)
	out, err := runCapsuleCmd(t, t.TempDir(), "validate", path, "--offline")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	for _, want := range []string{
		`Capsule "smoke" v0.0.1 validates clean`,
		"permissions:  1 capabilities",
		"tools:        1 declared",
		"runtime:      subprocess (mcp)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCapsuleValidate_HappyPath_FilePath(t *testing.T) {
	// Direct .yaml file path (not a directory) should also work.
	path := writeManifest(t, validYAML, false)
	out, err := runCapsuleCmd(t, t.TempDir(), "validate", path, "--offline")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "validates clean") {
		t.Errorf("expected success line, got:\n%s", out)
	}
}

func TestCapsuleValidate_RejectsBadManifest_PrintsPunchList(t *testing.T) {
	bad := `
spec_version: "0.1"
name: BAD-NAME
version: not-semver
author:
  name: x
permissions:
  - capability: audit.write
tools:
  - name: t
    input_schema:
      type: object
model_requirements: {}
runtime:
  type: wasm
  entrypoint: []
  protocol: smtp
`
	path := writeManifest(t, bad, true)
	out, err := runCapsuleCmd(t, t.TempDir(), "validate", path, "--offline")
	if err == nil {
		t.Fatal("expected error")
	}
	// Punch list should mention multiple problems on separate lines.
	for _, fragment := range []string{
		"name", "version", "wasm", "entrypoint", "protocol", "audit.write",
	} {
		if !strings.Contains(out, fragment) {
			t.Errorf("punch list missing %q\nout:\n%s", fragment, out)
		}
	}
	// Each issue should be on its own indented line.
	if strings.Count(out, "  - ") < 3 {
		t.Errorf("expected indented punch list, got:\n%s", out)
	}
}

func TestCapsuleValidate_OfflineModeSkipsRegistryCheck(t *testing.T) {
	// Reference a capability that doesn't exist in the canonical
	// registry. With --offline, we don't consult the store, so
	// validate succeeds.
	body := `
spec_version: "0.1"
name: smoke
version: 0.0.1
author: {name: x}
permissions:
  - capability: hypothetical.future
    rationale: r
tools:
  - name: t
    input_schema:
      type: object
model_requirements: {}
runtime:
  type: subprocess
  entrypoint: [echo]
  protocol: mcp
`
	path := writeManifest(t, body, true)
	out, err := runCapsuleCmd(t, t.TempDir(), "validate", path, "--offline")
	if err != nil {
		t.Errorf("offline validate should not check the runtime registry, got: %v\n%s", err, out)
	}
}

func TestCapsuleValidate_OnlineRejectsUnknownCapability(t *testing.T) {
	// Same manifest as the offline-mode test, but WITHOUT --offline.
	// The runtime's canonical registry doesn't know "hypothetical.future",
	// so validation should fail.
	body := `
spec_version: "0.1"
name: smoke
version: 0.0.1
author: {name: x}
permissions:
  - capability: hypothetical.future
    rationale: r
tools:
  - name: t
    input_schema:
      type: object
model_requirements: {}
runtime:
  type: subprocess
  entrypoint: [echo]
  protocol: mcp
`
	path := writeManifest(t, body, true)
	out, err := runCapsuleCmd(t, t.TempDir(), "validate", path)
	if err == nil {
		t.Fatalf("expected validation error against the real registry\nout:\n%s", out)
	}
	if !strings.Contains(out, "hypothetical.future") {
		t.Errorf("expected mention of the unknown capability, got:\n%s", out)
	}
}

func TestCapsuleValidate_FileNotFound(t *testing.T) {
	_, err := runCapsuleCmd(t, t.TempDir(), "validate", "/nonexistent/capsule.yaml")
	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}
