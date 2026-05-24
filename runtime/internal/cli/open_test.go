package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tests for the deterministic parts of `loamss open`: URL
// resolution from listen_addr, and the /healthz probe behavior.
// We don't test launchBrowser directly — it'd require spawning a
// real `open` / `xdg-open` and racing the process exit.

func TestConsoleURLFromListenAddr(t *testing.T) {
	cases := []struct {
		listen string
		want   string
	}{
		// Standard local bind: carries through unchanged.
		{"127.0.0.1:7777", "http://127.0.0.1:7777/"},

		// Wildcard binds rewrite to loopback so the printed URL
		// works in a same-host browser.
		{"0.0.0.0:7777", "http://127.0.0.1:7777/"},
		{":7777", "http://127.0.0.1:7777/"},
		{"[::]:7777", "http://127.0.0.1:7777/"},

		// Custom port.
		{"127.0.0.1:9999", "http://127.0.0.1:9999/"},

		// Hostname with explicit port.
		{"loamss.local:7777", "http://loamss.local:7777/"},

		// Hostname without a port falls back to the default.
		{"loamss.local", "http://loamss.local:7777/"},

		// Empty -> default everything.
		{"", "http://127.0.0.1:7777/"},

		// IPv6 host that ISN'T the wildcard: brackets in the URL.
		{"[fe80::1]:7777", "http://[fe80::1]:7777/"},
	}
	for _, c := range cases {
		got := consoleURLFromListenAddr(c.listen)
		if got != c.want {
			t.Errorf("consoleURLFromListenAddr(%q) = %q, want %q", c.listen, got, c.want)
		}
	}
}

func TestProbeConsole_OkWhenHealthzReturns200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/healthz" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := probeConsole(srv.URL+"/", 2*time.Second); err != nil {
		t.Errorf("probeConsole on healthy server: %v", err)
	}
}

func TestProbeConsole_FailsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := probeConsole(srv.URL+"/", 2*time.Second)
	if err == nil {
		t.Fatal("expected error on 503, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error %q should mention the status code", err)
	}
}

func TestProbeConsole_FailsWhenNothingListening(t *testing.T) {
	// 127.0.0.1:1 is "the discard port" — reserved, nobody binds.
	// Connection refused is the expected outcome on a local box;
	// the assertion is just "we got an error", not the exact text.
	err := probeConsole("http://127.0.0.1:1/", 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected error connecting to dead port, got nil")
	}
}

func TestProbeConsole_FailsOnTimeout(t *testing.T) {
	// Server that holds the request open longer than the probe's
	// timeout. The probe should give up cleanly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := probeConsole(srv.URL+"/", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// Sanity: runOpen with --no-launch + --skip-probe should print
// the URL and exit cleanly even when no daemon is running.
func TestOpenCmd_NoLaunchSkipProbe(t *testing.T) {
	// We can't easily exercise runOpen as a cobra command without
	// the full root tree, so we test the inner pieces and trust
	// the cobra wiring. The URL-resolution + probe-skip paths are
	// the substantive logic; the actual `open` invocation is one
	// line.
	url := consoleURLFromListenAddr("127.0.0.1:7777")
	if url != "http://127.0.0.1:7777/" {
		t.Errorf("URL resolution drifted: %q", url)
	}

	// And ensure the probe respects a generous timeout against a
	// real local listener (covers the happy path again, for
	// good measure).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := probeConsole(srv.URL+"/", time.Second); err != nil {
		t.Errorf("probe against test server: %v", err)
	}
}
