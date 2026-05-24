// Package memory implements the semantic memory layer that sits
// above the memory.Adapter SPI. Where the adapter is dumb
// vector-and-metadata storage, the layer derives:
//
//   - Entities: people, organizations, projects, and the aliases
//     pointing at them ("Sarah Smith" ←→ "sarah@example.com")
//   - Threads: conversation threads (Gmail thread_id grouping)
//   - Mappings: which entries involve which entities + threads, so
//     capsules + apps can ask "what have I been discussing with Sarah?"
//     or "show me this thread"
//
// The layer owns its own SQLite tables (memory_layer_* in runtime.db).
// The vector adapter remains the source of truth for entries; the
// layer's tables are a derived index. They stay consistent because
// every Upsert / Delete is routed through Layer, which updates both
// the adapter and its own tables in lockstep.
//
// What's NOT in v0.1:
//
//   - Cross-source entity resolution (matching the same person across
//     Gmail, Slack, Calendar) — single-source only for now
//   - Episodic summarization (multi-document summaries) — out of scope
//   - Full knowledge graph (relations across entity kinds) — out of scope
//   - Live subscriptions / change feeds — Phase 2
package memory

import (
	"errors"
	"time"
)

// Entity is a thing the memory layer recognizes — a person, an
// organization, a place, a project. Entities are derived from the
// metadata of memory entries; they aren't a separate write surface.
// Two entries with the same canonical email address resolve to the
// same Entity.
type Entity struct {
	// ID is the layer-assigned identifier ("ent_01H...").
	ID string `json:"id"`

	// Kind is one of EntityKind* below. Determines which metadata
	// fields are meaningful for this entity.
	Kind EntityKind `json:"kind"`

	// Canonical is the human-readable name. For people this is the
	// display name from the most-recent message; falls back to the
	// email address local-part if no name is ever seen.
	Canonical string `json:"canonical"`

	// Namespace is the memory namespace this entity was first seen
	// in. Today v0.1 resolves entities per-namespace; cross-namespace
	// merging is Phase 2.
	Namespace string `json:"namespace"`

	// Aliases is every identifier observed for this entity — email
	// addresses, normalized name strings, eventually phone numbers
	// and handles. Populated lazily as more entries are upserted.
	Aliases []Alias `json:"aliases,omitempty"`

	// FirstSeen / LastSeen bracket the time range across which this
	// entity has appeared in memory entries. Derived from entry
	// metadata when available; falls back to upsert timestamps.
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	// EntryCount is the number of memory entries currently linked
	// to this entity (across all roles — from / to / mention).
	EntryCount int64 `json:"entry_count"`
}

// EntityKind enumerates the supported entity kinds. v0.1 ships
// person + organization; thread is a degenerate "this entity is
// itself a thread" used to bridge the two stores when callers want
// a uniform listing.
type EntityKind string

// EntityKind values.
const (
	EntityPerson       EntityKind = "person"
	EntityOrganization EntityKind = "organization"
)

// Alias is one identifier pointing at an entity. The (Value, Kind)
// pair is unique per entity. AliasKindEmail is the primary key for
// person entities in v0.1; other kinds layer on later.
type Alias struct {
	Value string    `json:"value"`
	Kind  AliasKind `json:"kind"`
}

// AliasKind enumerates alias forms.
type AliasKind string

// AliasKind values.
const (
	AliasKindEmail  AliasKind = "email"
	AliasKindName   AliasKind = "name"
	AliasKindDomain AliasKind = "domain"
)

// Thread groups related memory entries — a Gmail conversation
// thread, a Slack thread, a calendar event series. Threads have
// their own ID stable across re-syncs.
type Thread struct {
	// ID is the layer-assigned identifier ("thr_01H..."). Stable
	// across re-syncs.
	ID string `json:"id"`

	// Namespace + ExternalID together identify the thread inside
	// its source. (e.g. namespace="gmail-personal",
	// external_id=Gmail thread_id). UNIQUE per namespace.
	Namespace  string `json:"namespace"`
	ExternalID string `json:"external_id"`

	// Subject is a human-readable label — typically the subject of
	// the first message. Empty for threads with no obvious title.
	Subject string `json:"subject,omitempty"`

	// FirstSeen / LastSeen bracket the time range across the
	// thread's entries.
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	// EntryCount is the number of memory entries currently linked
	// to this thread.
	EntryCount int64 `json:"entry_count"`
}

// EntryRef is a lightweight pointer to a memory entry. Returned by
// EntriesByEntity / EntriesByThread so callers can fetch full
// entries via the memory adapter without the layer carrying entry
// content + vectors.
type EntryRef struct {
	Namespace string         `json:"namespace"`
	ID        string         `json:"id"`
	Role      EntryRole      `json:"role,omitempty"`     // populated by EntriesByEntity
	Date      time.Time      `json:"date,omitempty"`     // entry date, when known
	Subject   string         `json:"subject,omitempty"`  // for thread entries
	Snippet   string         `json:"snippet,omitempty"`  // short preview
	Metadata  map[string]any `json:"metadata,omitempty"` // selected fields
}

// EntryRole describes how an entity participated in an entry. For
// email-shaped entries: from / to / cc. For other shapes: mention.
type EntryRole string

// EntryRole values.
const (
	RoleFrom    EntryRole = "from"
	RoleTo      EntryRole = "to"
	RoleCC      EntryRole = "cc"
	RoleBCC     EntryRole = "bcc"
	RoleMention EntryRole = "mention"
)

// Entry is the shape the layer accepts on Upsert. Mirrors the
// source.Entry shape so source connectors can pass entries
// through to the layer without re-shaping. Distinct from
// adapter/memory.Entry, which carries the raw vector.
type Entry struct {
	Namespace  string
	ID         string
	Content    string
	Metadata   map[string]any
	Embeddings []float32
}

// EntityFilter narrows ListEntities.
type EntityFilter struct {
	Namespace string     // restrict to a namespace; empty = all
	Kind      EntityKind // restrict to a kind; empty = all
	Alias     string     // restrict to entities whose aliases include this value
	Limit     int        // max returned (default 50, cap 1000)
}

// ThreadFilter narrows ListThreads.
type ThreadFilter struct {
	Namespace string // restrict to a namespace; empty = all
	Limit     int    // max returned (default 50, cap 1000)
}

// RebuildStats is returned by Rebuild operations (Phase 2; v0.1 has
// only on-write derivation, no batch rebuild yet).
type RebuildStats struct {
	EntriesScanned  int64
	EntitiesAdded   int64
	EntitiesUpdated int64
	ThreadsAdded    int64
	ThreadsUpdated  int64
	MappingsAdded   int64
	StartedAt       time.Time
	FinishedAt      time.Time
}

// Sentinel errors.
var (
	// ErrEntityNotFound is returned by GetEntity when no entity
	// matches the requested id.
	ErrEntityNotFound = errors.New("memory layer: entity not found")

	// ErrThreadNotFound is returned by GetThread when no thread
	// matches the requested id.
	ErrThreadNotFound = errors.New("memory layer: thread not found")
)

const (
	defaultListLimit = 50
	maxListLimit     = 1000
)

func clampLimit(n int) int {
	if n <= 0 {
		return defaultListLimit
	}
	if n > maxListLimit {
		return maxListLimit
	}
	return n
}
