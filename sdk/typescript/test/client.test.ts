/**
 * Client tests. Stand up a Bun.serve mock that plays the runtime's
 * /mcp endpoint (POST for JSON-RPC, GET for SSE) and exercise the
 * full surface.
 */

import { describe, expect, test } from "bun:test";
import { createClient } from "../src/client.js";
import {
	ApprovalRequiredError,
	AuthorizationError,
} from "../src/errors.js";
import { RPCError } from "../src/jsonrpc.js";

type RPCResponseShape =
	| { result: unknown }
	| { error: { code: number; message: string; data?: unknown } };

/**
 * Start a mock /mcp endpoint. The handler receives the JSON-RPC
 * request body and returns the response shape (result | error). For
 * SSE requests, return an SSE Response directly.
 */
function startMockMCP(
	handler: (
		method: string,
		params: unknown,
		auth: string | null,
	) => Promise<RPCResponseShape> | RPCResponseShape,
	sseHandler?: (auth: string | null) => Response,
) {
	const server = Bun.serve({
		port: 0,
		fetch: async (req) => {
			if (req.method === "GET") {
				if (!sseHandler) {
					return new Response("not implemented", { status: 501 });
				}
				return sseHandler(req.headers.get("authorization"));
			}
			const body = (await req.json()) as {
				id: number | string;
				method: string;
				params?: unknown;
			};
			const auth = req.headers.get("authorization");
			const resp = await handler(body.method, body.params, auth);
			return Response.json({ jsonrpc: "2.0", id: body.id, ...resp });
		},
	});
	return {
		endpoint: `http://${server.hostname}:${server.port}/mcp`,
		close: () => server.stop(),
	};
}

describe("createClient — tools", () => {
	test("tools.list returns the runtime's tools", async () => {
		const srv = startMockMCP((method) => {
			if (method === "tools/list") {
				return {
					result: {
						tools: [
							{ name: "memory.query", description: "..." },
							{ name: "files.read", description: "..." },
						],
					},
				};
			}
			return { error: { code: -32601, message: "unknown" } };
		});
		try {
			const client = createClient({
				endpoint: srv.endpoint,
				token: "tok-test",
			});
			const tools = await client.tools.list();
			expect(tools.map((t) => t.name).sort()).toEqual([
				"files.read",
				"memory.query",
			]);
		} finally {
			srv.close();
		}
	});

	test("tools.call passes args + Authorization header", async () => {
		let observedAuth = "";
		let observedArgs: unknown = null;
		const srv = startMockMCP((method, params, auth) => {
			observedAuth = auth ?? "";
			if (method === "tools/call") {
				observedArgs = (params as { arguments: unknown }).arguments;
				return {
					result: {
						content: [{ type: "text", text: "ok" }],
					},
				};
			}
			return { error: { code: -32601, message: "unknown" } };
		});
		try {
			const client = createClient({
				endpoint: srv.endpoint,
				token: "tok-x",
			});
			const out = await client.tools.call("memory.query", { q: "Sarah" });
			expect(out.content[0]?.text).toBe("ok");
			expect(observedAuth).toBe("Bearer tok-x");
			expect(observedArgs).toEqual({ q: "Sarah" });
		} finally {
			srv.close();
		}
	});

	test("tools.call wraps -32001 in RPCError", async () => {
		const srv = startMockMCP(() => ({
			error: {
				code: -32001,
				message: "permission denied",
				data: { capability: "memory.read" },
			},
		}));
		try {
			const client = createClient({ endpoint: srv.endpoint, token: "t" });
			let caught: unknown = null;
			try {
				await client.tools.call("memory.query");
			} catch (err) {
				caught = err;
			}
			expect(caught).toBeInstanceOf(RPCError);
			expect((caught as RPCError).code).toBe(-32001);
		} finally {
			srv.close();
		}
	});

	test("tools.call maps -32002 to ApprovalRequiredError with details", async () => {
		const srv = startMockMCP(() => ({
			error: {
				code: -32002,
				message: "user approval required",
				data: {
					approval_id: "appr_01H...",
					capability: "email.send",
				},
			},
		}));
		try {
			const client = createClient({ endpoint: srv.endpoint, token: "t" });
			let caught: unknown = null;
			try {
				await client.tools.call("email.send");
			} catch (err) {
				caught = err;
			}
			expect(caught).toBeInstanceOf(ApprovalRequiredError);
			expect((caught as ApprovalRequiredError).approvalId).toBe(
				"appr_01H...",
			);
			expect((caught as ApprovalRequiredError).capability).toBe(
				"email.send",
			);
		} finally {
			srv.close();
		}
	});

	test("HTTP 401 becomes AuthorizationError", async () => {
		const server = Bun.serve({
			port: 0,
			fetch: () => new Response("invalid token", { status: 401 }),
		});
		try {
			const client = createClient({
				endpoint: `http://${server.hostname}:${server.port}/mcp`,
				token: "stale",
			});
			let caught: unknown = null;
			try {
				await client.tools.list();
			} catch (err) {
				caught = err;
			}
			expect(caught).toBeInstanceOf(AuthorizationError);
		} finally {
			server.stop();
		}
	});
});

describe("createClient — resources", () => {
	test("resources.list returns descriptors", async () => {
		const srv = startMockMCP((method) => {
			if (method === "resources/list") {
				return {
					result: {
						resources: [
							{ uri: "memory://entries", name: "Memory" },
							{ uri: "audit://entries", name: "Audit" },
						],
					},
				};
			}
			return { error: { code: -32601, message: "unknown" } };
		});
		try {
			const client = createClient({ endpoint: srv.endpoint, token: "t" });
			const res = await client.resources.list();
			expect(res.length).toBe(2);
			expect(res[0]?.uri).toBe("memory://entries");
		} finally {
			srv.close();
		}
	});

	test("resources.read passes the uri", async () => {
		let observedUri = "";
		const srv = startMockMCP((method, params) => {
			if (method === "resources/read") {
				observedUri = (params as { uri: string }).uri;
				return {
					result: {
						contents: [
							{
								uri: observedUri,
								mimeType: "application/json",
								text: '{"k":"v"}',
							},
						],
					},
				};
			}
			return { error: { code: -32601, message: "" } };
		});
		try {
			const client = createClient({ endpoint: srv.endpoint, token: "t" });
			const res = await client.resources.read("memory://entries/x");
			expect(observedUri).toBe("memory://entries/x");
			expect(res.contents[0]?.text).toBe('{"k":"v"}');
		} finally {
			srv.close();
		}
	});
});

describe("createClient — SSE subscribe", () => {
	test("reads hello + ping events from the stream", async () => {
		const sseBody =
			"event: hello\n" +
			"data: {\"server\":\"loamss\",\"version\":\"v0.1\",\"protocolVersion\":\"2025-03-26\"}\n" +
			"\n" +
			"event: ping\n" +
			"data: {\"timestamp\":\"2026-05-24T12:00:00Z\"}\n" +
			"\n";
		const srv = startMockMCP(
			() => ({ error: { code: -32601, message: "no rpc" } }),
			() =>
				new Response(sseBody, {
					headers: { "Content-Type": "text/event-stream" },
				}),
		);
		try {
			const client = createClient({ endpoint: srv.endpoint, token: "t" });
			const events: { event: string; data: unknown }[] = [];
			for await (const ev of client.subscribe()) {
				events.push(ev);
				if (events.length >= 2) break;
			}
			expect(events.length).toBe(2);
			expect(events[0]?.event).toBe("hello");
			expect((events[0]?.data as { server: string }).server).toBe("loamss");
			expect(events[1]?.event).toBe("ping");
		} finally {
			srv.close();
		}
	});

	test("subscribe with 401 throws AuthorizationError", async () => {
		const server = Bun.serve({
			port: 0,
			fetch: () => new Response("nope", { status: 401 }),
		});
		try {
			const client = createClient({
				endpoint: `http://${server.hostname}:${server.port}/mcp`,
				token: "stale",
			});
			let caught: unknown = null;
			try {
				for await (const _ of client.subscribe()) {
					break;
				}
			} catch (err) {
				caught = err;
			}
			expect(caught).toBeInstanceOf(AuthorizationError);
		} finally {
			server.stop();
		}
	});
});

describe("createClient — call (low-level)", () => {
	test("forwards arbitrary methods", async () => {
		let observedMethod = "";
		const srv = startMockMCP((method) => {
			observedMethod = method;
			return { result: { ok: true } };
		});
		try {
			const client = createClient({ endpoint: srv.endpoint, token: "t" });
			const res = await client.call<{ ok: boolean }>("audit/tail", {
				limit: 10,
			});
			expect(observedMethod).toBe("audit/tail");
			expect(res.ok).toBe(true);
		} finally {
			srv.close();
		}
	});
});
