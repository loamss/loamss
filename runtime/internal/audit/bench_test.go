package audit

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// BenchmarkAppend measures the cost of one audit entry: BEGIN
// IMMEDIATE → read head → compute hash → INSERT → COMMIT. Run with:
//
//	go test ./internal/audit/ -bench=BenchmarkAppend -benchtime=2s -run=^$
//
// Tracks the hot path the runtime hits on every external request +
// every capsule callback. Target: <1ms p50; if it ever regresses past
// 5ms p50 we have a problem.
func BenchmarkAppend(b *testing.B) {
	w := newBenchWriter(b)
	ctx := context.Background()
	e := basicEntry()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := w.Append(ctx, e); err != nil {
			b.Fatalf("Append: %v", err)
		}
	}
}

// BenchmarkAppendParallel measures throughput under concurrent
// in-process writers. Cross-process serialization (BEGIN IMMEDIATE
// + SQLite write lock) is the bottleneck; this benchmark stresses
// the in-process mutex + WAL behavior.
func BenchmarkAppendParallel(b *testing.B) {
	w := newBenchWriter(b)
	ctx := context.Background()
	e := basicEntry()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := w.Append(ctx, e); err != nil {
				b.Fatalf("Append: %v", err)
			}
		}
	})
}

// BenchmarkVerify measures Verify time as a function of chain size.
// Variants benchmark at 100, 1k, 10k entries. Verify is read-only
// so it doesn't contend with writers; this is pure decode + hash
// recomputation cost.
func BenchmarkVerify(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000} {
		b.Run(name(n), func(b *testing.B) {
			w := newBenchWriter(b)
			ctx := context.Background()
			e := basicEntry()
			for i := 0; i < n; i++ {
				if _, err := w.Append(ctx, e); err != nil {
					b.Fatalf("seed Append: %v", err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r, err := w.Verify(ctx)
				if err != nil {
					b.Fatalf("Verify: %v", err)
				}
				if !r.Valid {
					b.Fatalf("Verify reported invalid chain: %+v", r)
				}
			}
		})
	}
}

// BenchmarkCrossInstance measures Append throughput when TWO writer
// instances (simulating daemon + CLI) write to the same DB. This is
// the path that previously broke the chain; the fix uses BEGIN
// IMMEDIATE to serialize cross-instance.
func BenchmarkCrossInstance(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "audit.db")
	ctx := context.Background()
	wA, err := OpenSQLite(ctx, path)
	if err != nil {
		b.Fatalf("open A: %v", err)
	}
	defer wA.Close(ctx)
	wB, err := OpenSQLite(ctx, path)
	if err != nil {
		b.Fatalf("open B: %v", err)
	}
	defer wB.Close(ctx)

	e := basicEntry()
	b.ResetTimer()
	var wg sync.WaitGroup
	for _, w := range []*SQLite{wA, wB} {
		wg.Add(1)
		go func(w *SQLite) {
			defer wg.Done()
			for i := 0; i < b.N/2; i++ {
				if _, err := w.Append(ctx, e); err != nil {
					b.Errorf("Append: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}

func newBenchWriter(b *testing.B) *SQLite {
	b.Helper()
	dir := b.TempDir()
	w, err := OpenSQLite(context.Background(), filepath.Join(dir, "audit.db"))
	if err != nil {
		b.Fatalf("OpenSQLite: %v", err)
	}
	b.Cleanup(func() { _ = w.Close(context.Background()) })
	return w
}

func name(n int) string {
	switch {
	case n >= 1_000_000:
		return "1M"
	case n >= 1_000:
		return itoa(n/1_000) + "k"
	default:
		return itoa(n)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
