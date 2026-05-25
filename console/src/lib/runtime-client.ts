/**
 * Runtime client for the console.
 *
 * The console talks to the runtime over a small set of HTTP endpoints
 * the runtime already exposes (no auth needed during first-run; the
 * listener is bound to 127.0.0.1):
 *
 *   GET  /version  →  { version, commit, build_date }
 *   GET  /healthz  →  { status, version, ... }
 *   POST /console/init  →  { ok: true, written_to, next_step, ... }
 *
 * The first two are read-only probes the wizard runs on mount to
 * answer "is the runtime alive?" The POST endpoint is the wizard's
 * Finish action; the runtime atomically writes the wizard's payload
 * to a YAML config file at written_to, then asks the user to restart
 * the daemon (next_step) to apply it.
 *
 * In production all of these would be served from the same origin
 * the console itself is served from (loamss runtime binary embeds
 * the console + serves it under /console/*). For dev the console
 * runs at localhost:3000 and the runtime at localhost:7777; we hit
 * the runtime's URL directly.
 */

// Default runtime base URL.
//
// In production the console is embedded inside the runtime binary
// and served from the same origin — every fetch resolves against
// window.location with no cross-origin hop.
//
// In dev (`bun dev` on :3000) the console runs as a separate
// process and has to address the runtime explicitly. We keep a
// small dev-mode hardcoded fallback for that case so contributors
// can iterate on the UI without rebuilding the binary.
//
// SSR (no window) defaults to 127.0.0.1:7777 — Next's static
// export only invokes module code in the browser for our app, but
// the guard is here because `output: "export"` still runs the
// module during the build step.
export const DEFAULT_RUNTIME_URL = (() => {
	if (typeof window === "undefined") return "http://127.0.0.1:7777";
	const { protocol, hostname, port } = window.location;
	// Same-origin path: the production-embedded console and any
	// future production deployments both fall through here.
	if (port !== "3000") {
		return `${protocol}//${hostname}${port ? `:${port}` : ""}`;
	}
	// Dev fallback: console on :3000, runtime on :7777.
	return "http://127.0.0.1:7777";
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
 * The shape posted to /console/init when the wizard finishes. The
 * runtime persists this to ~/.loamss/config.yaml (or wherever the
 * running daemon was launched with --config). The write is atomic;
 * a re-run hits 409 unless the user opts into overwrite.
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
 * Result of a successful POST to /console/init. The shape mirrors
 * the runtime's response so the Done screen can show the user
 * exactly where the config landed and what to do next.
 */
export interface ConsoleInitOk {
	ok: true;
	writtenTo: string;
	nextStep: string;
	capability: {
		writesConfigFile: boolean;
		restartsRuntime: boolean;
		createsPairedConsole: boolean;
		addsConfiguredSources: boolean;
	};
}

/**
 * Result when the runtime refuses to overwrite an existing config
 * file. The console can offer a "back up the old file and write
 * the new one" affordance, which retries with overwrite=true.
 */
export interface ConsoleInitConflict {
	ok: false;
	kind: "conflict";
	path: string;
	hint: string;
}

/**
 * Catch-all failure (network, 4xx, 5xx other than 409). The console
 * shows `reason` verbatim in the Done screen's warn note.
 */
export interface ConsoleInitError {
	ok: false;
	kind: "error";
	status?: number;
	reason: string;
}

export type ConsoleInitResult =
	| ConsoleInitOk
	| ConsoleInitConflict
	| ConsoleInitError;

/**
 * Generate a pairing code so an external client (Claude Desktop,
 * ChatGPT, a custom MCP tool) can redeem it via the runtime's
 * /pair endpoint and become a paired Client row.
 *
 * The runtime caps the TTL at 1 hour from the dashboard; pass a
 * `ttlSeconds` to request a shorter window. Empty TTL → engine
 * default (10 minutes today).
 */
export type CreatePairingCodeResult =
	| {
			ok: true;
			code: string;
			clientName: string;
			expiresAt: string;
	  }
	| { ok: false; reason: string };

export async function createPairingCode(
	clientName: string,
	opts: { ttlSeconds?: number; baseUrl?: string; signal?: AbortSignal } = {},
): Promise<CreatePairingCodeResult> {
	const baseUrl = opts.baseUrl ?? DEFAULT_RUNTIME_URL;
	try {
		const resp = await fetch(`${baseUrl}/console/clients/pair`, {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({
				client_name: clientName,
				...(opts.ttlSeconds ? { ttl_seconds: opts.ttlSeconds } : {}),
			}),
			signal: opts.signal,
		});
		if (resp.ok) {
			const body = (await resp.json()) as {
				code: string;
				client_name: string;
				expires_at: string;
			};
			return {
				ok: true,
				code: body.code,
				clientName: body.client_name,
				expiresAt: body.expires_at,
			};
		}
		return { ok: false, reason: await extractError(resp) };
	} catch (err) {
		return {
			ok: false,
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}

/**
 * Revoke a paired client. The row remains in /console/state.clients
 * (so the user can see what was revoked + when) but its bearer
 * token is invalidated and all its grants are revoked.
 */
export type RevokeClientResult =
	| { ok: true }
	| { ok: false; kind: "not-found"; reason: string }
	| { ok: false; kind: "error"; reason: string };

export async function revokeClient(
	id: string,
	opts: { reason?: string; baseUrl?: string; signal?: AbortSignal } = {},
): Promise<RevokeClientResult> {
	const baseUrl = opts.baseUrl ?? DEFAULT_RUNTIME_URL;
	try {
		const resp = await fetch(
			`${baseUrl}/console/clients/${encodeURIComponent(id)}`,
			{
				method: "DELETE",
				headers: { "Content-Type": "application/json" },
				body: opts.reason ? JSON.stringify({ reason: opts.reason }) : "",
				signal: opts.signal,
			},
		);
		if (resp.ok) return { ok: true };
		const reason = await extractError(resp);
		if (resp.status === 404) return { ok: false, kind: "not-found", reason };
		return { ok: false, kind: "error", reason };
	} catch (err) {
		return {
			ok: false,
			kind: "error",
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}

/**
 * Resolve a pending approval. POST /console/approvals/{id}/approve
 * or /deny. The optional `note` field is persisted on the resolved
 * approval and emitted in the audit log.
 *
 * Returns ok=true on success. The "conflict" kind covers the race
 * where two deciders click at once (or a single user double-clicks)
 * — the dashboard should refresh state when it sees one.
 */
export type ResolveApprovalResult =
	| { ok: true; decision: "granted" | "denied" }
	| { ok: false; kind: "conflict"; reason: string }
	| { ok: false; kind: "not-found"; reason: string }
	| { ok: false; kind: "error"; reason: string };

export async function resolveApproval(
	id: string,
	decision: "approve" | "deny",
	opts: { note?: string; baseUrl?: string; signal?: AbortSignal } = {},
): Promise<ResolveApprovalResult> {
	const baseUrl = opts.baseUrl ?? DEFAULT_RUNTIME_URL;
	try {
		const resp = await fetch(
			`${baseUrl}/console/approvals/${encodeURIComponent(id)}/${decision}`,
			{
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: opts.note ? JSON.stringify({ note: opts.note }) : "",
				signal: opts.signal,
			},
		);
		if (resp.ok) {
			const body = (await resp.json()) as { decision: string };
			return {
				ok: true,
				decision: body.decision === "granted" ? "granted" : "denied",
			};
		}
		const reason = await extractError(resp);
		if (resp.status === 409) return { ok: false, kind: "conflict", reason };
		if (resp.status === 404) return { ok: false, kind: "not-found", reason };
		return { ok: false, kind: "error", reason };
	} catch (err) {
		return {
			ok: false,
			kind: "error",
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}

/**
 * Install a capsule from a filesystem path. The runtime parses the
 * manifest, issues permission grants, copies code into <data_dir>/
 * capsules/, and (when the host is wired) starts the subprocess.
 *
 * The 201 response carries the parsed manifest so the dashboard
 * can render a permission-slip review modal immediately after the
 * install completes.
 */
export interface CapsuleManifestSummary {
	name: string;
	version: string;
	description?: string;
	author?: string;
	permissions: Array<{
		capability: string;
		scope?: Record<string, unknown>;
		rationale?: string;
		requires_user_approval: boolean;
	}>;
	tools?: Array<{ name: string; description?: string }>;
}

export type InstallCapsuleResult =
	| {
			ok: true;
			capsule: {
				id: string;
				name: string;
				version: string;
				running: boolean;
			};
			grants: string[];
			manifest: CapsuleManifestSummary;
			note?: string;
	  }
	| { ok: false; kind: "conflict"; reason: string } // already installed
	| { ok: false; kind: "rejected"; reason: string } // bad path / bad manifest
	| { ok: false; kind: "error"; reason: string };

export async function installCapsule(
	path: string,
	opts: { baseUrl?: string; signal?: AbortSignal } = {},
): Promise<InstallCapsuleResult> {
	const baseUrl = opts.baseUrl ?? DEFAULT_RUNTIME_URL;
	try {
		const resp = await fetch(`${baseUrl}/console/capsules`, {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ path }),
			signal: opts.signal,
		});
		if (resp.ok) {
			const body = (await resp.json()) as {
				capsule: {
					id: string;
					name: string;
					version: string;
					running: boolean;
				};
				grants: string[];
				manifest: CapsuleManifestSummary;
				note?: string;
			};
			return {
				ok: true,
				capsule: body.capsule,
				grants: body.grants,
				manifest: body.manifest,
				note: body.note,
			};
		}
		const reason = await extractError(resp);
		if (resp.status === 409) return { ok: false, kind: "conflict", reason };
		if (resp.status === 400) return { ok: false, kind: "rejected", reason };
		return { ok: false, kind: "error", reason };
	} catch (err) {
		return {
			ok: false,
			kind: "error",
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}

export type CapsuleLifecycleResult =
	| { ok: true }
	| { ok: false; kind: "not-found"; reason: string }
	| { ok: false; kind: "error"; reason: string };

export async function startCapsule(
	name: string,
	opts: { baseUrl?: string; signal?: AbortSignal } = {},
): Promise<CapsuleLifecycleResult> {
	return capsuleAction(`/console/capsules/${encodeURIComponent(name)}/start`, opts);
}

export async function stopCapsule(
	name: string,
	opts: { baseUrl?: string; signal?: AbortSignal } = {},
): Promise<CapsuleLifecycleResult> {
	return capsuleAction(`/console/capsules/${encodeURIComponent(name)}/stop`, opts);
}

export async function uninstallCapsule(
	name: string,
	opts: { baseUrl?: string; signal?: AbortSignal } = {},
): Promise<CapsuleLifecycleResult> {
	const baseUrl = opts.baseUrl ?? DEFAULT_RUNTIME_URL;
	try {
		const resp = await fetch(
			`${baseUrl}/console/capsules/${encodeURIComponent(name)}`,
			{ method: "DELETE", signal: opts.signal },
		);
		if (resp.ok) return { ok: true };
		const reason = await extractError(resp);
		if (resp.status === 404) return { ok: false, kind: "not-found", reason };
		return { ok: false, kind: "error", reason };
	} catch (err) {
		return {
			ok: false,
			kind: "error",
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}

async function capsuleAction(
	path: string,
	opts: { baseUrl?: string; signal?: AbortSignal },
): Promise<CapsuleLifecycleResult> {
	const baseUrl = opts.baseUrl ?? DEFAULT_RUNTIME_URL;
	try {
		const resp = await fetch(`${baseUrl}${path}`, {
			method: "POST",
			signal: opts.signal,
		});
		if (resp.ok) return { ok: true };
		const reason = await extractError(resp);
		if (resp.status === 404) return { ok: false, kind: "not-found", reason };
		return { ok: false, kind: "error", reason };
	} catch (err) {
		return {
			ok: false,
			kind: "error",
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}

/**
 * Snapshot returned by GET /console/state. Mirrors the Go-side
 * shape in runtime/internal/server/state.go. Every pane carries
 * an `available` flag so the dashboard tile can distinguish
 * "subsystem not wired in this build" from "wired but empty".
 */
export interface ConsoleState {
	generated_at: string;
	runtime: {
		version: string;
		listen_addr: string;
		data_dir: string;
		started_at: string;
		uptime_seconds: number;
	};
	config: {
		available: boolean;
		storage_adapter?: string;
		memory_adapter?: string;
		model_adapters?: string[];
		// True iff a config file exists at the wizard's target path.
		// The dashboard routes off this — `available` is true the
		// moment the daemon starts (defaults populate every field).
		wizard_complete: boolean;
		wizard_path?: string;
	};
	sources: {
		available: boolean;
		items: Array<{
			id: string;
			name: string;
			adapter: string;
			last_sync_at?: string;
			last_sync_status: string; // "success" | "error" | "running" | ""
			summary?: Record<string, unknown>;
			added_at: string;
		}>;
		error?: string;
	};
	capsules: {
		available: boolean;
		items: Array<{
			id: string;
			name: string;
			version: string;
			author?: string;
			permissions: string[];
			installed_at: string;
			running: boolean;
		}>;
		error?: string;
	};
	clients: {
		available: boolean;
		items: Array<{
			id: string;
			name: string;
			paired_at: string;
			last_seen_at?: string;
			active: boolean;
		}>;
		error?: string;
	};
	approvals_pending: {
		available: boolean;
		items: Array<{
			id: string;
			principal_kind: string;
			principal_id: string;
			capability: string;
			rationale?: string;
			scope?: Record<string, unknown>;
			requested_at: string;
		}>;
		error?: string;
	};
	activity: {
		available: boolean;
		items: Array<{
			id: string;
			at: string;
			type: string;
			actor_kind: string;
			actor_id: string;
			subject_kind?: string;
			subject_id?: string;
			outcome: string; // "success" | "denied" | "error" | "pending" | "n/a"
		}>;
		error?: string;
	};
}

/**
 * Fetch the dashboard snapshot. Returns null when the runtime is
 * unreachable so the caller can render an offline state rather
 * than a crash. Real errors (4xx/5xx from a reachable runtime)
 * also collapse to null with a console.warn — by the time we're
 * past first-run, the dashboard's job is to surface state, not
 * to interpret HTTP semantics.
 */
export async function getConsoleState(
	baseUrl: string = DEFAULT_RUNTIME_URL,
	opts: { signal?: AbortSignal } = {},
): Promise<ConsoleState | null> {
	try {
		const resp = await fetch(`${baseUrl}/console/state`, {
			signal: opts.signal,
		});
		if (!resp.ok) {
			console.warn(`/console/state returned HTTP ${resp.status}`);
			return null;
		}
		return (await resp.json()) as ConsoleState;
	} catch {
		return null;
	}
}

/**
 * Add a source via POST /console/sources. The runtime validates the
 * adapter config by constructing a real source.Source instance and
 * Init'ing it; if Init fails, the inserted row is rolled back and
 * the response is 400.
 *
 * Error shape mirrors the discriminated unions used elsewhere in
 * this file (ConsoleInitResult): callers branch on `ok` and `kind`.
 */
export type AddSourceResult =
	| {
			ok: true;
			source: {
				id: string;
				name: string;
				adapter: string;
				added_at: string;
			};
	  }
	| { ok: false; kind: "conflict"; reason: string } // 409 — name taken
	| { ok: false; kind: "rejected"; reason: string } // 400 — adapter init failed
	| { ok: false; kind: "error"; status?: number; reason: string };

export async function addSource(
	payload: { adapter: string; name: string; config: Record<string, unknown> },
	opts: { baseUrl?: string; signal?: AbortSignal } = {},
): Promise<AddSourceResult> {
	const baseUrl = opts.baseUrl ?? DEFAULT_RUNTIME_URL;
	try {
		const resp = await fetch(`${baseUrl}/console/sources`, {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify(payload),
			signal: opts.signal,
		});
		if (resp.ok) {
			const body = (await resp.json()) as {
				source: { id: string; name: string; adapter: string; added_at: string };
			};
			return { ok: true, source: body.source };
		}
		const reason = await extractError(resp);
		if (resp.status === 409) return { ok: false, kind: "conflict", reason };
		if (resp.status === 400) return { ok: false, kind: "rejected", reason };
		return { ok: false, kind: "error", status: resp.status, reason };
	} catch (err) {
		return {
			ok: false,
			kind: "error",
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}

/**
 * Trigger an async sync. Returns 202 on success; the actual outcome
 * lands in the source's `last_sync_status` field, polled via
 * /console/state.
 */
export type SyncSourceResult =
	| { ok: true }
	| { ok: false; kind: "conflict"; reason: string } // 409 — already running
	| { ok: false; kind: "not-found"; reason: string } // 404
	| { ok: false; kind: "error"; reason: string };

export async function syncSource(
	name: string,
	opts: { baseUrl?: string; signal?: AbortSignal } = {},
): Promise<SyncSourceResult> {
	const baseUrl = opts.baseUrl ?? DEFAULT_RUNTIME_URL;
	try {
		const resp = await fetch(
			`${baseUrl}/console/sources/${encodeURIComponent(name)}/sync`,
			{ method: "POST", signal: opts.signal },
		);
		if (resp.ok) return { ok: true };
		const reason = await extractError(resp);
		if (resp.status === 409) return { ok: false, kind: "conflict", reason };
		if (resp.status === 404) return { ok: false, kind: "not-found", reason };
		return { ok: false, kind: "error", reason };
	} catch (err) {
		return {
			ok: false,
			kind: "error",
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}

/**
 * Remove a source. The runtime also clears the source's per-instance
 * credential blob from storage; orphaned credentials are a minor
 * cleanup issue at worst.
 */
export type DeleteSourceResult =
	| { ok: true }
	| { ok: false; kind: "not-found"; reason: string }
	| { ok: false; kind: "error"; reason: string };

export async function deleteSource(
	name: string,
	opts: { baseUrl?: string; signal?: AbortSignal } = {},
): Promise<DeleteSourceResult> {
	const baseUrl = opts.baseUrl ?? DEFAULT_RUNTIME_URL;
	try {
		const resp = await fetch(
			`${baseUrl}/console/sources/${encodeURIComponent(name)}`,
			{ method: "DELETE", signal: opts.signal },
		);
		if (resp.ok) return { ok: true };
		const reason = await extractError(resp);
		if (resp.status === 404) return { ok: false, kind: "not-found", reason };
		return { ok: false, kind: "error", reason };
	} catch (err) {
		return {
			ok: false,
			kind: "error",
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}

// extractError best-efforts the runtime's JSON error message, falling
// back to a generic "HTTP N" if the response body isn't structured.
async function extractError(resp: Response): Promise<string> {
	try {
		const body = (await resp.json()) as { error?: string };
		if (body.error) return body.error;
	} catch {
		/* not JSON */
	}
	return `HTTP ${resp.status}`;
}

/**
 * POST the wizard's collected config to the runtime. The runtime
 * atomically writes a YAML config file and returns where it landed +
 * a "restart the runtime to apply" hint. Re-runs against an existing
 * file return 409; pass `overwrite: true` to back up the old file
 * and replace it.
 */
export async function applyConsoleInit(
	payload: ConsoleInitPayload,
	opts: {
		baseUrl?: string;
		overwrite?: boolean;
		signal?: AbortSignal;
	} = {},
): Promise<ConsoleInitResult> {
	const baseUrl = opts.baseUrl ?? DEFAULT_RUNTIME_URL;
	const url = opts.overwrite
		? `${baseUrl}/console/init?overwrite=1`
		: `${baseUrl}/console/init`;
	try {
		const resp = await fetch(url, {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify(payload),
			signal: opts.signal,
		});
		if (resp.ok) {
			const body = (await resp.json()) as {
				written_to: string;
				next_step: string;
				capability: {
					writes_config_file: boolean;
					restarts_runtime: boolean;
					creates_paired_console: boolean;
					adds_configured_sources: boolean;
				};
			};
			return {
				ok: true,
				writtenTo: body.written_to,
				nextStep: body.next_step,
				capability: {
					writesConfigFile: body.capability.writes_config_file,
					restartsRuntime: body.capability.restarts_runtime,
					createsPairedConsole: body.capability.creates_paired_console,
					addsConfiguredSources: body.capability.adds_configured_sources,
				},
			};
		}
		if (resp.status === 409) {
			const body = (await resp.json()) as { path: string; hint: string };
			return {
				ok: false,
				kind: "conflict",
				path: body.path,
				hint: body.hint,
			};
		}
		// Best-effort error message from the response body, falling
		// back to the status code if the body isn't JSON.
		let reason = `Runtime returned HTTP ${resp.status}.`;
		try {
			const body = (await resp.json()) as { error?: string };
			if (body.error) reason = body.error;
		} catch {
			/* not JSON, keep the fallback */
		}
		return { ok: false, kind: "error", status: resp.status, reason };
	} catch (err) {
		return {
			ok: false,
			kind: "error",
			reason: err instanceof Error ? err.message : String(err),
		};
	}
}
