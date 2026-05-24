/**
 * The Capsule lifecycle — the SDK's primary entry point.
 *
 * Capsule authors call {@link createCapsule} once with their manifest
 * + tools, then `.start()`. The SDK:
 *
 *   1. Wires a {@link Transport} to process.stdin/stdout
 *   2. Implements the `initialize`, `tools/list`, `tools/call` MCP
 *      methods on the inbound side
 *   3. Provides the handler's runtime callback client on the outbound
 *      side via {@link ToolContext.runtime}
 *   4. Waits for the transport to close (typically when the runtime
 *      stops the capsule subprocess and EOFs stdin)
 *
 * A capsule looks like:
 *
 *   ```ts
 *   import { createCapsule, defineTool, text } from "@loamss/sdk";
 *
 *   const hello = defineTool({
 *     name: "hello",
 *     description: "Say hi",
 *     inputSchema: { type: "object", properties: { who: { type: "string" } } },
 *     handler: async (input: { who?: string }) => `hello ${input.who ?? "world"}`,
 *   });
 *
 *   await createCapsule({
 *     manifest: { name: "hello", version: "0.1.0", author: { name: "you" } },
 *     tools: [hello],
 *   }).start();
 *   ```
 */

import { type ToolResult, toToolResult } from "./content.js";
import { ErrorCodes, RPCError } from "./jsonrpc.js";
import type { CapsuleManifest, ManifestTool } from "./manifest.js";
import { type RuntimeClient, createRuntimeClient } from "./runtime.js";
import type { Tool } from "./tool.js";
import {
	type TransportLogger,
	Transport,
	type TransportStreams,
	processStreams,
} from "./transport.js";

/** Loamss-compatible MCP protocol version. */
export const PROTOCOL_VERSION = "2025-03-26" as const;

export interface CapsuleOptions {
	manifest: CapsuleManifest;
	// biome-ignore lint/suspicious/noExplicitAny: heterogeneous tool array
	tools?: Tool<any, any>[];
	/**
	 * Override stdio streams. Production capsules omit this; tests
	 * pass in-memory ReadableStream/WritableStream to drive the
	 * lifecycle without spawning a subprocess.
	 */
	streams?: TransportStreams;
	/** Logger for transport-level warnings/errors (stderr in production). */
	logger?: TransportLogger;
}

/** Handle returned by {@link createCapsule}. */
export interface CapsuleHandle {
	/**
	 * Start the capsule. Returns when the transport closes (the
	 * runtime stopped us). Throws if the transport fails to start.
	 */
	start(): Promise<void>;

	/** Force-close the transport (for tests). */
	stop(): Promise<void>;

	/** Access the underlying transport for tests / advanced use. */
	readonly transport: Transport;

	/** Access the runtime callback client (typically used from inside handlers). */
	readonly runtime: RuntimeClient;
}

export function createCapsule(opts: CapsuleOptions): CapsuleHandle {
	// biome-ignore lint/suspicious/noExplicitAny: heterogeneous tool registry
	const tools = new Map<string, Tool<any, any>>();
	for (const t of opts.tools ?? []) {
		if (tools.has(t.name)) {
			throw new Error(`createCapsule: duplicate tool name "${t.name}"`);
		}
		tools.set(t.name, t);
	}

	const streams = opts.streams ?? processStreams();
	// Forward declaration so handlers (defined here) can call the
	// runtime client (constructed after transport).
	let runtimeClient!: RuntimeClient;

	const transport = new Transport({
		streams,
		logger: opts.logger,
		handler: async (method, params) => {
			switch (method) {
				case "initialize":
					return handleInitialize(opts.manifest);
				case "tools/list":
					return handleToolsList(opts.manifest, tools);
				case "tools/call":
					return handleToolsCall(tools, params, runtimeClient);
				case "ping":
					return {};
				case "notifications/initialized":
					// Notification: the runtime tells us initialize is done.
					// No response needed; the transport handler returns
					// undefined which is fine for notifications.
					return undefined;
				case "shutdown":
					// Notification or request: graceful shutdown ack.
					return {};
				default:
					throw new RPCError(
						ErrorCodes.MethodNotFound,
						`unknown method: ${method}`,
					);
			}
		},
	});

	runtimeClient = createRuntimeClient(transport);

	return {
		async start() {
			await transport.start();
		},
		async stop() {
			await transport.close();
		},
		get transport() {
			return transport;
		},
		get runtime() {
			return runtimeClient;
		},
	};
}

// --- inbound method handlers ---------------------------------------

interface InitializeResult {
	protocolVersion: string;
	capabilities: Record<string, unknown>;
	serverInfo: { name: string; version: string };
}

function handleInitialize(manifest: CapsuleManifest): InitializeResult {
	return {
		protocolVersion: PROTOCOL_VERSION,
		capabilities: {
			tools: { listChanged: false },
		},
		serverInfo: {
			name: manifest.name,
			version: manifest.version,
		},
	};
}

interface ToolsListResult {
	tools: Array<{
		name: string;
		description: string;
		inputSchema: object;
	}>;
}

function handleToolsList(
	manifest: CapsuleManifest,
	tools: Map<string, Tool<any, any>>,
): ToolsListResult {
	// Manifest tools are advisory metadata; the authoritative list is
	// what's been registered with createCapsule. Manifest validation
	// against this happens in the runtime, not here.
	void manifestToolsCount(manifest);

	const out: ToolsListResult["tools"] = [];
	for (const t of tools.values()) {
		out.push({
			name: t.name,
			description: t.description,
			inputSchema: t.inputSchema,
		});
	}
	return { tools: out };
}

function manifestToolsCount(m: CapsuleManifest): number {
	return (m.tools ?? []).length;
}

interface CallToolParams {
	name?: string;
	arguments?: unknown;
}

async function handleToolsCall(
	tools: Map<string, Tool<any, any>>,
	params: unknown,
	runtime: RuntimeClient,
): Promise<ToolResult> {
	const p = (params ?? {}) as CallToolParams;
	const name = p.name;
	if (!name) {
		throw new RPCError(ErrorCodes.InvalidParams, "tools/call: name required");
	}
	const tool = tools.get(name);
	if (!tool) {
		throw new RPCError(
			ErrorCodes.UnknownTool,
			`tools/call: unknown tool "${name}"`,
		);
	}
	const ac = new AbortController();
	try {
		const value = await tool.handler(p.arguments, {
			runtime,
			signal: ac.signal,
		});
		return toToolResult(value);
	} catch (err) {
		if (err instanceof RPCError) {
			throw err;
		}
		throw new RPCError(
			ErrorCodes.BackendError,
			err instanceof Error ? err.message : String(err),
		);
	}
}

// Exported manifest-shape exports for capsule-spec.md conformance docs.
export type { ManifestTool };
