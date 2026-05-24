package mcp

import (
	"encoding/json"
	"net/http"
)

// handleInitialize implements the MCP `initialize` method.
//
// The flow: client announces its protocol version + capabilities and
// describes itself; the server confirms the version it'll use (which
// may downgrade), advertises its capabilities, and returns its own
// identity. After initialize, the client typically follows with
// tools/list and resources/list.
//
// Version negotiation: in v0.1 we only support one version. If the
// client asks for something different, we still return our version
// and let the client decide whether to proceed.
func (h *Handler) handleInitialize(_ *http.Request, req Request) Response {
	var params InitializeParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return invalidParamsResponse(req.ID, "cannot decode initialize params: "+err.Error())
		}
	}
	// We don't reject on version mismatch (client may choose to
	// continue with our version); we only log for observability.
	if params.ProtocolVersion != "" && params.ProtocolVersion != mcpProtocolVersion {
		h.deps.Logger.Info("mcp initialize: version downgrade offered",
			"client_requested", params.ProtocolVersion,
			"server_supports", mcpProtocolVersion,
			"client_name", params.ClientInfo.Name,
		)
	}

	caps := ServerCapabilities{
		// Tools and resources land in subsequent commits. We
		// advertise tools immediately so clients that condition
		// their workflow on the capability don't bail; the actual
		// tools/list returns the empty set until commit 2.
		Tools:     &ToolsServer{ListChanged: false},
		Resources: &ResourcesServer{Subscribe: false, ListChanged: false},
		Logging:   &LoggingServer{},
	}

	result := InitializeResult{
		ProtocolVersion: mcpProtocolVersion,
		Capabilities:    caps,
		ServerInfo: Implementation{
			Name:    h.deps.ServerName,
			Version: h.deps.ServerVersion,
		},
	}
	return successResponse(req.ID, result)
}
