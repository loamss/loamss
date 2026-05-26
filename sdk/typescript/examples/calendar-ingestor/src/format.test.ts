/**
 * Unit tests for the small pure functions in the capsule. The
 * network-driven sync loop is covered via the runtime's
 * integration suite; here we just pin the formatting behavior.
 */

import { test, expect } from "bun:test";
import { describeWhen } from "./index.js";

test("describeWhen: timed event renders start → end", () => {
	const out = describeWhen({
		id: "x",
		start: { dateTime: "2026-05-26T15:00:00-07:00" },
		end: { dateTime: "2026-05-26T16:00:00-07:00" },
	});
	expect(out).toBe("2026-05-26T15:00:00-07:00 → 2026-05-26T16:00:00-07:00");
});

test("describeWhen: all-day event uses date form", () => {
	const out = describeWhen({
		id: "x",
		start: { date: "2026-05-30" },
		end: { date: "2026-05-30" },
	});
	expect(out).toBe("All day on 2026-05-30");
});

test("describeWhen: multi-day all-day event names the end date", () => {
	const out = describeWhen({
		id: "x",
		start: { date: "2026-05-30" },
		end: { date: "2026-06-02" },
	});
	expect(out).toContain("All day on 2026-05-30");
	expect(out).toContain("through 2026-06-02");
});

test("describeWhen: missing start returns empty", () => {
	expect(describeWhen({ id: "x" })).toBe("");
});
