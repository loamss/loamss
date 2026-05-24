package mcp

import (
	"context"
	"encoding/json"
)

// client.info is the introspection tool every authenticated client
// can call regardless of granted capabilities. It returns the
// caller's own Client record (name, paired_at, last_seen_at, but
// not the credential or hash). Acts as the "who am I?" handshake
// AI clients use after initialize.
//
// Capability is empty — auth is the only gate. Listing this tool in
// tools/list with no scope tells clients they always have it,
// matching the upstream MCP convention for low-privilege
// self-introspection.

// clientInfoResult is the JSON shape returned in the tool's text
// Content block. Mirrors permission.Client minus credential_hash.
type clientInfoResult struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	PairedAt   string         `json:"paired_at"`
	LastSeenAt string         `json:"last_seen_at,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// NewClientInfoTool builds the tool. Caller registers it on the
// runtime's Registry at startup.
func NewClientInfoTool() Tool {
	return Tool{
		Name:        "client.info",
		Description: "Returns information about the authenticated client (id, name, paired_at, last_seen_at). Always available; requires no extra capability.",
		Capability:  "", // auth-only
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler:     handleClientInfo,
	}
}

func handleClientInfo(_ context.Context, in ToolInput) (ToolResult, error) {
	c := in.Client
	out := clientInfoResult{
		ID:       c.ID,
		Name:     c.Name,
		PairedAt: c.PairedAt.Format("2006-01-02T15:04:05.000000Z"),
		Metadata: c.Metadata,
	}
	if c.LastSeenAt != nil {
		out.LastSeenAt = c.LastSeenAt.Format("2006-01-02T15:04:05.000000Z")
	}
	content, err := JSONContent(out)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Content: []Content{content}}, nil
}
