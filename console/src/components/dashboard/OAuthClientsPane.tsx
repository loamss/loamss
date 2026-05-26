"use client";

import { useCallback, useEffect, useState } from "react";
import {
	deleteOAuthClient,
	listOAuthClients,
	listOAuthProviders,
	type OAuthClient,
	type OAuthProviderInfo,
} from "@/lib/runtime-client";
import { EmptyState, Pane } from "./Pane";
import { RegisterOAuthClientModal } from "./RegisterOAuthClientModal";

/*
 * OAuthClientsPane — per-user OAuth client management.
 *
 * Loamss runs as a single-user system, but OAuth providers
 * (Google, GitHub, …) require a per-user developer-console
 * registration (a "client_id"). Setting Google up once unlocks
 * every Google-using capsule the user installs later
 * (calendar-ingestor today, drive-ingestor / gmail-ingestor / …
 * tomorrow). The store is provider-keyed, capsule-independent.
 *
 * Per row:
 *   <dot>  <provider>  ·  client_id <abbreviated>   [edit] [remove]
 *
 * The dot is sage when the client has a secret as well; amber when
 * it's PKCE-only (which is the recommended Google setup). Different
 * shape, not different meaning — both are usable.
 *
 * Fetches on mount; refetches after any mutation. No polling — the
 * data doesn't change behind the user's back.
 */
export function OAuthClientsPane() {
	const [clients, setClients] = useState<OAuthClient[] | null>(null);
	const [providers, setProviders] = useState<OAuthProviderInfo[]>([]);
	const [error, setError] = useState<string | null>(null);
	const [registerOpen, setRegisterOpen] = useState<{
		preselectProvider?: string;
		existing?: OAuthClient;
	} | null>(null);

	const refresh = useCallback(async () => {
		const [clientsRes, providersRes] = await Promise.all([
			listOAuthClients(),
			listOAuthProviders(),
		]);
		if (!clientsRes.ok) {
			setError(clientsRes.reason);
			return;
		}
		setError(null);
		setClients(clientsRes.clients);
		if (providersRes.ok) {
			setProviders(providersRes.providers);
		}
	}, []);

	useEffect(() => {
		void refresh();
	}, [refresh]);

	const action = (
		<button
			type="button"
			onClick={() => setRegisterOpen({})}
			className="text-xs text-brand hover:text-brand-deep underline underline-offset-2"
		>
			+ Register provider
		</button>
	);

	return (
		<>
			<Pane eyebrow="OAuth providers" action={action}>
				{error && (
					<div className="mb-3 text-xs text-brick font-mono">{error}</div>
				)}
				{clients === null ? (
					<PaneSkeleton />
				) : clients.length === 0 ? (
					<EmptyState
						message={
							"No OAuth providers registered yet. Set one up once and every " +
							"capsule that targets that provider (calendar, gmail, drive, …) " +
							"reuses it."
						}
						hint="curl -X POST /console/oauth/clients/google  (or click + Register provider)"
					/>
				) : (
					<ul className="divide-y divide-ink-hairline-soft">
						{clients.map((c) => (
							<ClientRow
								key={c.provider}
								client={c}
								onEdit={() =>
									setRegisterOpen({
										preselectProvider: c.provider,
										existing: c,
									})
								}
								onChanged={() => void refresh()}
							/>
						))}
					</ul>
				)}
			</Pane>
			{registerOpen && (
				<RegisterOAuthClientModal
					providers={providers}
					preselectProvider={registerOpen.preselectProvider}
					existing={registerOpen.existing}
					onClose={() => setRegisterOpen(null)}
					onSaved={() => {
						setRegisterOpen(null);
						void refresh();
					}}
				/>
			)}
		</>
	);
}

interface ClientRowProps {
	client: OAuthClient;
	onEdit: () => void;
	onChanged: () => void;
}

function ClientRow({ client, onEdit, onChanged }: ClientRowProps) {
	const [removing, setRemoving] = useState(false);
	const [rowError, setRowError] = useState<string | null>(null);

	const hasSecret =
		typeof client.client_secret === "string" && client.client_secret !== "";

	async function handleRemove() {
		if (
			!window.confirm(
				`Remove the OAuth client for "${client.provider}"? Any capsule that ` +
					`uses this provider will fail its next sync until you register a ` +
					`new client. Already-issued refresh tokens stay in capsule storage ` +
					`but become unusable.`,
			)
		) {
			return;
		}
		setRowError(null);
		setRemoving(true);
		const result = await deleteOAuthClient(client.provider);
		if (!result.ok) {
			setRowError(result.reason);
			setRemoving(false);
			return;
		}
		onChanged();
	}

	return (
		<li className="py-3">
			<div className="flex items-baseline gap-4">
				<span
					className={[
						"inline-block w-1.5 h-1.5 rounded-full flex-none translate-y-[-2px]",
						hasSecret ? "bg-sage" : "bg-amber",
					].join(" ")}
					aria-label={hasSecret ? "has secret" : "PKCE-only"}
				/>
				<div className="flex-1 min-w-0">
					<div className="flex flex-wrap items-baseline gap-x-3">
						<span className="text-sm text-ink">{client.provider}</span>
						<span className="font-mono text-2xs text-ink-quiet">
							{abbreviateClientID(client.client_id)}
						</span>
						<span className="font-mono text-2xs text-ink-quiet">
							{hasSecret ? "secret: set" : "PKCE only"}
						</span>
					</div>
					{rowError && (
						<p className="mt-1 text-xs text-brick font-mono">{rowError}</p>
					)}
				</div>
				<div className="flex items-center gap-3 flex-none">
					<button
						type="button"
						onClick={onEdit}
						disabled={removing}
						className="text-xs text-ink-quiet hover:text-ink-muted underline underline-offset-2 disabled:opacity-50"
					>
						edit
					</button>
					<button
						type="button"
						onClick={() => void handleRemove()}
						disabled={removing}
						className="text-xs text-ink-quiet hover:text-brick underline underline-offset-2 disabled:opacity-50"
					>
						{removing ? "removing…" : "remove"}
					</button>
				</div>
			</div>
		</li>
	);
}

function PaneSkeleton() {
	return (
		<div className="py-4 text-sm text-ink-quiet font-mono">
			loading providers…
		</div>
	);
}

/**
 * abbreviateClientID renders a long OAuth client_id as
 * "abc1…xyz9.apps.googleusercontent.com" so it's recognizable
 * without dominating the row.
 */
function abbreviateClientID(s: string): string {
	if (s.length <= 28) return s;
	const at = s.indexOf(".");
	if (at > 0 && at < s.length - 1) {
		const head = s.slice(0, Math.min(6, at));
		const tail = s.slice(at);
		return `${head}…${tail}`;
	}
	return `${s.slice(0, 6)}…${s.slice(-8)}`;
}
