package permission

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/loamss/loamss/runtime/internal/audit"
)

// BenchmarkCheck_Allow measures the hot path: client invokes a tool,
// runtime runs engine.Check, finds a matching grant, returns Allow.
// Includes the audit emission of check.allow.
//
// Target: <1ms p50. Above 2ms p50 means the SQLite lookup or audit
// append is regressing.
func BenchmarkCheck_Allow(b *testing.B) {
	e, s := newBenchEngine(b)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "bench-client"}
	_, err := s.IssueGrant(ctx, Grant{
		Principal:  p,
		Capability: "memory.read",
	})
	if err != nil {
		b.Fatalf("IssueGrant: %v", err)
	}
	req := CheckRequest{Principal: p, Capability: "memory.read"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := e.Check(ctx, req)
		if err != nil {
			b.Fatalf("Check: %v", err)
		}
		if res.Decision != DecisionAllow {
			b.Fatalf("expected Allow, got %s", res.Decision)
		}
	}
}

// BenchmarkCheck_Deny measures the deny path: same lookup but no
// matching grant. Slightly faster than Allow because no audit
// emission for the matched-grant subject.
func BenchmarkCheck_Deny(b *testing.B) {
	e, _ := newBenchEngine(b)
	ctx := context.Background()
	req := CheckRequest{
		Principal:  Principal{Kind: PrincipalClient, ID: "bench-stranger"},
		Capability: "memory.read",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := e.Check(ctx, req)
		if err != nil {
			b.Fatalf("Check: %v", err)
		}
		if res.Decision != DecisionDeny {
			b.Fatalf("expected Deny, got %s", res.Decision)
		}
	}
}

// BenchmarkCheck_WithScope exercises the match-primitive dispatch.
// A grant with a non-trivial scope; the attempted scope must match.
// Covers the realistic shape where matchScope walks each scope field.
func BenchmarkCheck_WithScope(b *testing.B) {
	e, s := newBenchEngine(b)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "bench-scoped"}
	if _, err := s.IssueGrant(ctx, Grant{
		Principal:  p,
		Capability: "memory.read",
		Scope: map[string]any{
			"entities":              []any{"people", "projects"},
			"data_classes_included": []any{"work"},
		},
	}); err != nil {
		b.Fatalf("IssueGrant: %v", err)
	}
	req := CheckRequest{
		Principal:  p,
		Capability: "memory.read",
		AttemptedScope: map[string]any{
			"entities":              []any{"people"},
			"data_classes_included": []any{"work"},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := e.Check(ctx, req)
		if err != nil {
			b.Fatalf("Check: %v", err)
		}
		if res.Decision != DecisionAllow {
			b.Fatalf("expected Allow, got %s", res.Decision)
		}
	}
}

// BenchmarkCheck_ParallelAllow stresses the concurrent path —
// multiple goroutines doing Check at once. SQLite reads can fan
// out under WAL mode, so this should scale better than serial.
func BenchmarkCheck_ParallelAllow(b *testing.B) {
	e, s := newBenchEngine(b)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "bench-parallel"}
	_, _ = s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read"})
	req := CheckRequest{Principal: p, Capability: "memory.read"}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := e.Check(ctx, req); err != nil {
				b.Fatalf("Check: %v", err)
			}
		}
	})
}

func newBenchEngine(b *testing.B) (*Engine, *Store) {
	b.Helper()
	dir := b.TempDir()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(dir, "runtime.db"))
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	w, err := audit.OpenSQLite(ctx, filepath.Join(dir, "audit.db"))
	if err != nil {
		b.Fatalf("OpenSQLite: %v", err)
	}
	b.Cleanup(func() {
		_ = s.Close()
		_ = w.Close(ctx)
	})
	return NewEngine(s, w), s
}
