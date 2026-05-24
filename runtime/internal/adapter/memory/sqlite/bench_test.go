package sqlite

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
)

// BenchmarkSearch measures brute-force cosine k-NN cost at varying
// corpus sizes. memory:sqlite reads every row on every Search, so
// cost is O(N · D). The benchmark surfaces the point at which we
// should swap to memory:sqlite-vec for indexed search.
//
// Run with:
//
//	go test ./internal/adapter/memory/sqlite/ -bench=BenchmarkSearch -benchtime=1x -run=^$
//
// 1x runs each size exactly once (the loop body is the meaningful
// measurement; b.N>1 doesn't add information for this benchmark
// shape).
func BenchmarkSearch(b *testing.B) {
	const dim = 384 // matches a typical small embedding model
	rng := rand.New(rand.NewSource(42))

	for _, n := range []int{100, 1_000, 10_000, 100_000} {
		b.Run(name(n), func(b *testing.B) {
			a := newBenchAdapter(b)
			ctx := context.Background()
			// Seed N random vectors.
			entries := make([]memory.Entry, 0, n)
			for i := 0; i < n; i++ {
				v := make([]float32, dim)
				for j := range v {
					v[j] = rng.Float32()*2 - 1
				}
				entries = append(entries, memory.Entry{
					ID:     fmt.Sprintf("e%07d", i),
					Vector: v,
					Metadata: map[string]any{
						"idx": i,
					},
				})
			}
			if err := a.BatchUpsert(ctx, entries); err != nil {
				b.Fatalf("BatchUpsert: %v", err)
			}

			query := make([]float32, dim)
			for j := range query {
				query[j] = rng.Float32()*2 - 1
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := a.Search(ctx, query, 10, memory.MetadataFilter{}); err != nil {
					b.Fatalf("Search: %v", err)
				}
			}
		})
	}
}

// BenchmarkUpsert measures single-entry write cost. Less hot than
// Search but matters for ingestion throughput.
func BenchmarkUpsert(b *testing.B) {
	const dim = 384
	a := newBenchAdapter(b)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(1))
	v := make([]float32, dim)
	for j := range v {
		v[j] = rng.Float32()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := fmt.Sprintf("u%07d", i)
		if err := a.Upsert(ctx, id, v, nil); err != nil {
			b.Fatalf("Upsert: %v", err)
		}
	}
}

func newBenchAdapter(b *testing.B) *Adapter {
	b.Helper()
	dir := b.TempDir()
	a := &Adapter{}
	if err := a.Init(context.Background(), map[string]any{
		"path": filepath.Join(dir, "memory.db"),
	}); err != nil {
		b.Fatalf("Init: %v", err)
	}
	b.Cleanup(func() { _ = a.Close(context.Background()) })
	return a
}

func name(n int) string {
	switch {
	case n >= 100_000:
		return "100k"
	case n >= 10_000:
		return "10k"
	case n >= 1_000:
		return "1k"
	default:
		return fmt.Sprintf("%d", n)
	}
}
