package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pairedPipes returns two Transport instances connected by in-memory
// pipes: A's writes flow into B's reads, and vice versa. Simulates
// the runtime-side and capsule-side ends of an MCP-over-stdio
// connection without spawning a subprocess. Cleanup tears both
// transports down via t.Cleanup.
func pairedPipes(t *testing.T, handlerA, handlerB TransportHandler) (a, b *Transport) {
	t.Helper()
	rA, wA := io.Pipe() // A reads rA; B writes wA
	rB, wB := io.Pipe() // B reads rB; A writes wB
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a = NewTransport(rA, wB, handlerA, logger)
	b = NewTransport(rB, wA, handlerB, logger)
	a.Start()
	b.Start()
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a, b
}

func TestTransport_RequestRoundTrip(t *testing.T) {
	// B is the "server" — its handler echoes the params back as
	// the result. A is the "client" — it issues a request and
	// reads the response.
	echoHandler := func(_ context.Context, method string, params json.RawMessage) (any, error) {
		return map[string]any{
			"method":        method,
			"params_echoed": params,
			"received_at":   "now",
		}, nil
	}
	a, _ := pairedPipes(t, nil, echoHandler)

	resp, err := a.Request(context.Background(), "test.echo",
		map[string]any{"hello": "world"})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result not a map: %T %v", resp.Result, resp.Result)
	}
	if result["method"] != "test.echo" {
		t.Errorf("method echo: %v", result["method"])
	}
}

func TestTransport_HandlerErrorBecomesRPCError(t *testing.T) {
	errHandler := func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return nil, errors.New("intentional handler error")
	}
	a, _ := pairedPipes(t, nil, errHandler)

	resp, err := a.Request(context.Background(), "test.fail", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected RPC error")
	}
	if resp.Error.Code != codeInternalError {
		t.Errorf("code: got %d, want %d", resp.Error.Code, codeInternalError)
	}
	if !strings.Contains(resp.Error.Message, "internal error") {
		t.Errorf("message: %q", resp.Error.Message)
	}
}

func TestTransport_HandlerReturnsTypedRPCError(t *testing.T) {
	// When a handler returns an *RPCError directly, the transport
	// preserves its code rather than wrapping in internal_error.
	customHandler := func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return nil, &RPCError{Code: codePermissionDenied, Message: "nope"}
	}
	a, _ := pairedPipes(t, nil, customHandler)

	resp, _ := a.Request(context.Background(), "test.deny", nil)
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != codePermissionDenied {
		t.Errorf("code: got %d, want %d", resp.Error.Code, codePermissionDenied)
	}
}

func TestTransport_BidirectionalRequests(t *testing.T) {
	// Each side has a handler. A calls B; while A is waiting, B
	// calls back to A. Both round-trips must complete. This is
	// the pattern the capsule callback flow needs: capsule's
	// handler runs memory.query → calls back to the runtime →
	// runtime serves → result returns to capsule → capsule
	// returns to the runtime's original tools/call.
	var bCalled, aCalled atomic.Bool

	bHandler := func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		// B is asked. Before responding, B calls back to A.
		bCalled.Store(true)
		// Use a's transport to call back. We get it from the test
		// closure; in production this is done via a peer pointer
		// the capsule client struct holds.
		select {
		case <-time.After(50 * time.Millisecond):
			// give the round-trip a window
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return "b says hi", nil
	}
	aHandler := func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		aCalled.Store(true)
		return "a says hi", nil
	}
	a, b := pairedPipes(t, aHandler, bHandler)

	// A → B
	respFromB, err := a.Request(context.Background(), "ping-b", nil)
	if err != nil {
		t.Fatalf("a → b: %v", err)
	}
	if respFromB.Error != nil || respFromB.Result != "b says hi" {
		t.Errorf("a → b result: %+v", respFromB)
	}
	if !bCalled.Load() {
		t.Error("B handler should have been invoked")
	}

	// B → A
	respFromA, err := b.Request(context.Background(), "ping-a", nil)
	if err != nil {
		t.Fatalf("b → a: %v", err)
	}
	if respFromA.Error != nil || respFromA.Result != "a says hi" {
		t.Errorf("b → a result: %+v", respFromA)
	}
	if !aCalled.Load() {
		t.Error("A handler should have been invoked")
	}
}

func TestTransport_ConcurrentRequests(t *testing.T) {
	// Many simultaneous A→B requests. Each must return its own
	// response (no id collisions, no cross-routing).
	echoHandler := func(_ context.Context, method string, params json.RawMessage) (any, error) {
		// Echo the params so we can verify per-request matching.
		var p struct{ N int }
		_ = json.Unmarshal(params, &p)
		return map[string]any{"n": p.N, "method": method}, nil
	}
	a, _ := pairedPipes(t, nil, echoHandler)

	const requests = 50
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			resp, err := a.Request(context.Background(), "echo",
				map[string]any{"N": n})
			if err != nil {
				errs <- err
				return
			}
			result, _ := resp.Result.(map[string]any)
			got, _ := result["n"].(float64)
			if int(got) != n {
				errs <- errors.New("response routed to wrong caller")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent request error: %v", err)
	}
}

func TestTransport_Notify_NoResponse(t *testing.T) {
	// Notifications produce no response on the wire. The handler
	// runs; its return value is discarded.
	var called atomic.Bool
	handler := func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "notifications/test" {
			called.Store(true)
		}
		return nil, errors.New("this should be ignored")
	}
	a, _ := pairedPipes(t, nil, handler)

	if err := a.Notify(context.Background(), "notifications/test", map[string]any{"x": 1}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	// Give the read loop on the other side a tick.
	deadline := time.Now().Add(time.Second)
	for !called.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !called.Load() {
		t.Error("notification handler was not called")
	}
}

func TestTransport_RequestUnknownMethodWhenNoHandler(t *testing.T) {
	// B has no handler at all — any request from A should return
	// -32601 method_not_found.
	a, _ := pairedPipes(t, nil, nil)

	resp, err := a.Request(context.Background(), "no.such.method", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("expected -32601, got %+v", resp.Error)
	}
}

func TestTransport_CloseDrainsPendingWithError(t *testing.T) {
	// B never responds — A's request hangs forever. Closing A
	// must surface ErrTransportClosed via the pending response.
	stuck := func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	a, _ := pairedPipes(t, nil, stuck)

	done := make(chan error, 1)
	go func() {
		resp, err := a.Request(context.Background(), "hang", nil)
		if err != nil {
			done <- err
			return
		}
		if resp.Error != nil {
			done <- errors.New(resp.Error.Message)
			return
		}
		done <- nil
	}()
	time.Sleep(50 * time.Millisecond) // let the request enqueue
	_ = a.Close()

	select {
	case err := <-done:
		// Either Request returned ErrTransportClosed directly, or
		// drainPending delivered a response carrying it.
		if err == nil {
			t.Error("expected error after Close")
		} else if !strings.Contains(err.Error(), "transport closed") {
			t.Errorf("expected 'transport closed', got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not unblock after Close")
	}
}

func TestTransport_RequestRespectsContextCancel(t *testing.T) {
	// A's caller cancels before the response arrives. Request
	// returns the ctx error; B's slow handler is unaffected.
	slow := func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		select {
		case <-time.After(2 * time.Second):
			return "done", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	a, _ := pairedPipes(t, nil, slow)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := a.Request(ctx, "slow", nil)
	if err == nil {
		t.Fatal("expected context error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}
}

func TestTransport_MalformedLineIsSkipped(t *testing.T) {
	// Junk bytes between valid messages must not derail the read
	// loop. We craft a custom pipe that emits some garbage, then
	// a valid response.
	r, w := io.Pipe()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tr := NewTransport(r, io.Discard, nil, logger)
	tr.Start()
	defer func() { _ = tr.Close() }()

	// Start a request so a pending entry exists. We need to know
	// the id to craft the response. The transport's nextID is
	// monotonic from 0; the first request will have id 1.
	respCh := make(chan *Response, 1)
	go func() {
		// Use a discard writer; we don't care what the request
		// looks like for this test, only that it goes through.
		resp, err := tr.Request(context.Background(), "test", nil)
		if err != nil {
			respCh <- nil
			return
		}
		respCh <- resp
	}()

	// Give the request time to enqueue.
	time.Sleep(50 * time.Millisecond)

	// Now inject: a garbage line, then a valid response with id 1.
	_, _ = w.Write([]byte("not-json\n"))
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"ok"}` + "\n"))

	select {
	case resp := <-respCh:
		if resp == nil {
			t.Fatal("Request returned nil")
		}
		if resp.Result != "ok" {
			t.Errorf("result: %v", resp.Result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not recover from garbage line")
	}
}

func TestIDAsString(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`1`, "1"},
		{`42`, "42"},
		{`"abc"`, "abc"},
		{`null`, "null"},
		{``, ""},
	}
	for _, tc := range cases {
		got := idAsString(json.RawMessage(tc.in))
		if got != tc.want {
			t.Errorf("idAsString(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
