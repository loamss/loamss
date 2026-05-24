/**
 * In-memory ReadableStream/WritableStream pair for tests.
 *
 * `bun test` has a known issue with TransformStream (works in
 * standalone Bun, hangs in the test runner). This module provides a
 * minimal controller-backed pipe that behaves the same way and works
 * in both environments.
 */

export interface Pipe {
	readable: ReadableStream<Uint8Array>;
	writable: WritableStream<Uint8Array>;
}

export function makePipe(): Pipe {
	let controller: ReadableStreamDefaultController<Uint8Array> | undefined;
	const readable = new ReadableStream<Uint8Array>({
		start(c) {
			controller = c;
		},
	});
	const writable = new WritableStream<Uint8Array>({
		write(chunk) {
			controller?.enqueue(chunk);
		},
		close() {
			controller?.close();
		},
		abort(reason) {
			controller?.error(reason);
		},
	});
	return { readable, writable };
}
