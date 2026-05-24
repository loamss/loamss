/**
 * One-time pairing step for the inbox-app example.
 *
 * Run via:
 *   bun examples/inbox-app/src/pair.ts <endpoint> <code>
 *
 * Where:
 *   <endpoint>  base URL of the user's Loamss runtime, e.g.
 *               http://127.0.0.1:7777
 *   <code>      the one-time code the user pasted in from
 *               `loamss client pair --name "Inbox App"`
 *
 * Writes the bearer token to ./inbox-app.token. The `run.ts` script
 * reads from the same file on every subsequent invocation.
 *
 * In a real Path-B app this lives behind a "Connect to your Loamss"
 * UI button and the token persists in app-managed storage (encrypted
 * keychain, web cookie, etc.). The CLI form here is just the smallest
 * possible reference.
 */

// In published form: import { pair } from "@loamss/sdk";
import { pair } from "../../../src/index.js";
import { writeFileSync } from "node:fs";

const [, , endpoint, code] = process.argv;
if (!endpoint || !code) {
	console.error(
		"usage: bun examples/inbox-app/src/pair.ts <endpoint> <code>",
	);
	process.exit(1);
}

const result = await pair(endpoint, code, {
	metadata: { app_name: "inbox-app", app_version: "0.1.0" },
});

writeFileSync(
	"./inbox-app.token",
	JSON.stringify(
		{
			endpointUrl: result.endpointUrl,
			token: result.token,
			clientId: result.client.id,
			clientName: result.client.name,
		},
		null,
		2,
	),
);

console.log(`✓ Paired client ${result.client.id}`);
console.log(`  endpoint: ${result.endpointUrl}`);
console.log(`  token saved to ./inbox-app.token`);
console.log("");
console.log("Next: bun examples/inbox-app/src/run.ts");
