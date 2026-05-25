/**
 * approval-demo — reference capsule for the runtime's user-approval
 * flow.
 *
 * Every call to `peek` attempts a runtime tool call (threads.list)
 * that requires user approval (per the manifest's
 * requires_user_approval flag). The runtime returns a JSON-RPC
 * -32002 error carrying the approval id; this capsule catches that
 * error and surfaces it as a structured "pending" response instead
 * of letting it bubble up to the caller as a raw error.
 *
 * The contract: `requires_user_approval: true` means *ask on every
 * single call*. The runtime queues a fresh approval row on each
 * engine.Check, regardless of prior resolved approvals. So in this
 * demo every invocation of peek returns `{ status: "pending" }` —
 * the dashboard's Approvals pane fills up over time, each approval
 * is decided independently, the audit log captures the lot.
 *
 * To demonstrate the "approval → underlying call succeeds" round-
 * trip in one tool invocation, a capsule would need a wait-and-
 * retry pattern: catch -32002, subscribe to the approval's state,
 * resume when it's resolved. That requires either an MCP-side
 * approval.wait tool (the runtime's WaitForApproval helper isn't
 * exposed via MCP yet) or subscribe-stream plumbing. Future work;
 * see the README's "wait-and-retry" section.
 */

// In published form: import { ... } from "@loamss/sdk";
// During in-repo development we import via relative paths so the
// example runs without a workspace install.
import { createCapsule, defineTool, RPCError } from "../../../src/index.js";

interface PeekInput {
	limit?: number;
}

interface ThreadRow {
	id: string;
	namespace: string;
	subject: string;
	entry_count: number;
	last_seen: string;
}

interface PeekOutput {
	status: "approved" | "pending";
	approval_id?: string;
	hint?: string;
	threads?: ThreadRow[];
}

const peek = defineTool<PeekInput, PeekOutput>({
	name: "peek",
	description:
		"List the user's recent threads. The runtime queues an approval " +
		"on every invocation; this tool surfaces the pending state cleanly " +
		"so the caller knows what to do next.",
	inputSchema: {
		type: "object",
		properties: {
			limit: {
				type: "integer",
				minimum: 1,
				maximum: 20,
				description: "How many threads to peek at. Default 5.",
			},
		},
		additionalProperties: false,
	},
	async handler(input, ctx) {
		const limit = input.limit ?? 5;

		try {
			const res = await ctx.runtime.tools.call("threads.list", { limit });
			// threads.list returns content[0] as a JSON text block. Parse it
			// and reshape into our cleaner output structure.
			const text = res.content?.[0];
			if (!text || text.type !== "text") {
				return {
					status: "approved",
					threads: [],
					hint: "threads.list returned no content block; nothing recent yet.",
				};
			}
			const payload = JSON.parse(text.text) as { threads?: ThreadRow[] };
			return {
				status: "approved",
				threads: payload.threads ?? [],
			};
		} catch (err) {
			// -32002 is the runtime's "user approval required" code. The
			// payload's data block carries the approval_id the dashboard
			// already shows in its Approvals pane.
			if (err instanceof RPCError && err.code === -32002) {
				const data = err.data as { approval_id?: string } | undefined;
				return {
					status: "pending",
					approval_id: data?.approval_id,
					hint:
						"This capsule's memory.read permission requires user " +
						"approval on every invocation. Open the Loamss dashboard, " +
						"approve the request in the Approvals pane, then call " +
						"peek again.",
				};
			}
			// Other errors are real failures — re-throw so the runtime's
			// tool dispatcher returns a proper JSON-RPC error response.
			throw err;
		}
	},
});

await createCapsule({
	manifest: {
		spec_version: "0.1",
		name: "approval-demo",
		version: "0.1.0",
	},
	tools: [peek],
}).start();
