/**
 * Tool-result content blocks — the MCP wire shape every tool/call
 * response carries.
 *
 *   { content: [{ type: "text", text: "..." }, ...], isError?: true }
 *
 * The SDK's high-level handler API accepts either:
 *   - A `ToolResult` returned verbatim
 *   - A plain object/string — auto-wrapped into a single JSON or text block
 *
 * Authors who want explicit control build their `ToolResult` with the
 * helpers below.
 */

export type Content = TextContent | DataContent;

export interface TextContent {
	type: "text";
	text: string;
}

export interface DataContent {
	type: "image" | "audio" | "resource";
	data: string;
	mimeType: string;
}

export interface ToolResult {
	content: Content[];
	isError?: boolean;
}

/** Wrap a plain string as a single text block. */
export function text(s: string): TextContent {
	return { type: "text", text: s };
}

/** JSON-encode a value into a single text block (MCP convention). */
export function json(v: unknown): TextContent {
	return { type: "text", text: JSON.stringify(v) };
}

/** Wrap base64-encoded bytes as an image block. */
export function image(base64: string, mimeType: string): DataContent {
	return { type: "image", data: base64, mimeType };
}

/** Convenience: assemble a ToolResult from one or more blocks. */
export function result(...blocks: Content[]): ToolResult {
	return { content: blocks };
}

/** Convenience: a ToolResult flagged isError=true. */
export function errorResult(...blocks: Content[]): ToolResult {
	return { content: blocks, isError: true };
}

/**
 * Coerce a handler's return value into a ToolResult. Strings become
 * text blocks; objects become JSON blocks; ToolResults pass through.
 */
export function toToolResult(value: unknown): ToolResult {
	if (isToolResult(value)) {
		return value;
	}
	if (typeof value === "string") {
		return { content: [text(value)] };
	}
	return { content: [json(value)] };
}

function isToolResult(v: unknown): v is ToolResult {
	if (v === null || typeof v !== "object") return false;
	const obj = v as Record<string, unknown>;
	return Array.isArray(obj.content);
}
