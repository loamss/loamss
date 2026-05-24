/**
 * Pairing tests. Spin a tiny Bun.serve() to play the role of the
 * runtime's POST /pair endpoint and verify happy + error paths.
 */

import { describe, expect, test } from "bun:test";
import { pair } from "../src/pair.js";

function startMockPairServer(
	respond: (req: Request) => Response | Promise<Response>,
) {
	const server = Bun.serve({ port: 0, fetch: respond });
	const url = `http://${server.hostname}:${server.port}`;
	return {
		url,
		close: () => server.stop(),
	};
}

describe("pair", () => {
	test("happy path returns client + token + endpoint_url", async () => {
		let received: { code?: string; metadata?: Record<string, unknown> } = {};
		const srv = startMockPairServer(async (req) => {
			received = (await req.json()) as typeof received;
			return Response.json({
				client: {
					id: "client-01H",
					name: "test app",
					created_at: "2026-05-24T12:00:00Z",
				},
				token: "lck_client-01H_abc123",
				endpoint_url: `http://${new URL(req.url).host}/mcp`,
			});
		});
		try {
			const out = await pair(srv.url, "1234-5678", {
				metadata: { app_version: "1.0" },
			});
			expect(out.token).toBe("lck_client-01H_abc123");
			expect(out.client.id).toBe("client-01H");
			expect(out.endpointUrl).toContain("/mcp");
			expect(received.code).toBe("1234-5678");
			expect(received.metadata?.app_version).toBe("1.0");
		} finally {
			srv.close();
		}
	});

	test("404 from runtime surfaces as readable error", async () => {
		const srv = startMockPairServer(() =>
			Response.json(
				{ error: "pairing code not found", code: 404 },
				{ status: 404 },
			),
		);
		try {
			await expect(pair(srv.url, "wrong-code")).rejects.toThrow(
				/pairing code not found/,
			);
		} finally {
			srv.close();
		}
	});

	test("malformed response is reported clearly", async () => {
		const srv = startMockPairServer(() => new Response("not json"));
		try {
			await expect(pair(srv.url, "x")).rejects.toThrow(/non-JSON response/);
		} finally {
			srv.close();
		}
	});

	test("missing fields in OK response are caught", async () => {
		const srv = startMockPairServer(() =>
			Response.json({ client: { id: "x" } }), // missing token + endpoint_url
		);
		try {
			await expect(pair(srv.url, "x")).rejects.toThrow(/malformed response/);
		} finally {
			srv.close();
		}
	});

	test("trims trailing slash from endpoint", async () => {
		let path = "";
		const srv = startMockPairServer((req) => {
			path = new URL(req.url).pathname;
			return Response.json({
				client: { id: "c", name: "n", created_at: "t" },
				token: "tok",
				endpoint_url: "http://x/mcp",
			});
		});
		try {
			await pair(`${srv.url}/`, "code");
			expect(path).toBe("/pair");
		} finally {
			srv.close();
		}
	});
});
