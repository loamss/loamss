"use client";

import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/primitives/Button";
import { Note } from "@/components/primitives/Note";
import {
	setOAuthClient,
	type OAuthClient,
	type OAuthProviderInfo,
} from "@/lib/runtime-client";

/*
 * RegisterOAuthClientModal — registers (or updates) a per-user
 * OAuth client_id for a provider.
 *
 * One screen: provider picker (well-known list + "custom" option),
 * client_id, optional client_secret. A collapsible "how to create
 * one" panel sits below the form with provider-specific links and
 * the exact redirect URI the user needs to register on the
 * provider's console.
 *
 * Why provider-specific guidance lives here: the friction point
 * for a new user isn't pasting a value into a form, it's figuring
 * out which form to use on Google Cloud Console / GitHub Developer
 * Settings / etc., and which redirect URI to register. Putting it
 * inline removes the round-trip to a docs site.
 *
 * Editing flow: if `existing` is supplied, the modal pre-fills the
 * provider, locks the picker, and labels the submit button "Update"
 * instead of "Register."
 */

interface RegisterOAuthClientModalProps {
	providers: OAuthProviderInfo[];
	preselectProvider?: string;
	existing?: OAuthClient;
	onClose: () => void;
	onSaved: () => void;
}

export function RegisterOAuthClientModal({
	providers,
	preselectProvider,
	existing,
	onClose,
	onSaved,
}: RegisterOAuthClientModalProps) {
	const isEdit = !!existing;
	const initialProvider =
		preselectProvider ?? existing?.provider ?? providers[0]?.name ?? "";
	const [provider, setProvider] = useState(initialProvider);
	const [customProvider, setCustomProvider] = useState("");
	const [clientId, setClientId] = useState(existing?.client_id ?? "");
	const [clientSecret, setClientSecret] = useState("");
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [showHelp, setShowHelp] = useState(!isEdit);

	const firstFieldRef = useRef<HTMLInputElement>(null);

	useEffect(() => {
		firstFieldRef.current?.focus();
		const onKey = (e: KeyboardEvent) => {
			if (e.key === "Escape") onClose();
		};
		document.addEventListener("keydown", onKey);
		return () => document.removeEventListener("keydown", onKey);
	}, [onClose]);

	const isCustom = provider === "__custom__";
	const resolvedProvider = isCustom ? customProvider.trim() : provider;

	async function submit(e: React.FormEvent) {
		e.preventDefault();
		if (busy) return;
		setError(null);

		if (!resolvedProvider) {
			setError("Pick a provider or name a custom one.");
			return;
		}
		if (!clientId.trim()) {
			setError("client_id is required.");
			return;
		}

		setBusy(true);
		const res = await setOAuthClient({
			provider: resolvedProvider,
			clientId: clientId.trim(),
			clientSecret: clientSecret.trim() || undefined,
		});
		if (!res.ok) {
			setError(res.reason);
			setBusy(false);
			return;
		}
		onSaved();
	}

	return (
		<div
			className="fixed inset-0 z-50 flex items-center justify-center bg-ink/30 p-4 animate-fade-in"
			role="dialog"
			aria-modal="true"
			aria-labelledby="register-oauth-title"
			onClick={(e) => {
				if (e.target === e.currentTarget && !busy) onClose();
			}}
		>
			<div className="bg-paper border border-ink-hairline rounded-md shadow-raise max-w-xl w-full max-h-[90vh] overflow-y-auto">
				<header className="px-6 py-5 border-b border-ink-hairline-soft">
					<div className="smallcap text-ink-quiet">
						{isEdit ? "Edit OAuth provider" : "Register OAuth provider"}
					</div>
					<h2
						id="register-oauth-title"
						className="mt-1 font-serif text-2xl text-ink leading-tight"
						style={{ fontVariationSettings: "'opsz' 96, 'wght' 400" }}
					>
						{isEdit
							? `Update ${existing.provider}`
							: "Hook up a new provider"}
					</h2>
					<p className="mt-2 text-sm text-ink-muted">
						Loamss never sees a provider's client_id until you paste it
						here. One client per provider — every capsule that uses it
						(calendar, mail, drive, …) reuses the same one.
					</p>
				</header>

				<form onSubmit={submit} className="px-6 py-5 space-y-5">
					{/* Provider picker */}
					<div>
						<label
							htmlFor="oauth-provider"
							className="smallcap text-ink-quiet block mb-2"
						>
							Provider
						</label>
						{isEdit ? (
							<input
								id="oauth-provider"
								type="text"
								value={existing.provider}
								disabled
								className="w-full font-mono text-sm bg-paper-deep/40 border border-ink-hairline-soft rounded-sm px-3 py-2 text-ink-muted"
							/>
						) : (
							<>
								<select
									id="oauth-provider"
									value={provider}
									onChange={(e) => setProvider(e.target.value)}
									disabled={busy}
									className="w-full font-sans text-sm bg-paper border border-ink-hairline-soft focus:border-brand focus:ring-1 focus:ring-brand/20 rounded-sm px-3 py-2 outline-none transition-colors"
								>
									{providers.map((p) => (
										<option key={p.name} value={p.name}>
											{prettyProviderName(p.name)}
										</option>
									))}
									<option value="__custom__">Other (custom)…</option>
								</select>
								{isCustom && (
									<input
										type="text"
										value={customProvider}
										onChange={(e) => setCustomProvider(e.target.value)}
										placeholder="lower-case identifier (e.g. 'corp-sso')"
										required
										disabled={busy}
										className="mt-2 w-full font-mono text-sm bg-paper border border-ink-hairline-soft focus:border-brand focus:ring-1 focus:ring-brand/20 rounded-sm px-3 py-2 outline-none transition-colors"
									/>
								)}
								{isCustom && (
									<p className="mt-1 text-xs text-ink-quiet font-sans">
										Custom providers also need to declare endpoints inline
										in the capsule manifest — the runtime only knows the
										well-known ones above.
									</p>
								)}
							</>
						)}
					</div>

					{/* client_id */}
					<div>
						<label
							htmlFor="oauth-client-id"
							className="smallcap text-ink-quiet block mb-2"
						>
							Client ID
						</label>
						<input
							ref={firstFieldRef}
							id="oauth-client-id"
							type="text"
							value={clientId}
							onChange={(e) => setClientId(e.target.value)}
							placeholder="abc1234.apps.googleusercontent.com"
							required
							disabled={busy}
							className="w-full font-mono text-sm bg-paper border border-ink-hairline-soft focus:border-brand focus:ring-1 focus:ring-brand/20 rounded-sm px-3 py-2 outline-none transition-colors disabled:bg-paper-deep/30"
						/>
					</div>

					{/* client_secret */}
					<div>
						<label
							htmlFor="oauth-client-secret"
							className="smallcap text-ink-quiet block mb-2"
						>
							Client secret <span className="text-ink-ghost">(optional)</span>
						</label>
						<input
							id="oauth-client-secret"
							type="password"
							value={clientSecret}
							onChange={(e) => setClientSecret(e.target.value)}
							placeholder={isEdit ? "leave blank to keep existing" : ""}
							disabled={busy}
							className="w-full font-mono text-sm bg-paper border border-ink-hairline-soft focus:border-brand focus:ring-1 focus:ring-brand/20 rounded-sm px-3 py-2 outline-none transition-colors disabled:bg-paper-deep/30"
						/>
						<p className="mt-1 text-xs text-ink-quiet font-sans">
							Desktop / native OAuth on PKCE doesn't need a secret —
							Google's recommended setup. Leave blank unless your provider
							requires one (web-app client IDs, some enterprise SSO).
						</p>
					</div>

					{error && <Note kind="warn">{error}</Note>}

					{/* Submit row */}
					<div className="flex items-center justify-end gap-3 pt-2">
						<button
							type="button"
							onClick={onClose}
							disabled={busy}
							className="text-sm text-ink-quiet hover:text-ink-muted underline underline-offset-2 disabled:opacity-50"
						>
							Cancel
						</button>
						<Button type="submit" disabled={busy}>
							{busy
								? isEdit
									? "Updating…"
									: "Registering…"
								: isEdit
									? "Update"
									: "Register"}
						</Button>
					</div>
				</form>

				{/* Provider-specific walkthrough */}
				{!isEdit && (
					<div className="px-6 pb-6">
						<button
							type="button"
							onClick={() => setShowHelp((v) => !v)}
							className="smallcap text-ink-quiet hover:text-ink-muted underline underline-offset-2"
						>
							{showHelp ? "Hide" : "Show"} how to create one
						</button>
						{showHelp && (
							<div className="mt-3 border border-ink-hairline-soft rounded-sm p-4 text-sm text-ink-muted leading-relaxed bg-paper-deep/30">
								<ProviderHelp provider={resolvedProvider} />
							</div>
						)}
					</div>
				)}
			</div>
		</div>
	);
}

function prettyProviderName(name: string): string {
	switch (name) {
		case "google":
			return "Google";
		case "github":
			return "GitHub";
		default:
			return name;
	}
}

interface ProviderHelpProps {
	provider: string;
}

function ProviderHelp({ provider }: ProviderHelpProps) {
	if (provider === "google") return <GoogleHelp />;
	if (provider === "github") return <GitHubHelp />;
	return <GenericHelp />;
}

function GoogleHelp() {
	return (
		<>
			<ol className="list-decimal pl-5 space-y-1.5">
				<li>
					Open{" "}
					<a
						href="https://console.cloud.google.com"
						target="_blank"
						rel="noreferrer"
						className="text-brand hover:text-brand-deep underline underline-offset-2"
					>
						Google Cloud Console
					</a>{" "}
					and pick (or create) a project.
				</li>
				<li>
					<span className="font-mono text-2xs">APIs &amp; Services</span> →{" "}
					<span className="font-mono text-2xs">Enabled APIs</span> → enable
					whichever Google APIs the capsule needs (Calendar API for{" "}
					<code className="font-mono text-2xs">calendar-ingestor</code>,
					Gmail API for a mail ingestor, etc.).
				</li>
				<li>
					<span className="font-mono text-2xs">OAuth consent screen</span> →
					User Type: External. Add your own Google account under{" "}
					<span className="font-mono text-2xs">Test users</span>. Scopes are
					requested per-capsule at pairing time; you don't need to add them
					here.
				</li>
				<li>
					<span className="font-mono text-2xs">Credentials</span> →{" "}
					<span className="font-mono text-2xs">Create credentials</span> →{" "}
					<span className="font-mono text-2xs">OAuth client ID</span> →
					Application type:{" "}
					<strong className="text-ink">Desktop app</strong>. Desktop clients
					support{" "}
					<code className="font-mono text-2xs">http://127.0.0.1</code> with
					arbitrary loopback ports, which is what Loamss uses.
				</li>
				<li>
					Copy the client ID into the form above. The client secret is
					optional (PKCE-only flow works without it).
				</li>
			</ol>
			<p className="mt-3 text-xs text-ink-quiet">
				The redirect URI is dynamic — Loamss allocates an ephemeral 127.0.0.1
				port for each flow. You don't need to pre-register one.
			</p>
		</>
	);
}

function GitHubHelp() {
	return (
		<>
			<ol className="list-decimal pl-5 space-y-1.5">
				<li>
					Open{" "}
					<a
						href="https://github.com/settings/developers"
						target="_blank"
						rel="noreferrer"
						className="text-brand hover:text-brand-deep underline underline-offset-2"
					>
						GitHub Developer Settings
					</a>{" "}
					→{" "}
					<span className="font-mono text-2xs">
						OAuth Apps → New OAuth App
					</span>
					.
				</li>
				<li>
					Application name: "Loamss" (or anything). Homepage:{" "}
					<code className="font-mono text-2xs">https://loamss.com</code>.
					Authorization callback URL:{" "}
					<code className="font-mono text-2xs">
						http://127.0.0.1
					</code>{" "}
					— GitHub accepts loopback hosts; Loamss picks the port at runtime.
				</li>
				<li>Register the application.</li>
				<li>
					Copy the Client ID into the form. Generate a client secret (GitHub
					OAuth apps require one) and paste it into the secret field.
				</li>
			</ol>
		</>
	);
}

function GenericHelp() {
	return (
		<p>
			Provider-specific instructions only ship for well-known providers
			(Google, GitHub). For a custom provider you'll also need to declare its{" "}
			<code className="font-mono text-2xs">authorization_endpoint</code> and{" "}
			<code className="font-mono text-2xs">token_endpoint</code> in the
			capsule's <code className="font-mono text-2xs">capsule.yaml</code>{" "}
			under <code className="font-mono text-2xs">oauth:</code>. See{" "}
			<code className="font-mono text-2xs">
				docs/capsule-ingestor-primitives.md
			</code>{" "}
			§4.
		</p>
	);
}
