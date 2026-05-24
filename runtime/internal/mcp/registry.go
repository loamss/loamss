package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/loamss/loamss/runtime/internal/permission"
)

// Tool is the runtime's view of an MCP tool. Each Tool carries
// everything the dispatcher needs to: surface it in tools/list,
// build a permission.CheckRequest, hand off to the implementation,
// and shape the result back into MCP form.
//
// Tools register themselves through Registry.Register at startup
// (for runtime tools) or capsule-install time (for capsule tools,
// arriving in Phase 1b). The registry is dynamic by design — see
// extensibility.md §No hardcoded tool list.
type Tool struct {
	// Name is the user-visible tool identifier surfaced in tools/list
	// (e.g., "client.info", "memory.show"). Must be unique within a
	// registry. By convention, runtime-provided tools mirror the
	// capability they require ("memory.show" needs "memory.read").
	Name string

	// Description is the human-readable explanation surfaced in
	// tools/list. Shown to AI clients so they can decide whether to
	// invoke the tool; clarity matters.
	Description string

	// Capability is the permission-framework capability required to
	// invoke this tool. Empty means "no extra capability beyond
	// authentication" (e.g., client.info — every authenticated client
	// can introspect itself). Non-empty values must exist in the
	// capability registry.
	Capability string

	// InputSchema is the JSON Schema for the tool's arguments,
	// surfaced verbatim to MCP clients in tools/list. Validation is
	// the client's responsibility (per MCP); the server cross-checks
	// known fields at invocation time but does not re-validate the
	// full schema on the hot path.
	InputSchema json.RawMessage

	// ScopeOf, if non-nil, projects the call's arguments to the
	// attempted-scope map that drives permission.Check. Returning
	// nil means "no scope" (the empty scope, which matches a grant
	// with no scope or any scope-as-superset narrower than that
	// capability's schema).
	ScopeOf func(args json.RawMessage) (map[string]any, error)

	// Handler is the actual implementation. Called only after the
	// permission check allows the invocation. The Tool's permission
	// check is already done; the handler may perform its own
	// finer-grained checks against the input.
	Handler func(ctx context.Context, in ToolInput) (ToolResult, error)
}

// ToolInput is what the dispatcher hands to a tool's handler. It
// carries everything the tool needs without forcing it to dip into
// HTTP-shaped context values.
type ToolInput struct {
	// Args is the raw JSON params from the MCP tools/call request.
	// The tool decodes this into its own typed shape.
	Args json.RawMessage

	// Principal is the authenticated caller.
	Principal permission.Principal

	// Client is the full Client record (name, paired_at, ...). Tools
	// that introspect the caller (client.info) read from this.
	Client *permission.Client

	// GrantID is the id of the grant that allowed this call. Empty
	// when the tool's Capability is empty (no permission check ran).
	// Tools that emit audit entries should include this for
	// traceability.
	GrantID string
}

// ToolResult is what a tool's Handler returns to the dispatcher. The
// MCP wire shape for tool results is {content: [{type, text|data}]};
// the dispatcher wraps these into that shape. Tools return one or
// more Content blocks.
type ToolResult struct {
	// Content is the list of content blocks. Most tools return one
	// text block; tools that surface binary data return a data block.
	Content []Content `json:"content"`

	// IsError signals a tool-level (semantic) error distinct from a
	// permission deny or backend error. Surfaced as a true value in
	// the MCP `isError` field. Most tools use the error return for
	// these conditions; this exists for the edge cases where a tool
	// wants to return structured error content alongside isError.
	IsError bool `json:"isError,omitempty"`
}

// Content is one MCP tool-result content block. The "type" field
// discriminates text from data; Loamss currently emits only text
// blocks. Binary content (images, audio) arrives with capsule tools.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`     // base64 for binary
	MIMEType string `json:"mimeType,omitempty"` // accompanies Data
}

// TextContent wraps a plain string into the standard text Content
// block. Most runtime tools build their result with this helper.
func TextContent(s string) Content { return Content{Type: "text", Text: s} }

// JSONContent wraps a value as a JSON text block. The MCP spec does
// not have a "json" content type yet; the convention is type=text
// with the payload pre-encoded. Clients that know how to parse
// pretty-printed JSON do; the rest see it as text.
func JSONContent(v any) (Content, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return Content{}, err
	}
	return Content{Type: "text", Text: string(b)}, nil
}

// Registry holds the set of Tools the runtime knows how to invoke.
// Safe for concurrent reads; Register acquires a write lock.
//
// In v0.1 the registry is in-memory and rebuilt at every process
// start. When capsule tools arrive (Phase 1b), the runtime will
// persist registrations so an MCP client's tools/list stays stable
// across restarts even before all capsules are loaded.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry constructs an empty registry. Runtime tools register
// themselves into it during server startup (see start.go).
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a Tool to the registry. Re-registering the same
// name is an error (matches the capability-registry semantics:
// names are stable identifiers).
func (r *Registry) Register(t Tool) error {
	if t.Name == "" {
		return errors.New("mcp: tool name is required")
	}
	if t.Handler == nil {
		return fmt.Errorf("mcp: tool %q has nil Handler", t.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("mcp: tool %q already registered", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// Get returns a Tool by name. The bool reports whether the tool
// exists; callers should NOT compare against the zero Tool because
// the empty Capability field is meaningful.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools sorted by name. The sort is
// stable and lexicographic — same order on every list call, so
// tools/list responses are reproducible.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}
