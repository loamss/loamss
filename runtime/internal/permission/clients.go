package permission

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
)

// This file implements the external-client pairing primitive on top
// of the permission Engine. The wire-level flow it supports is
// specified in mcp-surface.md §Pairing:
//
//   1. User: `loamss client pair --name "ChatGPT laptop"`
//   2. Runtime: CreatePairingCode → human-readable, TTL-bound code
//   3. User: relays the code to the client (paste, QR, etc.)
//   4. Client: presents the code to /pair (HTTP, future commit) or
//      to `loamss client pair complete <code>` (today)
//   5. Runtime: RedeemPairingCode → opaque bearer credential,
//      returned exactly once. The Client row is created here.
//   6. Subsequent client requests: AuthenticateClient maps the
//      bearer back to a Principal.
//
// Grants are issued separately via the existing grant flow; pairing
// codes carry no scope themselves, by design — the permission slip
// step (capability narrowing) is the user's, not the client's.

// Token format constants. The bearer token shape is:
//
//	lck_<client-id>_<base64url(32 random bytes)>
//
// "lck" stands for "loamss client key" and is a static, public
// prefix. The middle segment lets AuthenticateClient look up the
// client in O(1) without scanning every credential hash. Splitting
// on '_' yields exactly three fields because cli-<ULID> contains
// no underscores (Crockford base32 alphabet).
const (
	tokenPrefix     = "lck"
	tokenSecretLen  = 32 // bytes of entropy in the secret portion
	tokenSeparator  = "_"
	tokenFieldCount = 3
)

// Code generation. 8 characters from a confusion-resistant alphabet
// (no 0/O, no 1/I/L), grouped 4-4 with a dash for the human user.
// 32^8 = ~1.1 trillion codes; collision risk inside a 10-minute TTL
// window is negligible at any realistic pairing rate.
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // 31 chars, drop O & I/1 collisions

// DefaultPairingTTL is the default TTL applied when CreatePairingCode
// is called with a zero or negative duration. Matches mcp-surface.md.
const DefaultPairingTTL = 10 * time.Minute

// CreatePairingCode issues a new pairing code that will become a
// client named clientName upon redemption. The returned PairingCode
// has its Code field populated; the caller surfaces it to the user
// (CLI prints it, console renders a QR, etc.). The actor that
// generated the code is recorded in createdBy for audit.
//
// Emits the `client.pair_code_created` audit entry.
func (e *Engine) CreatePairingCode(ctx context.Context, clientName, createdBy string, ttl time.Duration) (*PairingCode, error) {
	if strings.TrimSpace(clientName) == "" {
		return nil, errors.New("permission: pairing code requires a client name")
	}
	if ttl <= 0 {
		ttl = DefaultPairingTTL
	}

	code, err := generatePairingCode()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	p := PairingCode{
		Code:       code,
		ClientName: clientName,
		CreatedBy:  defaulted(createdBy, "user"),
		CreatedAt:  now,
		ExpiresAt:  now.Add(ttl),
	}
	if err := e.store.InsertPairingCode(ctx, p); err != nil {
		return nil, err
	}

	entry := audit.Entry{
		Type:    "client.pair_code_created",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: p.CreatedBy},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"client_name": p.ClientName,
			"expires_at":  p.ExpiresAt.Format(time.RFC3339Nano),
		},
	}
	_, _ = e.audit.Append(ctx, entry)
	return &p, nil
}

// RedeemPairingCode atomically consumes a pairing code, creates a
// Client, and returns the plaintext bearer token. The token is
// returned **once** — its SHA-256 is what gets persisted; the
// runtime cannot recover the plaintext later.
//
// The metadata map is opaque pass-through stored on the Client (e.g.,
// {"paired_via": "cli", "client_version": "1.2.3"}); the runtime
// does not interpret it.
//
// On any failure path (unknown code, expired, already redeemed) a
// `client.pair_failed` audit entry is emitted with the reason; on
// success, `client.paired`.
func (e *Engine) RedeemPairingCode(ctx context.Context, code string, metadata map[string]any) (*Client, string, error) {
	now := time.Now().UTC()

	// Generate the client id and bearer token first so the
	// MarkPairingCodeRedeemed UPDATE can record the link atomically
	// with consumption.
	clientID := e.store.nextID("cli-")
	secret, err := generateSecret()
	if err != nil {
		return nil, "", err
	}
	token := assembleToken(clientID, secret)
	hash := hashSecret(secret)

	if _, err := e.store.MarkPairingCodeRedeemed(ctx, code, clientID, now); err != nil {
		e.recordPairFailed(ctx, code, err)
		return nil, "", err
	}

	c := Client{
		ID:             clientID,
		Name:           "", // filled below from the redeemed code
		CredentialHash: hash,
		Metadata:       metadata,
		PairedAt:       now,
	}
	// Re-fetch the code so we can carry its client_name onto the
	// Client. MarkPairingCodeRedeemed already returned the row, but
	// for clarity we read it back; the cost is one indexed SELECT.
	p, err := e.store.GetPairingCode(ctx, code)
	if err != nil {
		// Should not happen — we just updated it.
		return nil, "", err
	}
	c.Name = p.ClientName

	if err := e.store.InsertClient(ctx, c); err != nil {
		return nil, "", err
	}

	entry := audit.Entry{
		Type:    "client.paired",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: defaulted(p.CreatedBy, "user")},
		Subject: &audit.Subject{Kind: audit.SubjectClient, ID: c.ID},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"client_name": c.Name,
		},
	}
	_, _ = e.audit.Append(ctx, entry)
	return &c, token, nil
}

// AuthenticateClient maps a bearer token to the Principal that
// issued requests will be attributed to. The token format is
// validated, the client id is looked up in O(1), and the secret
// portion is constant-time compared against the stored hash.
//
// On success, the client's last_seen_at is updated (best-effort)
// and the Principal + Client are returned. On failure, a
// `client.auth_failed` audit entry is emitted. The error is one of
// ErrInvalidCredential or ErrClientRevoked.
//
// Authentication failures and authorization failures are
// deliberately separated in the audit log — per mcp-surface.md the
// former often indicates compromise, the latter is routine.
func (e *Engine) AuthenticateClient(ctx context.Context, token string) (*Principal, *Client, error) {
	clientID, secret, err := splitToken(token)
	if err != nil {
		e.recordAuthFailed(ctx, "", "malformed token")
		return nil, nil, ErrInvalidCredential
	}
	c, err := e.store.GetClient(ctx, clientID)
	if err != nil {
		// Unknown client id is treated as invalid credential, not
		// disclosed as "no such client". The audit entry records
		// the attempted id (it's already in the token).
		if errors.Is(err, ErrClientNotFound) {
			e.recordAuthFailed(ctx, clientID, "unknown client")
			return nil, nil, ErrInvalidCredential
		}
		return nil, nil, err
	}
	if c.RevokedAt != nil {
		e.recordAuthFailed(ctx, clientID, "client revoked")
		return nil, nil, ErrClientRevoked
	}
	if !constantTimeEqualHex(c.CredentialHash, hashSecret(secret)) {
		e.recordAuthFailed(ctx, clientID, "credential mismatch")
		return nil, nil, ErrInvalidCredential
	}

	// Touch last_seen_at. Best-effort: a failure here doesn't
	// invalidate the authentication itself — the credential was
	// already valid.
	_ = e.store.TouchClientLastSeen(ctx, c.ID, time.Now().UTC())

	return &Principal{Kind: PrincipalClient, ID: c.ID}, c, nil
}

// RevokeClient marks a client revoked and cascade-revokes every
// grant the client holds. Idempotent: revoking an already-revoked
// client returns nil without re-emitting audit. Emits
// `client.revoked` once on first revocation, plus the underlying
// `grant.revoke` entries for each cascaded grant.
func (e *Engine) RevokeClient(ctx context.Context, id, decidedBy, reason string) error {
	c, err := e.store.GetClient(ctx, id)
	if err != nil {
		return err
	}
	if c.RevokedAt != nil {
		return nil
	}

	// Cascade-revoke first, then the client itself. Order matters
	// only for the (vanishingly unlikely) case of a concurrent
	// Check race: if we revoke the client first, an in-flight
	// Check might still match a grant for the brief window between
	// the two writes. Doing grants first means the worst-case race
	// produces a Deny — strictly safer.
	grants, err := e.store.ListGrants(ctx, GrantFilter{
		PrincipalKind: PrincipalClient,
		PrincipalID:   id,
		Status:        StatusActive,
		Limit:         10_000,
	})
	if err != nil {
		return err
	}
	for _, g := range grants {
		// Use the engine method so each cascaded revocation produces
		// its own grant.revoke audit entry; reason is augmented so
		// the trail makes the cascade obvious.
		cascReason := reason
		if cascReason == "" {
			cascReason = "client " + id + " revoked"
		} else {
			cascReason = "client " + id + " revoked: " + reason
		}
		if err := e.RevokeGrant(ctx, g.ID, decidedBy, cascReason); err != nil {
			return err
		}
	}

	if err := e.store.RevokeClient(ctx, id); err != nil {
		return err
	}

	entry := audit.Entry{
		Type:    "client.revoked",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: defaulted(decidedBy, "user")},
		Subject: &audit.Subject{Kind: audit.SubjectClient, ID: c.ID},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"client_name":     c.Name,
			"grants_cascaded": len(grants),
		},
	}
	if reason != "" {
		entry.Data["reason"] = reason
	}
	_, _ = e.audit.Append(ctx, entry)
	return nil
}

// --- Audit helpers -----------------------------------------------------

func (e *Engine) recordPairFailed(ctx context.Context, code string, cause error) {
	reason := "unknown"
	switch {
	case errors.Is(cause, ErrPairingCodeNotFound):
		reason = "code not found"
	case errors.Is(cause, ErrPairingCodeExpired):
		reason = "code expired"
	case errors.Is(cause, ErrPairingCodeAlreadyRedeemed):
		reason = "code already redeemed"
	}
	// Codes are short and non-secret-after-issuance; we log them so
	// users can correlate failed attempts with the code they generated.
	entry := audit.Entry{
		Type:    "client.pair_failed",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: "user"},
		Outcome: audit.OutcomeDenied,
		Data: map[string]any{
			"code":   code,
			"reason": reason,
		},
	}
	_, _ = e.audit.Append(ctx, entry)
}

func (e *Engine) recordAuthFailed(ctx context.Context, clientID, reason string) {
	actor := audit.Actor{Kind: audit.ActorClient, ID: clientID}
	if clientID == "" {
		actor = audit.Actor{Kind: audit.ActorSystem, ID: "unauthenticated"}
	}
	entry := audit.Entry{
		Type:    "client.auth_failed",
		Actor:   actor,
		Outcome: audit.OutcomeDenied,
		Data: map[string]any{
			"reason": reason,
		},
	}
	if clientID != "" {
		entry.Subject = &audit.Subject{Kind: audit.SubjectClient, ID: clientID}
	}
	_, _ = e.audit.Append(ctx, entry)
}

// --- Token + code helpers ----------------------------------------------

// generatePairingCode returns a string of the form "ABCD-EFGH" using
// the confusion-resistant alphabet defined above.
func generatePairingCode() (string, error) {
	const codeLen = 8
	bytes := make([]byte, codeLen)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("permission: reading entropy for code: %w", err)
	}
	out := make([]byte, codeLen)
	for i, b := range bytes {
		out[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return string(out[:4]) + "-" + string(out[4:]), nil
}

// generateSecret returns 32 random bytes (the secret portion of the
// bearer token).
func generateSecret() ([]byte, error) {
	buf := make([]byte, tokenSecretLen)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("permission: reading entropy for token: %w", err)
	}
	return buf, nil
}

// assembleToken joins prefix, client id, and the base64url-encoded
// secret with the canonical separator. The base64 is unpadded
// because the secret length (32 bytes → 43 chars) is fixed.
func assembleToken(clientID string, secret []byte) string {
	return tokenPrefix + tokenSeparator + clientID + tokenSeparator +
		base64.RawURLEncoding.EncodeToString(secret)
}

// splitToken validates the token shape and returns the client id and
// raw secret bytes. Any deviation from the canonical form yields an
// error — callers translate that to ErrInvalidCredential without
// exposing the specific shape failure.
func splitToken(token string) (clientID string, secret []byte, err error) {
	parts := strings.SplitN(token, tokenSeparator, tokenFieldCount)
	if len(parts) != tokenFieldCount {
		return "", nil, errors.New("permission: malformed token")
	}
	if parts[0] != tokenPrefix {
		return "", nil, errors.New("permission: token has unexpected prefix")
	}
	if !strings.HasPrefix(parts[1], "cli-") {
		return "", nil, errors.New("permission: token has unexpected client id shape")
	}
	secret, err = base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", nil, fmt.Errorf("permission: decoding token secret: %w", err)
	}
	if len(secret) != tokenSecretLen {
		return "", nil, errors.New("permission: token secret has unexpected length")
	}
	return parts[1], secret, nil
}

// hashSecret returns hex(sha256(secret)). The secrets are uniformly
// random 256-bit values; a single SHA-256 is sufficient (the threat
// model is "stolen DB", not "weak user password").
func hashSecret(secret []byte) string {
	sum := sha256.Sum256(secret)
	return hex.EncodeToString(sum[:])
}

// constantTimeEqualHex compares two hex strings in constant time.
// Returns false if either string is malformed hex.
func constantTimeEqualHex(a, b string) bool {
	ab, err := hex.DecodeString(a)
	if err != nil {
		return false
	}
	bb, err := hex.DecodeString(b)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1
}
