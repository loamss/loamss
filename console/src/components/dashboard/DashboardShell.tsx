"use client";

import { useEffect } from "react";
import { Wordmark } from "@/components/primitives/Wordmark";
import { useDashboard } from "@/lib/dashboard-state";
import { SourcesPane } from "./SourcesPane";
import { CapsulesPane } from "./CapsulesPane";
import { ClientsPane } from "./ClientsPane";
import { ApprovalsPane } from "./ApprovalsPane";
import { ActivityPane } from "./ActivityPane";

/*
 * DashboardShell — the post-wizard landing.
 *
 * The Wizard's job was to get the user from zero to a working
 * config. This is what they see every time after that: an honest
 * mirror of what their Loamss is doing.
 *
 * Composition:
 *
 *   top       — wordmark + runtime badge + "last refreshed" mono tag.
 *   hero      — quiet one-line summary of the runtime (version,
 *               adapter set, uptime).
 *   approvals — only renders when something is pending. Big and
 *               obvious when present (this is the "humans must
 *               approve" surface); invisible otherwise.
 *   grid      — two-column layout on wide screens, stacked on
 *               narrow. Sources / Capsules / Clients on the left;
 *               Activity on the right and full-height.
 *   footer    — last-refresh tag, runtime-not-reachable banner,
 *               manual refresh button.
 *
 * Refresh: the store's start() kicks an immediate load + then a
 * background poll while the tab is visible. Errors collapse to a
 * banner; the stale snapshot stays visible so the user doesn't
 * watch the dashboard go blank when the daemon hiccups.
 */
export function DashboardShell() {
  const state = useDashboard((s) => s.state);
  const error = useDashboard((s) => s.error);
  const loading = useDashboard((s) => s.loading);
  const fetchedAt = useDashboard((s) => s.fetchedAt);
  const refresh = useDashboard((s) => s.refresh);
  const start = useDashboard((s) => s.start);

  useEffect(() => start(), [start]);

  return (
    <div className="min-h-screen flex flex-col">
      {/* Header — same visual weight as the wizard's, different
       * intent. The "Dashboard" eyebrow replaces "First-run
       * setup". */}
      <header className="px-6 sm:px-10 py-6 sm:py-8 flex items-center justify-between gap-4 border-b border-ink-hairline-soft">
        <div className="flex items-center gap-5">
          <Wordmark size="md" />
          <span className="smallcap text-ink-quiet hidden sm:inline">
            Dashboard
          </span>
        </div>
        <div className="flex items-center gap-4">
          <RuntimeBadge state={state} error={error} />
        </div>
      </header>

      {/* Hero — single-line runtime summary in mono, calm. */}
      <Hero />

      <main className="flex-1 px-6 sm:px-10 pb-16 max-w-screen-2xl w-full mx-auto">
        {/* Restart-required banner. Lights up when the config file
         * on disk differs from the live in-memory config in ways
         * the daemon can't hot-swap (storage / memory / models /
         * listen_addr / data_dir / audit settings). Stays visible
         * until the user runs `loamss start` against the new
         * file. */}
        {state?.config.restart_required &&
          state.config.restart_required.length > 0 && (
            <RestartBanner paths={state.config.restart_required.map((c) => c.path)} />
          )}

        {/* Approvals pane — only renders when there's something to
         * approve. Sits at the top, full-width, intentionally
         * weighty. */}
        {state?.approvals_pending.available &&
          state.approvals_pending.items.length > 0 && (
            <div className="mt-10">
              <ApprovalsPane items={state.approvals_pending.items} />
            </div>
          )}

        <div className="grid grid-cols-1 lg:grid-cols-3 gap-x-12 gap-y-12 mt-10">
          <div className="lg:col-span-2 space-y-12">
            <SourcesPane block={state?.sources} />
            <CapsulesPane block={state?.capsules} />
            <ClientsPane block={state?.clients} />
          </div>
          <div>
            <ActivityPane block={state?.activity} />
          </div>
        </div>
      </main>

      {/* Footer — the freshness signal + the manual refresh
       * affordance. Quiet by default. */}
      <footer className="px-6 sm:px-10 py-6 flex flex-wrap items-center justify-between gap-3 text-xs text-ink-quiet font-sans border-t border-ink-hairline-soft">
        <span className="font-mono text-2xs">
          {fetchedAt ? `last refresh ${relativeAge(fetchedAt)}` : "loading…"}
        </span>
        <button
          type="button"
          onClick={() => void refresh({ manual: true })}
          disabled={loading}
          className="text-xs text-ink-quiet hover:text-ink-muted underline underline-offset-2 disabled:opacity-50"
        >
          {loading ? "refreshing…" : "refresh now"}
        </button>
      </footer>
    </div>
  );
}

function Hero() {
  const state = useDashboard((s) => s.state);
  if (!state) {
    return (
      <div className="px-6 sm:px-10 py-8 border-b border-ink-hairline-soft">
        <span className="font-mono text-xs text-ink-quiet">loading…</span>
      </div>
    );
  }
  const adapters = [
    state.config.storage_adapter,
    state.config.memory_adapter,
    ...(state.config.model_adapters ?? []),
  ].filter(Boolean);

  return (
    <div className="px-6 sm:px-10 py-8 border-b border-ink-hairline-soft">
      <div className="max-w-screen-2xl w-full mx-auto">
        <div className="smallcap text-ink-quiet">Your Loamss</div>
        <div className="mt-2 flex flex-wrap items-baseline gap-x-6 gap-y-2 font-mono text-sm text-ink">
          <span>runtime · {state.runtime.version}</span>
          <span className="text-ink-quiet">·</span>
          <span>{state.runtime.listen_addr}</span>
          <span className="text-ink-quiet">·</span>
          <span className="text-ink-quiet">
            up {humanizeUptime(state.runtime.uptime_seconds)}
          </span>
        </div>
        {adapters.length > 0 && (
          <div className="mt-1 font-mono text-2xs text-ink-quiet">
            {adapters.join(" · ")}
          </div>
        )}
      </div>
    </div>
  );
}

interface RestartBannerProps {
  paths: string[];
}

/**
 * RestartBanner surfaces "the config on disk has diverged from
 * the live config" in a visually-loud-but-not-alarming way. The
 * runtime can hot-swap log.level / log.format; for storage,
 * memory, models, listen_addr, audit settings, the user has to
 * `loamss start` again. This banner names the paths so they know
 * exactly what changed.
 *
 * We deliberately avoid an "auto-restart" button. Restarting the
 * daemon mid-flight would terminate in-flight tool calls and
 * drop SSE subscriptions; doing that as a side-effect of a
 * banner click would be worse UX than the explicit terminal
 * command. The right place for "graceful restart" is a future
 * commit that handles fd handoff + drain semantics properly.
 */
function RestartBanner({ paths }: RestartBannerProps) {
  return (
    <div className="mt-10 border-l-2 border-amber bg-amber-tint/30 pl-5 pr-5 py-4 rounded-r-sm">
      <div className="flex items-start gap-4">
        <div className="flex-1 min-w-0">
          <div className="smallcap text-amber">Restart required</div>
          <p className="mt-1 text-sm text-ink-muted leading-relaxed">
            The config file on disk has changed in ways the running
            daemon can&apos;t hot-apply. Restart with{" "}
            <span className="font-mono text-2xs text-ink">loamss start</span>{" "}
            to pick up the new settings.
          </p>
          <ul className="mt-2 flex flex-wrap gap-x-3 gap-y-1">
            {paths.map((p) => (
              <li
                key={p}
                className="font-mono text-2xs text-ink-quiet"
              >
                {p}
              </li>
            ))}
          </ul>
        </div>
      </div>
    </div>
  );
}

interface RuntimeBadgeProps {
  state: ReturnType<typeof useDashboard.getState>["state"];
  error: ReturnType<typeof useDashboard.getState>["error"];
}

function RuntimeBadge({ state, error }: RuntimeBadgeProps) {
  if (error) {
    return (
      <span className="flex items-center gap-2 text-xs text-ink-quiet">
        <span className="inline-block w-1.5 h-1.5 rounded-full bg-amber animate-pulse-soft" />
        <span className="font-mono text-2xs">runtime · unreachable</span>
      </span>
    );
  }
  if (!state) {
    return (
      <span className="flex items-center gap-2 text-xs text-ink-quiet">
        <span className="inline-block w-1.5 h-1.5 rounded-full bg-ink-ghost" />
        <span className="font-mono text-2xs">probing…</span>
      </span>
    );
  }
  return (
    <span className="flex items-center gap-2 text-xs text-ink-muted">
      <span className="inline-block w-1.5 h-1.5 rounded-full bg-sage" />
      <span className="font-mono text-2xs">runtime · {state.runtime.version}</span>
    </span>
  );
}

/**
 * humanizeUptime turns "uptime_seconds" into a short human string.
 * We don't bother with a date library — the unit set is tiny and
 * the precision tolerable.
 */
function humanizeUptime(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h`;
  const days = Math.floor(hours / 24);
  return `${days}d`;
}

/**
 * relativeAge turns an ISO timestamp into "5s ago" / "2m ago".
 * Tolerant of clock skew between client and runtime — if the
 * "ago" computes to a negative, we show "just now".
 */
function relativeAge(iso: string): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "unknown";
  const elapsed = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (elapsed < 5) return "just now";
  if (elapsed < 60) return `${elapsed}s ago`;
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m ago`;
  return `${Math.floor(elapsed / 3600)}h ago`;
}
