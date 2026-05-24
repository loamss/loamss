/**
 * Pair an external app with a user's Loamss runtime.
 *
 * The flow:
 *   1. User runs `loamss client pair --name "<app name>"` and shares
 *      the one-time code with the app (manual paste, QR scan, etc.).
 *   2. The app POSTs the code to /pair on the user's runtime.
 *   3. The runtime returns a bearer token. The app persists it and
 *      uses it on every subsequent MCP request.
 *
 * The token is returned EXACTLY ONCE. The app is responsible for
 * persisting it immediately; there is no recovery if it's lost.
 *
 * The runtime never sees the app's own user accounts or credentials.
 * From its perspective, the app is a single principal identified by
 * `client.id`. The app sees only the bearer token and the runtime's
 * MCP endpoint URL.
 */

/** Wire shape of the persisted Client record. Matches `permission.Client`. */
export interface PairedClient {
	id: string;
	name: string;
	created_at: string;
	revoked_at?: string;
	metadata?: Record<string, unknown>;
}

export interface PairOptions {
	/**
	 * Arbitrary metadata to attach to the Client record. Useful for
	 * later auditing ("which version of my app paired in March?").
	 * The runtime stamps `paired_via: "http"` into this map; an
	 * `app_version` or similar from the caller is also reasonable.
	 */
	metadata?: Record<string, unknown>;

	/**
	 * Override the fetch implementation. Defaults to global fetch.
	 * Tests pass a mock; non-browser environments without a global
	 * fetch can pass `node-fetch` or similar.
	 */
	fetch?: typeof fetch;

	/** AbortSignal for the pairing request. */
	signal?: AbortSignal;
}

export interface PairResult {
	/** The persisted Client record. */
	client: PairedClient;
	/** Bearer token — shown exactly once. PERSIST IMMEDIATELY. */
	token: string;
	/** Where to send subsequent MCP requests (typically `<endpoint>/mcp`). */
	endpointUrl: string;
}

/**
 * Pair the app with a Loamss runtime by redeeming a one-time code.
 *
 * @param endpoint  Base URL of the runtime (e.g. "http://127.0.0.1:7777")
 * @param code      The one-time code from `loamss client pair`
 * @param opts      Optional metadata, fetch override, abort signal
 *
 * @throws {Error} If the code is unknown, expired, or already
 *   redeemed, or if the endpoint is unreachable.
 */
export async function pair(
	endpoint: string,
	code: string,
	opts: PairOptions = {},
): Promise<PairResult> {
	const trimmed = endpoint.replace(/\/+$/, "");
	const fetchFn = opts.fetch ?? globalThis.fetch;
	const resp = await fetchFn(`${trimmed}/pair`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({
			code,
			...(opts.metadata !== undefined ? { metadata: opts.metadata } : {}),
		}),
		...(opts.signal ? { signal: opts.signal } : {}),
	});

	const bodyText = await resp.text();
	let body: unknown;
	try {
		body = bodyText ? JSON.parse(bodyText) : {};
	} catch {
		throw new Error(
			`pair: non-JSON response from ${trimmed}/pair (status ${resp.status}): ${bodyText.slice(0, 200)}`,
		);
	}

	if (!resp.ok) {
		const message =
			(body as { error?: string }).error ??
			`HTTP ${resp.status} ${resp.statusText}`;
		throw new Error(`pair: ${message}`);
	}

	const ok = body as {
		client?: PairedClient;
		token?: string;
		endpoint_url?: string;
	};
	if (!ok.client || !ok.token || !ok.endpoint_url) {
		throw new Error(
			`pair: malformed response (missing client/token/endpoint_url): ${JSON.stringify(body).slice(0, 200)}`,
		);
	}
	return {
		client: ok.client,
		token: ok.token,
		endpointUrl: ok.endpoint_url,
	};
}
