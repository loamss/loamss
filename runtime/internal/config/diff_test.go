package config

import (
	"testing"
)

// Diff tests — the matrix of "field X changed → expected bucket"
// is the contract this file locks in. When someone adds a new
// schema field, they should add a test case here AND extend
// diff.go's Diff() — the lack of a test is the prompt to make
// the bucket choice (hot-swappable or restart-required) deliberate.

func TestDiff_IdenticalConfigsAreEmpty(t *testing.T) {
	cfg := Default()
	d := Diff(cfg, cfg)
	if !d.IsEmpty() {
		t.Errorf("Diff(c, c) should be empty, got %+v", d)
	}
}

func TestDiff_NilHandling(t *testing.T) {
	if !Diff(nil, nil).IsEmpty() {
		t.Errorf("Diff(nil, nil) should be empty")
	}
	cfg := Default()
	// nil → populated: every meaningful field is a difference.
	d := Diff(nil, cfg)
	if d.IsEmpty() {
		t.Errorf("Diff(nil, populated) should report differences")
	}
	// populated → nil: same diff in reverse.
	d2 := Diff(cfg, nil)
	if d2.IsEmpty() {
		t.Errorf("Diff(populated, nil) should report differences")
	}
}

func TestDiff_LogConfigIsHotSwappable(t *testing.T) {
	a := Default()
	b := Default()
	b.Log.Level = "debug"
	b.Log.Format = "json"
	d := Diff(a, b)
	if len(d.RestartRequired) > 0 {
		t.Errorf("log changes shouldn't require restart, got %+v", d.RestartRequired)
	}
	got := pathSet(d.HotSwapped)
	for _, want := range []string{"log.level", "log.format"} {
		if !got[want] {
			t.Errorf("missing hot-swappable path %q in %v", want, got)
		}
	}
}

func TestDiff_StorageMemoryListenAddrAreRestartRequired(t *testing.T) {
	a := Default()
	b := Default()
	b.Runtime.ListenAddr = "127.0.0.1:9999"
	b.Storage.Adapter = "storage:s3"
	b.Memory.Adapter = "memory:pgvector"
	d := Diff(a, b)
	got := pathSet(d.RestartRequired)
	for _, want := range []string{"runtime.listen_addr", "storage.adapter", "memory.adapter"} {
		if !got[want] {
			t.Errorf("missing restart-required path %q in %v", want, got)
		}
	}
	if len(d.HotSwapped) > 0 {
		t.Errorf("expected no hot-swappable changes, got %v", d.HotSwapped)
	}
}

func TestDiff_StorageConfigMapChange(t *testing.T) {
	a := Default()
	a.Storage = AdapterConfig{Adapter: "storage:fs-encrypted", Config: map[string]any{"root": "/a"}}
	b := Default()
	b.Storage = AdapterConfig{Adapter: "storage:fs-encrypted", Config: map[string]any{"root": "/b"}}
	d := Diff(a, b)
	got := pathSet(d.RestartRequired)
	if !got["storage.config"] {
		t.Errorf("storage.config map change should be restart-required, got %v", got)
	}
	if got["storage.adapter"] {
		t.Errorf("storage.adapter shouldn't change when only the config map differs")
	}
}

func TestDiff_ModelListChange(t *testing.T) {
	a := Default()
	a.Models = []AdapterConfig{{Adapter: "model:ollama"}}
	b := Default()
	b.Models = []AdapterConfig{{Adapter: "model:ollama"}, {Adapter: "model:anthropic"}}
	d := Diff(a, b)
	got := pathSet(d.RestartRequired)
	if !got["models"] {
		t.Errorf("adding a model should be restart-required, got %v", got)
	}
}

func TestDiff_ModelListReorderingIsADiff(t *testing.T) {
	// Order matters in the router — the first match wins. Reordering
	// the list is semantically meaningful and should appear in the
	// diff.
	a := Default()
	a.Models = []AdapterConfig{
		{Adapter: "model:anthropic"},
		{Adapter: "model:ollama"},
	}
	b := Default()
	b.Models = []AdapterConfig{
		{Adapter: "model:ollama"},
		{Adapter: "model:anthropic"},
	}
	d := Diff(a, b)
	if !pathSet(d.RestartRequired)["models"] {
		t.Errorf("reordered model list should show as a diff")
	}
}

func TestDiff_AuditConfigChange(t *testing.T) {
	a := Default()
	b := Default()
	b.Audit.RedactionLevel = "strict"
	b.Audit.HotStoreMaxDays = 30
	b.Audit.HotStoreMaxMB = 4096
	d := Diff(a, b)
	got := pathSet(d.RestartRequired)
	for _, want := range []string{
		"audit.redaction_level",
		"audit.hot_store_max_days",
		"audit.hot_store_max_mb",
	} {
		if !got[want] {
			t.Errorf("missing audit path %q in restart-required %v", want, got)
		}
	}
}

func TestDiff_RuntimeDataDirChange(t *testing.T) {
	a := Default()
	a.Runtime.DataDir = "/tmp/a"
	b := Default()
	b.Runtime.DataDir = "/tmp/b"
	if !pathSet(Diff(a, b).RestartRequired)["runtime.data_dir"] {
		t.Errorf("data_dir change must be restart-required")
	}
}

// pathSet is a tiny helper to make "is path X in bucket Y" checks
// readable. The Diff returns FieldChange values; the test only
// cares about which paths landed where.
func pathSet(changes []FieldChange) map[string]bool {
	out := map[string]bool{}
	for _, c := range changes {
		out[c.Path] = true
	}
	return out
}
