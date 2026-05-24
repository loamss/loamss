/**
 * MCP client — the Path-B app's view of a paired Loamss runtime.
 *
 * Constructed with a bearer token + endpoint URL (both obtained from
 * {@link pair}). Wraps:
 *
 *   - POST /mcp     JSON-RPC 2.0 with Authorization: Bearer <token>
 *   - GET  /mcp     SSE stream (hello + ping + future notifications)
 *
 * Symmetric in shape to the capsule SDK's `RuntimeClient`: same
 * `tools.list / tools.call / resources.list / resources.read` surface,
 * but speaking HTTP instead of MCP-over-stdio. An app written against
 * one surface can be moved to the other with minimal change.
 *
 * Error mapping:
 *   - HTTP 401            → AuthorizationError (token rejected/revoked)
 *   - JSON-RPC -32001     → RPCError(code, message, data)
 *   - JSON-RPC -32002     → ApprovalRequiredError(approvalId, capability)
 *   - JSON-RPC -32003     → RPCError(-32003, ...) (unknown tool)
 *   - Other JSON-RPC errors → RPCError
 *   - Network failures    → underlying fetch error (propagated)
 */

import {
	ApprovalRequiredError,
	AuthorizationError,
} from "./errors.js";
import {
	JSONRPC_VERSION,
	type JSONRPCId,
	RPCError,
} from "./jsonrpc.js";
import type {
	ResourceContents,
	ResourceDescriptor,
	ToolCallResult,
	ToolDescriptor,
} from "./runtime.js";
import { parseSSE, type SSEEvent } from "./sse.js";

export interface ClientOptions {
	/**
	 * Endpoint URL the runtime returned from {@link pair} (typically
	 * `http://<host>/mcp`). The client appends nothing — pass the
	 * full /mcp URL.
	 */
	endpoint: string;

	/** Bearer token from {@link pair}. */
	token: string;

	/**
	 * Override fetch. Defaults to global fetch. Tests pass a mock;
	 * non-browser environments without a global fetch can pass an
	 * implementation in.
	 */
	fetch?: typeof fetch;

	/**
	 * Default AbortSignal applied to every request. Per-call signals
	 * can override / extend. Optional.
	 */
	signal?: AbortSignal;
}

/**
 * The handle a Path-B app uses to drive a paired Loamss runtime.
 */
export interface LoamssClient {
	tools: {
		list(opts?: CallOptions): Promise<ToolDescriptor[]>;
		call(
			name: string,
			args?: unknown,
			opts?: CallOptions,
		): Promise<ToolCallResult>;
	};
	resources: {
		list(opts?: CallOptions): Promise<ResourceDescriptor[]>;
		read(uri: string, opts?: CallOptions): Promise<ResourceContents>;
	};

	/**
	 * Make a raw JSON-RPC call. Use for methods not covered by the
	 * higher-level accessors (e.g., audit.tail, custom capsule tools).
	 */
	call<T = unknown>(
		method: string,
		params?: unknown,
		opts?: CallOptions,
	): Promise<T>;

	/**
	 * Open the runtime's SSE stream. Returns an AsyncIterable of
	 * events; the caller iterates with `for await (const ev of …)`
	 * and exits the loop / cancels the signal to stop.
	 *
	 * The stream emits `hello` on connect and `ping` every 15s; v0.1
	 * also emits future subscription notifications (resources/updated,
	 * log messages) on the same channel as they ship.
	 */
	subscribe(opts?: { signal?: AbortSignal }): AsyncIterable<SSEEvent>;

	/** The endpoint URL this client targets. Useful for logging / debug. */
	readonly endpoint: string;
}

export interface CallOptions {
	/** Per-call abort signal. */
	signal?: AbortSignal;
}

interface JSONRPCRequestBody {
	jsonrpc: typeof JSONRPC_VERSION;
	id: JSONRPCId;
	method: string;
	params?: unknown;
}

interface JSONRPCSuccessBody {
	jsonrpc: typeof JSONRPC_VERSION;
	id: JSONRPCId;
	result: unknown;
}

interface JSONRPCErrorBody {
	jsonrpc: typeof JSONRPC_VERSION;
	id: JSONRPCId | null;
	error: { code: number; message: string; data?: unknown };
}

export function createClient(opts: ClientOptions): LoamssClient {
	const fetchFn = opts.fetch ?? globalThis.fetch;
	const endpoint = opts.endpoint;
	let nextID = 1;

	async function rpc<T>(
		method: string,
		params?: unknown,
		callOpts?: CallOptions,
	): Promise<T> {
		const id = nextID++;
		const body: JSONRPCRequestBody = {
			jsonrpc: JSONRPC_VERSION,
			id,
			method,
			...(params !== undefined ? { params } : {}),
		};
		const signal = mergeSignals(opts.signal, callOpts?.signal);
		const resp = await fetchFn(endpoint, {
			method: "POST",
			headers: {
				"Content-Type": "application/json",
				Accept: "application/json",
				Authorization: `Bearer ${opts.token}`,
			},
			body: JSON.stringify(body),
			...(signal ? { signal } : {}),
		});

		if (resp.status === 401) {
			const text = await resp.text();
			throw new AuthorizationError(
				`unauthorized: ${text.slice(0, 200) || resp.statusText}`,
			);
		}

		const text = await resp.text();
		let decoded: unknown;
		try {
			decoded = text ? JSON.parse(text) : null;
		} catch {
			throw new Error(
				`mcp: non-JSON response from ${endpoint} (status ${resp.status}): ${text.slice(0, 200)}`,
			);
		}

		if (decoded === null || typeof decoded !== "object") {
			throw new Error(
				`mcp: expected JSON-RPC envelope, got ${typeof decoded}`,
			);
		}

		if ("error" in (decoded as object)) {
			const err = (decoded as JSONRPCErrorBody).error;
			throw mapRPCError(err);
		}
		return (decoded as JSONRPCSuccessBody).result as T;
	}

	return {
		endpoint,

		async call<T>(method: string, params?: unknown, callOpts?: CallOptions) {
			return rpc<T>(method, params, callOpts);
		},

		tools: {
			async list(callOpts) {
				const res = await rpc<{ tools?: ToolDescriptor[] }>(
					"tools/list",
					undefined,
					callOpts,
				);
				return res.tools ?? [];
			},
			async call(name, args, callOpts) {
				return rpc<ToolCallResult>(
					"tools/call",
					{ name, arguments: args ?? {} },
					callOpts,
				);
			},
		},

		resources: {
			async list(callOpts) {
				const res = await rpc<{ resources?: ResourceDescriptor[] }>(
					"resources/list",
					undefined,
					callOpts,
				);
				return res.resources ?? [];
			},
			async read(uri, callOpts) {
				return rpc<ResourceContents>("resources/read", { uri }, callOpts);
			},
		},

		subscribe(subOpts) {
			return openSSE(endpoint, opts.token, fetchFn, subOpts?.signal);
		},
	};
}

// --- helpers ----------------------------------------------------------

function mapRPCError(err: {
	code: number;
	message: string;
	data?: unknown;
}): Error {
	// -32002 = approval required. The data carries the approval id +
	// capability so the caller can either prompt the user or poll.
	if (err.code === -32002) {
		const data = (err.data ?? {}) as {
			approval_id?: string;
			capability?: string;
		};
		return new ApprovalRequiredError(
			err.message,
			data.approval_id ?? "",
			data.capability ?? "",
			err.data,
		);
	}
	return new RPCError(err.code, err.message, err.data);
}

function mergeSignals(
	a: AbortSignal | undefined,
	b: AbortSignal | undefined,
): AbortSignal | undefined {
	if (!a && !b) return undefined;
	if (a && !b) return a;
	if (!a && b) return b;
	// Both present: AbortSignal.any landed in Node 20 + Bun.
	const anyImpl = (
		AbortSignal as unknown as { any?: (signals: AbortSignal[]) => AbortSignal }
	).any;
	if (anyImpl) {
		// biome-ignore lint/style/noNonNullAssertion: both checked above
		return anyImpl([a!, b!]);
	}
	// Fallback: manual fan-in.
	const ctrl = new AbortController();
	// biome-ignore lint/style/noNonNullAssertion: both checked above
	a!.addEventListener("abort", () => ctrl.abort(a!.reason));
	// biome-ignore lint/style/noNonNullAssertion: both checked above
	b!.addEventListener("abort", () => ctrl.abort(b!.reason));
	return ctrl.signal;
}

async function* openSSE(
	endpoint: string,
	token: string,
	fetchFn: typeof fetch,
	signal: AbortSignal | undefined,
): AsyncIterable<SSEEvent> {
	const resp = await fetchFn(endpoint, {
		method: "GET",
		headers: {
			Accept: "text/event-stream",
			Authorization: `Bearer ${token}`,
		},
		...(signal ? { signal } : {}),
	});
	if (resp.status === 401) {
		throw new AuthorizationError("subscribe: unauthorized");
	}
	if (!resp.ok) {
		throw new Error(
			`subscribe: ${resp.status} ${resp.statusText} from ${endpoint}`,
		);
	}
	if (!resp.body) {
		throw new Error("subscribe: response has no body");
	}
	yield* parseSSE(resp.body);
}
