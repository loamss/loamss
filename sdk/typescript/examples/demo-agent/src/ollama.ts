/**
 * Minimal Ollama HTTP client — the agent's brain.
 *
 * Why a hand-rolled wrapper instead of a library: the agent stays
 * dep-free except for @loamss/sdk, and Ollama's /api/chat surface is
 * small enough (one POST) that the wrapper is shorter than the
 * import statement of a typical client library.
 *
 * Streaming is intentionally NOT wired here — the demo wants one
 * complete summary block at a time. If you reuse this in your own
 * code and want streaming, set `stream: true` and parse the
 * newline-delimited JSON the endpoint returns.
 */

export interface OllamaMessage {
	role: "system" | "user" | "assistant";
	content: string;
}

export interface OllamaChatOptions {
	/** Ollama base URL. Defaults to http://localhost:11434. */
	endpoint?: string;
	/** Model to run, e.g. "llama3.2:1b". Required. */
	model: string;
	/** Conversation so far. */
	messages: OllamaMessage[];
	/**
	 * Optional temperature override. Lower = more focused; the demo
	 * wants stable summaries, so the default here (0.2) is below
	 * Ollama's typical 0.8.
	 */
	temperature?: number;
	/** Abort signal. */
	signal?: AbortSignal;
}

interface OllamaChatResponse {
	message: { role: "assistant"; content: string };
	done: boolean;
}

/**
 * Send a chat request and return the assistant's reply. Throws on
 * non-200 responses or connection failures — the caller is expected
 * to print a clean error and exit.
 */
export async function chat(opts: OllamaChatOptions): Promise<string> {
	const endpoint = opts.endpoint ?? "http://localhost:11434";
	const url = `${endpoint.replace(/\/$/, "")}/api/chat`;

	const body = {
		model: opts.model,
		messages: opts.messages,
		stream: false,
		options: {
			temperature: opts.temperature ?? 0.2,
		},
	};

	const res = await fetch(url, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify(body),
		signal: opts.signal,
	});

	if (!res.ok) {
		const text = await res.text().catch(() => "");
		throw new Error(
			`ollama /api/chat ${res.status} ${res.statusText}: ${text.slice(0, 300)}`,
		);
	}

	const data = (await res.json()) as OllamaChatResponse;
	return data.message.content;
}
