/**
 * SSE parser unit tests. The client tests cover the full HTTP+SSE
 * loop; these tests focus on the parser in isolation: framing,
 * blank-line termination, missing data, CRLF handling.
 */

import { describe, expect, test } from "bun:test";
import { parseSSE } from "../src/sse.js";

function streamOf(s: string): ReadableStream<Uint8Array> {
	const enc = new TextEncoder();
	return new ReadableStream<Uint8Array>({
		start(controller) {
			controller.enqueue(enc.encode(s));
			controller.close();
		},
	});
}

async function collect(
	iter: AsyncIterable<{ event: string; data: unknown }>,
): Promise<{ event: string; data: unknown }[]> {
	const out: { event: string; data: unknown }[] = [];
	for await (const ev of iter) out.push(ev);
	return out;
}

describe("parseSSE", () => {
	test("parses a sequence of events", async () => {
		const body =
			"event: hello\n" +
			"data: {\"server\":\"loamss\"}\n" +
			"\n" +
			"event: ping\n" +
			"data: {\"timestamp\":\"2026-05-24T12:00:00Z\"}\n" +
			"\n";
		const events = await collect(parseSSE(streamOf(body)));
		expect(events.length).toBe(2);
		expect(events[0]?.event).toBe("hello");
		expect((events[0]?.data as { server: string }).server).toBe("loamss");
	});

	test("handles CRLF line endings", async () => {
		const body =
			"event: hello\r\ndata: {\"a\":1}\r\n\r\nevent: ping\r\ndata: {}\r\n\r\n";
		const events = await collect(parseSSE(streamOf(body)));
		expect(events.length).toBe(2);
		expect((events[0]?.data as { a: number }).a).toBe(1);
	});

	test("skips events with no data line", async () => {
		const body = "event: empty\n\nevent: real\ndata: {\"ok\":true}\n\n";
		const events = await collect(parseSSE(streamOf(body)));
		expect(events.length).toBe(1);
		expect(events[0]?.event).toBe("real");
	});

	test("falls back to string when data isn't JSON", async () => {
		const body = "event: text\ndata: hello world\n\n";
		const events = await collect(parseSSE(streamOf(body)));
		expect(events[0]?.data).toBe("hello world");
	});

	test("handles split chunks (data arriving mid-line)", async () => {
		const chunks = ["event: a\nda", 'ta: {"x":', "1}\n\n"];
		const enc = new TextEncoder();
		const stream = new ReadableStream<Uint8Array>({
			start(controller) {
				for (const c of chunks) controller.enqueue(enc.encode(c));
				controller.close();
			},
		});
		const events = await collect(parseSSE(stream));
		expect(events.length).toBe(1);
		expect((events[0]?.data as { x: number }).x).toBe(1);
	});
});
