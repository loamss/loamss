"use client";

import { useEffect, useRef, useState } from "react";
import type { ConsoleState } from "@/lib/runtime-client";
import {
	beginOAuthFlow,
	deleteSource,
	getOAuthStatus,
	syncSource,
} from "@/lib/runtime-client";
import { useDashboard } from "@/lib/dashboard-state";
import { AddSourceModal } from "./AddSourceModal";
import { EmptyState, Pane, UnavailablePane, statusDotColor } from "./Pane";

/*
 * SourcesPane — list of configured sources, with mutations.
 *
 * Per row:
 *   <dot>  <name>  ·  <adapter>  ·  <last-sync hint>     [Sync] [Disconnect]
 *
 * Capsule-backed ingestors that declare an OAuth provider get
 * extra state:
 *   - "Needs auth"  → [Connect <Provider>] is the primary action
 *   - "Connecting…" → spinner + fallback URL while the browser
 *                     flow runs; polls /console/oauth/status
 *                     every 2 s until connected (3 min timeout)
 *   - "Connected"   → sage chip + normal sync/disconnect
 */

interface SourcesPaneProps {
	block: ConsoleState["sources"] | undefined;
}

export function SourcesPane({ block }: SourcesPaneProps) {
	const [addOpen, setAddOpen] = useState(false);
	const refresh = useDashboard((s) => s.refresh);

	const action = (
		<button
			type="button"
			onClick={() => setAddOpen(true)}
			className="text-xs text-brand hover:text-brand-deep underline underline-offset-2"
		>
			+ Add source
		</button>
	);

	if (!block) {
		return (
			<Pane eyebrow="Sources" action={action}>
				<PaneSkeleton rows={2} />
			</Pane>
		);
	}
	if (!block.available) {
		return (
			<Pane eyebrow="Sources" action={action}>
				<UnavailablePane />
			</Pane>
		);
	}

	return (
		<>
			<Pane eyebrow="Sources" action={action}>
				{block.items.length === 0 ? (
					<EmptyState
						message="No sources yet. A source pulls data into your storage — local files, a calendar, a mailbox — so the memory layer has something to organise. Click +Add source to wire one up."
						hint="loamss source add source:files --name docs --config root=$HOME/Documents"
					/>
				) : (
					<ul className="divide-y divide-ink-hairline-soft">
						{block.items.map((s) => (
							<SourceRow key={s.id} source={s} />
						))}
					</ul>
				)}
			</Pane>
			{addOpen && (
				<AddSourceModal
					onClose={() => setAddOpen(false)}
					onAdded={() => {
						// Trigger an immediate refresh so the row appears
						// without waiting for the next 8s tick.
						void refresh({ manual: true });
					}}
				/>
			)}
		</>
	);
}

interface SourceRowProps {
	source: ConsoleState["sources"]["items"][number];
}

type RowAction = "idle" | "syncing" | "deleting" | "error";
type AuthState = "idle" | "connecting" | "polling";

const POLL_INTERVAL_MS = 2000;
const POLL_TIMEOUT_MS = 3 * 60 * 1000; // 3 minutes

function SourceRow({ source }: SourceRowProps) {
	const refresh = useDashboard((s) => s.refresh);
	const [action, setAction] = useState<RowAction>("idle");
	const [actionError, setActionError] = useState<string | null>(null);
	const [authState, setAuthState] = useState<AuthState>("idle");
	const [authError, setAuthError] = useState<string | null>(null);
	const [authURL, setAuthURL] = useState<string | null>(null);

	// Track the polling interval so we can tear it down when the row
	// unmounts mid-flow (user navigates away or closes the tab).
	const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
	const pollTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

	useEffect(() => {
		return () => {
			if (pollRef.current) clearInterval(pollRef.current);
			if (pollTimeoutRef.current) clearTimeout(pollTimeoutRef.current);
		};
	}, []);

	const isOAuth = !!source.auth_provider;
	const needsAuth = isOAuth && !!source.auth_required;
	const isRunning =
		source.last_sync_status === "running" || action === "syncing";

	async function handleSync() {
		setActionError(null);
		setAction("syncing");
		const result = await syncSource(source.name);
		if (!result.ok) {
			setActionError(result.reason);
			setAction("error");
			void refresh({ manual: true });
			return;
		}
		void refresh({ manual: true });
		setAction("idle");
	}

	async function handleDelete() {
		if (
			!window.confirm(
				`Disconnect source "${source.name}"? Its credentials will be cleared. ` +
					`The audit log keeps the record of every previous sync.`,
			)
		) {
			return;
		}
		setActionError(null);
		setAction("deleting");
		const result = await deleteSource(source.name);
		if (!result.ok) {
			setActionError(result.reason);
			setAction("error");
			return;
		}
		void refresh({ manual: true });
	}

	async function handleConnect() {
		setAuthError(null);
		setAuthURL(null);
		setAuthState("connecting");
		const result = await beginOAuthFlow(source.name);
		if (!result.ok) {
			setAuthError(result.reason);
			setAuthState("idle");
			return;
		}
		// Stash the auth URL as a fallback in case the runtime's
		// browser-open failed (headless servers). The browser may
		// already be open at this URL; this gives the user a recovery
		// path either way.
		setAuthURL(result.data.auth_url);
		setAuthState("polling");

		// Set up the polling loop with a timeout.
		const startedAt = Date.now();
		pollRef.current = setInterval(async () => {
			if (Date.now() - startedAt >= POLL_TIMEOUT_MS) {
				stopPolling();
				setAuthError(
					"Didn't see a callback within 3 minutes. Try again, or open the URL manually.",
				);
				setAuthState("idle");
				return;
			}
			const status = await getOAuthStatus(source.name);
			if (status.ok && status.connected) {
				stopPolling();
				setAuthState("idle");
				setAuthURL(null);
				void refresh({ manual: true });
			}
		}, POLL_INTERVAL_MS);
		pollTimeoutRef.current = setTimeout(() => {
			stopPolling();
			setAuthState("idle");
		}, POLL_TIMEOUT_MS);
	}

	function stopPolling() {
		if (pollRef.current) {
			clearInterval(pollRef.current);
			pollRef.current = null;
		}
		if (pollTimeoutRef.current) {
			clearTimeout(pollTimeoutRef.current);
			pollTimeoutRef.current = null;
		}
	}

	// --- render -----------------------------------------------------

	const dotColor = needsAuth
		? "amber"
		: statusDotColor(source.last_sync_status || "");

	return (
		<li className="py-3">
			<div className="flex items-baseline gap-4">
				<span
					className={[
						"inline-block w-1.5 h-1.5 rounded-full flex-none translate-y-[-2px]",
						dotClass(dotColor),
					].join(" ")}
					aria-label={
						needsAuth
							? "needs auth"
							: source.last_sync_status || "never synced"
					}
				/>
				<div className="flex-1 min-w-0">
					<div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
						<span className="text-sm text-ink">{source.name}</span>
						<span className="font-mono text-2xs text-ink-quiet">
							{source.adapter}
						</span>
						{isOAuth && (
							<AuthBadge
								provider={source.auth_provider!}
								state={
									authState !== "idle"
										? authState
										: needsAuth
											? "needs-auth"
											: "connected"
								}
							/>
						)}
					</div>
					<div className="mt-0.5 font-mono text-2xs text-ink-quiet">
						{needsAuth
							? `pair the user's ${source.auth_provider} account to start syncing`
							: syncHint(source.last_sync_status, source.last_sync_at) +
								renderSummary(source.summary)}
					</div>
				</div>
				<div className="flex items-center gap-3 flex-none">
					{needsAuth ? (
						<button
							type="button"
							onClick={handleConnect}
							disabled={authState !== "idle"}
							className="text-xs text-brand hover:text-brand-deep underline underline-offset-2 disabled:opacity-50 disabled:no-underline"
						>
							{authState === "connecting" && "starting…"}
							{authState === "polling" && "waiting…"}
							{authState === "idle" &&
								`connect ${prettyProvider(source.auth_provider!)}`}
						</button>
					) : (
						<>
							<button
								type="button"
								onClick={handleSync}
								disabled={isRunning || action === "deleting"}
								className="text-xs text-ink-muted hover:text-ink underline underline-offset-2 disabled:opacity-40 disabled:no-underline"
							>
								{isRunning ? "syncing…" : "sync"}
							</button>
							{isOAuth && (
								<button
									type="button"
									onClick={handleConnect}
									disabled={authState !== "idle"}
									className="text-xs text-ink-quiet hover:text-ink-muted underline underline-offset-2 disabled:opacity-40 disabled:no-underline"
								>
									re-authenticate
								</button>
							)}
						</>
					)}
					<button
						type="button"
						onClick={handleDelete}
						disabled={action === "deleting" || action === "syncing"}
						className="text-xs text-ink-quiet hover:text-brick underline underline-offset-2 disabled:opacity-40 disabled:no-underline"
					>
						{action === "deleting" ? "removing…" : "disconnect"}
					</button>
				</div>
			</div>

			{/* Polling state: show the fallback URL if the runtime's
			 * browser-open didn't actually focus a window (headless
			 * server, browser blocked the popup, etc.). */}
			{authState === "polling" && authURL && (
				<div className="mt-2 text-xs text-ink-muted font-sans">
					Browser not opened?{" "}
					<a
						href={authURL}
						target="_blank"
						rel="noreferrer"
						className="text-brand hover:text-brand-deep underline underline-offset-2"
					>
						Open this URL manually
					</a>{" "}
					to finish.
				</div>
			)}
			{authError && (
				<div className="mt-2 font-mono text-2xs text-brick">{authError}</div>
			)}
			{actionError && (
				<div className="mt-2 font-mono text-2xs text-brick">
					{actionError}
				</div>
			)}
		</li>
	);
}

interface AuthBadgeProps {
	provider: string;
	state: "needs-auth" | "connecting" | "polling" | "connected";
}

function AuthBadge({ provider, state }: AuthBadgeProps) {
	const label = prettyProvider(provider);
	switch (state) {
		case "needs-auth":
			return (
				<span className="font-mono text-2xs text-amber">
					needs auth · {label}
				</span>
			);
		case "connecting":
		case "polling":
			return (
				<span className="font-mono text-2xs text-ink-muted animate-pulse-soft">
					connecting to {label}…
				</span>
			);
		case "connected":
			return (
				<span className="font-mono text-2xs text-sage">
					connected · {label}
				</span>
			);
	}
}

function prettyProvider(name: string): string {
	switch (name) {
		case "google":
			return "Google";
		case "github":
			return "GitHub";
		default:
			return name;
	}
}

function syncHint(status: string, at?: string): string {
	if (!at) return "never synced";
	const elapsed = Math.max(
		0,
		Math.floor((Date.now() - new Date(at).getTime()) / 1000),
	);
	const ago =
		elapsed < 60
			? `${elapsed}s ago`
			: elapsed < 3600
				? `${Math.floor(elapsed / 60)}m ago`
				: `${Math.floor(elapsed / 3600)}h ago`;
	if (status === "success") return `synced ${ago}`;
	if (status === "error") return `failed · ${ago}`;
	if (status === "running") return `syncing now · started ${ago}`;
	return ago;
}

function renderSummary(summary?: Record<string, unknown>): string {
	if (!summary) return "";
	const parts: string[] = [];
	for (const [k, v] of Object.entries(summary)) {
		if (parts.length >= 3) break;
		if (k === "started" || k === "finished" || k === "error_message") continue;
		if (typeof v === "number" || typeof v === "string") {
			parts.push(`${k}=${v}`);
		}
	}
	return parts.length ? ` · ${parts.join(" · ")}` : "";
}

function dotClass(color: "sage" | "amber" | "brick" | "quiet"): string {
	return {
		sage: "bg-sage",
		amber: "bg-amber",
		brick: "bg-brick",
		quiet: "bg-ink-ghost",
	}[color];
}

function PaneSkeleton({ rows = 2 }: { rows?: number }) {
	return (
		<ul className="divide-y divide-ink-hairline-soft">
			{Array.from({ length: rows }).map((_, i) => (
				<li key={i} className="py-4">
					<div className="h-3 w-32 bg-ink-hairline-soft rounded-sm" />
					<div className="mt-2 h-2 w-44 bg-ink-hairline-soft rounded-sm" />
				</li>
			))}
		</ul>
	);
}
