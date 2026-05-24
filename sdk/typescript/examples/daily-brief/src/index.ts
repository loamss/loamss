/**
 * Daily Briefing — reference capsule.
 *
 * Exposes one tool, `brief`. When invoked it:
 *
 *   1. Lists recent threads via runtime.tools.call("threads.list")
 *   2. Walks each thread's entries via threads.entries
 *   3. Pulls each entry's content via memory.show
 *   4. Hands the assembled context to model.call with a brief prompt
 *   5. Returns the generated summary + the threads + entities that
 *      informed it
 *
 * This is the canonical "full loop" — sources → memory layer →
 * capsule → model → output. If any link in that chain breaks, this
 * capsule is the fastest way to notice.
 *
 * Graceful degradation: if model.call returns isError (no generation-
 * capable adapter wired), the capsule still returns a structured
 * "facts only" brief — thread + entity counts without prose summary.
 * The caller learns the runtime isn't fully configured but doesn't
 * lose the read-side work.
 */

// In published form: import { ... } from "@loamss/sdk";
// During in-repo development we import via relative paths so the
// example runs without a workspace install.
import { createCapsule, defineTool } from "../../../src/index.js";

interface BriefInput {
	namespace?: string;
	thread_limit?: number;
	entries_per_thread?: number;
	model_id?: string;
}

interface ThreadSummary {
	id: string;
	subject: string;
	namespace: string;
	entry_count: number;
	last_seen: string;
	participants: string[];
}

interface BriefOutput {
	generated_at: string;
	summary: string;
	source_notes: string;
	threads_considered: ThreadSummary[];
	entities_seen: { canonical: string; entry_count: number }[];
	tokens_used?: { input: number; output: number };
	graceful_degradation?: string;
}

const brief = defineTool<BriefInput, BriefOutput>({
	name: "brief",
	description:
		"Generate today's brief from recent memory. Returns a short " +
		"summary plus the threads and entities that informed it.",
	inputSchema: {
		type: "object",
		properties: {
			namespace: { type: "string" },
			thread_limit: { type: "integer", minimum: 1, maximum: 20 },
			entries_per_thread: { type: "integer", minimum: 1, maximum: 50 },
			model_id: { type: "string" },
		},
		additionalProperties: false,
	},
	handler: async (input, ctx) => {
		const threadLimit = clamp(input.thread_limit ?? 5, 1, 20);
		const entriesPerThread = clamp(input.entries_per_thread ?? 5, 1, 50);
		const namespace = input.namespace?.trim() ?? "";

		// --- 1. Threads -----------------------------------------------
		const threadsArgs: Record<string, unknown> = { limit: threadLimit };
		if (namespace) threadsArgs.namespace = namespace;
		const threadsRes = await ctx.runtime.tools.call(
			"threads.list",
			threadsArgs,
		);
		const threadsPayload = parseToolJSON<{ threads: ThreadRow[] }>(threadsRes);
		const threads = threadsPayload.threads ?? [];

		// --- 2. Entities ----------------------------------------------
		const entitiesArgs: Record<string, unknown> = { limit: 20 };
		if (namespace) entitiesArgs.namespace = namespace;
		const entitiesRes = await ctx.runtime.tools.call(
			"entities.list",
			entitiesArgs,
		);
		const entitiesPayload = parseToolJSON<{ entities: EntityRow[] }>(
			entitiesRes,
		);
		const entities = entitiesPayload.entities ?? [];

		// --- 3. Walk each thread's entries + collect snippets ---------
		const threadSummaries: ThreadSummary[] = [];
		const contextBlocks: string[] = [];

		for (const thread of threads) {
			const entriesRes = await ctx.runtime.tools.call("threads.entries", {
				id: thread.id,
				limit: entriesPerThread,
			});
			const entriesPayload = parseToolJSON<{ entries: EntryRefRow[] }>(
				entriesRes,
			);
			const refs = entriesPayload.entries ?? [];

			// Fetch each entry's content (best effort — memory.show may
			// fail for entries without vectors, which is fine).
			const entryLines: string[] = [];
			const participants = new Set<string>();
			for (const ref of refs.slice(0, entriesPerThread)) {
				try {
					const show = await ctx.runtime.tools.call("memory.show", {
						id: `${ref.namespace}:${ref.id}`,
					});
					const entryPayload = parseToolJSON<{
						id: string;
						metadata?: Record<string, unknown>;
					}>(show);
					const md = entryPayload.metadata ?? {};
					const from = stringField(md, "from");
					const subject = stringField(md, "subject");
					if (from) participants.add(simplifyAddress(from));
					entryLines.push(formatEntryLine(ref, subject, from));
				} catch {
					// memory.show can fail when the entry has no vector
					// (no embedding model configured). The thread/entry
					// ref still tells us it exists.
					entryLines.push(formatEntryLine(ref, "", ""));
				}
			}

			threadSummaries.push({
				id: thread.id,
				subject: thread.subject || "(no subject)",
				namespace: thread.namespace,
				entry_count: thread.entry_count,
				last_seen: thread.last_seen,
				participants: Array.from(participants),
			});

			if (entryLines.length > 0) {
				contextBlocks.push(
					`### Thread: ${thread.subject || "(no subject)"}\n` +
						entryLines.map((l) => `  - ${l}`).join("\n"),
				);
			}
		}

		// --- 4. Ask the model to summarize ---------------------------
		const contextText = contextBlocks.length
			? contextBlocks.join("\n\n")
			: "(no recent activity)";

		const modelArgs: Record<string, unknown> = {
			messages: [
				{
					role: "system",
					content:
						"You are a brief, calm assistant generating a daily briefing " +
						"from the user's recent activity. Three short paragraphs: " +
						"(1) the headline of what's been happening, (2) one or two " +
						"specific items that need attention, (3) anything quiet " +
						"that may need a nudge. No bullet lists. No emoji.",
				},
				{
					role: "user",
					content:
						"Here is the user's recent activity. " +
						"Generate the brief.\n\n" +
						contextText,
				},
			],
			max_tokens: 600,
			temperature: 0.4,
		};
		if (input.model_id) modelArgs.model_id = input.model_id;

		let summary = "";
		let inputTokens = 0;
		let outputTokens = 0;
		let degradation: string | undefined;

		try {
			const modelRes = await ctx.runtime.tools.call("model.call", modelArgs);
			if ("isError" in modelRes && modelRes.isError) {
				degradation =
					"No generation-capable model is configured; the brief " +
					"contains the read-side facts but no prose summary. " +
					"Configure a model under Settings → Models.";
				summary = "(unavailable: no model configured)";
			} else {
				const payload = parseToolJSON<{
					text: string;
					input_tokens?: number;
					output_tokens?: number;
				}>(modelRes);
				summary = payload.text ?? "";
				inputTokens = payload.input_tokens ?? 0;
				outputTokens = payload.output_tokens ?? 0;
			}
		} catch (err) {
			degradation = `model.call failed: ${
				err instanceof Error ? err.message : String(err)
			}`;
			summary = "(generation failed; see graceful_degradation)";
		}

		// --- 5. Return the structured brief --------------------------
		const out: BriefOutput = {
			generated_at: new Date().toISOString(),
			summary,
			source_notes:
				`based on ${threads.length} thread(s) and ${entities.length} ` +
				`entity/entities${namespace ? ` in namespace "${namespace}"` : ""}`,
			threads_considered: threadSummaries,
			entities_seen: entities.slice(0, 10).map((e) => ({
				canonical: e.canonical,
				entry_count: e.entry_count,
			})),
		};
		if (inputTokens || outputTokens) {
			out.tokens_used = { input: inputTokens, output: outputTokens };
		}
		if (degradation) {
			out.graceful_degradation = degradation;
		}
		return out;
	},
});

await createCapsule({
	manifest: {
		name: "daily-brief",
		version: "0.1.0",
		author: { name: "Loamss contributors" },
	},
	tools: [brief],
}).start();

// --- wire types + helpers --------------------------------------------

interface ThreadRow {
	id: string;
	namespace: string;
	external_id: string;
	subject: string;
	entry_count: number;
	last_seen: string;
}

interface EntityRow {
	id: string;
	canonical: string;
	kind: string;
	entry_count: number;
}

interface EntryRefRow {
	namespace: string;
	id: string;
	role?: string;
	date?: string;
}

/**
 * The MCP runtime tools return their payload as a JSON text block;
 * this parses that envelope into the caller's typed shape. Throws
 * if the result has no content or the content isn't valid JSON.
 */
function parseToolJSON<T>(res: {
	content?: Array<{ type?: string; text?: string }>;
}): T {
	if (!res.content || res.content.length === 0) {
		throw new Error("tool returned no content");
	}
	const block = res.content[0];
	if (!block?.text) {
		throw new Error("tool returned non-text content");
	}
	return JSON.parse(block.text) as T;
}

function stringField(m: Record<string, unknown>, key: string): string {
	const v = m[key];
	return typeof v === "string" ? v : "";
}

function simplifyAddress(s: string): string {
	// "Sarah Smith <sarah@example.com>" → "Sarah Smith"
	// "sarah@example.com" → "sarah" (local-part)
	const angle = s.indexOf("<");
	if (angle > 0) return s.slice(0, angle).trim().replace(/^["']|["']$/g, "");
	const at = s.indexOf("@");
	if (at > 0) return s.slice(0, at);
	return s.trim();
}

function formatEntryLine(ref: EntryRefRow, subject: string, from: string): string {
	const parts = [ref.id];
	if (ref.role) parts.push(`[${ref.role}]`);
	if (from) parts.push(`from ${simplifyAddress(from)}`);
	if (subject) parts.push(`"${subject}"`);
	if (ref.date) parts.push(`@ ${ref.date.slice(0, 16).replace("T", " ")}`);
	return parts.join(" ");
}

function clamp(v: number, lo: number, hi: number): number {
	if (v < lo) return lo;
	if (v > hi) return hi;
	return v;
}

