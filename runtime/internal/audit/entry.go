// Package audit implements the Loamss audit log — the durable,
// tamper-evident record of everything the runtime does. The contract
// is defined in audit-spec.md; this package translates the schema
// and behavior into Go.
//
// Components:
//
//   - Entry, Actor, Subject, Context types (this file)
//   - Hash chain and canonical JSON (chain.go)
//   - Writer interface + SQLite hot-store implementation (writer.go)
//
// Cold-store rotation, CLI surfacing, and audit-as-MCP-resource are
// separate components (separate commits).
package audit

import "time"

// Entry is one audit record. The Writer assigns ID, Timestamp,
// PrevHash, and Hash; callers populate the rest.
//
// Field ordering here is informational only. canonicalJSON (in
// chain.go) emits the JSON with sorted keys so the hash computation
// is deterministic regardless of in-memory layout.
type Entry struct {
	ID        string         `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Type      string         `json:"type"`
	Actor     Actor          `json:"actor"`
	Subject   *Subject       `json:"subject,omitempty"`
	Outcome   Outcome        `json:"outcome"`
	Data      map[string]any `json:"data,omitempty"`
	Context   *Context       `json:"context,omitempty"`
	PrevHash  string         `json:"prev_hash"`
	Hash      string         `json:"hash"`
}

// Actor identifies who/what initiated the event. Never null.
type Actor struct {
	Kind ActorKind `json:"kind"`
	ID   string    `json:"id"`
}

// Subject is what the event acted on. May be nil for runtime-level
// events that have no specific subject (runtime.start, etc.).
type Subject struct {
	Kind SubjectKind `json:"kind"`
	ID   string      `json:"id"`
}

// Context carries cross-event correlation fields, filled when
// applicable. The runtime never invents context it doesn't have.
type Context struct {
	RequestID      string `json:"request_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	CorrelationID  string `json:"correlation_id,omitempty"`
	IP             string `json:"ip,omitempty"`
	UserAgent      string `json:"user_agent,omitempty"`
	RuntimeVersion string `json:"runtime_version,omitempty"`
}

// ActorKind is the kind of principal that initiated the event.
type ActorKind string

// Standard actor kinds. Per audit-spec.md §Universal entry schema.
const (
	ActorUser    ActorKind = "user"
	ActorCapsule ActorKind = "capsule"
	ActorClient  ActorKind = "client"
	ActorRuntime ActorKind = "runtime"
	ActorSystem  ActorKind = "system"
)

// SubjectKind is the kind of thing the event affected.
type SubjectKind string

// Standard subject kinds. Per audit-spec.md §Universal entry schema.
const (
	SubjectGrant    SubjectKind = "grant"
	SubjectResource SubjectKind = "resource"
	SubjectMemory   SubjectKind = "memory"
	SubjectTool     SubjectKind = "tool"
	SubjectCapsule  SubjectKind = "capsule"
	SubjectClient   SubjectKind = "client"
	SubjectSource   SubjectKind = "source"
	SubjectConfig   SubjectKind = "config"
)

// Outcome is the result of the event.
type Outcome string

// Standard outcomes. n/a is for purely informational events with no
// success/failure semantics (e.g., runtime.start).
const (
	OutcomeSuccess Outcome = "success"
	OutcomeDenied  Outcome = "denied"
	OutcomeError   Outcome = "error"
	OutcomePending Outcome = "pending"
	OutcomeNA      Outcome = "n/a"
)

// Validate returns an error if the entry's caller-supplied fields
// are missing or malformed. The Writer is expected to validate before
// computing the hash and persisting.
//
// Note: Validate does not check ID, Timestamp, PrevHash, or Hash —
// those are the Writer's responsibility.
func (e Entry) Validate() error {
	if e.Type == "" {
		return errMissingField("type")
	}
	if e.Actor.Kind == "" {
		return errMissingField("actor.kind")
	}
	if e.Actor.ID == "" {
		return errMissingField("actor.id")
	}
	if e.Outcome == "" {
		return errMissingField("outcome")
	}
	if e.Subject != nil {
		if e.Subject.Kind == "" {
			return errMissingField("subject.kind")
		}
		if e.Subject.ID == "" {
			return errMissingField("subject.id")
		}
	}
	return nil
}

// errMissingField is constructed via fmt.Errorf in package-private
// helper to avoid the import cycle between this file and writer.go.
func errMissingField(name string) error {
	return &validationError{field: name}
}

type validationError struct {
	field string
}

func (e *validationError) Error() string {
	return "audit: missing required field: " + e.field
}
