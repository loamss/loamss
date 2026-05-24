package memory

// Entry is a single record in the memory store. The runtime assigns
// the id (typically a ULID); the vector is the embedding (dimension
// is adapter-determined, usually fixed at first write or in config);
// metadata is opaque to the adapter except for any indexed fields.
type Entry struct {
	ID       string         `json:"id"`
	Vector   []float32      `json:"vector,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// SearchHit is a single result from Search. Distance follows the
// adapter's documented metric (lower is closer for L2; higher is
// closer for cosine — adapters should pick one consistently and
// document it).
type SearchHit struct {
	ID       string         `json:"id"`
	Vector   []float32      `json:"vector,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Distance float32        `json:"distance"`
}

// MetadataFilter narrows a Search to entries matching the given
// predicates. The structure is intentionally loose so each adapter
// can implement what its backend supports natively; adapters that
// don't support a particular predicate may choose to post-filter
// or return ErrUnsupported.
//
// v0.1 supports exact-match equality. Future fields:
//
//   - In        map[string][]any   — value-in-set
//   - Range     map[string]Range   — numeric range queries
//   - Excluded  []string           — entity ids to omit (re-rank)
//
// Loosening the filter (adding fields) is backwards-compatible;
// adding required behavior to an existing field is not.
type MetadataFilter struct {
	// Equals is a conjunction of key=value predicates. The empty
	// or nil map matches everything (no filtering).
	Equals map[string]any
}

// Stats describes the current state of the memory store. Returned by
// the Stats method. Adapters populate the fields they can compute
// cheaply; expensive metrics may be omitted (zero-valued) and
// surfaced through a separate /metrics path in the future.
type Stats struct {
	// Count is the total number of entries in the store.
	Count int64 `json:"count"`

	// Dimension is the configured vector dimension. 0 if no vectors
	// have been written yet (some adapters lazy-set this).
	Dimension int `json:"dimension"`

	// BackendInfo carries adapter-specific health/status data —
	// e.g., for pgvector, the postgres version; for chroma, the
	// collection name; for qdrant, the cluster status.
	BackendInfo map[string]string `json:"backend_info,omitempty"`
}
