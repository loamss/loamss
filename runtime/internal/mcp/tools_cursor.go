package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/loamss/loamss/runtime/internal/permission"
)

// The cursor.* tools let an ingestor capsule persist its
// incremental-sync cursor between triggers — the same round-trip an
// in-tree source.Source gets from `Sync(ctx, cursor) → SyncResult.Cursor`.
//
// Cursors are plaintext, one opaque value per capsule, no capability
// gate. The data is bytes the capsule wrote to itself; there's
// nothing privacy-sensitive to authorize. The capsule-only principal
// check still applies (defense in depth — there's no per-client
// storage slot, so a misrouted call would silently mis-key data).
//
// The dispatcher's tool.invoked audit entry covers each call; no
// per-op secondary entry — cursors change on every sync and a
// duplicate audit row per write would drown the log.

type cursorSetArgs struct {
	Value string `json:"value"`
}

type cursorGetResult struct {
	Value string `json:"value"`
}

// NewCursorSetTool builds cursor.set.
func NewCursorSetTool(store *CapsuleCursorStore) Tool {
	return Tool{
		Name: "cursor.set",
		Description: "Persist this capsule installation's incremental-sync cursor. " +
			"Opaque to the runtime — typically a JSON blob the capsule designed. " +
			"Empty value clears the cursor (next get returns empty). " +
			"Only callable by capsule principals.",
		Capability: "", // auth-only; cursor data is the capsule's own bytes
		InputSchema: json.RawMessage(`{
            "type": "object",
            "properties": {
                "value": {"type": "string"}
            },
            "required": ["value"],
            "additionalProperties": false
        }`),
		Handler: makeCursorSetHandler(store),
	}
}

func makeCursorSetHandler(
	store *CapsuleCursorStore,
) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		capsuleName, err := requireCapsulePrincipalCursor(in.Principal)
		if err != nil {
			return ToolResult{}, err
		}
		var args cursorSetArgs
		if err := json.Unmarshal(in.Args, &args); err != nil {
			return ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
		}
		if err := store.Set(ctx, capsuleName, args.Value); err != nil {
			return ToolResult{}, fmt.Errorf("%w: cursor.set: %v", ErrToolBackend, err)
		}
		content, err := JSONContent(map[string]any{"ok": true})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{content}}, nil
	}
}

// NewCursorGetTool builds cursor.get.
func NewCursorGetTool(store *CapsuleCursorStore) Tool {
	return Tool{
		Name: "cursor.get",
		Description: "Read this capsule installation's previously-persisted sync cursor. " +
			"Returns value=\"\" when no cursor has been set. " +
			"Only callable by capsule principals.",
		Capability:  "",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler:     makeCursorGetHandler(store),
	}
}

func makeCursorGetHandler(
	store *CapsuleCursorStore,
) func(context.Context, ToolInput) (ToolResult, error) {
	return func(ctx context.Context, in ToolInput) (ToolResult, error) {
		capsuleName, err := requireCapsulePrincipalCursor(in.Principal)
		if err != nil {
			return ToolResult{}, err
		}
		value, err := store.Get(ctx, capsuleName)
		if err != nil {
			return ToolResult{}, fmt.Errorf("%w: cursor.get: %v", ErrToolBackend, err)
		}
		content, err := JSONContent(cursorGetResult{Value: value})
		if err != nil {
			return ToolResult{}, err
		}
		return ToolResult{Content: []Content{content}}, nil
	}
}

// requireCapsulePrincipalCursor mirrors requireCapsulePrincipal in
// tools_credentials.go but with cursor-flavored errors so failures
// are self-identifying in logs. Kept separate to avoid coupling the
// two toolsets' error messages.
func requireCapsulePrincipalCursor(p permission.Principal) (string, error) {
	if p.Kind != permission.PrincipalCapsule {
		return "", fmt.Errorf("cursor.* tools are restricted to capsule principals (caller is %s)", p.Kind)
	}
	if p.ID == "" {
		return "", ErrEmptyCursorCapsuleName
	}
	return p.ID, nil
}
