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
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Process represents a single running (or runnable) capsule subprocess.
// It's a thin lifecycle wrapper around os/exec.Cmd with the pieces
// the MCP-over-stdio transport (next commit) and the capsule host
// supervisor (subsequent commit) actually need:
//
//   - bounded startup
//   - stdin/stdout pipes accessible to the transport layer
//   - stderr drained to a logger (capsules tend to spam stderr;
//     we log structured lines instead of letting it interleave
//     with the runtime's own stderr)
//   - graceful shutdown (SIGTERM, then SIGKILL after a deadline)
//   - exit observation via Wait
//
// Process is single-use: once it has exited, construct a new one to
// run the capsule again. This matches os/exec.Cmd semantics and
// keeps the state machine small.
type Process struct {
	// capsule is the installed record this process executes. Used
	// to resolve the entrypoint and to label log lines.
	capsule Installed

	// logger receives structured lines for stderr drain + lifecycle
	// transitions. Required.
	logger *slog.Logger

	// cmd is the live exec.Cmd, set during Start. Nil before Start.
	cmd *exec.Cmd

	// stdin / stdout pipes the transport layer reads/writes.
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// state is the current lifecycle state. atomic so observers
	// (HealthCheck callers, supervisor loops) can read without a
	// lock. The state machine progresses one-way: Created →
	// Starting → Running → Stopping → Exited (or Created → Failed
	// if Start itself errors).
	state atomic.Int32

	// exitErr is set after Wait returns. Captured so callers can
	// retrieve it after the fact without re-running Wait.
	exitErr   error
	exitedCh  chan struct{} // closed when the process exits
	exitOnce  sync.Once
	startOnce sync.Once
}

// State is the lifecycle phase of a Process.
type State int32

// State values. Stored as int32 in an atomic.Int32 for lock-free reads.
const (
	StateCreated State = iota
	StateStarting
	StateRunning
	StateStopping
	StateExited
	StateFailed
)

// String renders the State for log lines and error messages.
func (s State) String() string {
	switch s {
	case StateCreated:
		return "created"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateExited:
		return "exited"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// NewProcess constructs a Process for the given installed capsule.
// Does not spawn anything; the caller must call Start.
func NewProcess(c Installed, logger *slog.Logger) *Process {
	if logger == nil {
		// Defensive: a nil logger would crash inside drainStderr.
		// Subsystems should always pass a real logger.
		logger = slog.Default()
	}
	return &Process{
		capsule:  c,
		logger:   logger,
		exitedCh: make(chan struct{}),
	}
}

// State returns the current lifecycle state. Safe for concurrent
// callers; reads without a lock.
func (p *Process) State() State { return State(p.state.Load()) }

// Capsule returns the installed record this process executes.
// Exposed for the supervisor + transport which need the capsule's
// name and manifest for routing and audit emission.
func (p *Process) Capsule() Installed { return p.capsule }

// Stdin returns the pipe the transport layer writes to. Valid only
// after Start; returns nil otherwise.
func (p *Process) Stdin() io.WriteCloser { return p.stdin }

// Stdout returns the pipe the transport layer reads from. Valid
// only after Start; returns nil otherwise.
func (p *Process) Stdout() io.ReadCloser { return p.stdout }

// ExitedCh returns a channel that closes when the process exits.
// Supervisors select on this to react to crashes.
func (p *Process) ExitedCh() <-chan struct{} { return p.exitedCh }

// ExitErr returns the exit error captured by Wait. Nil if the
// process exited cleanly, or if Wait hasn't completed yet.
func (p *Process) ExitErr() error { return p.exitErr }

// Start spawns the subprocess. The entrypoint comes from the
// capsule's manifest; cwd is set to the capsule's install path so
// relative paths in the entrypoint (e.g., "code/server.js") resolve
// against the installed tree.
//
// Idempotent: a second Start call after the first succeeded returns
// nil without re-spawning. A second Start after Failed returns an
// error — construct a new Process instead.
//
// The caller is expected to follow Start with either Wait (blocking)
// or use ExitedCh + Stop (async). Stderr is drained into the
// configured logger in a goroutine that exits when the pipe closes.
func (p *Process) Start(ctx context.Context) error {
	var startErr error
	p.startOnce.Do(func() {
		startErr = p.startLocked(ctx)
	})
	return startErr
}

func (p *Process) startLocked(ctx context.Context) error {
	if !p.state.CompareAndSwap(int32(StateCreated), int32(StateStarting)) {
		return fmt.Errorf("capsule: cannot Start from state %s", p.State())
	}

	entry := p.capsule.Manifest.Runtime.Entrypoint
	if len(entry) == 0 {
		p.state.Store(int32(StateFailed))
		return errors.New("capsule: manifest entrypoint is empty (should have been caught at install time)")
	}

	cmd := exec.CommandContext(ctx, entry[0], entry[1:]...) //nolint:gosec // entrypoint comes from a validated manifest
	cmd.Dir = p.capsule.InstallPath
	// Inherit the parent environment for now. Sandboxing (network
	// namespaces, restricted env, etc.) is the Phase 5 concern.
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		p.state.Store(int32(StateFailed))
		return fmt.Errorf("capsule: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		p.state.Store(int32(StateFailed))
		return fmt.Errorf("capsule: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		p.state.Store(int32(StateFailed))
		return fmt.Errorf("capsule: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		p.state.Store(int32(StateFailed))
		return fmt.Errorf("capsule: spawn %q: %w", entry[0], err)
	}

	p.cmd = cmd
	p.stdin = stdin
	p.stdout = stdout
	p.state.Store(int32(StateRunning))
	p.logger.Info("capsule: started",
		"name", p.capsule.Name,
		"version", p.capsule.Version,
		"pid", cmd.Process.Pid,
	)

	// Drain stderr into the logger. Closes when the pipe closes
	// (capsule exits or closes its stderr).
	go p.drainStderr(stderr)

	// Watch the process for exit and signal observers via exitedCh.
	go p.watchExit()

	return nil
}

// drainStderr reads stderr line-by-line and forwards each line to
// the logger at the debug level. Capsules tend to write diagnostic
// text to stderr; aggregating it as structured log lines keeps it
// separable from the runtime's own stderr.
func (p *Process) drainStderr(r io.ReadCloser) {
	defer func() { _ = r.Close() }()
	scanner := bufio.NewScanner(r)
	// Capsules may emit large stack traces; bump the buffer.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		p.logger.Debug("capsule stderr",
			"name", p.capsule.Name,
			"line", scanner.Text(),
		)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		p.logger.Warn("capsule stderr drain ended with error",
			"name", p.capsule.Name,
			"err", err,
		)
	}
}

// watchExit calls cmd.Wait in a goroutine, stores the error, and
// closes exitedCh. Idempotent — Wait can be called by the
// supervisor concurrently and they'll both see the same exitErr.
func (p *Process) watchExit() {
	err := p.cmd.Wait()
	p.exitOnce.Do(func() {
		p.exitErr = err
		// Move to Exited unless we were already moving to Stopping;
		// in that case the next Stop call will transition through
		// Exited explicitly when it acknowledges the death.
		p.state.CompareAndSwap(int32(StateRunning), int32(StateExited))
		p.state.CompareAndSwap(int32(StateStopping), int32(StateExited))
		close(p.exitedCh)
	})
	if err != nil {
		p.logger.Info("capsule: exited with error",
			"name", p.capsule.Name,
			"err", err,
		)
	} else {
		p.logger.Info("capsule: exited cleanly",
			"name", p.capsule.Name,
		)
	}
}

// Wait blocks until the process exits and returns the exit error.
// Safe to call multiple times — subsequent calls return the cached
// error from the first observer.
//
// Waiting on a process that hasn't been Started returns an error
// without blocking.
func (p *Process) Wait() error {
	switch p.State() {
	case StateCreated:
		return errors.New("capsule: Wait called before Start")
	case StateFailed:
		return errors.New("capsule: Wait called on failed process")
	}
	<-p.exitedCh
	return p.exitErr
}

// Stop initiates graceful shutdown: closes stdin (signals EOF, the
// canonical way an MCP server detects end-of-session), then sends
// SIGTERM, then waits for the process to exit up to the context
// deadline. If the deadline elapses, sends SIGKILL.
//
// Idempotent: stopping an already-exited process is a no-op.
//
// The state transitions are Running → Stopping → Exited. If the
// process exits on its own (between our state read and signal),
// we still ride through Stopping and the watchExit goroutine moves
// us to Exited correctly.
func (p *Process) Stop(ctx context.Context) error {
	state := p.State()
	if state == StateExited || state == StateFailed || state == StateCreated {
		return nil
	}
	// Move into Stopping if we won the CAS. Tolerate the race when
	// another caller already moved us out of Running — both Stop
	// callers end up waiting on the same exitedCh.
	p.state.CompareAndSwap(int32(StateRunning), int32(StateStopping))

	// Close stdin to signal EOF. Well-behaved MCP capsules exit on
	// stdin close.
	if p.stdin != nil {
		_ = p.stdin.Close()
	}

	// SIGTERM in case stdin close alone isn't enough.
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}

	// Wait for exit, bounded by ctx.
	select {
	case <-p.exitedCh:
		return nil
	case <-ctx.Done():
		// Deadline elapsed; escalate.
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		// Give the kernel a brief moment to deliver SIGKILL.
		select {
		case <-p.exitedCh:
		case <-time.After(2 * time.Second):
			return fmt.Errorf("capsule: %s did not exit after SIGKILL", p.capsule.Name)
		}
		return ctx.Err()
	}
}
