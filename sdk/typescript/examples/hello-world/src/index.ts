/**
 * Hello-world capsule.
 *
 * The smallest interesting capsule: declares one tool, returns a
 * greeting, no runtime callbacks, no permissions. Use it as a
 * template for your first capsule, or as a sanity check that the
 * runtime can spawn + speak MCP to a Bun-backed subprocess.
 *
 * Run standalone (no runtime) to confirm it doesn't crash:
 *   echo '{"jsonrpc":"2.0","id":1,"method":"initialize"}' | bun src/index.ts
 *
 * Install into a Loamss runtime:
 *   loamss capsule install /path/to/this/directory
 *   loamss start &
 *   # then via a paired MCP client:
 *   #   tools/call com.loamss.example.hello-world.hello { "who": "world" }
 */

// In published form this is `import { ... } from "@loamss/sdk";`.
// For the in-repo example we import via relative path so it runs
// without `bun install`.
import { createCapsule, defineTool } from "../../../src/index.js";

const hello = defineTool({
	name: "hello",
	description: "Say hello to someone (or to the world).",
	inputSchema: {
		type: "object",
		properties: {
			who: {
				type: "string",
				description: 'Who to greet. Defaults to "world".',
			},
		},
	},
	handler: (input: { who?: string }) => {
		const who = input.who?.trim() || "world";
		return `Hello, ${who}!`;
	},
});

await createCapsule({
	manifest: {
		name: "com.loamss.example.hello-world",
		version: "0.1.0",
		author: { name: "Loamss contributors" },
	},
	tools: [hello],
}).start();
