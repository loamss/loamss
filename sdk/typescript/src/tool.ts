/**
 * Tool definitions — what capsules expose to the runtime via
 * `tools/list` and respond to via `tools/call`.
 */

import type { RuntimeClient } from "./runtime.js";

/** Context passed to every tool handler. */
export interface ToolContext {
	/**
	 * Runtime callback client. Use this to call runtime tools
	 * (memory.query, files.read, …) from inside a handler. Calls
	 * are gated by the same permission engine the user granted —
	 * a capsule cannot escalate by calling itself in.
	 */
	runtime: RuntimeClient;

	/**
	 * AbortSignal that fires when the inbound `tools/call` is
	 * canceled by the runtime (timeout, client disconnect). Pass it
	 * to long-running awaits to cooperate with shutdown.
	 */
	signal: AbortSignal;
}

/**
 * A tool the capsule exposes. The handler is invoked with the
 * decoded `arguments` JSON object and a {@link ToolContext}; its
 * return value is auto-wrapped into a ToolResult unless the handler
 * returns one explicitly.
 */
export interface Tool<Input = unknown, Output = unknown> {
	/** Tool name as exposed via `tools/list`. */
	name: string;

	/** Short human-readable description. */
	description: string;

	/**
	 * JSON Schema for the tool's input. The runtime validates against
	 * this before dispatching to the capsule, but the capsule should
	 * still defensively validate — schemas are advisory at the wire
	 * level.
	 */
	inputSchema: object;

	/** The implementation. */
	handler: (input: Input, ctx: ToolContext) => Promise<Output> | Output;
}

/**
 * Builder helper. Mostly for IDE ergonomics (it forwards types) and
 * a place to wire validation later if we add it. Today it's a
 * passthrough.
 */
export function defineTool<Input = unknown, Output = unknown>(
	tool: Tool<Input, Output>,
): Tool<Input, Output> {
	return tool;
}
