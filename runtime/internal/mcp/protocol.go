// Package mcp implements the Loamss MCP (Model Context Protocol)
// surface — the JSON-RPC 2.0 contract every external client speaks.
// Defined wire-side in mcp-surface.md; this package translates the
// spec into Go.
//
// Components:
//
//   - Protocol types (this file): JSON-RPC 2.0 envelope, MCP
//     initialize/tools/resources payloads, the four standard error
//     codes, and helpers for shaping responses.
//   - Tool registry (registry.go): the in-memory table of Tools the
//     runtime knows how to invoke. Runtime tools register at startup;
//     capsule tools (Phase 1b) will register at install time.
//   - Handler (handler.go): the HTTP entry point. Dispatches on
//     method to one of the registered handlers; per-method handlers
//     live alongside (initialize.go, tools.go, ...).
//   - SSE (sse.go): the GET /mcp streaming endpoint — heartbeat and
//     event scaffold. Subscriptions deferred to Phase 2.
//
// The package consumes Engine, audit.Writer, and memory.Adapter via
// constructor injection. It holds no global state.
package mcp

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 protocol version. Sent and required in every
// Request and Response.
const jsonRPCVersion = "2.0"

// MCP protocol version this server implements. Negotiated during
// `initialize`; clients announce the version they want and the
// server returns the version it'll actually use. Adopted directly
// from the upstream MCP spec; bumped when the upstream version we
// support changes.
const mcpProtocolVersion = "2025-03-26"

// Standard JSON-RPC 2.0 error codes (RFC 7159 §5.1) plus the
// MCP-specific range. The MCP-specific codes are documented in
// mcp-surface.md; they sit in -32000..-32099 (the "server-defined"
// range JSON-RPC reserves for application errors).
const (
	// JSON-RPC 2.0 standard codes.
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603

	// MCP-specific (application-level). These are how Loamss
	// surfaces permission / approval / not-found semantics to
	// MCP clients. Defined here in commit 1; first wired into
	// tools/call dispatch in commit 2.
	codePermissionDenied = -32001 //nolint:unused // wired in tools/call commit
	codeApprovalRequired = -32002 //nolint:unused // wired in tools/call commit
	codeUnknownTool      = -32003 //nolint:unused // wired in tools/call commit
	codeUnknownResource  = -32004 //nolint:unused // wired in resources commit
	codeBackendError     = -32099 //nolint:unused // wired in tools/call commit
)

// Request is the JSON-RPC 2.0 request envelope. Notifications (no id)
// are valid per the spec but Loamss does not yet emit or accept them;
// requests without an id are treated as notifications and produce no
// response.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification returns true if the request has no id (per
// JSON-RPC 2.0 §4.1).
func (r Request) IsNotification() bool { return len(r.ID) == 0 || string(r.ID) == "null" }

// Response is the JSON-RPC 2.0 response envelope. Exactly one of
// Result and Error MUST be set on a successful or failed response
// respectively; the helper constructors below maintain that invariant.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC 2.0 error object. Data is free-form
// per the spec; we use it to attach the principal id on auth-related
// errors and the approval id on ApprovalRequired errors.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error makes RPCError satisfy the error interface; convenient for
// wrapping in fmt.Errorf chains.
func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("rpc %d: %s", e.Code, e.Message)
}

// --- response constructors ---------------------------------------------

// successResponse builds a JSON-RPC response with the given result.
func successResponse(id json.RawMessage, result any) Response {
	return Response{JSONRPC: jsonRPCVersion, ID: id, Result: result}
}

// errorResponse builds a JSON-RPC response carrying an RPCError.
// Use the typed helpers (parseErrorResponse, methodNotFoundResponse,
// ...) where they apply; this is the general constructor.
func errorResponse(id json.RawMessage, code int, message string, data any) Response {
	return Response{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Error:   &RPCError{Code: code, Message: message, Data: data},
	}
}

func parseErrorResponse() Response {
	// Per JSON-RPC 2.0: id MUST be null when a Parse error occurs
	// (the request can't be parsed, so the id is unknown).
	return errorResponse(json.RawMessage("null"), codeParseError, "parse error", nil)
}

func invalidRequestResponse(id json.RawMessage, detail string) Response {
	return errorResponse(id, codeInvalidRequest, "invalid request", detail)
}

func methodNotFoundResponse(id json.RawMessage, method string) Response {
	return errorResponse(id, codeMethodNotFound, "method not found: "+method, nil)
}

func invalidParamsResponse(id json.RawMessage, detail string) Response {
	return errorResponse(id, codeInvalidParams, "invalid params", detail)
}

func internalErrorResponse(id json.RawMessage, detail string) Response {
	return errorResponse(id, codeInternalError, "internal error", detail)
}

// --- MCP-specific payloads ---------------------------------------------

// InitializeParams is the params for `initialize`. The client
// announces its protocol version + capabilities; the server returns
// the version it'll use plus its own capabilities + info.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      Implementation     `json:"clientInfo"`
}

// InitializeResult is the result of `initialize`. The protocol
// version is the one the server *will* use, which may downgrade from
// the client's request if the server doesn't support it. Clients
// must check.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
}

// Implementation is the {name, version} pair clients and servers
// exchange in `initialize`. Used for diagnostics and (eventually)
// per-client behavior toggles.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCapabilities reports what optional MCP features the client
// supports. Loamss reads these but currently makes no decisions
// based on them; they're recorded for future use.
type ClientCapabilities struct {
	Experimental map[string]any  `json:"experimental,omitempty"`
	Sampling     *SamplingClient `json:"sampling,omitempty"`
	Roots        *RootsClient    `json:"roots,omitempty"`
}

// SamplingClient is the client-side sampling capability advertisement.
// Reserved; Loamss does not currently issue sampling requests.
type SamplingClient struct{}

// RootsClient is the client-side roots capability advertisement.
// Reserved.
type RootsClient struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerCapabilities tells the client which MCP feature areas the
// server supports. Loamss advertises tools + resources from
// commit 2 onward; commit 1 advertises only logging (the most
// minimal valid capability set).
type ServerCapabilities struct {
	Tools     *ToolsServer     `json:"tools,omitempty"`
	Resources *ResourcesServer `json:"resources,omitempty"`
	Logging   *LoggingServer   `json:"logging,omitempty"`
	Prompts   *PromptsServer   `json:"prompts,omitempty"`
}

// ToolsServer advertises the tools capability. ListChanged is the
// only MCP-defined flag; Loamss sets it to false in v0.1 since
// tool sets are stable per session.
type ToolsServer struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesServer advertises the resources capability.
type ResourcesServer struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// LoggingServer advertises that the server emits log notifications.
// Loamss currently does not push log notifications; the flag exists
// so future versions can flip it on without an API change.
type LoggingServer struct{}

// PromptsServer advertises prompt-template support. Deferred.
type PromptsServer struct {
	ListChanged bool `json:"listChanged,omitempty"`
}
