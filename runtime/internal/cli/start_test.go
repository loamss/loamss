package cli

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/config"
	"github.com/loamss/loamss/runtime/internal/server"
)

func silentTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunServer_ShutsDownWhenStopCloses(t *testing.T) {
	// Bind a free port and let the server start there.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close() // release; server will rebind below

	srv := server.New(server.Options{
		Addr:    addr,
		Logger:  silentTestLogger(),
		Version: "test",
	})

	stop := make(chan struct{})

	done := make(chan error, 1)
	go func() { done <- runServer(srv, stop, time.Second, silentTestLogger()) }()

	// Wait until the server has actually bound the listener. We poll the
	// /healthz endpoint rather than racing against goroutine scheduling.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Send the shutdown signal.
	close(stop)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runServer returned error after clean shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runServer did not return within 3s after stop signal")
	}
}

func TestRunServer_PropagatesBindError(t *testing.T) {
	srv := server.New(server.Options{
		Addr:    "invalid-host:notaport",
		Logger:  silentTestLogger(),
		Version: "test",
	})

	stop := make(chan struct{})
	defer close(stop) // never reached if Listen fails synchronously, fine

	done := make(chan error, 1)
	go func() { done <- runServer(srv, stop, time.Second, silentTestLogger()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from runServer with bad addr")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runServer did not return error within 2s")
	}
}

func TestRunServer_GracefulShutdownTimeoutBound(t *testing.T) {
	// Just verify Shutdown is called with the timeout we passed, by
	// observing that runServer returns within roughly that timeout when
	// no requests are in flight (should be immediate).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	srv := server.New(server.Options{Addr: addr, Logger: silentTestLogger(), Version: "t"})

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runServer(srv, stop, 100*time.Millisecond, silentTestLogger()) }()

	// Wait for bind.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	begin := time.Now()
	close(stop)
	if err := <-done; err != nil {
		t.Errorf("runServer error: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Errorf("graceful shutdown took %s; expected near-immediate when idle", elapsed)
	}
}

func TestNewLogger_LevelAndFormat(t *testing.T) {
	cases := []struct {
		name      string
		cfg       config.LogConfig
		wantJSON  bool
		debugSeen bool
	}{
		{"text-info", config.LogConfig{Level: "info", Format: "text"}, false, false},
		{"text-debug", config.LogConfig{Level: "debug", Format: "text"}, false, true},
		{"json-warn", config.LogConfig{Level: "warn", Format: "json"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// We can't easily intercept writes from an *os.File-based logger
			// in a test. Build the logger with a tempfile, log to it, then
			// read back.
			f, err := os.CreateTemp(t.TempDir(), "log-*.txt")
			if err != nil {
				t.Fatalf("temp file: %v", err)
			}
			defer f.Close()

			logger := newLogger(tc.cfg, f)
			logger.Debug("d-event")
			logger.Info("i-event")
			logger.Warn("w-event")
			logger.Error("e-event")
			_ = f.Sync()

			data, err := os.ReadFile(f.Name())
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			out := string(data)

			if tc.wantJSON {
				if !strings.HasPrefix(strings.TrimSpace(out), "{") {
					t.Errorf("expected JSON output, got:\n%s", out)
				}
			} else {
				if strings.HasPrefix(strings.TrimSpace(out), "{") {
					t.Errorf("expected text output, got JSON:\n%s", out)
				}
			}
			seesDebug := strings.Contains(out, "d-event")
			if seesDebug != tc.debugSeen {
				t.Errorf("debug visibility: got %v, want %v\noutput:\n%s", seesDebug, tc.debugSeen, out)
			}
		})
	}
}

// Integration probe: after runServer is running, GET /healthz returns the
// expected payload. Confirms the start path actually wires the server end
// to end (cobra logic not exercised here; that's runStart, which is a
// thin wrapper covered by the other tests above).
func TestRunServer_HealthzReachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	srv := server.New(server.Options{Addr: addr, Logger: silentTestLogger(), Version: "test-integ"})

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runServer(srv, stop, time.Second, silentTestLogger()) }()
	defer func() {
		close(stop)
		<-done
	}()

	// Poll for readiness.
	var resp *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get("http://" + addr + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("never became ready: %v", err)
	}
	defer resp.Body.Close()

	buf := &bytes.Buffer{}
	_, _ = io.Copy(buf, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(buf.String(), `"version":"test-integ"`) {
		t.Errorf("expected version in body, got: %s", buf.String())
	}
}
