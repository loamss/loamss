package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/loamss/loamss/runtime/internal/adapter/memory"
)

// MemoryResourceProvider is the runtime-provided resource provider
// for the user's memory store. Owns the "memory" URI scheme; resolves
// memory://entry/{id} URIs by fetching the named entry from the
// memory adapter.
//
// This is the get-by-URI counterpart to the memory.show tool. Same
// underlying read, same capability gate (memory.read), different
// surface. MCP clients that prefer the resources protocol use this;
// clients that prefer tool-call semantics use memory.show. Both
// audited identically (resource.read vs tool.invoked); the user can
// tell which surface a given access came through.

const (
	memoryScheme    = "memory"
	memoryURIPrefix = memoryScheme + "://entry/"
)

// NewMemoryResourceProvider builds the provider. The memory adapter
// is captured for the lifetime of the provider; callers are
// responsible for Init / Close on the adapter itself.
func NewMemoryResourceProvider(m memory.Adapter) *MemoryResourceProvider {
	return &MemoryResourceProvider{m: m}
}

// MemoryResourceProvider serves memory://entry/{id} URIs.
type MemoryResourceProvider struct {
	m memory.Adapter
}

// Scheme returns the URI scheme this provider owns.
func (p *MemoryResourceProvider) Scheme() string { return memoryScheme }

// Capability returns the permission-framework capability required to
// read memory resources. Matches the canonical capability the
// memory.show tool also gates on.
func (p *MemoryResourceProvider) Capability() string { return "memory.read" }

// Templates declares the URI shape this provider serves. Clients
// substitute {id} themselves — there's no resource expansion in v0.1
// (the memory store can hold millions of entries; enumerating them
// in resources/list is impractical).
func (p *MemoryResourceProvider) Templates() []ResourceTemplate {
	return []ResourceTemplate{{
		URITemplate: memoryURIPrefix + "{id}",
		Name:        "Memory entry",
		Description: "A single memory entry by id. Resolved against the user's memory adapter; gated on the memory.read capability.",
		MIMEType:    "application/json",
	}}
}

// List intentionally returns nothing. The memory store doesn't have
// a natural "list everything" semantics — enumerating millions of
// entries is wasteful, and any narrowing would require search args
// the resources/list API doesn't carry. memory.query (when it lands
// with embeddings support) is the search surface; resources/list
// stays empty for this provider.
//
// Future: when we add curated collections (e.g., "pinned entries",
// "recent entities the user is reviewing"), this method returns
// those small, stable sets.
func (p *MemoryResourceProvider) List(_ context.Context, _ string) (ListResult, error) {
	return ListResult{Resources: []Resource{}}, nil
}

// Read resolves a memory://entry/{id} URI. Returns ErrResourceNotFound
// when the id doesn't exist (mapped by the dispatcher to -32004).
// Other errors propagate as backend errors.
//
// The content shape: text/plain JSON containing id + metadata +
// vector_size. The vector itself is not inlined for the same reason
// memory.show omits it — meaningless to AI consumers and large.
func (p *MemoryResourceProvider) Read(ctx context.Context, uri string) (ResourceContent, error) {
	if !strings.HasPrefix(uri, memoryURIPrefix) {
		return ResourceContent{}, fmt.Errorf("uri does not match memory template: %q", uri)
	}
	id := strings.TrimPrefix(uri, memoryURIPrefix)
	if id == "" {
		return ResourceContent{}, errors.New("uri is missing entry id")
	}
	entry, err := p.m.Get(ctx, id)
	if err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			return ResourceContent{}, fmt.Errorf("%w: %s", ErrResourceNotFound, id)
		}
		return ResourceContent{}, err
	}
	payload := map[string]any{
		"id":          entry.ID,
		"metadata":    entry.Metadata,
		"vector_size": len(entry.Vector),
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ResourceContent{}, err
	}
	return ResourceContent{
		URI:      uri,
		MIMEType: "application/json",
		Text:     string(body),
	}, nil
}
