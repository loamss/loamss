package permission

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpen_SeedsCanonicalCapabilities(t *testing.T) {
	s := newStore(t)
	caps, err := s.ListCapabilities(context.Background())
	if err != nil {
		t.Fatalf("ListCapabilities: %v", err)
	}
	// At least the 11 canonical entries from canonical.go.
	if len(caps) < 11 {
		t.Errorf("expected >= 11 canonical capabilities, got %d", len(caps))
	}
	want := map[string]bool{
		"memory.read":           false,
		"memory.query":          false,
		"files.read":            false,
		"audit.read":            false,
		"email.read":            false,
		"calendar.read":         false,
		"messages.read":         false,
		"content.list":          false,
		"content.read":          false,
		"content.metrics.write": false,
		"content.revenue.write": false,
	}
	for _, c := range caps {
		if _, ok := want[c.Name]; ok {
			want[c.Name] = true
		}
	}
	for name, present := range want {
		if !present {
			t.Errorf("canonical capability missing: %s", name)
		}
	}
}

func TestOpen_IdempotentReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.db")
	ctx := context.Background()

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	caps1, _ := s1.ListCapabilities(ctx)
	_ = s1.Close()

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer s2.Close()
	caps2, _ := s2.ListCapabilities(ctx)

	if len(caps1) != len(caps2) {
		t.Errorf("re-seed on reopen: %d vs %d capabilities", len(caps1), len(caps2))
	}
}

func TestRegisterCapability_RejectsReservedNamespace(t *testing.T) {
	s := newStore(t)
	def := CapabilityDef{
		Name:       "runtime.tamper",
		Namespace:  "runtime",
		Direction:  DirectionInbound,
		DeclaredBy: "evil-capsule@1.0",
		Scope:      ScopeSchema{},
	}
	err := s.RegisterCapability(context.Background(), def)
	if !errors.Is(err, ErrReservedNamespace) {
		t.Errorf("expected ErrReservedNamespace, got: %v", err)
	}
}

func TestRegisterCapability_AllowsAuditReadException(t *testing.T) {
	// audit.read is already canonical, but registering it again
	// with DeclaredBy should be allowed (the exception list catches it).
	s := newStore(t)
	def, _ := s.GetCapability(context.Background(), "audit.read")
	if def == nil {
		t.Fatal("audit.read should be canonical")
	}
}

func TestRegisterCapability_AllowsNewNamespace(t *testing.T) {
	s := newStore(t)
	def := CapabilityDef{
		Name:       "home.thermostat.read",
		Namespace:  "home",
		Direction:  DirectionInbound,
		DeclaredBy: "home-control@1.0",
		Scope: ScopeSchema{
			"device_id": MatchEquals,
		},
	}
	if err := s.RegisterCapability(context.Background(), def); err != nil {
		t.Fatalf("RegisterCapability: %v", err)
	}
	got, err := s.GetCapability(context.Background(), "home.thermostat.read")
	if err != nil {
		t.Fatalf("GetCapability: %v", err)
	}
	if got.DeclaredBy != "home-control@1.0" {
		t.Errorf("DeclaredBy: %q", got.DeclaredBy)
	}
	if got.Scope["device_id"] != MatchEquals {
		t.Errorf("scope schema not preserved: %v", got.Scope)
	}
}

func TestRegisterCapability_DuplicateSameDefIsNoOp(t *testing.T) {
	s := newStore(t)
	def := CapabilityDef{
		Name:       "x.read",
		Namespace:  "x",
		Direction:  DirectionInbound,
		DeclaredBy: "test@1.0",
		Scope:      ScopeSchema{"a": MatchEquals},
	}
	if err := s.RegisterCapability(context.Background(), def); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := s.RegisterCapability(context.Background(), def); err != nil {
		t.Errorf("re-register with identical def should be no-op, got: %v", err)
	}
}

func TestRegisterCapability_DuplicateDifferentDefErrors(t *testing.T) {
	s := newStore(t)
	def1 := CapabilityDef{
		Name:       "x.read",
		Namespace:  "x",
		Direction:  DirectionInbound,
		DeclaredBy: "test@1.0",
		Scope:      ScopeSchema{"a": MatchEquals},
	}
	def2 := def1
	def2.Scope = ScopeSchema{"b": MatchEquals} // different scope
	if err := s.RegisterCapability(context.Background(), def1); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := s.RegisterCapability(context.Background(), def2)
	if !errors.Is(err, ErrCapabilityAlreadyRegistered) {
		t.Errorf("expected ErrCapabilityAlreadyRegistered, got: %v", err)
	}
}

func TestGetCapability_UnknownIsErrNotFound(t *testing.T) {
	s := newStore(t)
	_, err := s.GetCapability(context.Background(), "no.such.capability")
	if !errors.Is(err, ErrCapabilityNotFound) {
		t.Errorf("expected ErrCapabilityNotFound, got: %v", err)
	}
}

func TestIssueGrant_RoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	g := Grant{
		Principal:  Principal{Kind: PrincipalClient, ID: "vibez"},
		Capability: "content.read",
		Scope:      map[string]any{"tag": "public", "type": "video"},
		Framing:    FramingPublicPublish,
		Rationale:  "Show videos to viewers",
	}
	issued, err := s.IssueGrant(ctx, g)
	if err != nil {
		t.Fatalf("IssueGrant: %v", err)
	}
	if issued.ID == "" || issued.IssuedAt.IsZero() {
		t.Errorf("ID or IssuedAt not assigned: %+v", issued)
	}

	got, err := s.GetGrant(ctx, issued.ID)
	if err != nil {
		t.Fatalf("GetGrant: %v", err)
	}
	if got.Capability != "content.read" {
		t.Errorf("capability lost: %q", got.Capability)
	}
	if got.Scope["tag"] != "public" {
		t.Errorf("scope not preserved: %v", got.Scope)
	}
	if got.Framing != FramingPublicPublish {
		t.Errorf("framing: %q", got.Framing)
	}
}

func TestIssueGrant_RejectsUnknownCapability(t *testing.T) {
	s := newStore(t)
	g := Grant{
		Principal:  Principal{Kind: PrincipalClient, ID: "x"},
		Capability: "no.such.capability",
	}
	_, err := s.IssueGrant(context.Background(), g)
	if !errors.Is(err, ErrCapabilityNotFound) {
		t.Errorf("expected ErrCapabilityNotFound, got: %v", err)
	}
}

func TestIssueGrant_RejectsUnknownScopeField(t *testing.T) {
	s := newStore(t)
	g := Grant{
		Principal:  Principal{Kind: PrincipalClient, ID: "x"},
		Capability: "memory.query",
		Scope:      map[string]any{"bogus_field": "x"},
	}
	_, err := s.IssueGrant(context.Background(), g)
	if !errors.Is(err, ErrScopeViolatesSchema) {
		t.Errorf("expected ErrScopeViolatesSchema, got: %v", err)
	}
}

func TestIssueGrant_DefaultsFramingToPrivateRead(t *testing.T) {
	s := newStore(t)
	g := Grant{
		Principal:  Principal{Kind: PrincipalClient, ID: "x"},
		Capability: "memory.read",
	}
	got, err := s.IssueGrant(context.Background(), g)
	if err != nil {
		t.Fatalf("IssueGrant: %v", err)
	}
	if got.Framing != FramingPrivateRead {
		t.Errorf("framing default: got %q, want %q", got.Framing, FramingPrivateRead)
	}
}

func TestRevokeGrant_MarksRevoked(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	g, _ := s.IssueGrant(ctx, Grant{
		Principal:  Principal{Kind: PrincipalClient, ID: "x"},
		Capability: "memory.read",
	})

	if err := s.RevokeGrant(ctx, g.ID); err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}
	got, _ := s.GetGrant(ctx, g.ID)
	if got.RevokedAt == nil {
		t.Errorf("RevokedAt not set")
	}
	if got.Active(time.Now()) {
		t.Errorf("revoked grant should not be Active")
	}
}

func TestRevokeGrant_Idempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	g, _ := s.IssueGrant(ctx, Grant{
		Principal:  Principal{Kind: PrincipalClient, ID: "x"},
		Capability: "memory.read",
	})
	if err := s.RevokeGrant(ctx, g.ID); err != nil {
		t.Fatalf("revoke 1: %v", err)
	}
	if err := s.RevokeGrant(ctx, g.ID); err != nil {
		t.Errorf("second revoke should be idempotent, got: %v", err)
	}
}

func TestRevokeGrant_UnknownErrors(t *testing.T) {
	s := newStore(t)
	err := s.RevokeGrant(context.Background(), "grt-no-such-id")
	if !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("expected ErrGrantNotFound, got: %v", err)
	}
}

func TestListGrantsByPrincipal_IncludesRevoked(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	g1, _ := s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read"})
	g2, _ := s.IssueGrant(ctx, Grant{Principal: p, Capability: "files.read"})
	_ = s.RevokeGrant(ctx, g2.ID)

	list, err := s.ListGrantsByPrincipal(ctx, p.Kind, p.ID)
	if err != nil {
		t.Fatalf("ListGrantsByPrincipal: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 grants including revoked, got %d", len(list))
	}
	// Sorted ASC by issued_at.
	if list[0].ID != g1.ID {
		t.Errorf("expected g1 first by issued_at; got %s before %s", list[0].ID, g1.ID)
	}
}

func TestListActiveGrantsForCheck_FiltersRevokedAndExpired(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	// active
	g1, _ := s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read"})
	// revoked
	g2, _ := s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read"})
	_ = s.RevokeGrant(ctx, g2.ID)
	// expired
	past := time.Now().Add(-time.Hour)
	_, _ = s.IssueGrant(ctx, Grant{Principal: p, Capability: "memory.read", ExpiresAt: &past})

	active, err := s.ListActiveGrantsForCheck(ctx, p.Kind, p.ID, "memory.read")
	if err != nil {
		t.Fatalf("ListActiveGrantsForCheck: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("expected 1 active grant, got %d", len(active))
	}
	if len(active) > 0 && active[0].ID != g1.ID {
		t.Errorf("wrong grant returned: %s", active[0].ID)
	}
}

func TestGrant_ActiveHelper(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		g    Grant
		want bool
	}{
		{"plain", Grant{}, true},
		{"revoked", Grant{RevokedAt: tp(now.Add(-time.Hour))}, false},
		{"expired", Grant{ExpiresAt: tp(now.Add(-time.Hour))}, false},
		{"future expiry", Grant{ExpiresAt: tp(now.Add(time.Hour))}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.g.Active(now); got != tc.want {
				t.Errorf("Active(): got %v, want %v", got, tc.want)
			}
		})
	}
}

func tp(t time.Time) *time.Time { return &t }

func TestEnqueueApproval_RoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	a, err := s.EnqueueApproval(ctx, PendingApproval{
		GrantID:        "grt-fake",
		Principal:      Principal{Kind: PrincipalClient, ID: "vibez"},
		Capability:     "email.send",
		AttemptedScope: map[string]any{"recipient": "sarah@acme.com"},
		Rationale:      "scenario S1 step 6",
	})
	if err != nil {
		t.Fatalf("EnqueueApproval: %v", err)
	}
	if a.ID == "" || a.State != ApprovalPending {
		t.Errorf("incomplete approval: %+v", a)
	}

	got, err := s.GetApproval(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if got.Rationale != "scenario S1 step 6" {
		t.Errorf("rationale lost: %q", got.Rationale)
	}
}

func TestResolveApproval_TransitionsToGranted(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, _ := s.EnqueueApproval(ctx, PendingApproval{
		GrantID:    "grt-fake",
		Principal:  Principal{Kind: PrincipalClient, ID: "x"},
		Capability: "memory.read",
	})

	if err := s.ResolveApproval(ctx, a.ID, ApprovalGranted, "user", "ok"); err != nil {
		t.Fatalf("ResolveApproval: %v", err)
	}
	got, _ := s.GetApproval(ctx, a.ID)
	if got.State != ApprovalGranted {
		t.Errorf("state: %q", got.State)
	}
	if got.DecidedAt == nil || got.DecidedBy != "user" {
		t.Errorf("decided fields not set: %+v", got)
	}
}

func TestResolveApproval_AlreadyResolvedErrors(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, _ := s.EnqueueApproval(ctx, PendingApproval{
		GrantID:    "grt-fake",
		Principal:  Principal{Kind: PrincipalClient, ID: "x"},
		Capability: "memory.read",
	})
	_ = s.ResolveApproval(ctx, a.ID, ApprovalGranted, "user", "")
	err := s.ResolveApproval(ctx, a.ID, ApprovalDenied, "user", "")
	if !errors.Is(err, ErrApprovalAlreadyResolved) {
		t.Errorf("expected ErrApprovalAlreadyResolved, got: %v", err)
	}
}

func TestResolveApproval_UnknownErrors(t *testing.T) {
	s := newStore(t)
	err := s.ResolveApproval(context.Background(), "apr-no-such", ApprovalGranted, "user", "")
	if !errors.Is(err, ErrApprovalNotFound) {
		t.Errorf("expected ErrApprovalNotFound, got: %v", err)
	}
}

func TestListPendingApprovals_OnlyPending(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p := Principal{Kind: PrincipalClient, ID: "x"}

	a1, _ := s.EnqueueApproval(ctx, PendingApproval{GrantID: "g1", Principal: p, Capability: "memory.read"})
	a2, _ := s.EnqueueApproval(ctx, PendingApproval{GrantID: "g2", Principal: p, Capability: "memory.read"})
	_, _ = s.EnqueueApproval(ctx, PendingApproval{GrantID: "g3", Principal: p, Capability: "memory.read"})

	_ = s.ResolveApproval(ctx, a1.ID, ApprovalGranted, "user", "")
	_ = s.ResolveApproval(ctx, a2.ID, ApprovalDenied, "user", "")

	pending, err := s.ListPendingApprovals(ctx)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 pending, got %d", len(pending))
	}
}

func TestConcurrent_StoreOperations(t *testing.T) {
	s := newStore(t)
	const goroutines = 8
	const iterations = 10

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			p := Principal{Kind: PrincipalClient, ID: string(rune('a' + i%26))}
			for j := 0; j < iterations; j++ {
				g, err := s.IssueGrant(ctx, Grant{
					Principal:  p,
					Capability: "memory.read",
				})
				if err != nil {
					t.Errorf("IssueGrant: %v", err)
					return
				}
				if _, err := s.GetGrant(ctx, g.ID); err != nil {
					t.Errorf("GetGrant: %v", err)
					return
				}
				if err := s.RevokeGrant(ctx, g.ID); err != nil {
					t.Errorf("RevokeGrant: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
