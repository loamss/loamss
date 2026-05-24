package permission

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
)

func TestCreatePairingCode_HappyPath(t *testing.T) {
	e, s, w := newEngine(t)
	ctx := context.Background()

	p, err := e.CreatePairingCode(ctx, "ChatGPT laptop", "user", 0)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}
	if !strings.Contains(p.Code, "-") || len(p.Code) != 9 {
		t.Errorf("code should be 4-4 with dash, got %q", p.Code)
	}
	if !p.ExpiresAt.After(p.CreatedAt) {
		t.Error("expires_at should be after created_at")
	}
	if p.ClientName != "ChatGPT laptop" {
		t.Errorf("client_name: %q", p.ClientName)
	}

	// Persisted.
	got, err := s.GetPairingCode(ctx, p.Code)
	if err != nil {
		t.Fatalf("GetPairingCode: %v", err)
	}
	if got.RedeemedAt != nil {
		t.Error("code should be unredeemed")
	}

	// Audit entry.
	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"client.pair_code_created"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 audit entry, got %d", len(entries))
	}
}

func TestCreatePairingCode_RequiresName(t *testing.T) {
	e, _, _ := newEngine(t)
	if _, err := e.CreatePairingCode(context.Background(), "", "user", 0); err == nil {
		t.Error("expected error for empty name")
	}
	if _, err := e.CreatePairingCode(context.Background(), "   ", "user", 0); err == nil {
		t.Error("expected error for whitespace name")
	}
}

func TestRedeemPairingCode_HappyPath(t *testing.T) {
	e, s, w := newEngine(t)
	ctx := context.Background()

	p, err := e.CreatePairingCode(ctx, "ChatGPT laptop", "user", time.Hour)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}

	c, token, err := e.RedeemPairingCode(ctx, p.Code, map[string]any{"paired_via": "cli"})
	if err != nil {
		t.Fatalf("RedeemPairingCode: %v", err)
	}
	if !strings.HasPrefix(token, "lck_"+c.ID+"_") {
		t.Errorf("token shape: %q", token)
	}
	if c.Name != "ChatGPT laptop" {
		t.Errorf("name: %q", c.Name)
	}
	if c.CredentialHash == "" {
		t.Error("credential_hash should be set")
	}
	if len(c.CredentialHash) != 64 {
		t.Errorf("credential_hash should be 64 hex chars, got %d", len(c.CredentialHash))
	}
	if c.Metadata["paired_via"] != "cli" {
		t.Errorf("metadata not preserved: %#v", c.Metadata)
	}

	// Code is now marked redeemed.
	pAfter, _ := s.GetPairingCode(ctx, p.Code)
	if pAfter.RedeemedAt == nil {
		t.Error("code should be marked redeemed")
	}
	if pAfter.RedeemedClientID != c.ID {
		t.Errorf("redeemed_client_id: got %q, want %q", pAfter.RedeemedClientID, c.ID)
	}

	// Client persisted.
	got, err := s.GetClient(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if got.Name != "ChatGPT laptop" {
		t.Errorf("persisted name: %q", got.Name)
	}

	// Audit entry.
	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"client.paired"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 client.paired entry, got %d", len(entries))
	}
}

func TestRedeemPairingCode_UnknownCode(t *testing.T) {
	e, _, w := newEngine(t)
	_, _, err := e.RedeemPairingCode(context.Background(), "NOPE-NOPE", nil)
	if !errors.Is(err, ErrPairingCodeNotFound) {
		t.Errorf("expected ErrPairingCodeNotFound, got: %v", err)
	}
	entries, _ := w.Query(context.Background(), audit.Filter{Types: []string{"client.pair_failed"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 pair_failed entry, got %d", len(entries))
	}
}

func TestRedeemPairingCode_ExpiredCode(t *testing.T) {
	e, s, _ := newEngine(t)
	ctx := context.Background()

	// Insert an already-expired code directly.
	now := time.Now().UTC()
	code := PairingCode{
		Code:       "OLDE-CODE",
		ClientName: "stale",
		CreatedBy:  "user",
		CreatedAt:  now.Add(-time.Hour),
		ExpiresAt:  now.Add(-time.Minute),
	}
	if err := s.InsertPairingCode(ctx, code); err != nil {
		t.Fatalf("InsertPairingCode: %v", err)
	}

	_, _, err := e.RedeemPairingCode(ctx, code.Code, nil)
	if !errors.Is(err, ErrPairingCodeExpired) {
		t.Errorf("expected ErrPairingCodeExpired, got: %v", err)
	}
}

func TestRedeemPairingCode_AlreadyRedeemed(t *testing.T) {
	e, _, _ := newEngine(t)
	ctx := context.Background()

	p, _ := e.CreatePairingCode(ctx, "once", "user", time.Hour)
	if _, _, err := e.RedeemPairingCode(ctx, p.Code, nil); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	_, _, err := e.RedeemPairingCode(ctx, p.Code, nil)
	if !errors.Is(err, ErrPairingCodeAlreadyRedeemed) {
		t.Errorf("expected ErrPairingCodeAlreadyRedeemed, got: %v", err)
	}
}

func TestAuthenticateClient_HappyPath(t *testing.T) {
	e, s, _ := newEngine(t)
	ctx := context.Background()

	p, _ := e.CreatePairingCode(ctx, "claude", "user", time.Hour)
	c, token, err := e.RedeemPairingCode(ctx, p.Code, nil)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	princ, got, err := e.AuthenticateClient(ctx, token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if princ.Kind != PrincipalClient || princ.ID != c.ID {
		t.Errorf("principal: %+v", princ)
	}
	if got.ID != c.ID {
		t.Errorf("client id: got %q want %q", got.ID, c.ID)
	}

	// last_seen_at was touched. AuthenticateClient updates the row
	// after returning the snapshot, so re-read from the store.
	after, err := s.GetClient(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if after.LastSeenAt == nil {
		t.Error("last_seen_at should be set after AuthenticateClient")
	}
}

func TestAuthenticateClient_MalformedToken(t *testing.T) {
	e, _, w := newEngine(t)
	ctx := context.Background()

	for _, bad := range []string{
		"",
		"not_a_token",
		"lck_cli-xyz",                   // only two fields
		"abc_cli-xyz_payload",           // wrong prefix
		"lck_xyz_payload",               // bad client id shape
		"lck_cli-xyz_!!!!notbase64!!!!", // bad base64
		"lck_cli-xyz_AAAAAAAA",          // short secret
	} {
		_, _, err := e.AuthenticateClient(ctx, bad)
		if !errors.Is(err, ErrInvalidCredential) {
			t.Errorf("token %q: expected ErrInvalidCredential, got %v", bad, err)
		}
	}

	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"client.auth_failed"}})
	if len(entries) == 0 {
		t.Errorf("expected at least one auth_failed entry")
	}
}

func TestAuthenticateClient_WrongSecret(t *testing.T) {
	e, _, w := newEngine(t)
	ctx := context.Background()

	p, _ := e.CreatePairingCode(ctx, "claude", "user", time.Hour)
	c, _, err := e.RedeemPairingCode(ctx, p.Code, nil)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	// Craft a token with the right client id but the wrong secret.
	wrongSecret := strings.Repeat("A", 43) // 43-char base64 of 32-byte secret
	bad := "lck_" + c.ID + "_" + wrongSecret
	_, _, err = e.AuthenticateClient(ctx, bad)
	if !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("expected ErrInvalidCredential, got: %v", err)
	}
	entries, _ := w.Query(ctx, audit.Filter{Types: []string{"client.auth_failed"}})
	if len(entries) != 1 {
		t.Errorf("expected 1 auth_failed entry, got %d", len(entries))
	}
}

func TestAuthenticateClient_RevokedClient(t *testing.T) {
	e, _, _ := newEngine(t)
	ctx := context.Background()

	p, _ := e.CreatePairingCode(ctx, "doomed", "user", time.Hour)
	_, token, err := e.RedeemPairingCode(ctx, p.Code, nil)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	// Pull client id out of the token to revoke.
	id, _, _ := splitToken(token)
	if err := e.RevokeClient(ctx, id, "user", "scheduled"); err != nil {
		t.Fatalf("RevokeClient: %v", err)
	}

	_, _, err = e.AuthenticateClient(ctx, token)
	if !errors.Is(err, ErrClientRevoked) {
		t.Errorf("expected ErrClientRevoked, got: %v", err)
	}
}

func TestRevokeClient_CascadesGrants(t *testing.T) {
	e, s, w := newEngine(t)
	ctx := context.Background()

	p, _ := e.CreatePairingCode(ctx, "vibez", "user", time.Hour)
	c, _, err := e.RedeemPairingCode(ctx, p.Code, nil)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	// Issue two grants directly through the store (the CLI does this
	// via engine, but the store path is sufficient for the cascade test).
	for _, capName := range []string{"memory.read", "content.read"} {
		if _, err := s.IssueGrant(ctx, Grant{
			Principal:  Principal{Kind: PrincipalClient, ID: c.ID},
			Capability: capName,
		}); err != nil {
			t.Fatalf("IssueGrant: %v", err)
		}
	}

	if err := e.RevokeClient(ctx, c.ID, "user", "rotation"); err != nil {
		t.Fatalf("RevokeClient: %v", err)
	}

	// All grants are revoked.
	gs, _ := s.ListGrantsByPrincipal(ctx, PrincipalClient, c.ID)
	if len(gs) != 2 {
		t.Fatalf("expected 2 grants, got %d", len(gs))
	}
	for _, g := range gs {
		if g.RevokedAt == nil {
			t.Errorf("grant %s should be revoked", g.ID)
		}
	}

	// Audit: 2 grant.revoke + 1 client.revoked.
	gr, _ := w.Query(ctx, audit.Filter{Types: []string{"grant.revoke"}})
	if len(gr) != 2 {
		t.Errorf("expected 2 grant.revoke entries, got %d", len(gr))
	}
	cr, _ := w.Query(ctx, audit.Filter{Types: []string{"client.revoked"}})
	if len(cr) != 1 {
		t.Errorf("expected 1 client.revoked entry, got %d", len(cr))
	}
}

func TestRevokeClient_Idempotent(t *testing.T) {
	e, _, w := newEngine(t)
	ctx := context.Background()

	p, _ := e.CreatePairingCode(ctx, "x", "user", time.Hour)
	c, _, err := e.RedeemPairingCode(ctx, p.Code, nil)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	if err := e.RevokeClient(ctx, c.ID, "user", ""); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := e.RevokeClient(ctx, c.ID, "user", ""); err != nil {
		t.Errorf("second revoke should be idempotent: %v", err)
	}

	// Only one audit entry.
	cr, _ := w.Query(ctx, audit.Filter{Types: []string{"client.revoked"}})
	if len(cr) != 1 {
		t.Errorf("expected 1 client.revoked entry, got %d", len(cr))
	}
}

func TestRevokeClient_UnknownErrors(t *testing.T) {
	e, _, _ := newEngine(t)
	err := e.RevokeClient(context.Background(), "cli-nope", "user", "")
	if !errors.Is(err, ErrClientNotFound) {
		t.Errorf("expected ErrClientNotFound, got: %v", err)
	}
}

func TestListClients_FiltersByStatus(t *testing.T) {
	e, s, _ := newEngine(t)
	ctx := context.Background()

	// Create two clients, revoke one.
	pA, _ := e.CreatePairingCode(ctx, "alpha", "user", time.Hour)
	a, _, _ := e.RedeemPairingCode(ctx, pA.Code, nil)
	pB, _ := e.CreatePairingCode(ctx, "beta", "user", time.Hour)
	b, _, _ := e.RedeemPairingCode(ctx, pB.Code, nil)
	if err := e.RevokeClient(ctx, b.ID, "user", ""); err != nil {
		t.Fatalf("RevokeClient: %v", err)
	}

	active, err := s.ListClients(ctx, ClientFilter{})
	if err != nil {
		t.Fatalf("ListClients active: %v", err)
	}
	if len(active) != 1 || active[0].ID != a.ID {
		t.Errorf("active filter: got %+v", active)
	}

	all, err := s.ListClients(ctx, ClientFilter{Status: StatusAll})
	if err != nil {
		t.Fatalf("ListClients all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("all filter: expected 2, got %d", len(all))
	}

	revoked, err := s.ListClients(ctx, ClientFilter{Status: StatusRevoked})
	if err != nil {
		t.Fatalf("ListClients revoked: %v", err)
	}
	if len(revoked) != 1 || revoked[0].ID != b.ID {
		t.Errorf("revoked filter: %+v", revoked)
	}
}

func TestGeneratePairingCode_Format(t *testing.T) {
	// Sanity: 100 codes all match the expected shape and alphabet.
	for i := 0; i < 100; i++ {
		code, err := generatePairingCode()
		if err != nil {
			t.Fatalf("generatePairingCode: %v", err)
		}
		if len(code) != 9 || code[4] != '-' {
			t.Errorf("bad shape: %q", code)
		}
		for j, r := range code {
			if j == 4 {
				continue
			}
			if !strings.ContainsRune(codeAlphabet, r) {
				t.Errorf("char %q not in alphabet: %q", r, code)
			}
		}
	}
}
