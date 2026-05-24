package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/config"
)

// startListener opens a TCP listener on a free port on localhost and returns
// its address as host:port. The listener is closed via t.Cleanup.
func startListener(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

func TestProbeDaemon_Listening(t *testing.T) {
	addr := startListener(t)

	s := probeDaemon(addr, 500*time.Millisecond)
	if !s.Listening {
		t.Errorf("expected Listening=true for live listener at %s; got %+v", addr, s)
	}
	if s.Reason != "" {
		t.Errorf("expected empty reason for OK probe; got %q", s.Reason)
	}
}

func TestProbeDaemon_ConnectionRefused(t *testing.T) {
	// Allocate a port, then immediately close the listener — the OS will
	// likely refuse subsequent connects to that exact address.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	s := probeDaemon(addr, 500*time.Millisecond)
	if s.Listening {
		t.Errorf("expected Listening=false after closing listener; got %+v", s)
	}
	if s.Reason == "" {
		t.Error("expected a non-empty reason when not listening")
	}
}

func TestProbeDaemon_EmptyAddr(t *testing.T) {
	s := probeDaemon("", time.Second)
	if s.Listening || !strings.Contains(s.Reason, "no listen_addr") {
		t.Errorf("expected no-listen-addr message; got %+v", s)
	}
}

func TestBuildStatusReport_FieldsCopiedFromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Runtime.DataDir = "/tmp/xyz"
	cfg.Runtime.ListenAddr = "127.0.0.1:0"
	cfg.Storage.Adapter = "storage:fs-encrypted"
	cfg.Memory.Adapter = "memory:sqlite-vec"
	cfg.Models = []config.AdapterConfig{
		{Adapter: "model:anthropic"},
		{Adapter: "model:ollama"},
	}

	r := buildStatusReport(cfg, 100*time.Millisecond)
	if r.DataDir != "/tmp/xyz" {
		t.Errorf("DataDir: %q", r.DataDir)
	}
	if r.StorageAdapter != "storage:fs-encrypted" || r.MemoryAdapter != "memory:sqlite-vec" {
		t.Errorf("adapters: %q %q", r.StorageAdapter, r.MemoryAdapter)
	}
	if len(r.ModelAdapters) != 2 || r.ModelAdapters[0] != "model:anthropic" {
		t.Errorf("ModelAdapters: %v", r.ModelAdapters)
	}
}

func TestBuildStatusReport_ListeningDaemon(t *testing.T) {
	addr := startListener(t)

	cfg := config.Default()
	cfg.Runtime.ListenAddr = addr

	r := buildStatusReport(cfg, 500*time.Millisecond)
	if !r.Daemon.Listening {
		t.Errorf("expected Daemon.Listening=true; got %+v", r.Daemon)
	}
}

func TestRenderStatus_HumanReadable(t *testing.T) {
	r := &statusReport{
		ListenAddr:     "127.0.0.1:7777",
		DataDir:        "/tmp/loamss",
		StorageAdapter: "storage:fs-encrypted",
		MemoryAdapter:  "memory:sqlite-vec",
		ModelAdapters:  []string{"model:anthropic"},
		Daemon:         daemonState{Listening: false, Reason: "connection refused"},
	}

	var buf bytes.Buffer
	if err := renderStatus(&buf, r); err != nil {
		t.Fatalf("renderStatus: %v", err)
	}

	for _, want := range []string{
		"Loamss runtime status",
		"Listen address:    127.0.0.1:7777",
		"Data directory:    /tmp/loamss",
		"Storage adapter:   storage:fs-encrypted",
		"Memory adapter:    memory:sqlite-vec",
		"Models:            model:anthropic",
		"not running (connection refused)",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q\nfull:\n%s", want, buf.String())
		}
	}
}

func TestRenderStatus_NoModelsReadsAsNoneConfigured(t *testing.T) {
	r := &statusReport{
		ListenAddr:    "127.0.0.1:7777",
		DataDir:       "/tmp/x",
		ModelAdapters: nil,
		Daemon:        daemonState{Listening: true},
	}
	var buf bytes.Buffer
	if err := renderStatus(&buf, r); err != nil {
		t.Fatalf("renderStatus: %v", err)
	}
	if !strings.Contains(buf.String(), "Models:            (none configured)") {
		t.Errorf("expected '(none configured)' when ModelAdapters is empty\nfull:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "running") {
		t.Errorf("listening should render as 'running'\nfull:\n%s", buf.String())
	}
}

func TestStatusReport_JSONRoundTrip(t *testing.T) {
	r := &statusReport{
		ListenAddr:    "127.0.0.1:7777",
		DataDir:       "/tmp",
		ModelAdapters: []string{"model:openai", "model:anthropic"},
		Daemon:        daemonState{Listening: false, Reason: "connection refused"},
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var rt statusReport
	if err := json.Unmarshal(data, &rt); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, data)
	}
	if rt.ListenAddr != r.ListenAddr || rt.Daemon.Listening != r.Daemon.Listening {
		t.Errorf("round-trip mismatch: %+v vs %+v", rt, r)
	}
}
