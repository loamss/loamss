/**
 * Runtime client for the console.
 *
 * The console talks to the runtime over a small set of HTTP endpoints
 * the runtime already exposes (no auth needed during first-run; the
 * listener is bound to 127.0.0.1):
 *
 *   GET  /version  →  { version, commit, build_date }
 *   GET  /healthz  →  { status, version, ... }
 *   POST /console/init  →  { ok: true, ... }      (NEW; not yet shipped)
 *
 * The first two are read-only probes the wizard runs on mount to
 * answer "is the runtime alive?" The POST endpoint is the wizard's
 * Finish action; it doesn't exist in the runtime yet — when called,
 * we currently fall through to a structured "would have written"
 * preview the user sees on the Done screen.
 *
 * In production all of these would be served from the same origin
 * the console itself is served from (loamss runtime binary embeds
 * the console + serves it under /console/*). For dev the console
 * runs at localhost:3000 and the runtime at localhost:7777; we hit
 * the runtime's URL directly.
 */

// Default runtime base URL. In production (console served from the
// runtime), this resolves to the same origin. In dev it points at
// the runtime's known listen port.
export const DEFAULT_RUNTIME_URL = (() => {
	if (typeof window === "undefined") return "http://127.0.0.1:7777";
	const { protocol, hostname, port } = window.location;
	// Dev: console on 3000, runtime on 7777.
	if (port === "3000") return "http://127.0.0.1:7777";
	// Same-origin (production): use whatever the console was served from.
	return `${protocol}//${hostname}${port ? `:${port}` : ""}`;
})();

/**
 * Health endpoint shape — the only runtime endpoint the wizard
 * needs to probe. /version returns text/plain so we read version
 * off the /healthz envelope which includes it.
 */
export interface HealthInfo {
	status: "ok" | "degraded" | "down";
	version: string;
}

/**
 * Result of pinging the runtime. Returns null if unreachable —
 * the caller surfaces a "runtime isn't running" hint rather than
 * failing the wizard outright.
 */
export interface RuntimeProbe {
	health: HealthInfo;
	probedAt: string;
}

export async function probeRuntime(
	baseUrl: string = DEFAULT_RUNTIME_URL,
	opts: { signal?: AbortSignal } = {},
): Promise<RuntimeProbe | null> {
	try {
		const resp = await fetch(`${baseUrl}/healthz`, { signal: opts.signal });
		if (!resp.ok) {
			return null;
		}
		const health = (await resp.json()) as HealthInfo;
		return { health, probedAt: new Date().toISOString() };
	} catch {
		// Network error / abort / runtime not running.
		return null;
	}
}

/**
 * Probe Ollama directly from the browser. Ollama exposes a /api/tags
 * endpoint that lists installed models; if we get a 200 back, Ollama
 * is reachable. The console hits this directly (rather than going
 * through the runtime) because Ollama may not be configured in the
 * runtime yet — the wizard's whole point is to set that up.
 *
 * Note: Ollama's default config doesn't set CORS headers, so this
 * fetch may fail in the browser even when Ollama IS running. We
 * detect that case (TypeError on fetch) and surface it as "unknown"
 * rather than "not detected" — the user can still proceed and the
 * runtime will validate at config-apply time.
 */
export type OllamaProbeResult =
	| { state: "detected"; models: string[] }
	| { state: "not-detected"; reason: string }
	| { state: "cors-blocked"; reason: string };

export async function probeOllama(
	url: string = "http://localhost:11434",
	opts: { signal?: AbortSignal } = {},
): Promise<OllamaProbeResult> {
	try {
		const resp = await fetch(`${url}/api/tags`, { signal: opts.signal });
		if (!resp.ok) {
			return { state: "not-detected", reason: `HTTP ${resp.status}` };
		}
		const body = (await resp.json()) as { models?: { name: string }[] };
		return {
			state: "detected",
			models: (body.models ?? []).map((m) => m.name),
		};
	} catch (err) {
		// In dev the most common failure is a CORS preflight — Ollama
		// running but not allowing the browser to read its responses.
		// We surface this distinctly from "not running" because the
		// runtime CAN talk to Ollama even when the browser can't.
		const msg = err instanceof Error ? err.message : String(err);
		if (msg.toLowerCase().includes("fetch")) {
			return {
				state: "cors-blocked",
				reason:
					"Browser can't reach Ollama (likely CORS). " +
					"The runtime will still be able to talk to it.",
			};
		}
		return { state: "not-detected", reason: msg };
	}
}

/**
 * The shape we'd POST to /console/init when the wizard finishes.
 * The runtime endpoint doesn't exist yet; the wizard renders this
 * payload as a "what would be written" preview on Done so the user
 * can verify and (in a future commit) apply it.
 */
export interface ConsoleInitPayload {
	storage: {
		adapter: "storage:fs-encrypted" | "storage:s3";
		config: Record<string, unknown>;
	};
	memory: {
		adapter: "memory:sqlite";
		config: Record<string, unknown>;
	};
	models: Array<{
		adapter: "model:anthropic" | "model:ollama";
		config: Record<string, unknown>;
	}>;
	source_intents?: Array<{
		adapter: string;
		name: string;
	}>;
}

/**
 * Attempt to POST the wizard's collected config to the runtime.
 * Today the endpoint doesn't exist (returns 404 or 405); we treat
 * any non-2xx as "would-be-written" rather than a hard failure.
 * Returns true if the runtime accepted the config, false otherwise.
 */
export async function applyConsoleInit(
	payload: ConsoleInitPayload,
	baseUrl: string = DEFAULT_RUNTIME_URL,
	opts: { signal?: AbortSignal } = {},
): Promise<{ ok: boolean; reason?: string }> {
	try {
		const resp = await fetch(`${baseUrl}/console/init`, {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify(payload),
			signal: opts.signal,
		});
		if (resp.ok) {
			return { ok: true };
		}
		return {
			ok: false,
			reason: `Runtime returned ${resp.status} — /console/init is not yet implemented.`,
		};
	} catch (err) {
		return {
			ok: false,
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}
