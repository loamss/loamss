package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loamss/loamss/runtime/internal/config"
)

// findResult returns the first check named `name` in r, or fails the test.
func findResult(t *testing.T, r *doctorReport, name string) checkResult {
	t.Helper()
	for _, c := range r.Results {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in report; results: %+v", name, r.Results)
	return checkResult{}
}

func TestRunChecks_AllOKWithFreshlyInitDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("# stub\n"), 0o600); err != nil {
		t.Fatalf("creating stub config.yaml: %v", err)
	}

	cfg := config.Default()
	cfg.Runtime.DataDir = dir

	r := runChecks(cfg)
	if r.Counts.Fail != 0 {
		t.Errorf("expected zero fails, got %d; report: %+v", r.Counts.Fail, r.Results)
	}
	if findResult(t, r, "Config source").Status != statusOK {
		t.Errorf("config source should be ok when config.yaml exists in data_dir")
	}
	if findResult(t, r, "Data directory").Status != statusOK {
		t.Errorf("data_dir should be ok when dir exists and is writable")
	}
	if findResult(t, r, "Listen address").Status != statusOK {
		t.Errorf("listen addr should be ok for the default 127.0.0.1:7777")
	}
}

func TestRunChecks_MissingDataDir_IsWarning(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.DataDir = filepath.Join(t.TempDir(), "does-not-exist")

	r := runChecks(cfg)
	c := findResult(t, r, "Data directory")
	if c.Status != statusWarn {
		t.Errorf("missing data_dir should warn, got %q (msg: %s)", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "does not exist") {
		t.Errorf("warning message should mention 'does not exist'; got: %s", c.Message)
	}
	if r.Counts.Fail != 0 {
		t.Errorf("warnings should not increment fail count")
	}
}

func TestRunChecks_NonDirectoryDataDir_Fails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatalf("creating fixture: %v", err)
	}

	cfg := config.Default()
	cfg.Runtime.DataDir = path

	r := runChecks(cfg)
	c := findResult(t, r, "Data directory")
	if c.Status != statusFail {
		t.Errorf("non-directory data_dir should fail, got %q (msg: %s)", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "not a directory") {
		t.Errorf("failure message should mention 'not a directory'; got: %s", c.Message)
	}
}

func TestRunChecks_MissingModelAPIKey_IsWarning(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Runtime.DataDir = dir
	cfg.Models = []config.AdapterConfig{
		{
			Adapter: "model:anthropic",
			Config:  map[string]any{"api_key_env": "LOAMSS_TEST_NEVER_SET_KEY"},
		},
	}

	if err := os.Unsetenv("LOAMSS_TEST_NEVER_SET_KEY"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}

	r := runChecks(cfg)
	c := findResult(t, r, "Model adapter[0]")
	if c.Status != statusWarn {
		t.Errorf("missing api key env should warn, got %q (msg: %s)", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "LOAMSS_TEST_NEVER_SET_KEY") {
		t.Errorf("warning should name the env var; got: %s", c.Message)
	}
}

func TestRunChecks_PresentModelAPIKey_IsOK(t *testing.T) {
	t.Setenv("LOAMSS_TEST_PRESENT_KEY", "abc")

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Runtime.DataDir = dir
	cfg.Models = []config.AdapterConfig{
		{
			Adapter: "model:openai",
			Config:  map[string]any{"api_key_env": "LOAMSS_TEST_PRESENT_KEY"},
		},
	}

	r := runChecks(cfg)
	c := findResult(t, r, "Model adapter[0]")
	if c.Status != statusOK {
		t.Errorf("present api key should be ok, got %q (msg: %s)", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "present") {
		t.Errorf("ok message should say 'present'; got: %s", c.Message)
	}
}

func TestRunChecks_BadListenAddr_Fails(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Runtime.DataDir = dir
	cfg.Runtime.ListenAddr = "not a host:port"

	r := runChecks(cfg)
	c := findResult(t, r, "Listen address")
	if c.Status != statusFail {
		t.Errorf("bad listen addr should fail, got %q (msg: %s)", c.Status, c.Message)
	}
}

func TestRunChecks_NonLoopbackListenAddr_IsWarning(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Runtime.DataDir = dir
	cfg.Runtime.ListenAddr = "0.0.0.0:7777"

	r := runChecks(cfg)
	c := findResult(t, r, "Listen address")
	if c.Status != statusWarn {
		t.Errorf("non-loopback should warn, got %q (msg: %s)", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "non-loopback") {
		t.Errorf("warning should mention 'non-loopback'; got: %s", c.Message)
	}
}

func TestRunChecks_BadAdapterShape_Fails(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Runtime.DataDir = dir
	cfg.Storage.Adapter = "filesystem" // missing namespace prefix

	r := runChecks(cfg)
	c := findResult(t, r, "Storage adapter")
	if c.Status != statusFail {
		t.Errorf("bad adapter shape should fail, got %q (msg: %s)", c.Status, c.Message)
	}
}

func TestRenderReport_ProducesReadableOutput(t *testing.T) {
	r := &doctorReport{
		Results: []checkResult{
			{Name: "Config source", Status: statusOK, Message: "/tmp/x/config.yaml"},
			{Name: "Data directory", Status: statusWarn, Message: "/tmp/x does not exist"},
			{Name: "Listen address", Status: statusFail, Message: "bad address"},
		},
		Counts: struct {
			OK   int `json:"ok"`
			Warn int `json:"warn"`
			Fail int `json:"fail"`
		}{OK: 1, Warn: 1, Fail: 1},
	}

	var buf bytes.Buffer
	if err := renderReport(&buf, r); err != nil {
		t.Fatalf("renderReport: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"Loamss runtime health check",
		"✓ Config source",
		"⚠ Data directory",
		"✗ Listen address",
		"1 ok, 1 warn, 1 fail",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRunChecks_JSONSerializable(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Runtime.DataDir = dir

	r := runChecks(cfg)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshaling report: %v", err)
	}

	var roundtrip doctorReport
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("unmarshaling report: %v\nraw: %s", err, data)
	}
	if len(roundtrip.Results) != len(r.Results) {
		t.Errorf("round-trip check count mismatch: %d vs %d", len(roundtrip.Results), len(r.Results))
	}
	if roundtrip.Counts.OK+roundtrip.Counts.Warn+roundtrip.Counts.Fail != len(roundtrip.Results) {
		t.Errorf("counts inconsistent after round-trip")
	}
}

// Integration test through the cobra command tree: verify that `loamss doctor`
// invoked via rootCmd produces a sensible report and exit behavior matching
// the underlying runChecks result.
func TestDoctorCommand_HappyPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOAMSS_DATA_DIR", dir)

	buf := &bytes.Buffer{}
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"doctor"})

	doctorJSON = false
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("doctor through rootCmd errored: %v\n%s", err, buf.String())
	}

	out := buf.String()
	if !strings.Contains(out, "Loamss runtime health check") {
		t.Errorf("doctor output missing header:\n%s", out)
	}
	// Default config + non-existent data_dir → at least one warn, zero fails.
	if !strings.Contains(out, "warn") {
		t.Errorf("expected warning summary line, got:\n%s", out)
	}
}
