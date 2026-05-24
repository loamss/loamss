/**
 * Error types used by the MCP client surface. Capsule-side errors
 * (RPCError) live in jsonrpc.ts; this file adds the client-specific
 * subclasses that carry richer context for the two error modes apps
 * actually have to branch on:
 *
 *   - The runtime rejected the bearer token (401)        → AuthorizationError
 *   - A tool requires user approval before proceeding    → ApprovalRequiredError
 *
 * Other -32xxx codes flow back as plain RPCError instances.
 */

import { RPCError } from "./jsonrpc.js";

/**
 * Thrown when the runtime returns HTTP 401 — typically because the
 * bearer token was revoked, expired, or never valid. The app should
 * surface this to the user and re-pair.
 */
export class AuthorizationError extends Error {
	constructor(message: string) {
		super(message);
		this.name = "AuthorizationError";
	}
}

/**
 * Thrown when a `tools/call` returned the runtime's
 * `approval_required` code (-32002). The app should either prompt the
 * user via the console (running `loamss approve`) or poll for the
 * approval state via the audit / approval tools.
 *
 * Carries the approval id + capability so callers don't have to dig
 * them out of the JSON-RPC `data` payload.
 */
export class ApprovalRequiredError extends RPCError {
	readonly approvalId: string;
	readonly capability: string;

	constructor(
		message: string,
		approvalId: string,
		capability: string,
		data?: unknown,
	) {
		super(-32002, message, data);
		this.name = "ApprovalRequiredError";
		this.approvalId = approvalId;
		this.capability = capability;
	}
}
