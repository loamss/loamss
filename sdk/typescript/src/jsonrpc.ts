/**
 * JSON-RPC 2.0 wire types — minimal subset Loamss capsules use.
 *
 * MCP is layered on JSON-RPC 2.0; every message a capsule reads or
 * writes on stdio matches one of these shapes. Keeping the types
 * here (rather than reaching for an external `jsonrpc` package)
 * keeps the SDK dependency-free and the wire shape inspectable in
 * the source.
 */

/** The literal version string every JSON-RPC 2.0 message carries. */
export const JSONRPC_VERSION = "2.0" as const;

/** Request id: number per the spec, but MCP implementations commonly use string. */
export type JSONRPCId = string | number;

export interface JSONRPCRequest {
	jsonrpc: typeof JSONRPC_VERSION;
	id: JSONRPCId;
	method: string;
	params?: unknown;
}

/** A request with no id — the server (capsule) should not respond. */
export interface JSONRPCNotification {
	jsonrpc: typeof JSONRPC_VERSION;
	method: string;
	params?: unknown;
}

export interface JSONRPCSuccessResponse {
	jsonrpc: typeof JSONRPC_VERSION;
	id: JSONRPCId;
	result: unknown;
}

export interface JSONRPCErrorResponse {
	jsonrpc: typeof JSONRPC_VERSION;
	id: JSONRPCId | null;
	error: JSONRPCError;
}

export type JSONRPCResponse = JSONRPCSuccessResponse | JSONRPCErrorResponse;

export interface JSONRPCError {
	code: number;
	message: string;
	data?: unknown;
}

/** Any inbound frame. The transport sniffs which kind it is. */
export type JSONRPCMessage =
	| JSONRPCRequest
	| JSONRPCNotification
	| JSONRPCResponse;

/**
 * Standard JSON-RPC 2.0 error codes plus the MCP/Loamss codes the
 * runtime uses. Capsules return these via {@link RPCError} when they
 * want to signal a specific failure mode rather than throwing a
 * generic Error (which becomes an internal-error -32603).
 */
export const ErrorCodes = {
	ParseError: -32700,
	InvalidRequest: -32600,
	MethodNotFound: -32601,
	InvalidParams: -32602,
	InternalError: -32603,

	// Loamss/MCP-specific (mirror `runtime/internal/mcp` codes)
	PermissionDenied: -32001,
	ApprovalRequired: -32002,
	UnknownTool: -32003,
	BackendError: -32099,
} as const;

/**
 * RPCError is the structured error capsule handlers can throw to
 * return a specific JSON-RPC code. Plain `Error` throws are wrapped
 * as -32603 InternalError by the transport.
 */
export class RPCError extends Error {
	readonly code: number;
	readonly data?: unknown;

	constructor(code: number, message: string, data?: unknown) {
		super(message);
		this.name = "RPCError";
		this.code = code;
		this.data = data;
	}
}

/** Type guards over inbound frames. */
export function isRequest(msg: JSONRPCMessage): msg is JSONRPCRequest {
	return "id" in msg && "method" in msg;
}

export function isNotification(
	msg: JSONRPCMessage,
): msg is JSONRPCNotification {
	return !("id" in msg) && "method" in msg;
}

export function isResponse(msg: JSONRPCMessage): msg is JSONRPCResponse {
	return "id" in msg && ("result" in msg || "error" in msg);
}
