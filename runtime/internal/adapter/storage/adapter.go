// Package storage defines the SPI for Loamss storage adapters. Each
// concrete adapter (storage:fs-encrypted, storage:sqlite-encrypted,
// storage:s3-compat, storage:postgres) lives in its own sub-package and
// registers a factory in init().
//
// The interface mirrors adapter-interface.md §Storage adapter. The spec
// is the authoritative contract; this file translates it into Go and
// must stay in sync with breaking changes to the spec.
//
// Adapter authors and the runtime both depend on this package. The
// interface itself is the integration surface; the registry is how the
// runtime resolves a configured adapter id to a constructed instance.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// Adapter is the contract every storage adapter must satisfy.
//
// All methods take a context that the runtime uses to bound work; an
// adapter that ignores cancellation is non-compliant. Methods are
// safe for concurrent use: the runtime serializes nothing on the
// adapter's behalf.
//
// Adapters are typically initialized once at runtime startup via Init,
// then used for the lifetime of the process. Close is called during
// graceful shutdown.
type Adapter interface {
	// Init binds the adapter to its backend. The config map is opaque
	// to the runtime — it's whatever the user wrote under
	// storage.config in their loamss config file. Returns an error
	// for malformed config or an unreachable backend.
	Init(ctx context.Context, config map[string]any) error

	// Read returns the entire bytes stored at path, or ErrNotFound.
	Read(ctx context.Context, path string) ([]byte, error)

	// ReadStream returns a reader for byte-range or large-content
	// access. If length is 0, the stream extends to the end of the
	// object. Caller must Close the returned reader.
	ReadStream(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error)

	// Write stores content at path. Creates intermediate prefixes if
	// the backend has the notion (e.g., filesystem mkdir -p).
	// Overwrites any existing object.
	Write(ctx context.Context, path string, content []byte) error

	// WriteStream is the streaming counterpart of Write. The adapter
	// is responsible for closing the underlying writer; the runtime
	// just provides the content reader.
	WriteStream(ctx context.Context, path string, content io.Reader) error

	// Delete removes the object at path. Idempotent: returns nil if
	// the object was already absent.
	Delete(ctx context.Context, path string) error

	// Exists is a cheap presence check.
	Exists(ctx context.Context, path string) (bool, error)

	// Metadata returns size, content type, mtime, and backend-specific
	// fields for the object at path. Returns ErrNotFound if absent.
	Metadata(ctx context.Context, path string) (ObjectMetadata, error)

	// List streams entries whose path begins with prefix. The returned
	// channel is closed when the listing completes or the context is
	// canceled. Callers must drain the channel to avoid leaking the
	// adapter's listing goroutine.
	List(ctx context.Context, prefix string) (<-chan ListEntry, error)

	// SignedURL returns a time-bound URL that lets the caller's
	// counterparty access the object directly from the underlying
	// storage, bypassing Loamss. Used by the MCP surface to hand off
	// binary content (see mcp-surface.md and scenarios.md §5).
	//
	// Adapters whose backend has no notion of signed URLs (e.g., a
	// plain local filesystem with no HTTP front) return
	// ErrUnsupported. The runtime will surface that as a
	// configuration warning at startup if any code path requires
	// signed URLs and the configured adapter doesn't support them.
	SignedURL(ctx context.Context, path string, ttl time.Duration, op Op) (string, error)

	// HealthCheck verifies the adapter can talk to its backend.
	// Cheap, frequently-callable. Used by `loamss doctor` and the
	// future /healthz endpoint.
	HealthCheck(ctx context.Context) error

	// Close releases adapter-held resources. Called during runtime
	// shutdown. Multiple calls should be safe.
	Close(ctx context.Context) error
}

// Op identifies the intended use of a signed URL. Read URLs let the
// holder GET an object; Write URLs let the holder PUT one (e.g., a
// creator's app uploading a new video directly to user storage).
type Op string

// Op constants for use in SignedURL requests.
const (
	OpRead  Op = "read"
	OpWrite Op = "write"
)

// Sentinel errors. Adapters wrap these (using fmt.Errorf with %w) when
// surfacing the corresponding condition; callers test with errors.Is.
var (
	// ErrNotFound is returned by Read, ReadStream, Metadata, and any
	// other operation against a path that has no object.
	ErrNotFound = errors.New("storage: object not found")

	// ErrUnsupported is returned by an adapter that cannot perform
	// the requested operation. Typically used by SignedURL on
	// backends without signed-URL support.
	ErrUnsupported = errors.New("storage: operation not supported")

	// ErrConnectionLost is returned when the backend becomes
	// unreachable mid-operation. Distinct from ordinary errors so
	// callers (e.g., the audit log writer's cold-store path) can
	// implement retry policies.
	ErrConnectionLost = errors.New("storage: connection lost")
)
