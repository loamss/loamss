package audit

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newWriter constructs an audit Writer at a fresh temp path.
func newWriter(t *testing.T) *SQLite {
	t.Helper()
	dir := t.TempDir()
	w, err := OpenSQLite(context.Background(), filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = w.Close(context.Background()) })
	return w
}

func basicEntry() Entry {
	return Entry{
		Type:    "grant.create",
		Actor:   Actor{Kind: ActorUser, ID: "fortunatus"},
		Outcome: OutcomeSuccess,
		Data:    map[string]any{"capability": "email.read"},
	}
}

func TestAppend_PopulatesIDTimestampAndHash(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()

	before := time.Now().UTC()
	got, err := w.Append(ctx, basicEntry())
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	after := time.Now().UTC()

	if !strings.HasPrefix(got.ID, "aud-") {
		t.Errorf("ID should have aud- prefix, got %q", got.ID)
	}
	if got.Timestamp.Before(before) || got.Timestamp.After(after) {
		t.Errorf("Timestamp %v outside expected window [%v, %v]", got.Timestamp, before, after)
	}
	if got.PrevHash != GenesisHash {
		t.Errorf("first entry's PrevHash should be genesis, got %q", got.PrevHash)
	}
	if !strings.HasPrefix(got.Hash, "sha256:") {
		t.Errorf("Hash should be sha256-prefixed, got %q", got.Hash)
	}
}

func TestAppend_ChainsEntries(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()

	first, err := w.Append(ctx, basicEntry())
	if err != nil {
		t.Fatalf("Append 1: %v", err)
	}

	second := basicEntry()
	second.Type = "grant.revoke"
	got, err := w.Append(ctx, second)
	if err != nil {
		t.Fatalf("Append 2: %v", err)
	}
	if got.PrevHash != first.Hash {
		t.Errorf("second.prev_hash %q should equal first.hash %q", got.PrevHash, first.Hash)
	}
	if got.Hash == first.Hash {
		t.Error("consecutive entries should have different hashes")
	}
}

func TestAppend_ValidatesRequiredFields(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()

	cases := []struct {
		name string
		e    Entry
	}{
		{"missing type", Entry{Actor: Actor{Kind: ActorUser, ID: "x"}, Outcome: OutcomeSuccess}},
		{"missing actor kind", Entry{Type: "x", Actor: Actor{ID: "x"}, Outcome: OutcomeSuccess}},
		{"missing actor id", Entry{Type: "x", Actor: Actor{Kind: ActorUser}, Outcome: OutcomeSuccess}},
		{"missing outcome", Entry{Type: "x", Actor: Actor{Kind: ActorUser, ID: "x"}}},
		{
			"subject without kind",
			Entry{
				Type:    "x",
				Actor:   Actor{Kind: ActorUser, ID: "x"},
				Outcome: OutcomeSuccess,
				Subject: &Subject{ID: "y"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := w.Append(ctx, tc.e); err == nil {
				t.Errorf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestQuery_FiltersAndOrdersByID(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()

	// Seed entries with different types/actors/outcomes.
	seeds := []Entry{
		{Type: "grant.create", Actor: Actor{Kind: ActorUser, ID: "a"}, Outcome: OutcomeSuccess},
		{Type: "grant.revoke", Actor: Actor{Kind: ActorUser, ID: "a"}, Outcome: OutcomeSuccess},
		{Type: "check.deny", Actor: Actor{Kind: ActorClient, ID: "vibez"}, Outcome: OutcomeDenied},
		{Type: "check.allow", Actor: Actor{Kind: ActorClient, ID: "vibez"}, Outcome: OutcomeSuccess},
	}
	for _, s := range seeds {
		if _, err := w.Append(ctx, s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	all, err := w.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("all: got %d, want 4", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Errorf("entries should be sorted ascending by ID, got %s <= %s", all[i].ID, all[i-1].ID)
		}
	}

	denials, err := w.Query(ctx, Filter{Outcomes: []Outcome{OutcomeDenied}})
	if err != nil {
		t.Fatalf("Query denials: %v", err)
	}
	if len(denials) != 1 || denials[0].Type != "check.deny" {
		t.Errorf("denials: %v", denials)
	}

	vibez, err := w.Query(ctx, Filter{ActorID: "vibez"})
	if err != nil {
		t.Fatalf("Query actor: %v", err)
	}
	if len(vibez) != 2 {
		t.Errorf("vibez: got %d, want 2", len(vibez))
	}

	grants, err := w.Query(ctx, Filter{Types: []string{"grant.create", "grant.revoke"}})
	if err != nil {
		t.Fatalf("Query types: %v", err)
	}
	if len(grants) != 2 {
		t.Errorf("grants: got %d, want 2", len(grants))
	}
}

func TestQuery_LimitsResults(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		_, _ = w.Append(ctx, basicEntry())
	}
	out, err := w.Query(ctx, Filter{Limit: 3})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(out) != 3 {
		t.Errorf("limit: got %d, want 3", len(out))
	}
}

func TestLatest_ReturnsMostRecent(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()

	empty, err := w.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest empty: %v", err)
	}
	if empty != nil {
		t.Errorf("Latest on empty log should be nil, got %v", empty)
	}

	for i := 0; i < 3; i++ {
		_, _ = w.Append(ctx, basicEntry())
	}
	last, err := w.Latest(ctx)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if last == nil {
		t.Fatal("Latest should not be nil after appends")
	}
	// Latest's hash should be the chain head.
	if last.Hash != w.lastHash {
		t.Errorf("Latest hash %q != head %q", last.Hash, w.lastHash)
	}
}

func TestVerify_CleanChain(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := w.Append(ctx, basicEntry()); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	r, err := w.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !r.Valid {
		t.Errorf("Verify on clean chain should be Valid: %+v", r)
	}
	if r.EntriesChecked != 5 {
		t.Errorf("EntriesChecked: got %d, want 5", r.EntriesChecked)
	}
}

func TestVerify_DetectsTamper(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _ = w.Append(ctx, basicEntry())
	}

	// Tamper: change the type column on a middle row.
	all, _ := w.Query(ctx, Filter{})
	if len(all) < 3 {
		t.Fatalf("need at least 3 entries to tamper")
	}
	tamperedID := all[2].ID

	if _, err := w.db.ExecContext(ctx,
		`UPDATE audit_entries SET type = 'tampered' WHERE id = ?`, tamperedID); err != nil {
		t.Fatalf("tamper UPDATE: %v", err)
	}

	r, err := w.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if r.Valid {
		t.Error("Verify should have detected tampering")
	}
	if r.BrokenAt != tamperedID {
		t.Errorf("BrokenAt: got %q, want %q", r.BrokenAt, tamperedID)
	}
}

func TestVerify_EmptyChain(t *testing.T) {
	w := newWriter(t)
	r, err := w.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify empty: %v", err)
	}
	if !r.Valid {
		t.Error("empty chain should verify as valid")
	}
	if r.EntriesChecked != 0 {
		t.Errorf("EntriesChecked on empty: %d", r.EntriesChecked)
	}
}

func TestOpen_ReopenPreservesChainHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.db")
	ctx := context.Background()

	w1, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("OpenSQLite 1: %v", err)
	}
	for i := 0; i < 3; i++ {
		_, _ = w1.Append(ctx, basicEntry())
	}
	head1, _ := w1.Latest(ctx)
	_ = w1.Close(ctx)

	w2, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("OpenSQLite 2: %v", err)
	}
	defer w2.Close(ctx)

	if w2.lastHash != head1.Hash {
		t.Errorf("lastHash after reopen: got %q, want %q", w2.lastHash, head1.Hash)
	}

	// Appending after reopen should chain from the recovered head.
	next, err := w2.Append(ctx, basicEntry())
	if err != nil {
		t.Fatalf("Append after reopen: %v", err)
	}
	if next.PrevHash != head1.Hash {
		t.Errorf("post-reopen entry's prev_hash %q should match prior head %q", next.PrevHash, head1.Hash)
	}

	// Full verify should still pass.
	r, _ := w2.Verify(ctx)
	if !r.Valid {
		t.Errorf("Verify after reopen should pass: %+v", r)
	}
}

func TestAppend_PersistsSubjectAndContext(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()

	e := basicEntry()
	e.Subject = &Subject{Kind: SubjectGrant, ID: "grt-01HVZ"}
	e.Context = &Context{RequestID: "req-1", IP: "127.0.0.1"}

	stored, err := w.Append(ctx, e)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	q, _ := w.Query(ctx, Filter{})
	if len(q) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(q))
	}
	got := q[0]
	if got.Subject == nil || got.Subject.Kind != SubjectGrant || got.Subject.ID != "grt-01HVZ" {
		t.Errorf("subject not preserved: %+v", got.Subject)
	}
	if got.Context == nil || got.Context.RequestID != "req-1" || got.Context.IP != "127.0.0.1" {
		t.Errorf("context not preserved: %+v", got.Context)
	}
	if got.Hash != stored.Hash {
		t.Errorf("hash differs after round-trip: %q vs %q", got.Hash, stored.Hash)
	}
}

func TestConcurrent_AppendsAreSerializedAndChained(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()

	const goroutines = 8
	const each = 10
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if _, err := w.Append(ctx, basicEntry()); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	all, err := w.Query(ctx, Filter{Limit: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != goroutines*each {
		t.Errorf("got %d entries, want %d", len(all), goroutines*each)
	}

	// Verify the resulting chain is still intact.
	r, err := w.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify after concurrent appends: %v", err)
	}
	if !r.Valid {
		t.Errorf("chain broken after concurrent appends: %+v", r)
	}
}

// TestConcurrent_TwoWritersSameFileChainIntact reproduces the
// daemon-plus-CLI concurrency case: two distinct SQLite writers
// open the same audit.db, both append entries, and the chain must
// still verify. The earlier TestConcurrent_AppendsAreSerializedAndChained
// only exercises one writer instance — its in-process mutex masks the
// cross-process / cross-instance bug the smoke test surfaced.
func TestConcurrent_TwoWritersSameFileChainIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.db")
	ctx := context.Background()

	wA, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	defer wA.Close(ctx)
	wB, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	defer wB.Close(ctx)

	const each = 25
	var wg sync.WaitGroup
	for _, w := range []*SQLite{wA, wB} {
		wg.Add(1)
		go func(w *SQLite) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := w.Append(ctx, basicEntry()); err != nil {
					t.Errorf("Append from %s: %v", w.path, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// Either writer can verify the chain end-to-end.
	r, err := wA.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !r.Valid {
		t.Errorf("chain broken under cross-instance concurrency: %+v", r)
	}
	all, _ := wA.Query(ctx, Filter{Limit: 1000})
	if len(all) != 2*each {
		t.Errorf("got %d entries, want %d", len(all), 2*each)
	}
}

func TestSentinelValidationErrors(t *testing.T) {
	// Direct call to Validate without going through Append, for
	// completeness.
	e := Entry{}
	err := e.Validate()
	if err == nil {
		t.Fatal("expected validation error on empty entry")
	}
	if !strings.Contains(err.Error(), "type") {
		t.Errorf("first missing field should be type, got: %v", err)
	}
}
