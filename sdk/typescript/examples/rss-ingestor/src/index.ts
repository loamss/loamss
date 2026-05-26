/**
 * RSS Ingestor — reference capsule.
 *
 * Exposes one tool, `sync`, which the runtime's scheduler invokes
 * every Ingestor.schedule.interval. On each call the capsule:
 *
 *   1. Reads its config (list of feed URLs)
 *   2. Loads the cursor (per-feed last-seen item ids + pubDates)
 *   3. Fetches each feed
 *   4. Skips items it's already seen
 *   5. Upserts each new item into memory via memory.upsert
 *   6. Persists the updated cursor
 *
 * Returns the standard SyncResult shape so the scheduler can
 * persist counters into source.Store and emit
 * source.sync.completed audit entries.
 */

// In published form: import { ... } from "@loamss/sdk";
// During in-repo development we import via relative paths so the
// example runs without a workspace install.
import { createCapsule, defineTool } from "../../../src/index.js";

type SyncInput = Record<string, never>;

interface SyncResult {
	records_added: number;
	records_updated: number;
	bytes_ingested: number;
	errors: number;
	per_feed: PerFeedSummary[];
}

interface PerFeedSummary {
	feed: string;
	items_seen: number;
	items_added: number;
	error?: string;
}

interface FeedCursor {
	last_id?: string;
	last_pub_date?: string;
}

interface CursorEnvelope {
	feeds: Record<string, FeedCursor>;
}

const DEFAULT_FEEDS: string[] = [
	"https://news.ycombinator.com/rss",
];

const sync = defineTool<SyncInput, SyncResult>({
	name: "sync",
	description:
		"Fetch every configured RSS feed, upsert new items into memory, " +
		"and advance the cursor. Invoked by the runtime's scheduler.",
	inputSchema: { type: "object" },
	handler: async (_, ctx) => {
		const feeds = await readFeedsFromConfig();
		const cursor = await loadCursor(ctx);

		let recordsAdded = 0;
		let bytesIngested = 0;
		let errorCount = 0;
		const perFeed: PerFeedSummary[] = [];

		for (const feedURL of feeds) {
			const summary: PerFeedSummary = {
				feed: feedURL,
				items_seen: 0,
				items_added: 0,
			};
			try {
				const xml = await fetchFeed(feedURL);
				bytesIngested += xml.length;
				const items = parseRSS(xml);
				summary.items_seen = items.length;

				const fc = cursor.feeds[feedURL] ?? {};
				let newLastID = fc.last_id;
				let newLastDate = fc.last_pub_date;

				for (const item of items) {
					if (isAlreadySeen(item, fc)) continue;

					const namespace = `rss-${feedHandle(feedURL)}`;
					const id = item.guid || item.link || hashID(item);
					await ctx.runtime.tools.call("memory.upsert", {
						namespace,
						id,
						content: `${item.title}\n\n${item.description ?? ""}`,
						metadata: {
							url: item.link,
							title: item.title,
							pub_date: item.pubDate,
							feed_url: feedURL,
							source: "rss-ingestor",
							entities: ["rss-item"],
						},
					});
					recordsAdded++;
					summary.items_added++;

					if (
						item.pubDate &&
						(!newLastDate || item.pubDate > newLastDate)
					) {
						newLastDate = item.pubDate;
					}
					if (item.guid && (!newLastID || item.guid > newLastID)) {
						newLastID = item.guid;
					}
				}

				cursor.feeds[feedURL] = {
					last_id: newLastID,
					last_pub_date: newLastDate,
				};
			} catch (err) {
				const msg = err instanceof Error ? err.message : String(err);
				summary.error = msg;
				errorCount++;
			}
			perFeed.push(summary);
		}

		await saveCursor(ctx, cursor);

		return {
			records_added: recordsAdded,
			records_updated: 0,
			bytes_ingested: bytesIngested,
			errors: errorCount,
			per_feed: perFeed,
		};
	},
});

await createCapsule({
	manifest: {
		name: "rss-ingestor",
		version: "0.1.0",
		author: { name: "Loamss contributors" },
	},
	tools: [sync],
}).start();

// --- helpers -------------------------------------------------------------

async function readFeedsFromConfig(): Promise<string[]> {
	const env = process.env.LOAMSS_RSS_FEEDS;
	if (env) {
		return env
			.split(",")
			.map((s) => s.trim())
			.filter(Boolean);
	}
	return DEFAULT_FEEDS;
}

type CallTool = (
	name: string,
	args: Record<string, unknown>,
) => Promise<{ content?: Array<{ text?: string }> }>;

async function loadCursor(ctx: {
	runtime: { tools: { call: CallTool } };
}): Promise<CursorEnvelope> {
	try {
		const res = await ctx.runtime.tools.call("cursor.get", {});
		const text = res.content?.[0]?.text ?? "";
		const payload = JSON.parse(text || "{}") as { value?: string };
		if (!payload.value) return { feeds: {} };
		return JSON.parse(payload.value) as CursorEnvelope;
	} catch {
		return { feeds: {} };
	}
}

async function saveCursor(
	ctx: { runtime: { tools: { call: CallTool } } },
	cursor: CursorEnvelope,
): Promise<void> {
	await ctx.runtime.tools.call("cursor.set", {
		value: JSON.stringify(cursor),
	});
}

async function fetchFeed(url: string): Promise<string> {
	const resp = await fetch(url, {
		headers: { "User-Agent": "loamss-rss-ingestor/0.1" },
	});
	if (!resp.ok) {
		throw new Error(`HTTP ${resp.status} ${resp.statusText}`);
	}
	return await resp.text();
}

interface RSSItem {
	guid?: string;
	link?: string;
	title: string;
	description?: string;
	pubDate?: string;
}

/**
 * Tiny regex-based RSS 2.0 / Atom parser. Not XML-spec-correct
 * (skips CDATA quirks, namespaces) but good enough for the
 * reference capsule. A production ingestor would depend on
 * something like `fast-xml-parser`.
 */
export function parseRSS(xml: string): RSSItem[] {
	const items: RSSItem[] = [];
	const blocks = [...xml.matchAll(/<item\b[\s\S]*?<\/item>/g)].map(
		(m) => m[0],
	);
	const entries = [...xml.matchAll(/<entry\b[\s\S]*?<\/entry>/g)].map(
		(m) => m[0],
	);
	const all = blocks.length > 0 ? blocks : entries;
	for (const block of all) {
		const guid = matchInner(block, /<guid[^>]*>([\s\S]*?)<\/guid>/);
		const id = matchInner(block, /<id[^>]*>([\s\S]*?)<\/id>/);
		let link = matchInner(block, /<link\b[^>]*>([\s\S]*?)<\/link>/);
		if (!link) {
			const href = block.match(/<link\b[^>]*\bhref="([^"]+)"/);
			if (href) link = href[1];
		}
		const title =
			matchInner(block, /<title[^>]*>([\s\S]*?)<\/title>/) ?? "";
		const description =
			matchInner(block, /<description[^>]*>([\s\S]*?)<\/description>/) ??
			matchInner(block, /<summary[^>]*>([\s\S]*?)<\/summary>/);
		const pubDate =
			matchInner(block, /<pubDate[^>]*>([\s\S]*?)<\/pubDate>/) ??
			matchInner(block, /<updated[^>]*>([\s\S]*?)<\/updated>/) ??
			matchInner(block, /<published[^>]*>([\s\S]*?)<\/published>/);
		items.push({
			guid: stripCDATA(guid ?? id ?? ""),
			link: stripCDATA(link ?? ""),
			title: stripCDATA(title),
			description: stripCDATA(description ?? ""),
			pubDate: toISO(pubDate ?? ""),
		});
	}
	return items;
}

function matchInner(s: string, re: RegExp): string | undefined {
	const m = s.match(re);
	return m && m[1] ? m[1].trim() : undefined;
}

function stripCDATA(s: string): string {
	return s.replace(/^<!\[CDATA\[([\s\S]*?)\]\]>$/, "$1").trim();
}

function toISO(s: string): string {
	if (!s) return "";
	const d = new Date(s);
	if (Number.isNaN(d.getTime())) return s;
	return d.toISOString();
}

function isAlreadySeen(item: RSSItem, cursor: FeedCursor): boolean {
	if (item.guid && cursor.last_id && item.guid <= cursor.last_id) {
		return true;
	}
	if (
		item.pubDate &&
		cursor.last_pub_date &&
		item.pubDate <= cursor.last_pub_date
	) {
		return true;
	}
	return false;
}

function feedHandle(url: string): string {
	try {
		const u = new URL(url);
		return u.hostname.replace(/\./g, "-");
	} catch {
		return "feed";
	}
}

function hashID(item: RSSItem): string {
	const seed = `${item.title}|${item.pubDate ?? ""}`;
	let h = 5381;
	for (let i = 0; i < seed.length; i++) {
		h = ((h << 5) + h + seed.charCodeAt(i)) & 0xffffffff;
	}
	return `h${(h >>> 0).toString(36)}`;
}
