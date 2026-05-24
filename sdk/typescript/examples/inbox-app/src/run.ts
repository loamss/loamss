/**
 * Inbox-app example — minimal Path-B app that pairs with a user's
 * Loamss, lists what tools the user granted, queries memory, and
 * subscribes to live notifications.
 *
 * Prerequisites:
 *   - Loamss runtime running locally (or remotely; the endpoint
 *     comes from the pairing token).
 *   - You ran `bun examples/inbox-app/src/pair.ts <endpoint> <code>`
 *     once to capture a bearer token at ./inbox-app.token.
 *
 * What this app demonstrates:
 *   1. Constructing a LoamssClient from a stored bearer token.
 *   2. Discovering what tools the user granted via tools.list.
 *   3. Querying memory (Gmail-namespace by default; falls back to
 *      whatever's there).
 *   4. Subscribing to the SSE stream and rendering events as they
 *      arrive — a real inbox UI would re-render on
 *      resources/updated events.
 */

// In published form: import { ... } from "@loamss/sdk";
import {
	ApprovalRequiredError,
	AuthorizationError,
	createClient,
	RPCError,
} from "../../../src/index.js";
import { readFileSync } from "node:fs";

interface StoredToken {
	endpointUrl: string;
	token: string;
	clientId: string;
	clientName: string;
}

const tokenJSON = readFileSync("./inbox-app.token", "utf8");
const stored = JSON.parse(tokenJSON) as StoredToken;

const client = createClient({
	endpoint: stored.endpointUrl,
	token: stored.token,
});

console.log(`Connected as client ${stored.clientId} (${stored.clientName})\n`);

// 1. What can we do?
try {
	const tools = await client.tools.list();
	console.log(`Tools available (${tools.length}):`);
	for (const t of tools) {
		console.log(`  - ${t.name}${t.description ? ` — ${t.description}` : ""}`);
	}
	console.log("");
} catch (err) {
	if (err instanceof AuthorizationError) {
		console.error("✗ Token rejected. Re-run pair.ts.");
		process.exit(1);
	}
	throw err;
}

// 2. Try a memory query (if the user granted memory.read).
try {
	const result = await client.tools.call("memory.query", {
		query: "",
		limit: 5,
	});
	console.log("memory.query →", JSON.stringify(result.content[0], null, 2));
} catch (err) {
	if (err instanceof RPCError && err.code === -32001) {
		console.log("memory.query → permission denied (issue a memory.read grant)");
	} else if (err instanceof ApprovalRequiredError) {
		console.log(
			`memory.query → approval required (approval_id=${err.approvalId})`,
		);
	} else if (err instanceof Error) {
		console.log(`memory.query → ${err.message}`);
	}
}

console.log("");

// 3. Subscribe to live events. In a real inbox UI you'd update DOM
// here; for the example we print events for 5 seconds then exit.
console.log("Subscribing to SSE for 5 seconds (Ctrl-C to stop earlier)...");
const ctrl = new AbortController();
const timer = setTimeout(() => ctrl.abort(), 5000);

try {
	for await (const ev of client.subscribe({ signal: ctrl.signal })) {
		console.log(`  [${ev.event}] ${JSON.stringify(ev.data)}`);
	}
} catch (err) {
	if ((err as { name?: string }).name !== "AbortError") {
		throw err;
	}
} finally {
	clearTimeout(timer);
}

console.log("\nDone.");
