/**
 * Calendar Ingestor — reference OAuth ingestor capsule.
 *
 * Exposes one tool, `sync`, which the runtime's scheduler invokes
 * every Ingestor.schedule.interval. On each call the capsule:
 *
 *   1. Reads the calendar list to ingest (config or default to primary)
 *   2. Loads the cursor (per-calendar Google syncToken)
 *   3. Asks the runtime for a fresh OAuth access token (one call —
 *      the runtime refreshes transparently when stale)
 *   4. Calls Google Calendar API events.list with the syncToken
 *      (or no syncToken on first sync — Google sends a full-sync
 *      payload and a brand-new syncToken in response)
 *   5. Upserts each changed event into memory; cancelled events are
 *      written with status=cancelled so consumers can filter them out
 *   6. Persists the new syncToken
 *
 * The capsule has zero OAuth code. The runtime owns the entire
 * flow — see docs/capsule-ingestor-primitives.md §4.
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
	per_calendar: PerCalendarSummary[];
}

interface PerCalendarSummary {
	calendar_id: string;
	events_seen: number;
	events_added: number;
	events_updated: number;
	events_cancelled: number;
	full_sync: boolean;
	error?: string;
}

// One per-calendar cursor entry. We store Google's syncToken — an
// opaque-to-us value that summarizes "everything as of last sync."
// Google rotates it on each call; we replace ours with the new one.
interface CalendarCursor {
	sync_token?: string;
}

interface CursorEnvelope {
	calendars: Record<string, CalendarCursor>;
}

// Default to the user's primary calendar; the capsule's config
// surface (when it lands) will let users add work + family
// calendars. For now, env-var override + the default keep the
// reference build runnable.
const DEFAULT_CALENDARS = ["primary"];

const sync = defineTool<SyncInput, SyncResult>({
	name: "sync",
	description:
		"Pull changed events from Google Calendar into memory and " +
		"advance the per-calendar syncToken. Invoked by the runtime's " +
		"scheduler.",
	inputSchema: { type: "object" },
	handler: async (_, ctx) => {
		const calendars = await readCalendarsFromConfig();
		const cursor = await loadCursor(ctx);

		let accessToken: string;
		try {
			const tok = await ctx.runtime.tools.call("oauth.access_token", {});
			const payload = parseToolJSON<{
				access_token?: string;
				error?: string;
				message?: string;
			}>(tok);
			if (!payload.access_token) {
				// oauth.access_token returns isError + a structured
				// "oauth.reauth_required" code when the refresh token
				// is revoked. The dashboard surfaces a re-auth chip in
				// the Approvals pane; we just bail out cleanly here.
				return failedSyncResult(
					calendars,
					payload.message || payload.error || "no access token",
				);
			}
			accessToken = payload.access_token;
		} catch (err) {
			return failedSyncResult(
				calendars,
				err instanceof Error ? err.message : String(err),
			);
		}

		let recordsAdded = 0;
		let recordsUpdated = 0;
		let bytesIngested = 0;
		let errorCount = 0;
		const perCalendar: PerCalendarSummary[] = [];

		for (const calendarID of calendars) {
			const summary: PerCalendarSummary = {
				calendar_id: calendarID,
				events_seen: 0,
				events_added: 0,
				events_updated: 0,
				events_cancelled: 0,
				full_sync: false,
			};
			try {
				const cc = cursor.calendars[calendarID] ?? {};
				summary.full_sync = !cc.sync_token;

				const { events, nextSyncToken, bytesRead } =
					await fetchAllEventPages(accessToken, calendarID, cc.sync_token);
				bytesIngested += bytesRead;
				summary.events_seen = events.length;

				const namespace = `calendar-${calendarSlug(calendarID)}`;
				for (const ev of events) {
					if (ev.status === "cancelled") {
						summary.events_cancelled++;
					}
					await ctx.runtime.tools.call("memory.upsert", {
						namespace,
						id: ev.id,
						content: formatEventContent(ev),
						metadata: formatEventMetadata(ev, calendarID),
					});
					// Google's incremental sync returns mutated events
					// without telling us which are new vs. updated.
					// memory.upsert is idempotent either way; for
					// counters we treat full-sync as "all added" and
					// incremental as "all updated."
					if (summary.full_sync) {
						summary.events_added++;
					} else {
						summary.events_updated++;
					}
				}

				cursor.calendars[calendarID] = { sync_token: nextSyncToken };
				recordsAdded += summary.events_added;
				recordsUpdated += summary.events_updated;
			} catch (err) {
				const msg = err instanceof Error ? err.message : String(err);
				summary.error = msg;
				errorCount++;
				// On 410 Gone (sync token invalidated), Google asks
				// us to start over. Drop the cursor so the next tick
				// triggers a full sync.
				if (msg.includes("410")) {
					cursor.calendars[calendarID] = {};
				}
			}
			perCalendar.push(summary);
		}

		await saveCursor(ctx, cursor);

		return {
			records_added: recordsAdded,
			records_updated: recordsUpdated,
			bytes_ingested: bytesIngested,
			errors: errorCount,
			per_calendar: perCalendar,
		};
	},
});

await createCapsule({
	manifest: {
		name: "calendar-ingestor",
		version: "0.1.0",
		author: { name: "Loamss contributors" },
	},
	tools: [sync],
}).start();

// --- helpers -------------------------------------------------------------

async function readCalendarsFromConfig(): Promise<string[]> {
	const env = process.env.LOAMSS_CALENDARS;
	if (env) {
		return env
			.split(",")
			.map((s) => s.trim())
			.filter(Boolean);
	}
	return DEFAULT_CALENDARS;
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
		if (!payload.value) return { calendars: {} };
		return JSON.parse(payload.value) as CursorEnvelope;
	} catch {
		return { calendars: {} };
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

// --- Google Calendar API --------------------------------------------------

interface GoogleEvent {
	id: string;
	status?: string;
	summary?: string;
	description?: string;
	location?: string;
	htmlLink?: string;
	start?: { dateTime?: string; date?: string; timeZone?: string };
	end?: { dateTime?: string; date?: string; timeZone?: string };
	attendees?: Array<{
		email: string;
		displayName?: string;
		responseStatus?: string;
		organizer?: boolean;
		self?: boolean;
	}>;
	organizer?: { email: string; displayName?: string; self?: boolean };
	creator?: { email: string; displayName?: string };
	updated?: string;
	created?: string;
}

interface EventsPage {
	items?: GoogleEvent[];
	nextPageToken?: string;
	nextSyncToken?: string;
}

async function fetchAllEventPages(
	accessToken: string,
	calendarID: string,
	syncToken: string | undefined,
): Promise<{ events: GoogleEvent[]; nextSyncToken?: string; bytesRead: number }> {
	const events: GoogleEvent[] = [];
	let pageToken: string | undefined;
	let nextSyncToken: string | undefined;
	let bytesRead = 0;

	for (let i = 0; i < 50; i++) {
		const u = new URL(
			`https://www.googleapis.com/calendar/v3/calendars/${encodeURIComponent(
				calendarID,
			)}/events`,
		);
		u.searchParams.set("maxResults", "250");
		// `singleEvents=true` expands recurring events. Without it,
		// you get the master event + RRULE; with it, each instance
		// becomes its own entry — much more useful for memory queries
		// ("what do I have on Thursday").
		u.searchParams.set("singleEvents", "true");
		// Show deleted instances of recurring events too — needed
		// for incremental sync to clean up cancellations.
		u.searchParams.set("showDeleted", "true");
		if (syncToken) {
			u.searchParams.set("syncToken", syncToken);
		}
		if (pageToken) {
			u.searchParams.set("pageToken", pageToken);
		}

		const resp = await fetch(u.toString(), {
			headers: {
				Authorization: `Bearer ${accessToken}`,
				"User-Agent": "loamss-calendar-ingestor/0.1",
			},
		});
		const body = await resp.text();
		bytesRead += body.length;
		if (!resp.ok) {
			throw new Error(
				`Calendar API ${resp.status} ${resp.statusText}: ${truncate(body, 300)}`,
			);
		}
		const page: EventsPage = JSON.parse(body);
		if (page.items) {
			for (const ev of page.items) {
				if (ev.id) events.push(ev);
			}
		}
		if (page.nextPageToken) {
			pageToken = page.nextPageToken;
			continue;
		}
		nextSyncToken = page.nextSyncToken;
		break;
	}

	return { events, nextSyncToken, bytesRead };
}

// --- memory shaping -------------------------------------------------------

function formatEventContent(ev: GoogleEvent): string {
	const title = ev.summary ?? "(no title)";
	const when = describeWhen(ev);
	const where = ev.location ? `\nLocation: ${ev.location}` : "";
	const desc = ev.description ? `\n\n${ev.description}` : "";
	return `${title}\n${when}${where}${desc}`.trim();
}

function formatEventMetadata(
	ev: GoogleEvent,
	calendarID: string,
): Record<string, unknown> {
	const md: Record<string, unknown> = {
		source: "calendar-ingestor",
		calendar_id: calendarID,
		status: ev.status ?? "confirmed",
		start: ev.start?.dateTime ?? ev.start?.date ?? "",
		end: ev.end?.dateTime ?? ev.end?.date ?? "",
		all_day: !!ev.start?.date && !ev.start?.dateTime,
		url: ev.htmlLink ?? "",
		entities: ["calendar-event"],
	};
	if (ev.organizer?.email) {
		md.organizer = ev.organizer.email;
	}
	if (ev.location) {
		md.location = ev.location;
	}
	if (ev.attendees && ev.attendees.length > 0) {
		md.attendees = ev.attendees.map((a) => a.email);
		// Memory layer entity-resolution hook: surfacing the
		// attendees as canonical email handles lets the layer
		// stitch "Sarah from this meeting" with "Sarah in this
		// email" without re-extracting from prose.
		md.participants = ev.attendees.map((a) => ({
			email: a.email,
			name: a.displayName ?? "",
			role: a.organizer ? "organizer" : "attendee",
		}));
	}
	if (ev.updated) md.updated = ev.updated;
	if (ev.created) md.created = ev.created;
	return md;
}

export function describeWhen(ev: GoogleEvent): string {
	const start = ev.start?.dateTime ?? ev.start?.date;
	const end = ev.end?.dateTime ?? ev.end?.date;
	if (!start) return "";
	if (ev.start?.date && !ev.start?.dateTime) {
		return `All day on ${ev.start.date}${end && end !== start ? ` (through ${end})` : ""}`;
	}
	if (ev.start?.dateTime && ev.end?.dateTime) {
		return `${ev.start.dateTime} → ${ev.end.dateTime}`;
	}
	return start;
}

function calendarSlug(calendarID: string): string {
	// "primary" → "primary"; user@gmail.com → "user-gmail-com"
	return calendarID.replace(/[^a-zA-Z0-9-]/g, "-").toLowerCase();
}

function failedSyncResult(
	calendars: string[],
	errMessage: string,
): SyncResult {
	return {
		records_added: 0,
		records_updated: 0,
		bytes_ingested: 0,
		errors: 1,
		per_calendar: calendars.map((c) => ({
			calendar_id: c,
			events_seen: 0,
			events_added: 0,
			events_updated: 0,
			events_cancelled: 0,
			full_sync: false,
			error: errMessage,
		})),
	};
}

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

function truncate(s: string, max: number): string {
	if (s.length <= max) return s;
	return s.slice(0, max) + "…";
}
