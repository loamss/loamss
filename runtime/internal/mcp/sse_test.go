package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWriteSSEEvent_ShapesCorrectly(t *testing.T) {
	var buf bytes.Buffer
	if err := writeSSEEvent(&buf, "hello", helloPayload{
		Server: "loamss", Version: "v0.1-test", ProtocolVersion: "2025-03-26",
	}); err != nil {
		t.Fatalf("writeSSEEvent: %v", err)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "event: hello\n") {
		t.Errorf("missing or wrong event line: %q", got)
	}
	if !strings.Contains(got, "data: ") {
		t.Errorf("missing data line: %q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("SSE event must terminate with blank line: %q", got)
	}
	// Extract the JSON portion and round-trip it.
	dataLine := strings.SplitN(strings.TrimPrefix(got, "event: hello\n"), "\n", 2)[0]
	payload := strings.TrimPrefix(dataLine, "data: ")
	var hp helloPayload
	if err := json.Unmarshal([]byte(payload), &hp); err != nil {
		t.Fatalf("data line is not valid JSON: %v\n%q", err, payload)
	}
	if hp.Server != "loamss" || hp.ProtocolVersion != "2025-03-26" {
		t.Errorf("payload round-trip: %+v", hp)
	}
}

func TestWriteSSEEvent_HandlesNewlinesInPayload(t *testing.T) {
	// SSE spec: a payload containing a newline must be emitted as
	// multiple `data:` lines. We test by handing in a payload with
	// an embedded newline.
	var buf bytes.Buffer
	payload := map[string]any{"multi": "line\nstring"}
	if err := writeSSEEvent(&buf, "test", payload); err != nil {
		t.Fatalf("writeSSEEvent: %v", err)
	}
	got := buf.String()
	// The encoded JSON contains \n inside the string ("line\nstring");
	// our writer should NOT split that, because Go's json.Marshal
	// escapes it as \n in the JSON literal. We do split if a literal
	// newline byte was in the encoded body — that path is only
	// reachable if json.Marshal emits multi-line output (it doesn't).
	// So this test pins that single-line JSON stays single-line.
	if strings.Count(got, "data: ") != 1 {
		t.Errorf("expected one data: line for escaped-newline payload, got: %q", got)
	}
}
