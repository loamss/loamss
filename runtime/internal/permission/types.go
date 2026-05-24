// Package permission implements the capability framework gating every
// read, write, and external action the runtime performs on a user's
// data. The contract is defined in permission-model.md; this package
// translates the model into Go.
//
// Two kinds of principals — capsules (untrusted code installed by the
// user) and external clients (paired via MCP) — hold grants of the
// same shape. Every grant ties a capability to a scope, with optional
// expiry and an optional approval flag. The runtime never accesses
// user data without first running a Check against the framework.
//
// Components:
//
//   - Types (this file): Grant, Principal, CapabilityDef, ScopeSchema,
//     MatchPrimitive, Direction, Framing, PendingApproval, ApprovalState,
//     Decision, sentinel errors.
//   - Persistence (store.go): SQLite-backed store wrapping runtime.db;
//     grants, capability registry, pending approvals.
//   - Canonical capabilities (canonical.go): the 9 MVP capabilities
//     pre-registered at first migration.
//   - Check engine (engine.go, future commit): the actual Check
//     decision logic plus match primitives.
//
// v0.1 has the framework persistence and canonical registry; the
// check engine and CLI surfacing land in subsequent commits.
package permission

import (
	"errors"
	"time"
)

// PrincipalKind identifies which class of actor holds a grant.
type PrincipalKind string

// Principal kinds.
const (
	PrincipalCapsule PrincipalKind = "capsule"
	PrincipalClient  PrincipalKind = "client"
)

// Principal is the actor a grant attaches to. The runtime resolves
// each request to a Principal before consulting the framework.
type Principal struct {
	Kind PrincipalKind `json:"kind"`
	ID   string        `json:"id"`
}

// Direction classifies what a capability does to the world.
type Direction string

// Direction values per permission-model.md.
const (
	// DirectionInbound: principal reads user data.
	DirectionInbound Direction = "inbound"
	// DirectionOutbound: principal takes action affecting the outside world.
	DirectionOutbound Direction = "outbound"
	// DirectionInternal: principal operates on Loamss-internal state.
	DirectionInternal Direction = "internal"
)

// Framing distinguishes how a grant is presented in the permission slip.
// Same underlying capability, different framing on the UI — enforcement
// is identical. See permission-model.md §Public-publish vs private-read.
type Framing string

// Framing values.
const (
	FramingPrivateRead   Framing = "private_read"
	FramingPublicPublish Framing = "public_publish"
)

// MatchPrimitive identifies how a scope field should be matched against
// an attempted value. Registered capabilities declare a ScopeSchema
// mapping each scope field to a primitive; the check engine dispatches
// on the primitive. Adding a new primitive requires a runtime code
// change — capsules may use any existing primitive but cannot
// introduce new ones.
type MatchPrimitive string

// Match primitives. The check engine implements one matcher function
// per primitive. See engine.go (future commit) for the implementations.
const (
	// MatchEquals: scope value equals attempted value (string-comparable).
	MatchEquals MatchPrimitive = "equals"
	// MatchGlobList: scope is a list of glob patterns; attempted value
	// must match at least one.
	MatchGlobList MatchPrimitive = "glob_list"
	// MatchPrefix: scope is a string prefix; attempted value must start with it.
	MatchPrefix MatchPrimitive = "prefix"
	// MatchSetIntersect: scope is a set; attempted is a set; non-empty
	// intersection required.
	MatchSetIntersect MatchPrimitive = "set_intersect"
	// MatchSetSubset: scope is a set; attempted is a set; all attempted
	// elements must be in scope.
	MatchSetSubset MatchPrimitive = "set_subset"
	// MatchSetExcludes: scope is a set; attempted is a set; intersection
	// must be empty.
	MatchSetExcludes MatchPrimitive = "set_excludes"
	// MatchRangeIncludes: scope is {since, until}; attempted value is a
	// time; must fall in the range.
	MatchRangeIncludes MatchPrimitive = "range_includes"
	// MatchSenderGlob: scope is an email-address glob; attempted is an
	// email address. Distinct from glob_list because senders have
	// domain semantics worth specializing.
	MatchSenderGlob MatchPrimitive = "sender_glob"
)

// ScopeSchema declares the match primitive for each scope field a
// capability accepts. Empty schema means the capability has no
// scope (rare; usually means "all-or-nothing" capability).
type ScopeSchema map[string]MatchPrimitive

// CapabilityDef describes a capability registered with the runtime.
// Canonical capabilities are pre-registered at first migration;
// capsule-declared capabilities are registered at install time via
// Store.RegisterCapability.
type CapabilityDef struct {
	// Name is the dotted capability identifier (e.g., "memory.query").
	Name string `json:"name"`

	// Namespace is the leading dot-separated component. Used for
	// reserved-namespace enforcement and for grouping in UIs.
	Namespace string `json:"namespace"`

	// Direction classifies what the capability does.
	Direction Direction `json:"direction"`

	// DefaultApproval, if true, means every invocation requires user
	// approval — even if the grant doesn't ask for it. Per-grant
	// approval flags can only further tighten this, never relax it.
	DefaultApproval bool `json:"default_approval"`

	// Scope declares the match primitive for each scope field.
	Scope ScopeSchema `json:"scope"`

	// DeclaredBy records which capsule registered this capability.
	// Empty for canonical capabilities pre-registered by the runtime.
	DeclaredBy string `json:"declared_by,omitempty"`

	// RegisteredAt is when the capability was added to the registry.
	RegisteredAt time.Time `json:"registered_at"`
}

// Grant ties a capability to a principal under a specific scope.
type Grant struct {
	// ID is the unique grant identifier (grt-<ULID>).
	ID string `json:"id"`

	// Principal is the actor the grant applies to.
	Principal Principal `json:"principal"`

	// Capability is the capability name (must exist in the registry).
	Capability string `json:"capability"`

	// Scope is the user-narrowed scope, conforming to the capability's
	// ScopeSchema. Validated at issue time against the schema.
	Scope map[string]any `json:"scope,omitempty"`

	// Framing controls the UX framing on the permission slip
	// (private_read vs public_publish). Enforcement does not change
	// based on framing.
	Framing Framing `json:"framing"`

	// Rationale is the capsule- or client-supplied reason for the grant
	// (shown on the permission slip).
	Rationale string `json:"rationale,omitempty"`

	// UserNote is optional user-added context (e.g., "approved during
	// the Sarah onboarding").
	UserNote string `json:"user_note,omitempty"`

	// RequiresUserApproval, if true, makes every invocation interactive.
	// May only strengthen the capability's DefaultApproval, never relax it.
	RequiresUserApproval bool `json:"requires_user_approval"`

	// IssuedAt is when the grant was created.
	IssuedAt time.Time `json:"issued_at"`

	// ExpiresAt, if non-nil, makes the grant inactive after this time.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	// RevokedAt, if non-nil, records when the grant was revoked.
	// Revoked grants are retained for audit but never match a check.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Active returns true if the grant is currently effective: not revoked
// and not expired.
func (g Grant) Active(now time.Time) bool {
	if g.RevokedAt != nil {
		return false
	}
	if g.ExpiresAt != nil && !now.Before(*g.ExpiresAt) {
		return false
	}
	return true
}

// ApprovalState is the lifecycle of a pending approval.
type ApprovalState string

// ApprovalState values.
const (
	ApprovalPending ApprovalState = "pending"
	ApprovalGranted ApprovalState = "granted"
	ApprovalDenied  ApprovalState = "denied"
	ApprovalExpired ApprovalState = "expired"
)

// PendingApproval represents a Check that returned ApprovalRequired
// and is waiting for the user to grant or deny. The user resolves it
// via the console or the `loamss approve` CLI; the original caller
// polls GetApproval until the state moves out of Pending.
type PendingApproval struct {
	ID             string         `json:"id"` // "apr-<ULID>"
	GrantID        string         `json:"grant_id"`
	Principal      Principal      `json:"principal"`
	Capability     string         `json:"capability"`
	AttemptedScope map[string]any `json:"attempted_scope,omitempty"`
	Rationale      string         `json:"rationale,omitempty"`
	State          ApprovalState  `json:"state"`
	RequestedAt    time.Time      `json:"requested_at"`
	DecidedAt      *time.Time     `json:"decided_at,omitempty"`
	DecidedBy      string         `json:"decided_by,omitempty"` // "user" | "timeout" | etc.
	DecisionNote   string         `json:"decision_note,omitempty"`
}

// Decision is the result of a Check.
type Decision string

// Decision values.
const (
	// DecisionAllow: principal may proceed.
	DecisionAllow Decision = "allow"
	// DecisionDeny: principal is rejected; reason in CheckResult.Reason.
	DecisionDeny Decision = "deny"
	// DecisionApprovalRequired: a grant matches but user approval is
	// required. CheckResult.ApprovalID is set; caller polls until
	// the approval is resolved.
	DecisionApprovalRequired Decision = "approval_required"
)

// Grant status strings used by GrantFilter and CLI output.
const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
	StatusExpired = "expired"
	StatusAll     = "all"
)

// GrantFilter narrows a ListGrants query. Zero-valued fields impose
// no constraint on that dimension. Status defaults to "active" if
// empty (current grants only); pass "all" for the full set including
// revoked and expired.
type GrantFilter struct {
	PrincipalKind PrincipalKind
	PrincipalID   string
	Capability    string
	Status        string // active | revoked | expired | all
	Limit         int    // 0 = default 1000
}

// Client is an external MCP client paired with the runtime. Clients
// are one of the two principal kinds; their grants live in the same
// grants table as capsules', keyed on Principal{Kind: PrincipalClient,
// ID: <Client.ID>}.
//
// The bearer credential is never stored in plaintext. Only the
// SHA-256 hex of the secret portion is persisted in CredentialHash;
// the full token is returned exactly once at RedeemPairingCode time.
type Client struct {
	// ID is the stable client identifier (cli-<ULID>). Used as the
	// Principal ID for every grant the client holds.
	ID string `json:"id"`

	// Name is the user-facing display name, supplied at `pair` time
	// ("ChatGPT laptop", "Claude desktop"). Not unique; for display only.
	Name string `json:"name"`

	// CredentialHash is the hex-encoded SHA-256 of the bearer-token
	// secret portion. Never the plaintext.
	CredentialHash string `json:"-"`

	// Metadata is opaque JSON attached at pair time. Used to record
	// pairing context (paired_via, client public key, version) without
	// schema churn. The runtime treats this as inert.
	Metadata map[string]any `json:"metadata,omitempty"`

	// PairedAt is when the pairing code was redeemed.
	PairedAt time.Time `json:"paired_at"`

	// LastSeenAt is the most recent successful AuthenticateClient
	// timestamp. Nil if the client has never authenticated.
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`

	// RevokedAt, if non-nil, marks the client as revoked. Revoked
	// clients fail authentication and all their grants are
	// cascade-revoked at the same time.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Active returns true if the client can authenticate right now.
func (c Client) Active() bool { return c.RevokedAt == nil }

// ClientFilter narrows a ListClients query. Status defaults to
// "active" when empty.
type ClientFilter struct {
	Status string // active | revoked | all
	Limit  int    // 0 = default 1000
}

// PairingCode is a one-time, TTL-bound code issued by
// `loamss client pair`. The client (or the user, on the client's
// behalf) redeems it to receive bearer credentials. Codes are
// single-use; redemption marks them as such.
//
// Grants are issued separately via `loamss grant create` after
// redemption — the pairing code carries no scope itself.
type PairingCode struct {
	// Code is the human-readable redemption string (e.g.,
	// "ABCD-1234"). It is the table's primary key.
	Code string `json:"code"`

	// ClientName is the display name supplied at code generation;
	// passed through to the Client created at redemption.
	ClientName string `json:"client_name"`

	// CreatedBy is the actor that generated the code (typically
	// "user"); recorded for audit.
	CreatedBy string `json:"created_by,omitempty"`

	// CreatedAt is when the code was generated.
	CreatedAt time.Time `json:"created_at"`

	// ExpiresAt is when the code stops being redeemable. Codes do
	// not become reusable after expiry — they are dead.
	ExpiresAt time.Time `json:"expires_at"`

	// RedeemedAt is when the code was consumed, if at all.
	RedeemedAt *time.Time `json:"redeemed_at,omitempty"`

	// RedeemedClientID is the Client.ID created on redemption. Nil
	// while the code is still pending.
	RedeemedClientID string `json:"redeemed_client_id,omitempty"`
}

// Active returns true if the code can still be redeemed at `now`.
func (p PairingCode) Active(now time.Time) bool {
	if p.RedeemedAt != nil {
		return false
	}
	return now.Before(p.ExpiresAt)
}

// Sentinel errors wrapped by store/registry/check operations.
// Callers test with errors.Is.
var (
	// ErrCapabilityNotFound: the requested capability is not registered.
	ErrCapabilityNotFound = errors.New("permission: capability not registered")

	// ErrCapabilityAlreadyRegistered: RegisterCapability called twice
	// for the same name with different definitions.
	ErrCapabilityAlreadyRegistered = errors.New("permission: capability already registered")

	// ErrReservedNamespace: a capsule attempted to register a capability
	// in a runtime-reserved namespace.
	ErrReservedNamespace = errors.New("permission: capability namespace is reserved")

	// ErrGrantNotFound: the requested grant does not exist.
	ErrGrantNotFound = errors.New("permission: grant not found")

	// ErrGrantRevoked: the grant exists but has been revoked.
	ErrGrantRevoked = errors.New("permission: grant has been revoked")

	// ErrGrantExpired: the grant's expires_at has passed.
	ErrGrantExpired = errors.New("permission: grant has expired")

	// ErrApprovalNotFound: the requested pending approval doesn't exist.
	ErrApprovalNotFound = errors.New("permission: approval not found")

	// ErrApprovalAlreadyResolved: tried to resolve an approval that
	// already left the pending state.
	ErrApprovalAlreadyResolved = errors.New("permission: approval already resolved")

	// ErrScopeViolatesSchema: a grant's scope contains a field not in
	// the capability's schema, or has a wrong value type.
	ErrScopeViolatesSchema = errors.New("permission: scope violates capability schema")

	// ErrInvalidApprovalDowngrade: tried to set a grant's
	// RequiresUserApproval=false on a capability whose default is true.
	ErrInvalidApprovalDowngrade = errors.New("permission: cannot weaken default approval requirement")

	// ErrClientNotFound: the requested client does not exist.
	ErrClientNotFound = errors.New("permission: client not found")

	// ErrClientRevoked: the client exists but has been revoked. Returned
	// by AuthenticateClient; revoked clients cannot authenticate.
	ErrClientRevoked = errors.New("permission: client has been revoked")

	// ErrPairingCodeNotFound: the supplied code does not match any
	// issued pairing code.
	ErrPairingCodeNotFound = errors.New("permission: pairing code not found")

	// ErrPairingCodeExpired: the code exists but its expires_at has
	// passed. Expired codes are dead — they cannot be revived.
	ErrPairingCodeExpired = errors.New("permission: pairing code expired")

	// ErrPairingCodeAlreadyRedeemed: the code was single-use and has
	// already been redeemed.
	ErrPairingCodeAlreadyRedeemed = errors.New("permission: pairing code already redeemed")

	// ErrInvalidCredential: the supplied bearer token did not match
	// any active client. Used by AuthenticateClient.
	ErrInvalidCredential = errors.New("permission: invalid credential")
)
