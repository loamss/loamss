package capsule

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The subprocess tests use the classic Go helper-process pattern:
// the test binary re-execs itself with GO_CAPSULE_HELPER=<mode>
// and TestMain dispatches that env var to a tiny fake-capsule
// behavior. This avoids dependence on /bin/sh or external scripts
// (which disappear under restricted CI environments) and keeps
// everything in-tree.

// TestMain dispatches early to helper modes before the regular
// testing.M is invoked. Modes:
//
//	echo:         reads lines from stdin, writes "echo: <line>"
//	              to stdout. Exits on EOF.
//	noop:         drains stdin to EOF, exits 0 immediately.
//	crash:        exits with code 7 immediately.
//	ignore-term:  traps SIGTERM and drops it; only SIGKILL stops it.
//	stderr-noise: writes 3 lines to stderr, drains stdin, exits 0.
func TestMain(m *testing.M) {
	switch os.Getenv("GO_CAPSULE_HELPER") {
	case "echo":
		runEchoHelper()
		return
	case "noop":
		runNoopHelper()
		return
	case "crash":
		os.Exit(7)
	case "ignore-term":
		runIgnoreTermHelper()
		return
	case "stderr-noise":
		runStderrNoiseHelper()
		return
	}
	os.Exit(m.Run())
}

func runEchoHelper() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		_, _ = fmt.Fprintf(os.Stdout, "echo: %s\n", scanner.Text())
	}
	os.Exit(0)
}

func runNoopHelper() {
	// Exit immediately — don't read stdin. Tests that call Wait
	// without first closing stdin would deadlock if we drained.
	os.Exit(0)
}

func runIgnoreTermHelper() {
	// Trap SIGTERM and drop it on the floor. Only SIGKILL stops
	// us — which is exactly what TestProcess_StopEscalatesToKill
	// exercises.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM)
	go func() {
		//nolint:revive // empty body: we deliberately drop each signal
		for range sigCh {
		}
	}()
	// Announce readiness on stdout so the parent test can wait for
	// the trap to be installed before sending SIGTERM. Without this
	// the test races: a SIGTERM that arrives before signal.Notify
	// completes kills the process by default.
	_, _ = fmt.Fprintln(os.Stdout, "ready")
	for {
		time.Sleep(time.Second)
	}
}

func runStderrNoiseHelper() {
	for i := 1; i <= 3; i++ {
		_, _ = fmt.Fprintf(os.Stderr, "warn line %d\n", i)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// fakeCapsule builds an Installed record whose Entrypoint re-execs
// the test binary. The actual helper mode is chosen via the env
// var GO_CAPSULE_HELPER which the caller sets with t.Setenv before
// calling Start.
func fakeCapsule(t *testing.T) Installed {
	t.Helper()
	testBin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	m := &Manifest{
		SpecVersion: "0.1",
		Name:        "test-capsule",
		Version:     "0.0.1",
		Author:      Author{Name: "test"},
		Runtime: RuntimeSpec{
			Type:       "subprocess",
			Entrypoint: []string{testBin, "-test.run=ZzzNoSuchTest"},
			Protocol:   "mcp",
		},
	}
	return Installed{
		Name:        m.Name,
		Version:     m.Version,
		Manifest:    m,
		InstallPath: dir,
	}
}

// runHelper builds a Process whose subprocess will behave per the
// named helper mode. Sets GO_CAPSULE_HELPER on the parent so the
// child inherits it; t.Setenv handles cleanup.
func runHelper(t *testing.T, mode string) *Process {
	t.Helper()
	t.Setenv("GO_CAPSULE_HELPER", mode)
	return NewProcess(fakeCapsule(t), slog.Default())
}

func TestProcess_StateMachine(t *testing.T) {
	p := runHelper(t, "noop")
	if p.State() != StateCreated {
		t.Errorf("initial state: got %s, want created", p.State())
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if p.State() != StateExited {
		t.Errorf("final state: got %s, want exited", p.State())
	}
	if p.ExitErr() != nil {
		t.Errorf("noop helper should exit cleanly, got: %v", p.ExitErr())
	}
}

func TestProcess_StdinStdoutEcho(t *testing.T) {
	p := runHelper(t, "echo")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop(context.Background()) }()

	stdin := p.Stdin()
	stdout := p.Stdout()
	if stdin == nil || stdout == nil {
		t.Fatal("stdin/stdout should be non-nil after Start")
	}

	if _, err := fmt.Fprintln(stdin, "hello"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if strings.TrimRight(line, "\n") != "echo: hello" {
		t.Errorf("echo: got %q, want %q", line, "echo: hello")
	}
}

func TestProcess_WaitWithoutStart(t *testing.T) {
	p := NewProcess(fakeCapsule(t), slog.Default())
	err := p.Wait()
	if err == nil || !strings.Contains(err.Error(), "Wait called before Start") {
		t.Errorf("expected pre-Start Wait error, got: %v", err)
	}
}

func TestProcess_StartTwiceIsIdempotent(t *testing.T) {
	p := runHelper(t, "noop")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Errorf("second Start should be a no-op, got: %v", err)
	}
	_ = p.Wait()
}

func TestProcess_StopAfterClean(t *testing.T) {
	p := runHelper(t, "noop")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Errorf("Stop after exit should be a no-op: %v", err)
	}
}

func TestProcess_StopGracefulOnEchoCapsule(t *testing.T) {
	p := runHelper(t, "echo")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Errorf("graceful Stop: %v", err)
	}
	if p.State() != StateExited {
		t.Errorf("state after Stop: got %s, want exited", p.State())
	}
}

func TestProcess_StopEscalatesToKill(t *testing.T) {
	if testing.Short() {
		t.Skip("skip slow kill-escalation test in -short mode")
	}
	p := runHelper(t, "ignore-term")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait for the helper to announce it has installed its SIGTERM
	// trap. Otherwise SIGTERM arrives before signal.Notify completes
	// and the process dies by default, masking the test's intent.
	ready := make(chan string, 1)
	go func() {
		r := bufio.NewReader(p.Stdout())
		line, _ := r.ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if !strings.Contains(line, "ready") {
			t.Fatalf("expected 'ready' from helper, got %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper did not signal ready within 2s")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := p.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}
	if p.State() != StateExited {
		t.Errorf("state after SIGKILL: got %s, want exited", p.State())
	}
}

func TestProcess_CrashSurfacesExitError(t *testing.T) {
	p := runHelper(t, "crash")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err := p.Wait()
	if err == nil {
		t.Fatal("expected non-nil exit error for code 7")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 7 {
		t.Errorf("exit code: got %d, want 7", exitErr.ExitCode())
	}
}

func TestProcess_StderrDrainedToLogger(t *testing.T) {
	c := fakeCapsule(t)
	captured := newCaptureLogger()
	p := NewProcess(c, captured.logger)
	t.Setenv("GO_CAPSULE_HELPER", "stderr-noise")
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = p.Stdin().Close() // tell helper to exit
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// Drain goroutine flushes after the pipe closes; give it a tick.
	time.Sleep(50 * time.Millisecond)

	stderrLines := 0
	for _, l := range captured.lines() {
		if strings.Contains(l, "capsule stderr") && strings.Contains(l, "warn line") {
			stderrLines++
		}
	}
	if stderrLines != 3 {
		t.Errorf("expected 3 stderr lines logged, got %d", stderrLines)
	}
}

func TestProcess_FailedSpawn(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{
		SpecVersion: "0.1", Name: "bad", Version: "0.0.1",
		Author: Author{Name: "test"},
		Runtime: RuntimeSpec{
			Type:       "subprocess",
			Entrypoint: []string{filepath.Join(dir, "no-such-binary")},
			Protocol:   "mcp",
		},
	}
	c := Installed{Name: "bad", Version: "0.0.1", Manifest: m, InstallPath: dir}
	p := NewProcess(c, slog.Default())
	if err := p.Start(context.Background()); err == nil {
		t.Fatal("expected error spawning nonexistent binary")
	}
	if p.State() != StateFailed {
		t.Errorf("state after failed spawn: got %s, want failed", p.State())
	}
}

// --- captureLogger -----------------------------------------------------

// captureLogger collects every emitted log line into an in-memory
// buffer the test can assert on.
type captureLogger struct {
	mu     sync.Mutex
	buf    strings.Builder
	logger *slog.Logger
}

func newCaptureLogger() *captureLogger {
	c := &captureLogger{}
	c.logger = slog.New(slog.NewTextHandler(&syncWriter{c: c}, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	return c
}

func (c *captureLogger) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Split(c.buf.String(), "\n")
}

// syncWriter is the slog.TextHandler's writer; serializes writes
// against captureLogger.mu so reads from .lines() don't race with
// the stderr drain goroutine.
type syncWriter struct{ c *captureLogger }

func (w *syncWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.c.buf.Write(p)
}
