/**
 * Runtime callback client — the capsule's view of the runtime.
 *
 * Wraps {@link Transport.request} for the methods the runtime
 * exposes to capsules:
 *   - tools.list / tools.call    — invoke a runtime tool (memory.query, …)
 *   - resources.list / resources.read — read runtime resources
 *   - log                        — structured log to runtime stderr
 *
 * Every call goes through the runtime's permission engine; deny
 * surfaces as an `RPCError` with code -32001.
 */

import { RPCError } from "./jsonrpc.js";
import type { Transport } from "./transport.js";

export interface ToolDescriptor {
	name: string;
	description?: string;
	inputSchema?: object;
}

export interface ResourceDescriptor {
	uri: string;
	name?: string;
	description?: string;
	mimeType?: string;
}

export interface ResourceContents {
	contents: Array<{
		uri: string;
		mimeType?: string;
		text?: string;
		blob?: string; // base64
	}>;
}

export interface ToolCallResult {
	content: Array<{
		type: "text" | "image" | "audio" | "resource";
		text?: string;
		data?: string;
		mimeType?: string;
	}>;
	isError?: boolean;
}

export type LogLevel = "debug" | "info" | "warn" | "error";

/**
 * The handle a tool handler receives via {@link ToolContext.runtime}.
 * Construct via {@link createRuntimeClient}; tests can pass in a
 * fake by constructing a Transport whose handler returns canned
 * results.
 */
export interface RuntimeClient {
	tools: {
		list(): Promise<ToolDescriptor[]>;
		call(name: string, args?: unknown): Promise<ToolCallResult>;
	};
	resources: {
		list(): Promise<ResourceDescriptor[]>;
		read(uri: string): Promise<ResourceContents>;
	};
	/** Send a structured log line to the runtime's logger. */
	log(level: LogLevel, msg: string, extra?: Record<string, unknown>): void;
}

export function createRuntimeClient(transport: Transport): RuntimeClient {
	return {
		tools: {
			async list() {
				const res = (await transport.request("tools/list")) as {
					tools?: ToolDescriptor[];
				};
				return res.tools ?? [];
			},
			async call(name, args) {
				if (!name) {
					throw new RPCError(-32602, "tools.call: name is required");
				}
				return (await transport.request("tools/call", {
					name,
					arguments: args ?? {},
				})) as ToolCallResult;
			},
		},
		resources: {
			async list() {
				const res = (await transport.request("resources/list")) as {
					resources?: ResourceDescriptor[];
				};
				return res.resources ?? [];
			},
			async read(uri) {
				if (!uri) {
					throw new RPCError(-32602, "resources.read: uri is required");
				}
				return (await transport.request("resources/read", {
					uri,
				})) as ResourceContents;
			},
		},
		log(level, msg, extra) {
			// Notifications: no response needed. The runtime listens for
			// logging/message per MCP convention. Errors here are
			// swallowed — logging must never crash a handler.
			transport
				.notify("logging/message", {
					level,
					message: msg,
					...(extra ? { data: extra } : {}),
				})
				.catch(() => {
					/* logging must never throw */
				});
		},
	};
}
