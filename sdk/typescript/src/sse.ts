/**
 * Server-Sent Events parser as an async iterator.
 *
 * The Loamss runtime serves a GET /mcp endpoint as `text/event-stream`
 * with a `hello` event on connect and a `ping` heartbeat every 15s.
 * Future events (resources/updated, log notifications) layer on the
 * same stream.
 *
 * This parser deliberately handles only the subset of the SSE spec
 * the Loamss runtime emits:
 *   - lines starting with `event: <name>`
 *   - lines starting with `data: <json>`
 *   - blank line terminates an event
 *
 * It does NOT implement reconnection with `Last-Event-ID` (the
 * runtime doesn't backfill on reconnect — clients re-fetch state
 * from /mcp instead). It does NOT implement comment lines (`: text`)
 * because the runtime emits none.
 *
 * Errors during streaming (network, server) propagate by ending the
 * iterator. Callers should wrap the for-await loop in a try/catch
 * if they want to distinguish clean close from error.
 */

export interface SSEEvent {
	event: string;
	/** JSON-decoded payload from the `data:` line(s). */
	data: unknown;
}

/**
 * Parse an SSE stream into an async iterable of decoded events.
 *
 * Each yielded SSEEvent has a non-empty `event` name and JSON-decoded
 * data. Events without a `data` line are skipped (the runtime never
 * sends them, but be defensive).
 */
export async function* parseSSE(
	stream: ReadableStream<Uint8Array>,
): AsyncIterable<SSEEvent> {
	const decoder = new TextDecoder();
	const reader = stream.getReader();
	let buffer = "";
	let pendingEvent = "";
	let pendingData: string[] = [];

	try {
		while (true) {
			const { done, value } = await reader.read();
			if (done) break;
			buffer += decoder.decode(value, { stream: true });

			let nl: number;
			while ((nl = buffer.indexOf("\n")) !== -1) {
				const line = buffer.slice(0, nl).replace(/\r$/, "");
				buffer = buffer.slice(nl + 1);

				if (line === "") {
					// Empty line = event terminator.
					if (pendingEvent && pendingData.length > 0) {
						const ev = pendingEvent;
						const raw = pendingData.join("\n");
						pendingEvent = "";
						pendingData = [];
						let data: unknown;
						try {
							data = JSON.parse(raw);
						} catch {
							// Non-JSON data: yield as string.
							data = raw;
						}
						yield { event: ev, data };
					} else {
						pendingEvent = "";
						pendingData = [];
					}
					continue;
				}

				if (line.startsWith("event:")) {
					pendingEvent = line.slice(6).trim();
				} else if (line.startsWith("data:")) {
					pendingData.push(line.slice(5).trim());
				}
				// Other prefixes (id:, retry:, comment lines starting with
				// ":") are ignored — the runtime doesn't emit them.
			}
		}
	} finally {
		try {
			reader.releaseLock();
		} catch {
			// already released
		}
	}
}
