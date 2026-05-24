package source

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_InsertGet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	in := Configured{
		Name:      "gmail-personal",
		AdapterID: "source:gmail",
		Config:    map[string]any{"label": "INBOX"},
	}
	out, err := s.Insert(ctx, in)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if out.ID == "" {
		t.Error("expected assigned ID")
	}
	if out.AddedAt.IsZero() || out.UpdatedAt.IsZero() {
		t.Errorf("timestamps unset: added=%v updated=%v", out.AddedAt, out.UpdatedAt)
	}

	got, err := s.Get(ctx, "gmail-personal")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AdapterID != "source:gmail" {
		t.Errorf("adapter_id: %q", got.AdapterID)
	}
	if got.Config["label"] != "INBOX" {
		t.Errorf("config: %+v", got.Config)
	}
}

func TestStore_Insert_DuplicateName(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.Insert(ctx, Configured{Name: "dup", AdapterID: "source:gmail"}); err != nil {
		t.Fatalf("Insert 1: %v", err)
	}
	_, err := s.Insert(ctx, Configured{Name: "dup", AdapterID: "source:calendar"})
	if !errors.Is(err, ErrSourceNameTaken) {
		t.Errorf("err=%v, want ErrSourceNameTaken", err)
	}
}

func TestStore_Get_NotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.Get(context.Background(), "missing")
	if !errors.Is(err, ErrSourceNotFound) {
		t.Errorf("err=%v, want ErrSourceNotFound", err)
	}
}

func TestStore_List_OrdersNewestFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, _ = s.Insert(ctx, Configured{Name: "old", AdapterID: "source:gmail"})
	// Tiny gap so ordering by added_at is unambiguous on fast clocks.
	time.Sleep(2 * time.Millisecond)
	_, _ = s.Insert(ctx, Configured{Name: "newer", AdapterID: "source:gmail"})

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d, want 2", len(list))
	}
	if list[0].Name != "newer" || list[1].Name != "old" {
		t.Errorf("order: %s, %s", list[0].Name, list[1].Name)
	}
}

func TestStore_UpdateCursor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.Insert(ctx, Configured{Name: "x", AdapterID: "source:gmail"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.UpdateCursor(ctx, "x", []byte("history:42")); err != nil {
		t.Fatalf("UpdateCursor: %v", err)
	}
	got, _ := s.Get(ctx, "x")
	if string(got.Cursor) != "history:42" {
		t.Errorf("cursor: %q", got.Cursor)
	}

	if err := s.UpdateCursor(ctx, "missing", []byte("x")); !errors.Is(err, ErrSourceNotFound) {
		t.Errorf("UpdateCursor missing: err=%v", err)
	}
}

func TestStore_SetLastSync(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.Insert(ctx, Configured{Name: "x", AdapterID: "source:gmail"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	finished := time.Now().UTC().Truncate(time.Microsecond)
	summary := map[string]any{"records_added": 14, "bytes_ingested": 2048}
	if err := s.SetLastSync(ctx, "x", "success", summary, finished); err != nil {
		t.Fatalf("SetLastSync: %v", err)
	}

	got, _ := s.Get(ctx, "x")
	if got.LastSyncStatus != "success" {
		t.Errorf("status: %q", got.LastSyncStatus)
	}
	if !got.LastSyncAt.Equal(finished) {
		t.Errorf("last_sync_at: got %v, want %v", got.LastSyncAt, finished)
	}
	// JSON round-trip lifts numbers to float64.
	if got.LastSyncSummary["records_added"] != float64(14) {
		t.Errorf("summary: %+v", got.LastSyncSummary)
	}
}

func TestStore_Delete(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.Insert(ctx, Configured{Name: "x", AdapterID: "source:gmail"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Delete(ctx, "x"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "x"); !errors.Is(err, ErrSourceNotFound) {
		t.Errorf("Get after Delete: err=%v", err)
	}
	if err := s.Delete(ctx, "x"); !errors.Is(err, ErrSourceNotFound) {
		t.Errorf("Delete twice: err=%v", err)
	}
}

func TestStore_MonotonicIDs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	a, _ := s.Insert(ctx, Configured{Name: "a", AdapterID: "source:gmail"})
	b, _ := s.Insert(ctx, Configured{Name: "b", AdapterID: "source:gmail"})

	if a.ID >= b.ID {
		t.Errorf("ids not monotonic: %s, %s", a.ID, b.ID)
	}
}
