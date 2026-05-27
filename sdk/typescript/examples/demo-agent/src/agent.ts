/**
 * Demo agent — an external MCP client with a local Ollama brain.
 *
 * What it shows
 * -------------
 * The agent and Loamss are **separate processes**. The agent speaks
 * MCP to Loamss exactly the way Claude Desktop, ChatGPT, or Cursor
 * would — bearer-token auth, JSON-RPC, every call checked against
 * the user's grants. The agent has no privileged access; if a
 * capability isn't granted, Loamss refuses and the agent has to
 * cope with that gracefully.
 *
 * Two scenarios:
 *
 *   bun src/agent.ts "what did Sarah want?"
 *     1. tools.list — print what the user granted us.
 *     2. memory.query — ALLOWED (memory.read grant present).
 *     3. Hand the hits to Ollama; print a summary.
 *
 *   bun src/agent.ts --write "I should buy milk"
 *     1. memory.upsert — DENIED (no memory.write grant).
 *     2. Agent reports the denial cleanly: "Loamss didn't grant me
 *        memory.write. Add that grant in the console if you want me
 *        to write."
 *
 * The audit log on the Loamss side records every attempt — allowed
 * or denied. The agent doesn't audit itself; it doesn't have to.
 * The infrastructure does.
 */

import {
	ApprovalRequiredError,
	AuthorizationError,
	createClient,
	RPCError,
} from "@loamss/sdk";
import { readFileSync } from "node:fs";
import { chat } from "./ollama.js";

// --- config ------------------------------------------------------------

const OLLAMA_MODEL = process.env.OLLAMA_MODEL ?? "llama3.2:1b";
const OLLAMA_ENDPOINT = process.env.OLLAMA_ENDPOINT ?? "http://localhost:11434";
const TOKEN_FILE = "./demo-agent.token";

interface StoredToken {
	endpointUrl: string;
	token: string;
	clientId: string;
	clientName: string;
}

// --- CLI parsing -------------------------------------------------------

const args = process.argv.slice(2);
let writeMode = false;
const positional: string[] = [];
for (const a of args) {
	if (a === "--write" || a === "-w") writeMode = true;
	else positional.push(a);
}
const question = positional.join(" ").trim();

if (!question) {
	console.error('usage: bun src/agent.ts [--write] "<question or note>"');
	process.exit(1);
}

// --- load paired token -------------------------------------------------

let stored: StoredToken;
try {
	stored = JSON.parse(readFileSync(TOKEN_FILE, "utf8")) as StoredToken;
} catch {
	console.error(`✗ No token at ${TOKEN_FILE}.`);
	console.error("  Pair first:");
	console.error('    loamss client pair --name "Demo Agent"');
	console.error("    bun src/pair.ts http://127.0.0.1:7777 <CODE>");
	process.exit(1);
}

const loamss = createClient({
	endpoint: stored.endpointUrl,
	token: stored.token,
});

// --- pretty printing ---------------------------------------------------

const tag = {
	agent: "\x1b[36m[agent]\x1b[0m", // cyan
	loamss: "\x1b[35m[loamss]\x1b[0m", // magenta
	ollama: "\x1b[33m[ollama]\x1b[0m", // yellow
	ok: "\x1b[32m✓ ALLOWED\x1b[0m", // green
	deny: "\x1b[31m✗ DENIED\x1b[0m", // red
};

function line(prefix: string, text: string) {
	console.log(`${prefix} ${text}`);
}

// --- run ---------------------------------------------------------------

line(tag.agent, `Connecting as client ${stored.clientId} (${stored.clientName})`);
line(tag.agent, `Loamss endpoint: ${stored.endpointUrl}`);
line(tag.agent, `Brain: Ollama ${OLLAMA_MODEL} at ${OLLAMA_ENDPOINT}`);
console.log("");

// 1. Discover what we can do.
line(tag.agent, "Discovering tools the user granted me...");
try {
	const tools = await loamss.tools.list();
	for (const t of tools.slice(0, 8)) {
		line(tag.loamss, `  ${t.name}${t.description ? ` — ${t.description}` : ""}`);
	}
	if (tools.length > 8) {
		line(tag.loamss, `  ... and ${tools.length - 8} more`);
	}
} catch (err) {
	if (err instanceof AuthorizationError) {
		line(tag.loamss, `${tag.deny}  token rejected — re-pair the agent`);
		process.exit(1);
	}
	throw err;
}
console.log("");

if (writeMode) {
	await runWriteScenario(question);
} else {
	await runReadScenario(question);
}

// --- scenarios ---------------------------------------------------------

async function runReadScenario(q: string) {
	line(tag.agent, `Question: "${q}"`);
	line(tag.agent, `Calling memory.query(query=${JSON.stringify(q)}, limit=3) ...`);

	let hits: MemoryHit[] = [];
	try {
		const out = await loamss.tools.call("memory.query", { query: q, limit: 3 });
		const first = out.content[0];
		if (!first || first.type !== "text") {
			line(tag.loamss, "(empty response)");
			return;
		}
		const parsed = JSON.parse(first.text) as { hits: MemoryHit[] };
		hits = parsed.hits;
		line(tag.loamss, `${tag.ok}  memory.query returned ${hits.length} hits`);
		for (const h of hits) {
			line(tag.loamss, `  ${h.id} (distance ${h.distance.toFixed(3)})`);
		}
	} catch (err) {
		handleDenial(err, "memory.query");
		return;
	}

	if (hits.length === 0) {
		line(tag.agent, "Nothing in your memory matched. Nothing to summarize.");
		return;
	}

	// Pull a snippet from each hit's metadata for the LLM context.
	// In a richer demo we'd fetch the full content; here metadata.path
	// + filename is enough for the model to ground its answer when
	// the underlying content sits in /tmp/loamss-demo/notes.
	const context = await assembleContext(hits);

	console.log("");
	if (process.env.DEBUG_CONTEXT) {
		// Opt-in dump of what the LLM is about to see. Useful when an
		// answer looks wrong and you want to know whether memory.query
		// actually surfaced the right notes.
		line(tag.agent, "Context handed to the LLM:");
		console.log(context);
		console.log("");
	}
	line(tag.agent, `Asking ${OLLAMA_MODEL} to summarize...`);
	const summary = await chat({
		endpoint: OLLAMA_ENDPOINT,
		model: OLLAMA_MODEL,
		messages: [
			{
				role: "system",
				content:
					"You are a concise assistant. You will be given notes and a " +
					"question. Read the notes carefully — the answer is in them. " +
					"Reply with the answer in three sentences or fewer. Do not say " +
					"you lack information; the notes contain what you need.",
			},
			{
				role: "user",
				content: `Notes:\n${context}\n\nQuestion: ${q}\nAnswer:`,
			},
		],
	});
	console.log("");
	line(tag.ollama, summary.trim());
}

async function runWriteScenario(note: string) {
	line(tag.agent, `Note to remember: "${note}"`);
	line(tag.agent, "Calling memory.upsert(...) ...");
	try {
		await loamss.tools.call("memory.upsert", {
			namespace: "agent-notes",
			id: `note-${Date.now()}`,
			content: note,
			metadata: { source: "demo-agent" },
		});
		line(tag.loamss, `${tag.ok}  memory.upsert accepted`);
	} catch (err) {
		handleDenial(err, "memory.upsert");
		console.log("");
		line(
			tag.agent,
			"I can't store anything without your permission. To let me " +
				"write, run:",
		);
		line(
			tag.agent,
			`  loamss grant create --principal-kind client --principal-id ${stored.clientId} \\`,
		);
		line(
			tag.agent,
			"    --capability memory.write --scope-json '{}' --rationale \"agent notes\"",
		);
	}
}

// --- helpers -----------------------------------------------------------

interface MemoryHit {
	id: string;
	distance: number;
	metadata: Record<string, unknown>;
}

function handleDenial(err: unknown, tool: string): void {
	if (err instanceof RPCError && err.code === -32001) {
		const cap =
			((err.data as { capability?: string } | undefined)?.capability) ??
			"(unknown)";
		const reason =
			((err.data as { reason?: string } | undefined)?.reason) ?? err.message;
		line(tag.loamss, `${tag.deny}  ${tool} blocked`);
		line(tag.loamss, `         capability: ${cap}`);
		line(tag.loamss, `         reason:     ${reason}`);
		return;
	}
	if (err instanceof ApprovalRequiredError) {
		line(
			tag.loamss,
			`⏸  approval required for ${tool} (approval_id=${err.approvalId})`,
		);
		return;
	}
	if (err instanceof AuthorizationError) {
		line(tag.loamss, `${tag.deny}  token rejected — re-pair the agent`);
		return;
	}
	if (err instanceof Error) {
		line(tag.loamss, `${tag.deny}  ${tool} failed: ${err.message}`);
		return;
	}
	throw err;
}

/**
 * Build the LLM context from memory.query hits.
 *
 * source:files and source:gmail stash a bounded `snippet` field in
 * each entry's metadata at sync time — enough to ground a summary
 * without a second round-trip. When no snippet is present (older
 * connectors, capsule ingestors that opt not to write one), we fall
 * back to whatever the metadata carries: subject, path, sender, etc.
 *
 * Notably we DO NOT try to read raw file bodies from disk here.
 * The agent's only window into the user's data is the MCP surface;
 * keeping it that way is the entire point of the demo.
 */
async function assembleContext(hits: MemoryHit[]): Promise<string> {
	const blocks: string[] = [];
	for (const h of hits) {
		const snippet = h.metadata.snippet as string | undefined;
		const path = (h.metadata.path as string | undefined) ?? h.id;
		const subject = h.metadata.subject as string | undefined;

		const header = subject ? `${path} — "${subject}"` : path;
		if (snippet && snippet.trim().length > 0) {
			blocks.push(`--- ${header} ---\n${snippet}`);
		} else {
			blocks.push(`--- ${header} ---\n(no snippet in metadata)`);
		}
	}
	return blocks.join("\n\n");
}
