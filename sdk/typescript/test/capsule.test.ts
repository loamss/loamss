/**
 * Capsule lifecycle tests. Drive a capsule built with createCapsule
 * by piping JSON-RPC frames through in-memory streams the way the
 * runtime would over a real subprocess.
 */

import { describe, expect, test } from "bun:test";
import { createCapsule, defineTool, PROTOCOL_VERSION } from "../src/index.js";
import { makePipe } from "./pipe.js";

/**
 * Build a capsule with in-memory streams and return a small "drive
 * the runtime side" harness: `send(frame)` writes a JSON-RPC line
 * to the capsule's stdin, `next()` reads the next JSON line off
 * the capsule's stdout.
 */
function harness() {
	const stdin = makePipe();
	const stdout = makePipe();
	const enc = new TextEncoder();
	const dec = new TextDecoder();

	const handle = createCapsule({
		manifest: {
			name: "test-capsule",
			version: "0.1.0",
			author: { name: "tests" },
		},
		tools: [
			defineTool({
				name: "echo",
				description: "Echo input",
				inputSchema: { type: "object" },
				handler: (input) => ({ echoed: input }),
			}),
			defineTool({
				name: "greet",
				description: "Greet someone",
				inputSchema: {
					type: "object",
					properties: { who: { type: "string" } },
				},
				handler: ({ who }: { who: string }) => `hello ${who}`,
			}),
		],
		streams: { input: stdin.readable, output: stdout.writable },
	});

	const started = handle.start();

	const inputWriter = stdin.writable.getWriter();
	const outputReader = stdout.readable.getReader();
	let buf = "";

	return {
		handle,
		started,
		async send(frame: object) {
			await inputWriter.write(enc.encode(`${JSON.stringify(frame)}\n`));
		},
		async next(): Promise<unknown> {
			// Read until we have at least one newline.
			while (buf.indexOf("\n") === -1) {
				const { done, value } = await outputReader.read();
				if (done) {
					throw new Error("stdout closed");
				}
				buf += dec.decode(value, { stream: true });
			}
			const nl = buf.indexOf("\n");
			const line = buf.slice(0, nl);
			buf = buf.slice(nl + 1);
			return JSON.parse(line);
		},
		async stop() {
			await inputWriter.close().catch(() => {});
			await started;
		},
	};
}

describe("createCapsule", () => {
	test("responds to initialize with protocol version + server info", async () => {
		const h = harness();
		await h.send({ jsonrpc: "2.0", id: 1, method: "initialize" });
		const resp = (await h.next()) as {
			id: number;
			result: { protocolVersion: string; serverInfo: { name: string } };
		};
		expect(resp.id).toBe(1);
		expect(resp.result.protocolVersion).toBe(PROTOCOL_VERSION);
		expect(resp.result.serverInfo.name).toBe("test-capsule");
		await h.stop();
	});

	test("responds to tools/list with registered tools", async () => {
		const h = harness();
		await h.send({ jsonrpc: "2.0", id: 1, method: "initialize" });
		await h.next();
		await h.send({ jsonrpc: "2.0", id: 2, method: "tools/list" });
		const resp = (await h.next()) as {
			result: { tools: Array<{ name: string }> };
		};
		const names = resp.result.tools.map((t) => t.name).sort();
		expect(names).toEqual(["echo", "greet"]);
		await h.stop();
	});

	test("dispatches tools/call with arguments + wraps result", async () => {
		const h = harness();
		await h.send({ jsonrpc: "2.0", id: 1, method: "initialize" });
		await h.next();
		await h.send({
			jsonrpc: "2.0",
			id: 2,
			method: "tools/call",
			params: { name: "greet", arguments: { who: "world" } },
		});
		const resp = (await h.next()) as {
			result: { content: Array<{ type: string; text: string }> };
		};
		expect(resp.result.content[0]?.type).toBe("text");
		expect(resp.result.content[0]?.text).toBe("hello world");
		await h.stop();
	});

	test("returns -32003 for unknown tool", async () => {
		const h = harness();
		await h.send({ jsonrpc: "2.0", id: 1, method: "initialize" });
		await h.next();
		await h.send({
			jsonrpc: "2.0",
			id: 2,
			method: "tools/call",
			params: { name: "missing" },
		});
		const resp = (await h.next()) as { error: { code: number } };
		expect(resp.error.code).toBe(-32003);
		await h.stop();
	});

	test("returns -32601 for unknown method", async () => {
		const h = harness();
		await h.send({ jsonrpc: "2.0", id: 1, method: "nonsense" });
		const resp = (await h.next()) as { error: { code: number } };
		expect(resp.error.code).toBe(-32601);
		await h.stop();
	});

	test("rejects duplicate tool names at construction", () => {
		expect(() =>
			createCapsule({
				manifest: { name: "x", version: "0.0.1", author: { name: "y" } },
				tools: [
					defineTool({
						name: "dup",
						description: "",
						inputSchema: {},
						handler: () => null,
					}),
					defineTool({
						name: "dup",
						description: "",
						inputSchema: {},
						handler: () => null,
					}),
				],
			}),
		).toThrow(/duplicate tool/);
	});
});
