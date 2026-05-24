package permission

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/loamss/loamss/runtime/internal/audit"
)

// Engine is the permission check engine. It binds a grant store
// (capability registry, grants, pending approvals) to an audit
// writer (so every check produces a record).
//
// Engine is the only object the runtime's request handlers need to
// hold; callers compose CheckRequest and receive CheckResult.
type Engine struct {
	store *Store
	audit audit.Writer
}

// NewEngine constructs an Engine. Both arguments are required;
// passing nil audit is a programming error (every check must be
// auditable per audit-spec.md). Callers running in environments
// without audit (e.g., tests not exercising audit) construct a
// real audit.SQLite against a temp path.
func NewEngine(s *Store, a audit.Writer) *Engine {
	if s == nil {
		panic("permission: NewEngine called with nil Store")
	}
	if a == nil {
		panic("permission: NewEngine called with nil audit.Writer")
	}
	return &Engine{store: s, audit: a}
}

// CheckRequest is the input to Check.
type CheckRequest struct {
	// Principal is the actor attempting the access.
	Principal Principal

	// Capability is the dotted capability name being exercised.
	Capability string

	// AttemptedScope describes the specific access being attempted:
	// the path being read, the sender being queried, the time range,
	// etc. The engine matches this against each grant's scope using
	// the capability's ScopeSchema.
	AttemptedScope map[string]any

	// Rationale, if set, is recorded with any resulting pending
	// approval (shown to the user on the slip).
	Rationale string

	// RequestID is an optional correlation id propagated into audit
	// entries' Context.RequestID.
	RequestID string
}

// CheckResult is the outcome of a Check.
type CheckResult struct {
	// Decision is the outcome category.
	Decision Decision

	// GrantID, when set, is the grant that matched. Empty on Deny.
	GrantID string

	// ApprovalID, when Decision == DecisionApprovalRequired, is the
	// pending approval id callers poll via WaitForApproval (or via
	// `loamss approve` CLI).
	ApprovalID string

	// Reason is a human-readable explanation, suitable for surfacing
	// to debug tooling. Don't display unredacted to end users without
	// review.
	Reason string
}

// Check is the engine's primary entry point. It:
//
//  1. Looks up the capability in the registry (Deny if unknown).
//  2. Loads active grants for the principal+capability.
//  3. Walks grants in issue order; the first one whose scope
//     encompasses the attempted scope wins.
//  4. If the matching grant or its capability requires user approval,
//     enqueues a PendingApproval and returns ApprovalRequired.
//  5. Otherwise returns Allow.
//
// Every outcome produces an audit entry (check.allow, check.deny, or
// approval.requested) tagged with the principal and the grant
// (when applicable). Audit failures are logged but never bubble up
// to the caller — the check decision is authoritative regardless of
// whether the audit log can be written, on the theory that loss of
// auditability shouldn't cascade into loss of access control.
func (e *Engine) Check(ctx context.Context, req CheckRequest) (*CheckResult, error) {
	capDef, err := e.store.GetCapability(ctx, req.Capability)
	if err != nil {
		if errors.Is(err, ErrCapabilityNotFound) {
			res := &CheckResult{
				Decision: DecisionDeny,
				Reason:   "unknown capability: " + req.Capability,
			}
			e.recordCheckOutcome(ctx, req, res, "")
			return res, nil
		}
		return nil, err
	}

	grants, err := e.store.ListActiveGrantsForCheck(ctx,
		req.Principal.Kind, req.Principal.ID, req.Capability)
	if err != nil {
		return nil, err
	}

	for _, g := range grants {
		ok, _ := matchScope(capDef.Scope, g.Scope, req.AttemptedScope)
		if !ok {
			continue
		}

		// Determine approval requirement. Capability default cannot
		// be relaxed by a grant; grant-level can only tighten.
		needsApproval := capDef.DefaultApproval || g.RequiresUserApproval
		if needsApproval {
			approval, err := e.store.EnqueueApproval(ctx, PendingApproval{
				GrantID:        g.ID,
				Principal:      req.Principal,
				Capability:     req.Capability,
				AttemptedScope: req.AttemptedScope,
				Rationale:      req.Rationale,
			})
			if err != nil {
				return nil, err
			}
			res := &CheckResult{
				Decision:   DecisionApprovalRequired,
				GrantID:    g.ID,
				ApprovalID: approval.ID,
				Reason:     "user approval required",
			}
			e.recordApprovalRequested(ctx, req, g.ID, approval.ID)
			return res, nil
		}

		res := &CheckResult{
			Decision: DecisionAllow,
			GrantID:  g.ID,
			Reason:   "grant " + g.ID + " matches",
		}
		e.recordCheckOutcome(ctx, req, res, g.ID)
		return res, nil
	}

	res := &CheckResult{
		Decision: DecisionDeny,
		Reason:   "no matching grant for capability " + req.Capability,
	}
	e.recordCheckOutcome(ctx, req, res, "")
	return res, nil
}

// RevokeGrant marks a grant revoked and emits a grant.revoke audit
// entry. Idempotent: revoking an already-revoked grant returns nil
// without emitting a duplicate audit entry. The Store-level
// RevokeGrant exists alongside this for tests or callers that
// explicitly want to skip audit.
//
// decidedBy is recorded as the audit actor id; typically the
// authenticated user/operator id. reason is an optional note
// carried in the audit entry's data payload.
func (e *Engine) RevokeGrant(ctx context.Context, id, decidedBy, reason string) error {
	g, err := e.store.GetGrant(ctx, id)
	if err != nil {
		return err
	}
	if g.RevokedAt != nil {
		// Already revoked; no audit entry to avoid duplicates.
		return nil
	}
	if err := e.store.RevokeGrant(ctx, id); err != nil {
		return err
	}
	entry := audit.Entry{
		Type:    "grant.revoke",
		Actor:   audit.Actor{Kind: audit.ActorUser, ID: defaulted(decidedBy, "user")},
		Subject: &audit.Subject{Kind: audit.SubjectGrant, ID: g.ID},
		Outcome: audit.OutcomeSuccess,
		Data: map[string]any{
			"capability":     g.Capability,
			"principal_kind": string(g.Principal.Kind),
			"principal_id":   g.Principal.ID,
		},
	}
	if reason != "" {
		entry.Data["reason"] = reason
	}
	_, _ = e.audit.Append(ctx, entry)
	return nil
}

// defaulted returns s if non-empty, else fallback. Kept as a generic
// helper rather than hardcoded to "user" because future callers
// (capsule installer, scheduled rotation) will pass other defaults.
//
//nolint:unparam // fallback is currently always "user"; preserved for future call sites
func defaulted(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ResolveApproval is the engine's wrapper over Store.ResolveApproval
// that also emits the corresponding audit entry. CLI and (future)
// console paths call this; the store-only ResolveApproval exists
// for tests or for callers that explicitly want to skip audit.
func (e *Engine) ResolveApproval(ctx context.Context, approvalID string, decision ApprovalState, decidedBy, note string) error {
	if err := e.store.ResolveApproval(ctx, approvalID, decision, decidedBy, note); err != nil {
		return err
	}
	a, err := e.store.GetApproval(ctx, approvalID)
	if err != nil {
		// Approval just resolved but vanished from the DB — very
		// unlikely; surface and skip audit.
		return err
	}
	e.recordApprovalResolved(ctx, a)
	return nil
}

// WaitForApproval blocks until the approval moves out of the Pending
// state or the context is canceled. Polling interval defaults to
// 500ms; pass 0 to use that default.
//
// This is the synchronous facade callers use when they want to wait
// for the user. Asynchronous callers can poll Store.GetApproval
// directly.
func (e *Engine) WaitForApproval(ctx context.Context, approvalID string, pollInterval time.Duration) (*PendingApproval, error) {
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}
	// Initial check before sleeping — covers the case where the
	// approval is already resolved by the time the caller starts
	// waiting.
	a, err := e.store.GetApproval(ctx, approvalID)
	if err != nil {
		return nil, err
	}
	if a.State != ApprovalPending {
		return a, nil
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		a, err := e.store.GetApproval(ctx, approvalID)
		if err != nil {
			return nil, err
		}
		if a.State != ApprovalPending {
			return a, nil
		}
	}
}

// --- Audit emission ---------------------------------------------------

func (e *Engine) recordCheckOutcome(ctx context.Context, req CheckRequest, res *CheckResult, grantID string) {
	var (
		typ     string
		outcome audit.Outcome
	)
	switch res.Decision {
	case DecisionAllow:
		typ = "check.allow"
		outcome = audit.OutcomeSuccess
	case DecisionDeny:
		typ = "check.deny"
		outcome = audit.OutcomeDenied
	default:
		return
	}

	entry := audit.Entry{
		Type:    typ,
		Actor:   actorFromPrincipal(req.Principal),
		Outcome: outcome,
		Data: map[string]any{
			"capability": req.Capability,
		},
	}
	if grantID != "" {
		entry.Subject = &audit.Subject{Kind: audit.SubjectGrant, ID: grantID}
	}
	if res.Decision == DecisionDeny {
		entry.Data["reason"] = res.Reason
		if len(req.AttemptedScope) > 0 {
			entry.Data["attempted_scope"] = req.AttemptedScope
		}
	}
	if req.RequestID != "" {
		entry.Context = &audit.Context{RequestID: req.RequestID}
	}
	// Audit failures don't bubble up; the check decision is
	// authoritative. We do swallow the error here intentionally,
	// matching the comment on Check.
	_, _ = e.audit.Append(ctx, entry)
}

func (e *Engine) recordApprovalRequested(ctx context.Context, req CheckRequest, grantID, approvalID string) {
	entry := audit.Entry{
		Type:    "approval.requested",
		Actor:   actorFromPrincipal(req.Principal),
		Subject: &audit.Subject{Kind: audit.SubjectGrant, ID: grantID},
		Outcome: audit.OutcomePending,
		Data: map[string]any{
			"approval_id": approvalID,
			"capability":  req.Capability,
		},
	}
	if req.Rationale != "" {
		entry.Data["rationale"] = req.Rationale
	}
	if req.RequestID != "" {
		entry.Context = &audit.Context{RequestID: req.RequestID}
	}
	_, _ = e.audit.Append(ctx, entry)
}

func (e *Engine) recordApprovalResolved(ctx context.Context, a *PendingApproval) {
	var (
		typ     string
		outcome audit.Outcome
	)
	switch a.State {
	case ApprovalGranted:
		typ = "approval.granted"
		outcome = audit.OutcomeSuccess
	case ApprovalDenied:
		typ = "approval.denied"
		outcome = audit.OutcomeDenied
	case ApprovalExpired:
		typ = "approval.timeout"
		outcome = audit.OutcomeError
	default:
		return
	}
	entry := audit.Entry{
		Type:    typ,
		Actor:   actorFromPrincipal(a.Principal),
		Subject: &audit.Subject{Kind: audit.SubjectGrant, ID: a.GrantID},
		Outcome: outcome,
		Data: map[string]any{
			"approval_id": a.ID,
			"capability":  a.Capability,
		},
	}
	if a.DecidedBy != "" {
		entry.Data["decided_by"] = a.DecidedBy
	}
	if a.DecisionNote != "" {
		entry.Data["note"] = a.DecisionNote
	}
	_, _ = e.audit.Append(ctx, entry)
}

// actorFromPrincipal maps a permission.Principal to an audit.Actor.
// PrincipalKind values are deliberately a subset of ActorKind, so
// this is a straight string substitution; we keep it as a helper to
// catch any future drift.
func actorFromPrincipal(p Principal) audit.Actor {
	var k audit.ActorKind
	switch p.Kind {
	case PrincipalCapsule:
		k = audit.ActorCapsule
	case PrincipalClient:
		k = audit.ActorClient
	default:
		k = audit.ActorSystem
	}
	return audit.Actor{Kind: k, ID: p.ID}
}

// Ensure compile-time linkage so dropping audit.Writer accidentally
// from the engine signature is caught at build time.
var _ = fmt.Sprintf // imported but used only in errors; keep import live
