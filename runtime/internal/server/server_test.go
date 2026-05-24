package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// silentLogger returns a slog.Logger that discards everything. Tests use
// this to avoid noisy stderr output.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startServerAt opens a listener on a random localhost port, starts the
// server on it, and returns the base URL plus a stop function the test
// can defer.
func startServerAt(t *testing.T, version string) (baseURL string, srv *Server, stop func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	srv = New(Options{
		Addr:    l.Addr().String(),
		Logger:  silentLogger(),
		Version: version,
	})

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(l) }()

	stop = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		if err := <-errCh; err != nil {
			t.Errorf("Serve returned error: %v", err)
		}
	}

	return "http://" + l.Addr().String(), srv, stop
}

func TestHealthz_ReturnsOKWithVersion(t *testing.T) {
	baseURL, _, stop := startServerAt(t, "v0.1-test")
	defer stop()

	resp, err := http.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q", ct)
	}

	var body HealthzResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status field: got %q, want %q", body.Status, "ok")
	}
	if body.Version != "v0.1-test" {
		t.Errorf("version field: got %q, want %q", body.Version, "v0.1-test")
	}
}

func TestVersion_Endpoint(t *testing.T) {
	baseURL, _, stop := startServerAt(t, "v0.1-test")
	defer stop()

	resp, err := http.Get(baseURL + "/version")
	if err != nil {
		t.Fatalf("GET /version: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != "v0.1-test" {
		t.Errorf("body: got %q, want %q", string(body), "v0.1-test")
	}
}

func TestUnknownRoute_Returns404(t *testing.T) {
	baseURL, _, stop := startServerAt(t, "v0.1-test")
	defer stop()

	resp, err := http.Get(baseURL + "/no/such/route")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", resp.StatusCode)
	}
}

func TestShutdown_GracefulNoError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	srv := New(Options{Addr: l.Addr().String(), Logger: silentLogger(), Version: "v"})

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(l) }()

	// Give Serve a moment to install the listener.
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Errorf("Serve returned error: %v", err)
	}
}

func TestListenAndServe_FailsOnBadAddress(t *testing.T) {
	srv := New(Options{
		Addr:    "not.a.host:notaport",
		Logger:  silentLogger(),
		Version: "v",
	})
	err := srv.ListenAndServe()
	if err == nil {
		t.Fatal("expected error for invalid addr, got nil")
	}
}
