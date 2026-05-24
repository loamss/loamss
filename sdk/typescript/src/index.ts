/**
 * @loamss/sdk public surface.
 *
 * A capsule typically needs just three names:
 *
 *   import { createCapsule, defineTool, text } from "@loamss/sdk";
 *
 * The full export is here for advanced uses (custom transports for
 * tests, lower-level RPCError throws, manifest types for code-as-
 * config setups).
 */

// Lifecycle
export {
	createCapsule,
	PROTOCOL_VERSION,
	type CapsuleHandle,
	type CapsuleOptions,
} from "./capsule.js";

// Tool authoring
export { defineTool, type Tool, type ToolContext } from "./tool.js";

// Content helpers + result types
export {
	text,
	json,
	image,
	result,
	errorResult,
	type Content,
	type DataContent,
	type TextContent,
	type ToolResult,
} from "./content.js";

// Runtime callback client (what a handler's `ctx.runtime` is)
export {
	createRuntimeClient,
	type LogLevel,
	type ResourceContents,
	type ResourceDescriptor,
	type RuntimeClient,
	type ToolCallResult,
	type ToolDescriptor,
} from "./runtime.js";

// Manifest types
export {
	SPEC_VERSION,
	type CapsuleManifest,
	type ManifestAuthor,
	type ManifestMemoryExtension,
	type ManifestPermissionRequest,
	type ManifestPermissions,
	type ManifestRequirements,
	type ManifestResource,
	type ManifestRuntime,
	type ManifestTool,
} from "./manifest.js";

// JSON-RPC primitives (for tools that need to throw specific codes)
export {
	ErrorCodes,
	JSONRPC_VERSION,
	RPCError,
	type JSONRPCError,
	type JSONRPCId,
	type JSONRPCMessage,
	type JSONRPCNotification,
	type JSONRPCRequest,
	type JSONRPCResponse,
} from "./jsonrpc.js";

// Transport (advanced — custom streams, tests)
export {
	processStreams,
	Transport,
	type RequestHandler,
	type TransportLogger,
	type TransportOptions,
	type TransportStreams,
} from "./transport.js";
