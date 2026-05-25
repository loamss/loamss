"use client";

import { create } from "zustand";
import { type ConsoleState, getConsoleState } from "./runtime-client";

/*
 * Dashboard state.
 *
 * One store, one snapshot. The dashboard reads /console/state on
 * mount, then refreshes on a quiet cadence (default 8s) while the
 * tab is visible. The store exposes:
 *
 *   - the latest snapshot (`state`)
 *   - the request status (`loading`, `error`)
 *   - the last successful fetch timestamp (`fetchedAt`)
 *   - a `refresh()` action for the manual "refresh now" button
 *   - a `start()` lifecycle that owns the polling interval
 *
 * State shape is intentionally flat. The dashboard doesn't need
 * cached pane state, optimistic mutations, or anything that would
 * justify slicing this further — it's a read-only mirror of the
 * runtime's view.
 *
 * Errors are surfaced as a string, not thrown. The dashboard
 * renders an "unreachable" banner when the runtime is down; we
 * don't want to crash the React tree over a network blip.
 */

export interface DashboardSlice {
	// Latest snapshot. null means "haven't fetched yet" — the first
	// render shows skeletons. After a successful fetch this stays
	// populated even across subsequent failed refreshes so the
	// dashboard doesn't go blank when the runtime hiccups.
	state: ConsoleState | null;

	// When true, a fetch is in flight. Stays false during background
	// polls that go through; only flips on for the initial load and
	// for manually-triggered refreshes.
	loading: boolean;

	// Most recent error message, or null. Cleared on the next
	// successful fetch.
	error: string | null;

	// ISO timestamp of the last successful fetch. Surfaced in the
	// shell's footer so the user can tell stale data from live data.
	fetchedAt: string | null;

	// Actions.
	refresh: (opts?: { manual?: boolean }) => Promise<void>;
	start: () => () => void;
}

// Refresh cadence while the dashboard is mounted and visible. 8s
// is slow enough that the UI never feels jittery, fast enough that
// a finished `loamss source sync` shows up in the activity feed
// well within the time it takes the user to alt-tab back. Pause
// while the tab is hidden — there's no point burning the wire
// when no one's looking.
const REFRESH_INTERVAL_MS = 8_000;

export const useDashboard = create<DashboardSlice>((set, get) => ({
	state: null,
	loading: false,
	error: null,
	fetchedAt: null,

	refresh: async ({ manual = false } = {}) => {
		// Show the spinner for manual refreshes and for the first
		// load. Quiet background polls don't flip the loading flag —
		// the UI shouldn't blink every 8 seconds.
		const isFirstLoad = get().state === null;
		if (manual || isFirstLoad) set({ loading: true });

		const next = await getConsoleState();
		if (next === null) {
			set({
				loading: false,
				error: "Runtime not reachable.",
			});
			return;
		}
		set({
			state: next,
			fetchedAt: new Date().toISOString(),
			error: null,
			loading: false,
		});
	},

	start: () => {
		// Kick off the initial load immediately so the first render
		// after navigation can show real data fast.
		void get().refresh();

		const tick = () => {
			if (typeof document !== "undefined" && document.hidden) return;
			void get().refresh();
		};
		const id = setInterval(tick, REFRESH_INTERVAL_MS);

		// Return the teardown for the caller's useEffect cleanup.
		return () => clearInterval(id);
	},
}));
