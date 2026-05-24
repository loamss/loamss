// Package memory defines the SPI for Loamss memory adapters — the
// vector-storage layer underneath the runtime's semantic memory.
//
// The adapter is deliberately narrow: vectors in, vectors out, with
// metadata along for the ride. The "memory layer" — entity resolution,
// knowledge graph, episodic summarization, provenance — runs *inside*
// the runtime on top of whichever adapter holds the vectors. See
// adapter-interface.md §Memory adapter for the contract.
//
// Each concrete adapter (memory:sqlite-vec, memory:pgvector,
// memory:chroma, memory:qdrant) lives in its own sub-package and
// registers a factory in init(). The runtime resolves the configured
// adapter id to a factory at startup, constructs the adapter, then
// calls Init with the user-supplied config map.
package memory

import (
	"context"
	"errors"
)

// Adapter is the contract every memory adapter must satisfy.
//
// All methods take a context that the runtime uses to bound work; an
// adapter that ignores cancellation is non-compliant. Methods are
// safe for concurrent use: the runtime serializes nothing.
//
// What the adapter does NOT do (these live above the adapter, in
// the runtime's memory layer):
//
//   - Entity resolution (knowing two records refer to the same person)
//   - Episodic summarization
//   - Knowledge graph traversal
//   - Provenance tracking (the adapter stores provenance fields as
//     opaque metadata; semantics live in the runtime)
//   - Embedding generation (model adapter's concern)
//
// The runtime is the only writer of new vectors; capsules and external
// clients write through the runtime, which calls Upsert.
type Adapter interface {
	// Init binds the adapter to its backend with the user-supplied
	// config map (e.g., dsn for postgres, path for sqlite). Returns
	// an error on bad config or unreachable backend.
	Init(ctx context.Context, config map[string]any) error

	// Upsert inserts or replaces a single entry. The id is opaque to
	// the adapter; the runtime assigns and owns it. Metadata is
	// indexed where the backend supports it; otherwise stored
	// alongside the vector for retrieval.
	Upsert(ctx context.Context, id string, vector []float32, metadata map[string]any) error

	// BatchUpsert inserts or replaces many entries in one round-trip.
	// Adapters should optimize this — it's the hot path for ingestion.
	// On partial failure, the adapter MUST report which entries
	// succeeded and which failed via a typed error (TBD; v0.1 may
	// return all-or-nothing).
	BatchUpsert(ctx context.Context, entries []Entry) error

	// Get returns the entry for a known id, or ErrNotFound.
	Get(ctx context.Context, id string) (*Entry, error)

	// Search returns up to k nearest-neighbor entries to query, with
	// optional metadata filtering. Distance semantics are
	// adapter-specific but every adapter must document its metric
	// (typically L2 or cosine). Filter is opaque to the runtime;
	// adapters that support pre-filtering should do so for
	// efficiency, others post-filter.
	Search(ctx context.Context, query []float32, k int, filter MetadataFilter) ([]SearchHit, error)

	// Delete removes an entry. Idempotent: returns nil if the entry
	// was already absent.
	Delete(ctx context.Context, id string) error

	// Stats returns counts and backend health information. Used by
	// `loamss doctor`, the memory browser in the console, and the
	// future memory-stats endpoint.
	Stats(ctx context.Context) (Stats, error)

	// HealthCheck verifies the adapter can still reach its backend.
	// Cheap, frequently-callable.
	HealthCheck(ctx context.Context) error

	// Close releases adapter-held resources. Called during runtime
	// shutdown. Multiple calls should be safe.
	Close(ctx context.Context) error
}

// Sentinel errors. Adapters wrap these (using fmt.Errorf with %w)
// when surfacing the corresponding condition; callers test with
// errors.Is.
var (
	// ErrNotFound is returned by Get and Delete when the requested
	// id has no associated entry.
	ErrNotFound = errors.New("memory: entry not found")

	// ErrDimensionMismatch is returned by Upsert/BatchUpsert when
	// the supplied vector dimension doesn't match what the adapter
	// expects (often set at first Upsert or in config).
	ErrDimensionMismatch = errors.New("memory: vector dimension mismatch")

	// ErrConnectionLost signals that the backend became unreachable
	// mid-operation. Distinct so callers can implement retry policies.
	ErrConnectionLost = errors.New("memory: connection lost")

	// ErrUnsupported is returned by adapters that cannot perform the
	// requested operation under the current configuration (e.g., a
	// filter predicate the backend doesn't support pre-filtering on).
	ErrUnsupported = errors.New("memory: operation not supported")
)
