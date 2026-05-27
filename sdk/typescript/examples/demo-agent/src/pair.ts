/**
 * One-time pairing for the demo-agent example.
 *
 * Run via:
 *   bun examples/demo-agent/src/pair.ts <endpoint> <code>
 *
 *   <endpoint>  base URL of the user's Loamss runtime
 *                 (e.g. http://127.0.0.1:7777)
 *   <code>      one-time code from `loamss client pair --name "Demo Agent"`
 *
 * Writes a token bundle to ./demo-agent.token. The agent reads from
 * that file on every subsequent run.
 *
 * The agent and the runtime are two separate processes by design:
 * pairing is the moment of consent. The agent has no privileged
 * relationship — it speaks the same MCP wire protocol any external
 * tool (Claude, ChatGPT, Cursor, ...) would speak.
 */

import { pair } from "@loamss/sdk";
import { writeFileSync } from "node:fs";

const [, , endpoint, code] = process.argv;
if (!endpoint || !code) {
	console.error(
		"usage: bun examples/demo-agent/src/pair.ts <endpoint> <code>",
	);
	process.exit(1);
}

const result = await pair(endpoint, code, {
	metadata: { app_name: "demo-agent", app_version: "0.1.0" },
});

writeFileSync(
	"./demo-agent.token",
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
console.log(`  token saved to ./demo-agent.token`);
console.log("");
console.log("Next:");
console.log('  loamss grant create \\');
console.log(`    --principal-kind client --principal-id ${result.client.id} \\`);
console.log("    --capability memory.read --scope-json '{}' \\");
console.log('    --rationale "let the agent read memory"');
console.log("");
console.log("  bun src/agent.ts \"what did Sarah want?\"");
