// Package source defines the SPI for Loamss data-source connectors.
//
// A *source* pulls data from an external system (Gmail, Calendar, a
// Slack workspace, a filesystem watcher, …) into the user's storage
// and memory. Each concrete connector lives in its own sub-package
// and registers a Factory in init(), the same pattern the storage,
// memory, and model adapters use.
//
// The interface is deliberately small. Sources own three lifecycle
// transitions and one work loop:
//
//   - Init           — bind to runtime deps (storage, memory, creds, log)
//   - BeginAuth +
//     CompleteAuth   — interactive auth flow (OAuth, device code, …)
//   - Sync           — incremental synchronization driven by an opaque
//     cursor
//   - Close          — release resources
//
// The runtime persists the cursor and the credentials between Syncs;
// the source treats them as black-box state it produced.
//
// This file declares the contract. registry.go is how the runtime
// resolves a configured source id to a constructed instance.
// store.go is how the runtime persists configured-source records.
package source

import (
	"context"
	"errors"
	"time"
)

// Source is the contract every data-source connector must satisfy.
//
// All methods take a context that the runtime uses to bound work.
// A source that ignores cancellation is non-compliant.
//
// Sources are typically constructed by the registry, Init-ed once,
// authenticated interactively, then Sync-ed many times before being
// Closed at shutdown. The runtime serializes nothing on the source's
// behalf — implementations that touch shared state must use their
// own locking.
type Source interface {
	// ID returns the adapter id, e.g. "source:gmail". The same string
	// the source registered itself under.
	ID() string

	// Init binds the source to runtime dependencies. Called once
	// before any other method. The Deps struct carries everything the
	// source is allowed to touch — storage and memory adapters, the
	// credential store, the logger.
	Init(ctx context.Context, deps Deps) error

	// AuthStatus reports whether the source currently has valid
	// credentials. The runtime uses this to decide whether a Sync
	// can proceed or the user has to re-authenticate first.
	AuthStatus(ctx context.Context) (AuthStatus, error)

	// BeginAuth starts an interactive authentication flow. Returns
	// the user-facing instructions (URL to open in a browser, code
	// to paste back, etc.). Sources that don't need interactive auth
	// (e.g., API-key sources) return AuthFlow{Kind: AuthFlowNone}.
	BeginAuth(ctx context.Context) (AuthFlow, error)

	// CompleteAuth finishes an interactive flow. The runtime forwards
	// whatever the user pasted back or whatever the OAuth callback
	// captured (an authorization code, a verifier, a device code, …).
	// On success the source persists durable credentials via the
	// CredentialStore from Deps.
	CompleteAuth(ctx context.Context, params map[string]string) error

	// Sync runs one synchronization pass. The runtime supplies the
	// previously-stored cursor (nil on the first sync) and persists
	// whatever cursor the source returns in SyncResult. Sources are
	// expected to use the cursor for incremental fetch — full re-syncs
	// every call are a correctness failure for any non-trivial source.
	Sync(ctx context.Context, cursor []byte) (SyncResult, error)

	// HealthCheck verifies the source can talk to its backend.
	// Cheap, frequently-callable. Used by `loamss doctor` and the
	// /healthz endpoint.
	HealthCheck(ctx context.Context) error

	// Close releases source-held resources. Called during runtime
	// shutdown. Multiple calls should be safe.
	Close(ctx context.Context) error
}

// Deps bundles the runtime dependencies a source receives in Init.
// The runtime constructs this; the source treats it as immutable.
type Deps struct {
	// SourceName is the user's chosen handle for this configured
	// source instance, e.g. "gmail-personal". A user can configure
	// multiple instances of the same source id ("source:gmail")
	// under different names. Sources use this to namespace any
	// per-instance state they keep in storage.
	SourceName string

	// Config is the opaque per-instance config map the user wrote
	// in their loamss config (or supplied via CLI flags at add time).
	// The source itself validates the shape during Init.
	Config map[string]any

	// Storage is the user's storage adapter. Sources write raw
	// payloads (raw RFC822 emails, JSON document blobs, …) here.
	// The path namespace inside storage is by convention
	// "sources/<source_name>/...".
	Storage StorageAdapter

	// Memory is the user's memory adapter. Sources write normalized
	// entries (one entry per logical record — message, event, file)
	// here. The namespace convention is "<source_name>" so users can
	// scope grants by source via memory.namespace.
	Memory MemoryAdapter

	// Credentials is the source's per-instance credential store.
	// Sources use it to persist OAuth tokens, refresh tokens, API
	// keys, anything they need to recover authenticated state across
	// runtime restarts. Implementations encrypt at rest via the
	// storage adapter.
	Credentials CredentialStore

	// Logger is the runtime's slog logger, scoped by the runtime to
	// include "source_name" and "source_id" attributes.
	Logger Logger
}

// StorageAdapter is the narrow surface sources see of the storage
// adapter SPI. Sources do not need List, signed URLs, or metadata
// queries; keeping the consumer-side interface small lets us change
// the full SPI without touching every source.
//
// The full interface lives in internal/adapter/storage.
type StorageAdapter interface {
	Write(ctx context.Context, path string, content []byte) error
	Read(ctx context.Context, path string) ([]byte, error)
	Exists(ctx context.Context, path string) (bool, error)
	Delete(ctx context.Context, path string) error
}

// MemoryAdapter is the narrow surface sources see of the memory
// adapter SPI. Sources upsert entries by stable external id; they
// do not query memory themselves (organizer capsules do that).
type MemoryAdapter interface {
	Upsert(ctx context.Context, entry MemoryEntry) error
	Delete(ctx context.Context, namespace, id string) error
}

// MemoryEntry is the normalized record a source writes into memory.
// Mirrors the in-memory shape of memory.Entry without binding sources
// to the full memory package surface.
type MemoryEntry struct {
	Namespace  string
	ID         string
	Content    string
	Metadata   map[string]any
	Embeddings []float32
}

// CredentialStore persists per-source credentials (OAuth tokens, API
// keys, etc.). One CredentialStore is bound to one configured-source
// instance; the source name appears nowhere in this interface
// because each store is scoped at construction time.
type CredentialStore interface {
	// Get returns the previously-stored credential blob, or
	// ErrNoCredentials if the source has never authenticated.
	Get(ctx context.Context) (map[string]any, error)

	// Set persists creds, overwriting any existing blob.
	Set(ctx context.Context, creds map[string]any) error

	// Delete clears stored credentials. Idempotent: returns nil if
	// no creds were stored.
	Delete(ctx context.Context) error
}

// Logger is the narrow slog-shaped interface sources see of the
// runtime's structured logger. Sources should not import "log/slog"
// directly so we can substitute a no-op logger in tests without
// pulling in the standard library wiring.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	Debug(msg string, args ...any)
}

// AuthStatus reports the credential state of a configured source.
type AuthStatus struct {
	// Authenticated is true when the source has usable credentials.
	Authenticated bool

	// ExpiresAt is when the current credential expires, if it does.
	// Zero value means "no fixed expiry" (e.g., a static API key).
	ExpiresAt time.Time

	// NeedsRefresh is true when the source should be re-authenticated
	// soon (e.g., a refresh token is close to expiring). Distinct
	// from !Authenticated: a NeedsRefresh source can still sync now,
	// but the user should be nudged.
	NeedsRefresh bool

	// Reason carries a short human-readable explanation when
	// Authenticated is false ("no credentials", "refresh token
	// rejected", …). Empty when Authenticated is true.
	Reason string
}

// AuthFlowKind is the kind of interactive auth handshake a source
// requires. The CLI / console branches on this to render the right
// prompt.
type AuthFlowKind string

const (
	// AuthFlowNone means the source needs no interactive auth.
	// Used by sources that authenticate via static config (e.g., an
	// API key in the source's Config map). CompleteAuth is still
	// called with an empty params map to give the source a chance
	// to validate the static credential.
	AuthFlowNone AuthFlowKind = "none"

	// AuthFlowBrowser means: open URL, complete the flow there,
	// the source captures the result itself (e.g., via a loopback
	// HTTP listener). CompleteAuth is called once the source signals
	// success out-of-band.
	AuthFlowBrowser AuthFlowKind = "browser"

	// AuthFlowCodePaste means: open URL, the user pastes the
	// returned code back into the CLI / console, the runtime hands
	// it to CompleteAuth.
	AuthFlowCodePaste AuthFlowKind = "code_paste"

	// AuthFlowDeviceCode is the OAuth 2.0 device authorization
	// grant (RFC 8628). The source polls; the user enters a code on
	// another device.
	AuthFlowDeviceCode AuthFlowKind = "device_code"
)

// AuthFlow describes an interactive auth handshake to the user.
type AuthFlow struct {
	Kind AuthFlowKind

	// URL the user opens in their browser. Always set for Browser /
	// CodePaste / DeviceCode flows.
	URL string

	// Code the user must enter on the URL above. Set for
	// DeviceCode flows.
	Code string

	// Instructions is human-readable, surfaced verbatim by the CLI /
	// console. Sources can use it to add provider-specific guidance
	// ("Click 'Allow' when prompted", "Use a Chrome profile that
	// has the right Google account active", …).
	Instructions string

	// ExpiresAt bounds the validity of the URL / Code. Zero means
	// "no specific expiry" — let the user take as long as they need.
	ExpiresAt time.Time
}

// SyncResult is what a source returns from one Sync pass.
//
// Cursor is opaque to the runtime; the source consumes it on the
// next Sync call. The counters and Errors are for the audit log and
// the CLI's progress display.
type SyncResult struct {
	// Cursor is the source-defined incremental position. Empty
	// means "next sync should start from scratch" — used after a
	// reset.
	Cursor []byte

	// RecordsAdded counts records newly written to storage or memory.
	RecordsAdded int64

	// RecordsUpdated counts records that were already present and
	// got refreshed (e.g., a Gmail message whose labels changed).
	RecordsUpdated int64

	// BytesIngested is the total raw payload size written to storage
	// during this pass.
	BytesIngested int64

	// Started / Finished bound the wall-clock duration of the pass.
	Started  time.Time
	Finished time.Time

	// Errors carries non-fatal per-record failures. A fully-failed
	// sync is reported via the top-level error returned by Sync, not
	// here.
	Errors []SyncError
}

// SyncError is one per-record failure within a Sync pass. The source
// kept going; the runtime records it.
type SyncError struct {
	// RecordID identifies the failing record in the source's own
	// namespace (a Gmail message id, a Calendar event id, …).
	RecordID string

	// Reason is a short human-readable failure description.
	Reason string

	// Fatal is true if continuing the sync was impossible. (In that
	// case the source should also return a non-nil error from Sync.)
	Fatal bool
}

// Sentinel errors. Sources and the runtime wrap these (using
// fmt.Errorf with %w) when surfacing the corresponding condition;
// callers test with errors.Is.
var (
	// ErrUnknownSource is returned by registry.New for an
	// unregistered adapter id.
	ErrUnknownSource = errors.New("source: unknown source adapter")

	// ErrCapsuleIngestorNotYetExecutable is returned by Build when the
	// Configured row's OwnerCapsule is non-empty — capsule ingestors
	// are dispatched by the capsule host's scheduled-trigger path,
	// which lands in step 5 of docs/capsule-ingestor-primitives.md.
	// Until then, listing/visibility works but Sync does not.
	ErrCapsuleIngestorNotYetExecutable = errors.New("source: capsule ingestor not yet executable (scheduled triggers wired in step 5)")

	// ErrNoCredentials is returned by CredentialStore.Get when the
	// source has never authenticated.
	ErrNoCredentials = errors.New("source: no credentials stored")

	// ErrAuthRequired is returned by Sync when the source has no
	// usable credentials. The runtime surfaces this to the user as
	// "please run `loamss source authenticate <name>`".
	ErrAuthRequired = errors.New("source: authentication required")

	// ErrAuthInProgress is returned by BeginAuth when a flow is
	// already underway for this source (e.g., another CLI invocation
	// is mid-handshake).
	ErrAuthInProgress = errors.New("source: auth flow already in progress")

	// ErrUnsupported is returned by sources for operations they
	// cannot perform (e.g., a read-only source returning this from
	// a hypothetical Write method we don't have today).
	ErrUnsupported = errors.New("source: operation not supported")
)
