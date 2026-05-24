/**
 * Transport-level tests. Build a pair of in-memory pipes, attach
 * a Transport, drive frames through each end, and observe the
 * handler / response routing.
 *
 * Uses {@link makePipe} (controller-backed ReadableStream/WritableStream)
 * instead of TransformStream because `bun test` has a known issue
 * with TransformStream that doesn't reproduce in standalone Bun.
 */

import { describe, expect, test } from "bun:test";
import { ErrorCodes, RPCError } from "../src/jsonrpc.js";
import { type RequestHandler, Transport } from "../src/transport.js";
import { makePipe } from "./pipe.js";

function makePair(handlerA: RequestHandler, handlerB: RequestHandler) {
	const aToB = makePipe();
	const bToA = makePipe();

	const a = new Transport({
		streams: { input: bToA.readable, output: aToB.writable },
		handler: handlerA,
	});
	const b = new Transport({
		streams: { input: aToB.readable, output: bToA.writable },
		handler: handlerB,
	});

	void a.start();
	void b.start();
	return { a, b };
}

describe("Transport", () => {
	test("round-trips a request", async () => {
		const { a, b } = makePair(
			async () => ({ unused: true }),
			async (method, params) => {
				expect(method).toBe("echo");
				return { echoed: (params as { msg: string }).msg };
			},
		);
		const result = await a.request<{ echoed: string }>("echo", { msg: "hi" });
		expect(result.echoed).toBe("hi");
		await a.close();
		await b.close();
	});

	test("surfaces RPCError code + message + data", async () => {
		const { a, b } = makePair(
			async () => null,
			async () => {
				throw new RPCError(ErrorCodes.PermissionDenied, "nope", {
					capability: "x",
				});
			},
		);
		await expect(a.request("denied")).rejects.toMatchObject({
			code: ErrorCodes.PermissionDenied,
			message: "nope",
		});
		await a.close();
		await b.close();
	});

	test("wraps thrown generic Error as InternalError", async () => {
		const { a, b } = makePair(
			async () => null,
			async () => {
				throw new Error("boom");
			},
		);
		await expect(a.request("explode")).rejects.toMatchObject({
			code: ErrorCodes.InternalError,
			message: "boom",
		});
		await a.close();
		await b.close();
	});

	test("supports notifications (no response)", async () => {
		let received = "";
		const { a, b } = makePair(
			async () => null,
			async (method, params) => {
				if (method === "ping") {
					received = (params as { tag: string }).tag;
				}
				return undefined;
			},
		);
		await a.notify("ping", { tag: "first" });
		await new Promise((r) => setTimeout(r, 20));
		expect(received).toBe("first");
		await a.close();
		await b.close();
	});

	test("rejects pending requests when transport closes", async () => {
		const { a, b } = makePair(
			async () => null,
			async () =>
				new Promise<unknown>(() => {
					/* never resolves */
				}),
		);
		const pending = a.request("hang");
		await new Promise((r) => setTimeout(r, 10));
		await a.close();
		await expect(pending).rejects.toThrow(/closed/);
		await b.close();
	});

	test("ignores malformed JSON without crashing", async () => {
		const pipe = makePipe();
		const out = makePipe();
		const enc = new TextEncoder();

		const handlerCalled: string[] = [];
		const t = new Transport({
			streams: { input: pipe.readable, output: out.writable },
			handler: async (method) => {
				handlerCalled.push(method);
				return { ok: true };
			},
		});
		const loop = t.start();

		const writer = pipe.writable.getWriter();
		await writer.write(enc.encode("this is not json\n"));
		await writer.write(
			enc.encode(`{"jsonrpc":"2.0","id":1,"method":"ping"}\n`),
		);
		await writer.close();

		await loop;
		expect(handlerCalled).toEqual(["ping"]);
	});
});
