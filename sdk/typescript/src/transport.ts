/**
 * MCP-over-stdio transport.
 *
 * The Loamss runtime spawns a capsule subprocess and pipes JSON-RPC
 * 2.0 frames over stdin/stdout. Framing is newline-delimited JSON
 * (each message on its own line, terminated by `\n`) — same as the
 * runtime-side transport in `runtime/internal/mcp/transport.go`.
 *
 * The transport multiplexes both directions on the same pair of
 * pipes:
 *   - Runtime → capsule: tools/list, tools/call, …  (Transport.handle)
 *   - Capsule → runtime: memory.query, files.read, … (Transport.request)
 *
 * Pending capsule→runtime requests are tracked by their JSON-RPC id;
 * responses route back to the awaiting Promise.
 *
 * IMPORTANT: stdout is RESERVED for MCP frames. Any debug/log output
 * must go to stderr (use {@link RuntimeClient.log} or `console.error`).
 * A stray `console.log` will corrupt the stream and the runtime will
 * disconnect the capsule.
 */

import {
	ErrorCodes,
	JSONRPC_VERSION,
	type JSONRPCError,
	type JSONRPCId,
	type JSONRPCMessage,
	type JSONRPCNotification,
	type JSONRPCRequest,
	type JSONRPCResponse,
	type JSONRPCSuccessResponse,
	RPCError,
	isNotification,
	isRequest,
	isResponse,
} from "./jsonrpc.js";

/**
 * Handler is invoked for every inbound request and notification. It
 * returns the result for the JSON-RPC response (or throws an
 * {@link RPCError} / generic Error). Notifications discard the
 * return value.
 */
export type RequestHandler = (
	method: string,
	params: unknown,
) => Promise<unknown>;

/**
 * Streams the transport reads/writes from. In production these are
 * `process.stdin` / `process.stdout`; tests inject in-memory streams.
 */
export interface TransportStreams {
	input: ReadableStream<Uint8Array>;
	output: WritableStream<Uint8Array>;
}

interface PendingRequest {
	resolve: (value: unknown) => void;
	reject: (reason: unknown) => void;
}

export interface TransportOptions {
	streams: TransportStreams;
	/** Invoked for inbound requests + notifications (capsule-side handlers). */
	handler: RequestHandler;
	/** Logger for transport-level events. Defaults to a no-op. */
	logger?: TransportLogger;
}

export interface TransportLogger {
	warn: (msg: string, extra?: object) => void;
	error: (msg: string, extra?: object) => void;
}

const noopLogger: TransportLogger = {
	warn: () => {},
	error: () => {},
};

/**
 * Transport wraps the stdio pipes with newline-delimited JSON-RPC.
 * Construct with {@link TransportOptions}; call `start()` to begin
 * reading; call `request()` to issue an outbound call; call
 * `close()` to drain and shut down.
 */
export class Transport {
	private readonly handler: RequestHandler;
	private readonly logger: TransportLogger;
	private readonly input: ReadableStream<Uint8Array>;
	private readonly writer: WritableStreamDefaultWriter<Uint8Array>;
	private readonly pending = new Map<JSONRPCId, PendingRequest>();
	private readonly encoder = new TextEncoder();
	private nextID = 1;
	private closed = false;
	private closedDeferred: PromiseWithResolvers<void>;
	// Captured by start() so close() can cancel a blocked read.
	private currentReader: ReadableStreamDefaultReader<Uint8Array> | null = null;

	constructor(opts: TransportOptions) {
		this.handler = opts.handler;
		this.logger = opts.logger ?? noopLogger;
		this.input = opts.streams.input;
		this.writer = opts.streams.output.getWriter();
		this.closedDeferred = Promise.withResolvers<void>();
	}

	/**
	 * Begin reading inbound frames. Resolves when the input stream
	 * closes (EOF on stdin, in normal shutdown) or when `close()` is
	 * called.
	 */
	async start(): Promise<void> {
		const decoder = new TextDecoder();
		const reader = this.input.getReader() as ReadableStreamDefaultReader<Uint8Array>;
		this.currentReader = reader;
		let buffer = "";
		try {
			while (!this.closed) {
				const { done, value } = await reader.read();
				if (done) break;
				buffer += decoder.decode(value, { stream: true });
				let nl: number;
				while ((nl = buffer.indexOf("\n")) !== -1) {
					const line = buffer.slice(0, nl).trim();
					buffer = buffer.slice(nl + 1);
					if (line.length === 0) continue;
					this.dispatchLine(line);
				}
			}
		} catch (err) {
			// AbortError / cancellation is expected when close() is called
			// mid-read; treat it as a clean shutdown rather than a crash.
			if (!this.closed) {
				this.logger.error("transport read loop crashed", {
					error: errorToObj(err),
				});
			}
		} finally {
			this.currentReader = null;
			try {
				reader.releaseLock();
			} catch {
				// already released by cancel()
			}
			this.failPending(new Error("transport closed"));
			this.closedDeferred.resolve();
		}
	}

	/** Issue an outbound request (capsule → runtime). */
	async request<T = unknown>(method: string, params?: unknown): Promise<T> {
		if (this.closed) {
			throw new Error("transport: closed");
		}
		const id = this.nextID++;
		const frame: JSONRPCRequest = {
			jsonrpc: JSONRPC_VERSION,
			id,
			method,
			...(params !== undefined ? { params } : {}),
		};
		const deferred = Promise.withResolvers<unknown>();
		this.pending.set(id, deferred);
		try {
			await this.writeFrame(frame);
		} catch (err) {
			this.pending.delete(id);
			throw err;
		}
		return deferred.promise as Promise<T>;
	}

	/** Issue an outbound notification (capsule → runtime, no reply expected). */
	async notify(method: string, params?: unknown): Promise<void> {
		if (this.closed) {
			throw new Error("transport: closed");
		}
		const frame: JSONRPCNotification = {
			jsonrpc: JSONRPC_VERSION,
			method,
			...(params !== undefined ? { params } : {}),
		};
		await this.writeFrame(frame);
	}

	/** Gracefully close: stop reading, fail pending, drain writer. */
	async close(): Promise<void> {
		if (this.closed) return;
		this.closed = true;
		// Cancel the inbound reader so start()'s loop unblocks from its
		// pending reader.read() and exits.
		if (this.currentReader) {
			try {
				await this.currentReader.cancel();
			} catch {
				// ignore cancellation errors
			}
		}
		try {
			await this.writer.close();
		} catch {
			// already closed
		}
		await this.closedDeferred.promise;
	}

	/** A Promise that resolves when the transport has fully shut down. */
	get done(): Promise<void> {
		return this.closedDeferred.promise;
	}

	// --- internals ----------------------------------------------------

	private dispatchLine(line: string): void {
		let msg: JSONRPCMessage;
		try {
			msg = JSON.parse(line) as JSONRPCMessage;
		} catch (err) {
			this.logger.warn("malformed JSON on stdin", {
				error: errorToObj(err),
				line: line.slice(0, 200),
			});
			return;
		}
		if (isResponse(msg)) {
			this.deliverResponse(msg);
			return;
		}
		if (isRequest(msg)) {
			void this.runHandler(msg);
			return;
		}
		if (isNotification(msg)) {
			void this.runNotification(msg);
			return;
		}
		this.logger.warn("unrecognized frame shape", {
			line: line.slice(0, 200),
		});
	}

	private deliverResponse(msg: JSONRPCResponse): void {
		if (msg.id == null) {
			this.logger.warn("response with null id; cannot route");
			return;
		}
		const pending = this.pending.get(msg.id);
		if (!pending) {
			this.logger.warn("response for unknown id", { id: String(msg.id) });
			return;
		}
		this.pending.delete(msg.id);
		if ("error" in msg) {
			pending.reject(rpcErrorFrom(msg.error));
		} else {
			pending.resolve((msg as JSONRPCSuccessResponse).result);
		}
	}

	private async runHandler(req: JSONRPCRequest): Promise<void> {
		try {
			const result = await this.handler(req.method, req.params);
			const reply: JSONRPCSuccessResponse = {
				jsonrpc: JSONRPC_VERSION,
				id: req.id,
				result: result ?? null,
			};
			await this.writeFrame(reply);
		} catch (err) {
			const rpcErr =
				err instanceof RPCError
					? err
					: new RPCError(
							ErrorCodes.InternalError,
							err instanceof Error ? err.message : String(err),
						);
			const reply: JSONRPCResponse = {
				jsonrpc: JSONRPC_VERSION,
				id: req.id,
				error: {
					code: rpcErr.code,
					message: rpcErr.message,
					...(rpcErr.data !== undefined ? { data: rpcErr.data } : {}),
				},
			};
			await this.writeFrame(reply);
		}
	}

	private async runNotification(n: JSONRPCNotification): Promise<void> {
		try {
			await this.handler(n.method, n.params);
		} catch (err) {
			this.logger.warn("notification handler threw", {
				method: n.method,
				error: errorToObj(err),
			});
		}
	}

	private async writeFrame(frame: JSONRPCMessage): Promise<void> {
		const line = `${JSON.stringify(frame)}\n`;
		await this.writer.write(this.encoder.encode(line));
	}

	private failPending(err: Error): void {
		for (const [id, pending] of this.pending) {
			pending.reject(err);
			this.pending.delete(id);
		}
	}
}

function rpcErrorFrom(e: JSONRPCError): RPCError {
	return new RPCError(e.code, e.message, e.data);
}

function errorToObj(err: unknown): object {
	if (err instanceof Error) {
		return { name: err.name, message: err.message };
	}
	return { value: String(err) };
}

/**
 * Construct streams that wrap `process.stdin` / `process.stdout`.
 * Pulled out so tests can inject mock streams via {@link Transport}'s
 * constructor without going through process globals.
 *
 * Works in both Bun and Node 20+:
 *   - Stdin: Bun exposes `Bun.stdin.stream()`; on Node we adapt the
 *     legacy Readable into a ReadableStream.
 *   - Stdout: neither Bun nor Node provides a turnkey
 *     WritableStream<Uint8Array> for process.stdout, so we wrap
 *     `process.stdout.write` directly.
 */
export function processStreams(): TransportStreams {
	return {
		input: createStdinStream(),
		output: createStdoutStream(),
	};
}

function createStdinStream(): ReadableStream<Uint8Array> {
	// Bun: prefer Bun.stdin.stream() — already a ReadableStream<Uint8Array>.
	const bunGlobal = (globalThis as { Bun?: { stdin?: { stream(): ReadableStream<Uint8Array> } } }).Bun;
	if (bunGlobal?.stdin?.stream) {
		return bunGlobal.stdin.stream();
	}
	// Node: adapt the legacy Readable.
	const ps = process.stdin;
	return new ReadableStream<Uint8Array>({
		start(controller) {
			ps.on("data", (chunk: Buffer | string) => {
				controller.enqueue(
					typeof chunk === "string"
						? new TextEncoder().encode(chunk)
						: new Uint8Array(
								chunk.buffer,
								chunk.byteOffset,
								chunk.byteLength,
							),
				);
			});
			ps.on("end", () => controller.close());
			ps.on("error", (err) => controller.error(err));
		},
	});
}

function createStdoutStream(): WritableStream<Uint8Array> {
	const ps = process.stdout;
	return new WritableStream<Uint8Array>({
		write(chunk) {
			return new Promise<void>((resolve, reject) => {
				const ok = ps.write(chunk as unknown as Uint8Array, (err) =>
					err ? reject(err) : resolve(),
				);
				// Backpressure: process.stdout.write returns false when
				// the kernel buffer is full; we wait for "drain" before
				// resolving so we don't unbounded-queue writes.
				if (!ok) {
					ps.once("drain", () => resolve());
				}
			});
		},
	});
}
