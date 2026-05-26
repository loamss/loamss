package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// --- CapsuleCursorStore unit tests -------------------------------

func TestCapsuleCursorStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewCapsuleCursorStore(newInMemStorage())

	// Get before any Set returns empty, no error — matches the
	// in-tree source.Sync(ctx, nil-cursor) contract.
	got, err := store.Get(ctx, "calendar-ingestor")
	if err != nil {
		t.Fatalf("Get before Set: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty before Set, got %q", got)
	}

	cursor := `{"syncToken":"abc123"}`
	if err := store.Set(ctx, "calendar-ingestor", cursor); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err = store.Get(ctx, "calendar-ingestor")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got != cursor {
		t.Errorf("round-trip: got %q want %q", got, cursor)
	}

	// Overwrite.
	updated := `{"syncToken":"def456"}`
	_ = store.Set(ctx, "calendar-ingestor", updated)
	got, _ = store.Get(ctx, "calendar-ingestor")
	if got != updated {
		t.Errorf("after overwrite: %q", got)
	}
}

func TestCapsuleCursorStore_EmptyValueClears(t *testing.T) {
	ctx := context.Background()
	store := NewCapsuleCursorStore(newInMemStorage())

	_ = store.Set(ctx, "cap", "v1")
	_ = store.Set(ctx, "cap", "")

	got, _ := store.Get(ctx, "cap")
	if got != "" {
		t.Errorf("expected empty after Set(\"\"), got %q", got)
	}
	// Idempotent.
	if err := store.Set(ctx, "cap", ""); err != nil {
		t.Errorf("Set(\"\") twice: %v", err)
	}
}

func TestCapsuleCursorStore_IsolationBetweenCapsules(t *testing.T) {
	ctx := context.Background()
	store := NewCapsuleCursorStore(newInMemStorage())

	_ = store.Set(ctx, "calendar-ingestor", "cal-cursor")
	_ = store.Set(ctx, "slack-ingestor", "slack-cursor")

	cal, _ := store.Get(ctx, "calendar-ingestor")
	slk, _ := store.Get(ctx, "slack-ingestor")
	if cal != "cal-cursor" || slk != "slack-cursor" {
		t.Errorf("isolation broken: cal=%q slk=%q", cal, slk)
	}
}

func TestCapsuleCursorStore_DeleteAll(t *testing.T) {
	ctx := context.Background()
	store := NewCapsuleCursorStore(newInMemStorage())

	_ = store.Set(ctx, "cap", "v1")
	if err := store.DeleteAll(ctx, "cap"); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	got, _ := store.Get(ctx, "cap")
	if got != "" {
		t.Errorf("after DeleteAll: %q", got)
	}
	// Idempotent.
	if err := store.DeleteAll(ctx, "cap"); err != nil {
		t.Errorf("DeleteAll on empty: %v", err)
	}
}

func TestCapsuleCursorStore_RejectsEmptyName(t *testing.T) {
	store := NewCapsuleCursorStore(newInMemStorage())
	ctx := context.Background()
	if err := store.Set(ctx, "", "v"); !errors.Is(err, ErrEmptyCursorCapsuleName) {
		t.Errorf("Set empty: %v", err)
	}
	if _, err := store.Get(ctx, ""); !errors.Is(err, ErrEmptyCursorCapsuleName) {
		t.Errorf("Get empty: %v", err)
	}
}

func TestCapsuleCursorStore_PersistsAcrossInstances(t *testing.T) {
	mem := newInMemStorage()
	a := NewCapsuleCursorStore(mem)
	b := NewCapsuleCursorStore(mem)
	ctx := context.Background()

	_ = a.Set(ctx, "cap", "shared-cursor")
	got, _ := b.Get(ctx, "cap")
	if got != "shared-cursor" {
		t.Errorf("cross-instance: %q", got)
	}
}

// --- Tool handler tests -------------------------------------------

func TestCursorSet_RejectsNonCapsulePrincipal(t *testing.T) {
	store := NewCapsuleCursorStore(newInMemStorage())
	tool := NewCursorSetTool(store)

	_, err := tool.Handler(context.Background(), ToolInput{
		Principal: clientPrincipal("vibez"),
		Args:      json.RawMessage(`{"value":"x"}`),
	})
	if err == nil {
		t.Fatal("expected error for client principal")
	}
	if !strings.Contains(err.Error(), "restricted to capsule principals") {
		t.Errorf("err: %v", err)
	}
}

func TestCursor_RoundTrip_HandlerLayer(t *testing.T) {
	store := NewCapsuleCursorStore(newInMemStorage())
	setTool := NewCursorSetTool(store)
	getTool := NewCursorGetTool(store)
	ctx := context.Background()
	princ := capsulePrincipal("hn-ingestor")

	cursor := `{"highwater":42291391}`
	args, _ := json.Marshal(map[string]any{"value": cursor})
	if _, err := setTool.Handler(ctx, ToolInput{Principal: princ, Args: args}); err != nil {
		t.Fatalf("set: %v", err)
	}

	res, err := getTool.Handler(ctx, ToolInput{
		Principal: princ,
		Args:      json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var got cursorGetResult
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Value != cursor {
		t.Errorf("round-trip: %q vs %q", got.Value, cursor)
	}
}

func TestCursorGet_EmptyWhenUnset(t *testing.T) {
	store := NewCapsuleCursorStore(newInMemStorage())
	tool := NewCursorGetTool(store)

	res, err := tool.Handler(context.Background(), ToolInput{
		Principal: capsulePrincipal("fresh-cap"),
		Args:      json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("get fresh: %v", err)
	}
	var got cursorGetResult
	_ = json.Unmarshal([]byte(res.Content[0].Text), &got)
	if got.Value != "" {
		t.Errorf("fresh cap should have empty cursor, got %q", got.Value)
	}
}
