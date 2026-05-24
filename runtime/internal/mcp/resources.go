package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/loamss/loamss/runtime/internal/audit"
	"github.com/loamss/loamss/runtime/internal/permission"
)

// MCP resources surface. Separate from tools because the wire methods
// are different and the read pattern is "get by stable URI" rather
// than "call this function with args." Both ride the same
// permission + audit machinery; the only new abstraction is
// ResourceProvider — a thing that owns a URI scheme.
//
// The dynamic registry pattern matches Tool registry: providers
// register at startup (runtime-provided) or capsule-install time
// (capsule-provided). No hardcoded resource lists anywhere; the
// extensibility.md anti-pattern applies here too.

// --- types -------------------------------------------------------------

// Resource is one MCP resource record returned in resources/list.
// Mirrors the MCP spec shape; clients use the URI to call
// resources/read.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// ResourceTemplate declares a URI pattern the provider serves (e.g.,
// "memory://entry/{id}"). Surfaced via resources/templates/list so
// clients can build URIs without exhaustively listing concrete
// resources first — important for memory, where listing every entry
// is impractical.
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// ResourceContent is the payload returned by resources/read. Either
// Text or Blob is set, never both. Blob is base64-encoded for
// binary content; MIMEType disambiguates.
type ResourceContent struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"` // base64
}

// ListResult is what ResourceProvider.List returns. The cursor field
// supports MCP pagination; providers that return all rows at once
// leave it empty. Loamss currently ignores client-supplied cursors
// (no pagination state in v0.1); a provider that needs pagination
// stores its own state.
type ListResult struct {
	Resources  []Resource `json:"resources"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// ResourceProvider is the contract every resource provider satisfies.
// A provider owns a URI scheme (e.g., "memory") and is responsible
// for resolving URIs in that scheme to concrete content.
//
// Providers are stateless from the registry's perspective — the
// runtime calls List / Read with a new context each time. Providers
// hold their own backend references (memory adapter, storage adapter,
// capsule MCP connection) via closure capture or struct fields.
type ResourceProvider interface {
	// Scheme returns the URI scheme this provider owns ("memory",
	// "file", "vibez.content"). Used by the dispatcher to route
	// resources/read by URI prefix.
	Scheme() string

	// Templates returns the URI templates this provider serves.
	// Surfaced verbatim in resources/templates/list. Providers that
	// don't have a stable template shape return nil.
	Templates() []ResourceTemplate

	// Capability is the permission-framework capability required to
	// read any resource served by this provider. The runtime gates
	// every resources/read on Engine.Check against this capability.
	// Empty string means auth-only (no extra grant needed) — rare;
	// usually a provider implies a privileged read.
	Capability() string

	// List returns up to one page of concrete resources. cursor is
	// the value returned in the previous call's NextCursor, or empty
	// for the first page. Providers that don't support listing (e.g.,
	// purely template-based ones) return an empty ListResult.
	List(ctx context.Context, cursor string) (ListResult, error)

	// Read resolves a single URI to its content. The URI's scheme
	// has already been validated by the dispatcher to match Scheme().
	// Returning ErrResourceNotFound surfaces -32004 to the client.
	Read(ctx context.Context, uri string) (ResourceContent, error)
}

// ErrResourceNotFound is the sentinel providers return when a URI
// resolves to no resource. The dispatcher maps this to MCP's
// -32004 (codeUnknownResource).
var ErrResourceNotFound = errors.New("mcp: resource not found")

// --- registry ----------------------------------------------------------

// ResourceRegistry holds the set of ResourceProviders by scheme.
// Safe for concurrent reads; Register acquires a write lock.
// Same shape as Tool Registry — see registry.go for the design rationale.
type ResourceRegistry struct {
	mu        sync.RWMutex
	providers map[string]ResourceProvider
}

// NewResourceRegistry constructs an empty registry. Runtime providers
// register at startup (see start.go); capsule providers will join at
// install time when the capsule host lands.
func NewResourceRegistry() *ResourceRegistry {
	return &ResourceRegistry{providers: make(map[string]ResourceProvider)}
}

// Register adds a provider. Two providers cannot claim the same
// scheme; that would make URI routing ambiguous.
func (r *ResourceRegistry) Register(p ResourceProvider) error {
	if p == nil {
		return errors.New("mcp: resource provider is nil")
	}
	scheme := p.Scheme()
	if scheme == "" {
		return errors.New("mcp: resource provider has empty Scheme")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[scheme]; exists {
		return fmt.Errorf("mcp: resource scheme %q already registered", scheme)
	}
	r.providers[scheme] = p
	return nil
}

// Get returns the provider for a scheme, or nil. Used internally by
// the dispatcher; exposed for tests and future capsule wiring.
func (r *ResourceRegistry) Get(scheme string) (ResourceProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[scheme]
	return p, ok
}

// List returns all registered providers sorted by scheme. The sort
// is lexicographic so resources/list results are reproducible across
// calls (clients can diff by content rather than reorder).
func (r *ResourceRegistry) List() []ResourceProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ResourceProvider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scheme() < out[j].Scheme() })
	return out
}

// Len returns the number of registered providers.
func (r *ResourceRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers)
}

// --- dispatch ----------------------------------------------------------

// resources/list, resources/templates/list, resources/read live here.
// The dispatcher in handler.go routes to handle* by method name.

type listResourcesParams struct {
	// Cursor is the MCP pagination cursor. v0.1 forwards it verbatim
	// to each provider; providers that don't support paging ignore it.
	Cursor string `json:"cursor,omitempty"`
}

// handleResourcesList aggregates one page from each provider into a
// combined response. v0.1 does not support cross-provider cursors;
// each provider's page is included in full. Future versions can add
// a multiplexed cursor that resumes individual provider streams.
func (h *Handler) handleResourcesList(r *http.Request, req Request) Response {
	var params listResourcesParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return invalidParamsResponse(req.ID, "cannot decode resources/list params: "+err.Error())
		}
	}
	all := make([]Resource, 0)
	for _, p := range h.deps.Resources.List() {
		page, err := p.List(r.Context(), params.Cursor)
		if err != nil {
			return internalErrorResponse(req.ID, fmt.Sprintf("provider %s list failed: %v", p.Scheme(), err))
		}
		all = append(all, page.Resources...)
	}
	return successResponse(req.ID, ListResult{Resources: all})
}

// templatesResult is the wire shape for resources/templates/list.
type templatesResult struct {
	ResourceTemplates []ResourceTemplate `json:"resourceTemplates"`
}

// handleResourcesTemplatesList returns every template every provider
// declares. Unlike resources/list, this is not gated — clients learn
// what URI shapes exist regardless of whether they can read them.
// Same rationale as tools/list: advertise everything, let
// resources/read return authoritative deny.
func (h *Handler) handleResourcesTemplatesList(_ *http.Request, req Request) Response {
	all := make([]ResourceTemplate, 0)
	for _, p := range h.deps.Resources.List() {
		all = append(all, p.Templates()...)
	}
	return successResponse(req.ID, templatesResult{ResourceTemplates: all})
}

type readResourceParams struct {
	URI string `json:"uri"`
}

// readResourceResult is the MCP shape for resources/read responses.
// MCP returns a list of contents because some URIs (e.g., directory-
// like resources) expand to multiple files. Loamss providers return
// one element; the shape leaves room for future multi-content
// providers.
type readResourceResult struct {
	Contents []ResourceContent `json:"contents"`
}

// handleResourcesRead is the read dispatcher. Mirrors handleToolsCall:
// parse, route by URI scheme, permission check, execute, audit.
//
// Permission check uses the provider's Capability. Scope projection
// is intentionally absent at the dispatcher level — providers that
// want per-URI scope (e.g., memory.read with data_classes_included)
// do the post-check inside Read. The dispatcher's job is to enforce
// the broad capability gate; finer-grained scope decisions are the
// provider's domain knowledge.
func (h *Handler) handleResourcesRead(r *http.Request, req Request) Response {
	var params readResourceParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return invalidParamsResponse(req.ID, "cannot decode resources/read params: "+err.Error())
	}
	if params.URI == "" {
		return invalidParamsResponse(req.ID, "uri is required")
	}
	scheme := schemeOf(params.URI)
	if scheme == "" {
		return errorResponse(req.ID, codeUnknownResource, "uri is missing scheme", map[string]any{"uri": params.URI})
	}
	provider, ok := h.deps.Resources.Get(scheme)
	if !ok {
		return errorResponse(req.ID, codeUnknownResource, "no provider for scheme: "+scheme, map[string]any{
			"uri":    params.URI,
			"scheme": scheme,
		})
	}

	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		return internalErrorResponse(req.ID, "missing principal in resource dispatch")
	}

	// Permission check on the provider's capability. Empty capability
	// means auth-only (rare for resources; the runtime memory
	// provider declares memory.read).
	var grantID string
	if capName := provider.Capability(); capName != "" {
		decision, err := h.deps.Engine.Check(r.Context(), permission.CheckRequest{
			Principal:  *principal,
			Capability: capName,
			Rationale:  "MCP resources/read " + params.URI,
		})
		if err != nil {
			return internalErrorResponse(req.ID, "permission check failed: "+err.Error())
		}
		switch decision.Decision {
		case permission.DecisionDeny:
			return errorResponse(req.ID, codePermissionDenied, "permission denied", map[string]any{
				"capability": capName,
				"reason":     decision.Reason,
				"uri":        params.URI,
			})
		case permission.DecisionApprovalRequired:
			return errorResponse(req.ID, codeApprovalRequired, "user approval required", map[string]any{
				"capability":  capName,
				"approval_id": decision.ApprovalID,
				"uri":         params.URI,
				"poll_via":    "loamss approve list / show / grant / deny",
			})
		case permission.DecisionAllow:
			grantID = decision.GrantID
		}
	}

	content, err := provider.Read(r.Context(), params.URI)
	if err != nil {
		if errors.Is(err, ErrResourceNotFound) {
			return errorResponse(req.ID, codeUnknownResource, "resource not found", map[string]any{"uri": params.URI})
		}
		h.recordResourceFailed(r.Context(), principal, params.URI, err)
		return errorResponse(req.ID, codeBackendError, "resource read failed", map[string]any{
			"uri":   params.URI,
			"error": err.Error(),
		})
	}

	h.recordResourceRead(r.Context(), principal, params.URI, grantID)
	return successResponse(req.ID, readResourceResult{Contents: []ResourceContent{content}})
}

// schemeOf returns the URI scheme portion before "://", or empty.
// We don't use net/url because MCP URIs are often not net/url-parseable
// (custom schemes like "vibez.content" are valid here even though
// net/url.Parse might quibble). A simple prefix split is sufficient.
func schemeOf(uri string) string {
	idx := strings.Index(uri, "://")
	if idx <= 0 {
		return ""
	}
	return uri[:idx]
}

// --- audit emission ----------------------------------------------------

func (h *Handler) recordResourceRead(ctx context.Context, p *permission.Principal, uri, grantID string) {
	entry := audit.Entry{
		Type:    "resource.read",
		Actor:   audit.Actor{Kind: audit.ActorClient, ID: p.ID},
		Subject: &audit.Subject{Kind: audit.SubjectResource, ID: uri},
		Outcome: audit.OutcomeSuccess,
		Data:    map[string]any{"uri": uri},
	}
	if grantID != "" {
		entry.Data["grant_id"] = grantID
	}
	_, _ = h.deps.Audit.Append(ctx, entry)
}

func (h *Handler) recordResourceFailed(ctx context.Context, p *permission.Principal, uri string, err error) {
	entry := audit.Entry{
		Type:    "resource.failed",
		Actor:   audit.Actor{Kind: audit.ActorClient, ID: p.ID},
		Subject: &audit.Subject{Kind: audit.SubjectResource, ID: uri},
		Outcome: audit.OutcomeError,
		Data: map[string]any{
			"uri":   uri,
			"error": err.Error(),
		},
	}
	_, _ = h.deps.Audit.Append(ctx, entry)
}
