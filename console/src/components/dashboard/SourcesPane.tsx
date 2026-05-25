"use client";

import type { ConsoleState } from "@/lib/runtime-client";
import { EmptyState, Pane, UnavailablePane, statusDotColor } from "./Pane";

/*
 * SourcesPane — list of configured sources.
 *
 * What the user sees per row:
 *
 *   <dot>  <name>  ·  <adapter>  ·  <last-sync hint>
 *                                    summary metadata when present
 *
 * The hint is gracious: "synced 5m ago", "never synced", or
 * "failed · 30s ago". The summary line carries any adapter-
 * specific counters the connector wrote (entries_added,
 * messages_seen, etc.) without us hardcoding any per-source
 * vocabulary in the console.
 */

interface SourcesPaneProps {
  block: ConsoleState["sources"] | undefined;
}

export function SourcesPane({ block }: SourcesPaneProps) {
  if (!block) {
    return (
      <Pane eyebrow="Sources">
        <PaneSkeleton rows={2} />
      </Pane>
    );
  }
  if (!block.available) {
    return (
      <Pane eyebrow="Sources">
        <UnavailablePane />
      </Pane>
    );
  }
  if (block.items.length === 0) {
    return (
      <Pane eyebrow="Sources">
        <EmptyState
          message="No sources yet. Sources pull data into your storage — files, a calendar, a mailbox — so the memory layer has something to organise."
          hint="loamss source add source:files --name my-files --config root=$HOME/Documents"
        />
      </Pane>
    );
  }

  return (
    <Pane eyebrow="Sources">
      <ul className="divide-y divide-ink-hairline-soft">
        {block.items.map((s) => (
          <li key={s.id} className="py-3 flex items-baseline gap-4">
            <span
              className={[
                "inline-block w-1.5 h-1.5 rounded-full flex-none translate-y-[-2px]",
                dotClass(statusDotColor(s.last_sync_status || "")),
              ].join(" ")}
              aria-label={s.last_sync_status || "never synced"}
            />
            <div className="flex-1 min-w-0">
              <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                <span className="text-sm text-ink">{s.name}</span>
                <span className="font-mono text-2xs text-ink-quiet">
                  {s.adapter}
                </span>
              </div>
              <div className="mt-0.5 font-mono text-2xs text-ink-quiet">
                {syncHint(s.last_sync_status, s.last_sync_at)}
                {renderSummary(s.summary)}
              </div>
            </div>
          </li>
        ))}
      </ul>
    </Pane>
  );
}

function syncHint(status: string, at?: string): string {
  if (!at) return "never synced";
  const elapsed = Math.max(0, Math.floor((Date.now() - new Date(at).getTime()) / 1000));
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
  // Pick a small set of integer counters most connectors emit.
  // Other shapes ride through as " · k=v" pairs, capped at three
  // so the line stays readable.
  const parts: string[] = [];
  for (const [k, v] of Object.entries(summary)) {
    if (parts.length >= 3) break;
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

/**
 * PaneSkeleton renders inert hairline rows while the first fetch
 * is in flight. Keep it minimal — flashing skeleton content is
 * worse than a small mono "loading…" tag for our aesthetic.
 */
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
