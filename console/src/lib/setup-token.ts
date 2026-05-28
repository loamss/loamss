/**
 * Setup-token capture + storage for cloud-deployed runtimes.
 *
 * The runtime's setup-token gate (see runtime/internal/server/setuptoken.go)
 * requires every /console/* and /pair request to carry
 * `Authorization: Bearer <token>` when the daemon is running in cloud
 * profile. On laptop installs the gate is inactive and these helpers
 * are no-ops.
 *
 * The token has two entry paths:
 *
 *   1. URL parameter — the operator opens
 *      `https://yourapp.run.app/?setup=<token>` (often by clicking a
 *      link copied from Cloud Run logs). We extract it on first paint,
 *      stash it in localStorage, and strip it from the URL so it's not
 *      visible in the address bar or written to history.
 *
 *   2. Paste box — when an operator lands without a `?setup=` param
 *      (typed the URL by hand, or arrived from a bookmark), the wizard
 *      surfaces a paste field. They paste, we stash, fetches start
 *      working.
 *
 * After /console/init succeeds, the runtime burns the token. The
 * dashboard's next polls will 401 — at that point the operator pairs
 * a real client (Claude Desktop, the CLI, a custom MCP client) and
 * the dashboard switches to using that client's bearer credential.
 * v0.2 ships the setup-token half; the auto-pair-the-console-on-init
 * half is alpha.3.
 *
 * No server round-trip: the gate is per-request enforcement, not
 * per-session. We simply attach the token to every fetch via
 * authHeaders() in runtime-client.ts.
 */

const STORAGE_KEY = "loamss.setup_token";
const URL_PARAM = "setup";

/**
 * captureSetupTokenFromURL extracts the `?setup=<token>` parameter
 * from window.location, stores it, and removes it from the visible
 * URL via history.replaceState. Returns true when a token was found
 * and captured (used by callers that want to show a "Token loaded
 * from URL" toast on first paint).
 *
 * Idempotent — safe to call on every render. After the first call the
 * URL no longer carries the param so subsequent calls are no-ops.
 *
 * SSR-safe: returns false during Next's static export build.
 */
export function captureSetupTokenFromURL(): boolean {
  if (typeof window === "undefined") return false;
  const url = new URL(window.location.href);
  const tok = url.searchParams.get(URL_PARAM);
  if (!tok) return false;

  // Save to localStorage *before* mutating the URL so we don't lose it
  // if history.replaceState throws (older browsers without proper SOP
  // support, sandboxed iframes, etc.).
  try {
    window.localStorage.setItem(STORAGE_KEY, tok);
  } catch {
    // Storage disabled (private mode, quota exceeded). The token is
    // still effectively captured for the current page lifetime via
    // the in-memory cache below; subsequent reloads will lose it.
    inMemoryToken = tok;
  }

  // Strip the param. Use replaceState so we don't add a new history
  // entry — back button shouldn't return to the leaked-token URL.
  url.searchParams.delete(URL_PARAM);
  const cleaned = url.pathname + (url.search || "") + url.hash;
  try {
    window.history.replaceState(window.history.state, "", cleaned);
  } catch {
    // history.replaceState can throw in cross-origin iframes; the
    // token is captured either way, the URL just stays dirty.
  }
  return true;
}

/**
 * In-memory fallback when localStorage is unavailable (private mode,
 * iframe with storage blocked, etc.). Lasts only for the current
 * page lifetime — not great UX on reload, but a strict improvement
 * over "the wizard silently 401s and the user has no recourse".
 */
let inMemoryToken: string | null = null;

/**
 * Read the active setup token. Returns null when no token has been
 * captured. Callers in runtime-client.ts use this to decide whether
 * to attach an Authorization header.
 */
export function getSetupToken(): string | null {
  if (typeof window === "undefined") return null;
  if (inMemoryToken) return inMemoryToken;
  try {
    return window.localStorage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
}

/**
 * Persist a token supplied by the user via the paste box. Same
 * semantics as captureSetupTokenFromURL but driven by an event
 * handler rather than the URL.
 */
export function setSetupToken(token: string): void {
  const t = token.trim();
  if (!t) return;
  inMemoryToken = t;
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(STORAGE_KEY, t);
  } catch {
    // Already mirrored in inMemoryToken — nothing else to do.
  }
}

/**
 * Drop the stored token. Called after a successful /console/init when
 * the runtime has burned the token server-side — keeping a now-useless
 * value around would only generate spurious "this token is invalid"
 * 401s on subsequent pre-pair requests.
 */
export function clearSetupToken(): void {
  inMemoryToken = null;
  if (typeof window === "undefined") return;
  try {
    window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // ignore
  }
}

/**
 * hasSetupToken is a non-null check used by UI components that want
 * to show a "token loaded" indicator without exposing the value
 * itself. Returns false in SSR.
 */
export function hasSetupToken(): boolean {
  return getSetupToken() !== null;
}
