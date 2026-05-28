package permission

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
)

// newEngine builds a permission Engine with a fresh runtime.db and
// audit.db under a temp dir. Returns the engine, its store (for
// direct manipulation), and the audit writer (for verifying entries).
func newEngine(t *testing.T) (*Engine, *Store, *audit.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	w, err := audit.OpenSQLite(context.Background(), filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatalf("Open audit: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_ = w.Close(context.Background())
	})
	return NewEngine(s, w), s, w
}

func TestCheck_AllowHappyPath(t *testing.T) {
	e, s, w := newEngine(t)
	ctx := context.Background()

	p := Principal{Kind: PrincipalClient, ID: "vibez"}
	_, err := s.IssueGrant(ctx, Grant{
		Principal:  p,
		Capability: "content.read",
		Scope:      map[string]any{"tag": "public", "type": "video"},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := e.Check(ctx, CheckRequest{
		Principal:      p,
		Capability:     "content.read",
		AttemptedScope: map[string]any{"tag": "public", "type": "video", "resource_id": "abc123"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Errorf("decision: got %q, want allow", res.Decision)
	}
	if res.GrantID == "" {
		t.Error("GrantID should be set on allow")
	}

	// Audit entry emitted.
	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"check.allow"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 check.allow entry, got %d", len(entries))
	}
}

func TestCheck_DenyNoMatchingGrant(t *testing.T) {
	e, _, w := newEngine(t)
	ctx := context.Background()

	res, err := e.Check(ctx, CheckRequest{
		Principal:  Principal{Kind: PrincipalClient, ID: "stranger"},
		Capability: "memory.read",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Decision != DecisionDeny {
		t.Errorf("decision: got %q, want deny", res.Decision)
	}
	if res.GrantID != "" {
		t.Errorf("GrantID should be empty on deny, got %q", res.GrantID)
	}
	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"check.deny"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 check.deny entry, got %d", len(entries))
	}
}

func TestCheck_DenyUnknownCapability(t *testing.T) {
	e, _, w := newEngine(t)
	ctx := context.Background()

	res, _ := e.Check(ctx, CheckRequest{
		Principal:  Principal{Kind: PrincipalClient, ID: "x"},
		Capability: "no.such.capability",
	})
	if res.Decision != DecisionDeny {
		t.Errorf("unknown capability should deny, got %q", res.Decision)
	}
	if !strings.Contains(res.Reason, "unknown capability") {
		t.Errorf("reason should mention unknown capability, got: %s", res.Reason)
	}
	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"check.deny"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 check.deny entry, got %d", len(entries))
	}
}

func TestCheck_DenyRevokedGrant(t *testing.T) {
	e, s, _ := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	g, _ := s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read"})
	_ = s.RevokeGrant(ctx, g.ID)

	res, _ := e.Check(ctx, CheckRequest{Principal: p, Capability: "memory.read"})
	if res.Decision != DecisionDeny {
		t.Errorf("revoked grant should result in deny, got %q", res.Decision)
	}
}

func TestCheck_DenyExpiredGrant(t *testing.T) {
	e, s, _ := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	past := time.Now().Add(-time.Hour)
	_, err := s.IssueGrant(ctx, Grant{
		Principal:  p,
		Capability: "memory.read",
		ExpiresAt:  &past,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, _ := e.Check(ctx, CheckRequest{Principal: p, Capability: "memory.read"})
	if res.Decision != DecisionDeny {
		t.Errorf("expired grant should deny, got %q", res.Decision)
	}
}

func TestCheck_ApprovalRequiredOnGrantFlag(t *testing.T) {
	e, s, w := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	_, _ = s.IssueGrant(ctx, Grant{
		Principal:            p,
		Capability:           "memory.read",
		RequiresUserApproval: true,
	})

	res, err := e.Check(ctx, CheckRequest{
		Principal:  p,
		Capability: "memory.read",
		Rationale:  "needs approval per grant flag",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Decision != DecisionApprovalRequired {
		t.Errorf("decision: got %q, want approval_required", res.Decision)
	}
	if res.ApprovalID == "" {
		t.Error("ApprovalID should be set")
	}

	// approval.requested audit entry emitted.
	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"approval.requested"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 approval.requested entry, got %d", len(entries))
	}
	if entries[0].Outcome != audit.OutcomePending {
		t.Errorf("approval.requested outcome: got %q, want pending", entries[0].Outcome)
	}

	// PendingApproval persisted.
	pa, err := s.GetApproval(ctx, res.ApprovalID)
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if pa.State != ApprovalPending {
		t.Errorf("state: got %q, want pending", pa.State)
	}
}

func TestCheck_MultipleGrantsUnionSemantics(t *testing.T) {
	// Two grants for the same capability with disjoint scopes:
	// attempt that matches the SECOND grant should still allow.
	e, s, _ := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	_, _ = s.IssueGrant(ctx, Grant{
		Principal: p, Capability: "content.read",
		Scope: map[string]any{"tag": "family"},
	})
	_, _ = s.IssueGrant(ctx, Grant{
		Principal: p, Capability: "content.read",
		Scope: map[string]any{"tag": "public"},
	})

	// Attempt against the public tag — second grant matches.
	res, _ := e.Check(ctx, CheckRequest{
		Principal:      p,
		Capability:     "content.read",
		AttemptedScope: map[string]any{"tag": "public"},
	})
	if res.Decision != DecisionAllow {
		t.Errorf("second grant should match: %+v", res)
	}
}

func TestCheck_ScopeMismatchDoesNotMatch(t *testing.T) {
	e, s, _ := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	_, _ = s.IssueGrant(ctx, Grant{
		Principal: p, Capability: "content.read",
		Scope: map[string]any{"tag": "public"},
	})
	// Attempt with a different tag.
	res, _ := e.Check(ctx, CheckRequest{
		Principal:      p,
		Capability:     "content.read",
		AttemptedScope: map[string]any{"tag": "family"},
	})
	if res.Decision != DecisionDeny {
		t.Errorf("mismatched scope should deny, got %q", res.Decision)
	}
}

func TestResolveApproval_EmitsAuditEntry(t *testing.T) {
	e, s, w := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	_, _ = s.IssueGrant(ctx, Grant{
		Principal: p, Capability: "memory.read", RequiresUserApproval: true,
	})
	res, _ := e.Check(ctx, CheckRequest{Principal: p, Capability: "memory.read"})

	if err := e.ResolveApproval(ctx, res.ApprovalID, ApprovalGranted, "user", "ok"); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}

	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"approval.granted"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 approval.granted entry, got %d", len(entries))
	}
}

func TestResolveApproval_DeniedEmitsCorrectAudit(t *testing.T) {
	e, s, w := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	_, _ = s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read", RequiresUserApproval: true})
	res, _ := e.Check(ctx, CheckRequest{Principal: p, Capability: "memory.read"})
	_ = e.ResolveApproval(ctx, res.ApprovalID, ApprovalDenied, "user", "no")

	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"approval.denied"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 approval.denied entry, got %d", len(entries))
	}
}

func TestWaitForApproval_ReturnsResolvedState(t *testing.T) {
	e, s, _ := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	_, _ = s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read", RequiresUserApproval: true})
	res, _ := e.Check(ctx, CheckRequest{Principal: p, Capability: "memory.read"})

	// Resolve from another goroutine after a delay.
	//
	// `done` synchronizes the goroutine with the test's Cleanup: the
	// goroutine may still be writing the resolution audit entry by the
	// time WaitForApproval observes the new state (the state DB update
	// is sequenced *before* the audit write). Without this barrier
	// t.Cleanup can close the audit writer mid-write and the test
	// crashes with a nil-DB panic from another goroutine. The race
	// was visible on CI under load.
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(50 * time.Millisecond)
		_ = e.ResolveApproval(ctx, res.ApprovalID, ApprovalGranted, "user", "")
	}()
	t.Cleanup(func() { <-done })

	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	a, err := e.WaitForApproval(wctx, res.ApprovalID, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForApproval: %v", err)
	}
	if a.State != ApprovalGranted {
		t.Errorf("state: got %q, want granted", a.State)
	}
}

func TestWaitForApproval_RespectsContextCancel(t *testing.T) {
	e, s, _ := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	_, _ = s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read", RequiresUserApproval: true})
	res, _ := e.Check(ctx, CheckRequest{Principal: p, Capability: "memory.read"})

	wctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err := e.WaitForApproval(wctx, res.ApprovalID, 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}
}

func TestWaitForApproval_AlreadyResolved(t *testing.T) {
	e, s, _ := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	_, _ = s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read", RequiresUserApproval: true})
	res, _ := e.Check(ctx, CheckRequest{Principal: p, Capability: "memory.read"})
	_ = e.ResolveApproval(ctx, res.ApprovalID, ApprovalGranted, "user", "")

	a, err := e.WaitForApproval(ctx, res.ApprovalID, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForApproval: %v", err)
	}
	if a.State != ApprovalGranted {
		t.Errorf("state: got %q, want granted", a.State)
	}
}

func TestCheck_PrincipalKindMapping(t *testing.T) {
	e, s, w := newEngine(t)
	ctx := context.Background()

	for _, tc := range []struct {
		pk        PrincipalKind
		wantActor audit.ActorKind
	}{
		{PrincipalCapsule, audit.ActorCapsule},
		{PrincipalClient, audit.ActorClient},
	} {
		t.Run(string(tc.pk), func(t *testing.T) {
			p := Principal{Kind: tc.pk, ID: string(tc.pk) + "-test"}
			_, _ = s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read"})
			_, _ = e.Check(ctx, CheckRequest{Principal: p, Capability: "memory.read"})

			entries, _ := w.Query(ctx, audit.Filter{
				Types:     []string{"check.allow"},
				ActorKind: tc.wantActor,
				ActorID:   p.ID,
			})
			if len(entries) != 1 {
				t.Errorf("expected 1 entry for actor %s/%s, got %d", tc.wantActor, p.ID, len(entries))
			}
		})
	}
}

func TestNewEngine_PanicsOnNilArgs(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil Store")
		}
	}()
	NewEngine(nil, nil)
}

func TestCheck_RequestIDPropagatesToAuditContext(t *testing.T) {
	e, s, w := newEngine(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}
	_, _ = s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read"})

	_, _ = e.Check(ctx, CheckRequest{
		Principal:  p,
		Capability: "memory.read",
		RequestID:  "req-12345",
	})

	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"check.allow"}})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Context == nil || entries[0].Context.RequestID != "req-12345" {
		t.Errorf("RequestID not propagated to audit context: %+v", entries[0].Context)
	}
}
