package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Stress tests for WriteAtomic. These are slower and more invasive
// than the basic write_test.go cases; they exist to surface bugs
// that only appear under concurrency, repeated overwrites, or
// hostile filesystem conditions.
//
// Run with the race detector during pre-commit:
//   go test ./internal/config/ -race -run Stress

func TestStress_ManyOverwritesInSequence(t *testing.T) {
	// 200 sequential overwrites. Each one renames the previous file
	// aside with a timestamped suffix. We expect:
	//   - every write succeeds
	//   - every Load round-trips
	//   - no temp files left behind in the directory
	//   - we DO accumulate backup files (one per overwrite after the first)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := validConfig(t)
	if err := WriteAtomic(path, cfg, WriteOptions{}); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	const n = 200
	for i := 0; i < n; i++ {
		cfg.Runtime.ListenAddr = fmt.Sprintf("127.0.0.1:%d", 10000+i)
		err := WriteAtomic(path, cfg, WriteOptions{
			Overwrite:    true,
			BackupSuffix: ".%s.bak",
		})
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		// Spot-check round-trip every 50 iterations to keep the test
		// fast while still catching corruption.
		if i%50 == 0 {
			loaded, err := Load(path)
			if err != nil {
				t.Fatalf("iter %d Load: %v", i, err)
			}
			want := fmt.Sprintf("127.0.0.1:%d", 10000+i)
			if loaded.Runtime.ListenAddr != want {
				t.Errorf("iter %d: ListenAddr = %q, want %q", i, loaded.Runtime.ListenAddr, want)
			}
		}
	}

	// No temp files leaked.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var tmpLeaks []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") && strings.HasSuffix(e.Name(), ".tmp") {
			tmpLeaks = append(tmpLeaks, e.Name())
		}
	}
	if len(tmpLeaks) > 0 {
		t.Errorf("temp file leaks after %d overwrites: %v", n, tmpLeaks)
	}
}

func TestStress_SameSecondBackupCollision(t *testing.T) {
	// If the wizard is re-run twice within the same UTC second, two
	// overwrites would compute the same backup filename
	// (config.yaml.YYYYMMDD-HHMMSS.bak). os.Rename on POSIX silently
	// replaces the destination — which would destroy the older
	// backup without warning.
	//
	// This test runs ten back-to-back overwrites (all comfortably
	// within one second on any modern machine) and counts how many
	// .bak files survive. If fewer than 10 exist, the writer is
	// clobbering its own backups under rapid re-runs and the user's
	// "you'll always have a backup" promise is broken.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := validConfig(t)
	if err := WriteAtomic(path, cfg, WriteOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const n = 10
	for i := 0; i < n; i++ {
		cfg.Runtime.ListenAddr = fmt.Sprintf("127.0.0.1:%d", 20000+i)
		if err := WriteAtomic(path, cfg, WriteOptions{
			Overwrite:    true,
			BackupSuffix: ".%s.bak",
		}); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var backups []string
	for _, e := range entries {
		if e.Name() == "config.yaml" {
			continue
		}
		if strings.HasPrefix(e.Name(), "config.yaml") && strings.HasSuffix(e.Name(), ".bak") {
			backups = append(backups, e.Name())
		}
	}
	if len(backups) < n {
		t.Errorf("only %d backups survived %d rapid overwrites (want %d): %v",
			len(backups), n, n, backups)
	}
}

func TestStress_ConcurrentWritersSamePath(t *testing.T) {
	// Twenty goroutines hammer the same destination concurrently with
	// Overwrite=true. The contract under contention is loose: we
	// don't promise which write wins, but we DO promise:
	//   1. The destination always parses back as a valid Config
	//      (no half-written YAML).
	//   2. No temp files leak.
	//   3. No goroutine returns an unexpected error.
	//
	// Run under -race to also surface data-race regressions in the
	// writer itself (e.g., if someone adds shared state).
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Seed so Overwrite=true is meaningful from the start.
	if err := WriteAtomic(path, validConfig(t), WriteOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const goroutines = 20
	const writesPerGoroutine = 25

	var wg sync.WaitGroup
	var failed atomic.Uint64
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				cfg := validConfig(t)
				cfg.Runtime.ListenAddr = fmt.Sprintf("127.0.0.1:%d", 30000+id*100+i)
				err := WriteAtomic(path, cfg, WriteOptions{
					Overwrite: true,
					// No backup — we'd otherwise generate a flood of
					// collisions that obscures the concurrency check.
				})
				if err != nil {
					failed.Add(1)
					t.Errorf("goroutine %d iter %d: %v", id, i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	if failed.Load() > 0 {
		t.Fatalf("%d concurrent writes failed", failed.Load())
	}

	// File is parseable after the dust settles.
	if _, err := Load(path); err != nil {
		t.Fatalf("Load after concurrent writes: %v", err)
	}

	// No temp file detritus.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("temp leak: %s", e.Name())
		}
	}
}

func TestStress_AtomicityUnderConcurrentReaders(t *testing.T) {
	// While one goroutine is overwriting in a tight loop, a fleet of
	// readers calls Load() on the same path. The rename-based atomic
	// write guarantees that every reader either sees the old file or
	// the new file — never a half-written one. Any Load error other
	// than "file not present" (which can't happen — we seeded it)
	// is a contract violation.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := WriteAtomic(path, validConfig(t), WriteOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writer: tight loop, ~500 writes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			cfg := validConfig(t)
			cfg.Runtime.ListenAddr = fmt.Sprintf("127.0.0.1:%d", 40000+(i%1000))
			if err := WriteAtomic(path, cfg, WriteOptions{Overwrite: true}); err != nil {
				t.Errorf("writer iter %d: %v", i, err)
				return
			}
		}
		close(stop)
	}()

	// Readers: keep loading until the writer is done.
	const readers = 8
	var readFailed atomic.Uint64
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := Load(path); err != nil {
					readFailed.Add(1)
					t.Errorf("reader Load: %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()
	if readFailed.Load() > 0 {
		t.Fatalf("%d reader failures — atomic rename promise broken", readFailed.Load())
	}
}

func TestStress_RejectsDirectoryAsTarget(t *testing.T) {
	// If the caller points us at an existing directory we should
	// fail cleanly (not, e.g., create a sibling temp file and then
	// try to rename it on top of a directory).
	dir := t.TempDir()
	target := filepath.Join(dir, "iam-a-dir")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err := WriteAtomic(target, validConfig(t), WriteOptions{})
	if err == nil {
		t.Fatal("expected error when target is a directory, got nil")
	}
	// Without Overwrite, the existence check triggers
	// ErrAlreadyExists. Either error mode is acceptable; the
	// important property is that we don't silently succeed and we
	// don't corrupt the directory.
	if !errors.Is(err, ErrAlreadyExists) && !strings.Contains(err.Error(), "rename") &&
		!strings.Contains(err.Error(), "directory") {
		t.Logf("error: %v", err)
	}

	// Directory survives.
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("target gone: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("target was a directory, now it's not: mode=%v", info.Mode())
	}
}

func TestStress_FailsCleanlyOnReadOnlyParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only-dir semantics differ on Windows")
	}
	// Make the parent dir read-only so neither the temp file nor
	// the rename can happen. The writer should return an error and
	// leave no detritus behind.
	dir := t.TempDir()
	subdir := filepath.Join(dir, "ro")
	if err := os.Mkdir(subdir, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Restore permissions on cleanup so t.TempDir can remove it.
	t.Cleanup(func() { _ = os.Chmod(subdir, 0o700) })

	path := filepath.Join(subdir, "config.yaml")
	err := WriteAtomic(path, validConfig(t), WriteOptions{})
	if err == nil {
		t.Fatal("expected error writing into read-only dir, got nil")
	}

	// No temp file should remain in the read-only dir.
	entries, _ := os.ReadDir(subdir)
	for _, e := range entries {
		t.Errorf("unexpected file left behind in ro dir: %s", e.Name())
	}
}

func TestStress_OverwriteReplacesSymlinkTarget(t *testing.T) {
	// If the destination is a symlink, an atomic-rename write
	// REPLACES the symlink (because rename targets the link itself,
	// not the file it points at). That's the contract we want — the
	// user's wizard-written file should be at the configured path,
	// not somewhere they didn't ask for it to land.
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	dir := t.TempDir()
	realFile := filepath.Join(dir, "real.yaml")
	link := filepath.Join(dir, "link.yaml")

	if err := WriteAtomic(realFile, validConfig(t), WriteOptions{}); err != nil {
		t.Fatalf("seed real: %v", err)
	}
	if err := os.Symlink(realFile, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Overwrite via the link path.
	cfg := validConfig(t)
	cfg.Runtime.ListenAddr = "127.0.0.1:55555"
	if err := WriteAtomic(link, cfg, WriteOptions{Overwrite: true}); err != nil {
		t.Fatalf("overwrite via link: %v", err)
	}

	// The link is now a regular file with the new contents.
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("expected link to be replaced by a regular file, still a symlink: %v", info.Mode())
	}

	loaded, err := Load(link)
	if err != nil {
		t.Fatalf("load via link: %v", err)
	}
	if loaded.Runtime.ListenAddr != "127.0.0.1:55555" {
		t.Errorf("listen_addr via link = %q, want 127.0.0.1:55555", loaded.Runtime.ListenAddr)
	}

	// The original file at `real` was NOT modified by the rename,
	// because rename targets the symlink itself.
	originalLoaded, err := Load(realFile)
	if err != nil {
		t.Fatalf("load real: %v", err)
	}
	if originalLoaded.Runtime.ListenAddr == "127.0.0.1:55555" {
		t.Errorf("rename followed the symlink — real file was unexpectedly overwritten")
	}
}

func TestStress_RoundTripPreservesEverything(t *testing.T) {
	// A Config that exercises every section, written and re-loaded
	// repeatedly, should be bit-stable after the first write.
	// (We don't compare bytes — the YAML encoder is allowed to
	// re-order; we compare structurally.)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := validConfig(t)
	cfg.Runtime.ListenAddr = "127.0.0.1:7777"
	cfg.Storage.Config = map[string]any{
		"root":    "/var/lib/loamss/storage",
		"encrypt": true,
		"nested": map[string]any{
			"a": 1,
			"b": "two",
		},
	}
	cfg.Models = []AdapterConfig{
		{Adapter: "model:anthropic", Config: map[string]any{"api_key_env": "ANTHROPIC_API_KEY"}},
		{Adapter: "model:ollama", Config: map[string]any{"endpoint": "http://localhost:11434"}},
	}
	cfg.Routing = []RoutingRule{
		{Task: "summarize", Prefer: "model:anthropic", CostCeiling: 0.5},
	}

	if err := WriteAtomic(path, cfg, WriteOptions{}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Load, write back, load again — five generations.
	for i := 0; i < 5; i++ {
		loaded, err := Load(path)
		if err != nil {
			t.Fatalf("gen %d Load: %v", i, err)
		}
		if loaded.Runtime.ListenAddr != "127.0.0.1:7777" {
			t.Errorf("gen %d: listen_addr drift: %q", i, loaded.Runtime.ListenAddr)
		}
		if len(loaded.Models) != 2 {
			t.Errorf("gen %d: model count drift: %d", i, len(loaded.Models))
		}
		if len(loaded.Routing) != 1 || loaded.Routing[0].Task != "summarize" {
			t.Errorf("gen %d: routing drift: %+v", i, loaded.Routing)
		}

		if err := WriteAtomic(path, loaded, WriteOptions{Overwrite: true}); err != nil {
			t.Fatalf("gen %d write: %v", i, err)
		}
	}
}
