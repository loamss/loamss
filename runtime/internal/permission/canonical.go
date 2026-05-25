package permission

import (
	"strings"
	"time"
)

// reservedNamespaces are the leading dot-separated prefixes capsules
// cannot register capabilities under. Per permission-model.md
// §Reserved namespaces.
var reservedNamespaces = []string{
	"runtime.",
	"loamss.",
	"audit.",
	"permission.",
	"pairing.",
}

// reservedExceptions are capability names that LOOK reserved but
// are explicitly allowed (e.g., audit.read so paired clients can
// read the audit log).
var reservedExceptions = map[string]bool{
	"audit.read": true,
}

// IsReservedNamespace reports whether name falls within a reserved
// namespace (runtime.*, loamss.*, audit.*, permission.*, pairing.*)
// and is not on the exceptions list. Capsule manifests are rejected
// at validate time if they declare a capability matching this rule.
// Exported so the capsule package can validate without re-implementing
// the rule.
func IsReservedNamespace(name string) bool {
	if reservedExceptions[name] {
		return false
	}
	for _, prefix := range reservedNamespaces {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// isReservedNamespace is the package-private alias preserved for the
// store's internal use. New code should call the exported variant.
func isReservedNamespace(name string) bool { return IsReservedNamespace(name) }

// namespaceOf returns the leading dot-separated component of a
// capability name, or the whole name if no dot is present.
func namespaceOf(name string) string {
	if i := strings.Index(name, "."); i >= 0 {
		return name[:i]
	}
	return name
}

// canonicalCapabilities is the v0.1 MVP set seeded into the
// registry at first migration. Each entry is the user's chosen
// "full inbound set" from the scope conversation.
//
// Scope schemas reference the match primitives defined in types.go.
// Adding new capabilities or new fields here is a migration-bearing
// change (existing grants may need re-validation against the
// stricter schema).
func canonicalCapabilities(now time.Time) []CapabilityDef {
	return []CapabilityDef{
		{
			Name:            "memory.read",
			Namespace:       "memory",
			Direction:       DirectionInbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"entities":              MatchSetIntersect,
				"data_classes_included": MatchSetSubset,
				"data_classes_excluded": MatchSetExcludes,
			},
			RegisteredAt: now,
		},
		{
			Name:            "memory.query",
			Namespace:       "memory",
			Direction:       DirectionInbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"entities":              MatchSetIntersect,
				"time_range":            MatchRangeIncludes,
				"data_classes_included": MatchSetSubset,
				"data_classes_excluded": MatchSetExcludes,
			},
			RegisteredAt: now,
		},
		{
			// memory.write is how capsules (and external clients
			// granted it explicitly) persist new entities into
			// memory. Scope narrows by entity type / namespace —
			// a tax-organizer capsule with scope
			// {entities: ["com.acme.tax/receipt"]} can write only
			// receipts, not arbitrary entries.
			Name:            "memory.write",
			Namespace:       "memory",
			Direction:       DirectionInternal,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"entities":              MatchSetIntersect,
				"data_classes_included": MatchSetSubset,
				"data_classes_excluded": MatchSetExcludes,
			},
			RegisteredAt: now,
		},
		{
			Name:            "files.read",
			Namespace:       "files",
			Direction:       DirectionInbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"paths":                 MatchGlobList,
				"time_range":            MatchRangeIncludes,
				"data_classes_included": MatchSetSubset,
				"data_classes_excluded": MatchSetExcludes,
			},
			RegisteredAt: now,
		},
		{
			Name:            "audit.read",
			Namespace:       "audit",
			Direction:       DirectionInbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"time_range":  MatchRangeIncludes,
				"event_types": MatchSetSubset,
			},
			RegisteredAt: now,
		},
		{
			Name:            "email.read",
			Namespace:       "email",
			Direction:       DirectionInbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"sender":     MatchSenderGlob,
				"folder":     MatchEquals,
				"time_range": MatchRangeIncludes,
				"thread_id":  MatchEquals,
			},
			RegisteredAt: now,
		},
		{
			Name:            "calendar.read",
			Namespace:       "calendar",
			Direction:       DirectionInbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"tag":        MatchEquals,
				"time_range": MatchRangeIncludes,
			},
			RegisteredAt: now,
		},
		{
			Name:            "messages.read",
			Namespace:       "messages",
			Direction:       DirectionInbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"channel":    MatchEquals,
				"time_range": MatchRangeIncludes,
			},
			RegisteredAt: now,
		},
		{
			Name:            "content.list",
			Namespace:       "content",
			Direction:       DirectionInbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"tag":  MatchEquals,
				"type": MatchEquals,
			},
			RegisteredAt: now,
		},
		{
			Name:            "content.read",
			Namespace:       "content",
			Direction:       DirectionInbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"tag":         MatchEquals,
				"type":        MatchEquals,
				"resource_id": MatchEquals,
			},
			RegisteredAt: now,
		},
		// Two representative event-write capabilities pre-registered.
		// Capsules introducing new event types call RegisterCapability
		// with their own <type>.write entries.
		{
			Name:            "content.metrics.write",
			Namespace:       "events",
			Direction:       DirectionInbound, // event-write is principal-to-runtime, hence inbound
			DefaultApproval: false,
			Scope: ScopeSchema{
				"subject_pattern": MatchGlobList,
			},
			RegisteredAt: now,
		},
		{
			Name:            "content.revenue.write",
			Namespace:       "events",
			Direction:       DirectionInbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"subject_pattern": MatchGlobList,
			},
			RegisteredAt: now,
		},
		// --- consequential-action capabilities (default-approval) ---
		// These are the "this touches the outside world" capabilities.
		// DefaultApproval: true forces an interactive confirmation on
		// every invocation regardless of how narrow the per-grant
		// scope is — the safety rail consequential-action review
		// depends on. Capsules and clients may still tighten scope;
		// they cannot relax DefaultApproval.
		{
			Name:            "email.send",
			Namespace:       "email",
			Direction:       DirectionOutbound,
			DefaultApproval: true,
			Scope: ScopeSchema{
				"recipient_pattern": MatchGlobList,
				"sender":            MatchEquals,
				"reply_to_thread":   MatchEquals,
			},
			RegisteredAt: now,
		},
		{
			Name:            "email.draft",
			Namespace:       "email",
			Direction:       DirectionInternal,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"folder":          MatchEquals,
				"reply_to_thread": MatchEquals,
			},
			RegisteredAt: now,
		},
		{
			Name:            "calendar.write",
			Namespace:       "calendar",
			Direction:       DirectionOutbound,
			DefaultApproval: true,
			Scope: ScopeSchema{
				"tag":             MatchEquals,
				"calendar_id":     MatchEquals,
				"attendee_domain": MatchGlobList,
			},
			RegisteredAt: now,
		},
		{
			Name:            "messages.send",
			Namespace:       "messages",
			Direction:       DirectionOutbound,
			DefaultApproval: true,
			Scope: ScopeSchema{
				"channel":           MatchEquals,
				"recipient_pattern": MatchGlobList,
			},
			RegisteredAt: now,
		},
		// --- write to user storage (files.write) ---
		// Not Outbound — files.write affects user-owned storage, not
		// the outside world. But it can do real damage (overwrite,
		// delete-by-replace), so we DefaultApproval to true for the
		// safety rail. Sophisticated organizer capsules (tax-organizer
		// writing receipts to a dedicated folder) may bypass via
		// scope narrowing the user explicitly approves once at install.
		{
			Name:            "files.write",
			Namespace:       "files",
			Direction:       DirectionInternal,
			DefaultApproval: true,
			Scope: ScopeSchema{
				"paths":                 MatchGlobList,
				"data_classes_included": MatchSetSubset,
			},
			RegisteredAt: now,
		},
		// --- model.call (capsules dispatch through router) ---
		// Routing decisions (which provider, which model id, cost
		// ceiling, data-class filters) live in the router; the
		// capability is just "may you initiate a model call at all."
		// DefaultApproval: false — model calls are routine for organizer
		// capsules; per-call approval would make them unusable.
		// Cost ceilings live on the per-grant scope.
		{
			Name:            "model.call",
			Namespace:       "model",
			Direction:       DirectionOutbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"tasks":                  MatchSetSubset,
				"forbidden_data_classes": MatchSetExcludes,
				"cost_ceiling_usd":       MatchRangeIncludes,
			},
			RegisteredAt: now,
		},
		// --- credentials.{read,write} (capsule ingestors persist secrets) ---
		// Ingestor capsules need to persist long-lived secrets (OAuth
		// refresh tokens, API keys) between syncs. The credentials.*
		// MCP tools store these via the runtime's configured storage
		// adapter — same encryption-at-rest path source connectors use.
		// Both capabilities are capsule-namespaced by construction
		// (the tool dispatcher reads the principal name and keys the
		// blob there), so no per-key scope is needed.
		// DefaultApproval: false — capsule installation already
		// surfaces the rationale on these in the manifest; per-call
		// approval would make ingestion unusable.
		{
			Name:            "credentials.read",
			Namespace:       "credentials",
			Direction:       DirectionInternal,
			DefaultApproval: false,
			Scope:           ScopeSchema{},
			RegisteredAt:    now,
		},
		{
			Name:            "credentials.write",
			Namespace:       "credentials",
			Direction:       DirectionInternal,
			DefaultApproval: false,
			Scope:           ScopeSchema{},
			RegisteredAt:    now,
		},
		// --- external.http (capsules talking to the outside) ---
		// Capsules that need to call third-party APIs (Slack webhooks,
		// Stripe, etc.) declare external.http with a host allowlist.
		// DefaultApproval: false because by the time we're approving
		// the grant we already approved the host list; per-call
		// approval would defeat capsule autonomy.
		{
			Name:            "external.http",
			Namespace:       "external",
			Direction:       DirectionOutbound,
			DefaultApproval: false,
			Scope: ScopeSchema{
				"hosts":            MatchGlobList,
				"methods":          MatchSetSubset,
				"max_bytes":        MatchRangeIncludes,
				"cost_ceiling_usd": MatchRangeIncludes,
			},
			RegisteredAt: now,
		},
	}
}
