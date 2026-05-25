"use client";

import type { ConsoleState } from "@/lib/runtime-client";
import { EmptyState, Pane, UnavailablePane } from "./Pane";

/*
 * ActivityPane — the right-rail feed of recent audit events.
 *
 * The audit log is the design's promise made tangible — "every
 * external read and every external write gets logged, and the
 * audit log is a first-class user-facing surface." This pane is
 * the human-scale window into it.
 *
 * Each row: a small outcome dot, the type in mono, the subject
 * (when present), and a relative-time stamp. Wide enough at top
 * of the screen on desktop, scrolls within its own column on
 * smaller heights without growing the page.
 *
 * Outcome maps:
 *   success  → sage
 *   denied   → amber
 *   error    → brick
 *   pending  → quiet pulse
 *   n/a      → quiet (no dot color)
 */

interface ActivityPaneProps {
  block: ConsoleState["activity"] | undefined;
}

export function ActivityPane({ block }: ActivityPaneProps) {
  if (!block) {
    return (
      <Pane eyebrow="Activity">
        <PaneSkeleton />
      </Pane>
    );
  }
  if (!block.available) {
    return (
      <Pane eyebrow="Activity">
        <UnavailablePane />
      </Pane>
    );
  }
  if (block.items.length === 0) {
    return (
      <Pane eyebrow="Activity">
        <EmptyState
          message="Nothing yet. Once sources sync, capsules run, or apps query memory, every action will land here with the actor, subject, and outcome."
          hint="loamss audit tail  # streams the same events to a terminal"
        />
      </Pane>
    );
  }

  return (
    <Pane eyebrow="Activity">
      <ul className="space-y-3 max-h-[42rem] overflow-y-auto pr-2">
        {block.items.map((e) => (
          <li key={e.id} className="flex items-baseline gap-3">
            <span
              className={[
                "inline-block w-1.5 h-1.5 rounded-full flex-none translate-y-[-2px]",
                outcomeDotClass(e.outcome),
              ].join(" ")}
              aria-label={e.outcome}
            />
            <div className="flex-1 min-w-0">
              <div className="font-mono text-xs text-ink truncate">
                {e.type}
              </div>
              <div className="font-mono text-2xs text-ink-quiet truncate">
                {actorSummary(e)}
              </div>
              <div className="font-mono text-2xs text-ink-quiet">
                {relativeAge(e.at)}
              </div>
            </div>
          </li>
        ))}
      </ul>
    </Pane>
  );
}

function actorSummary(e: ConsoleState["activity"]["items"][number]): string {
  const actor = `${e.actor_kind}:${e.actor_id}`;
  if (e.subject_kind && e.subject_id) {
    return `${actor} → ${e.subject_kind}:${e.subject_id}`;
  }
  return actor;
}

function outcomeDotClass(outcome: string): string {
  switch (outcome) {
    case "success":
      return "bg-sage";
    case "denied":
      return "bg-amber";
    case "error":
      return "bg-brick";
    case "pending":
      return "bg-amber animate-pulse-soft";
    default:
      return "bg-ink-ghost";
  }
}

function relativeAge(iso: string): string {
  const elapsed = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
  if (elapsed < 5) return "just now";
  if (elapsed < 60) return `${elapsed}s ago`;
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m ago`;
  if (elapsed < 86400) return `${Math.floor(elapsed / 3600)}h ago`;
  return `${Math.floor(elapsed / 86400)}d ago`;
}

function PaneSkeleton() {
  return (
    <ul className="space-y-3">
      {[1, 2, 3, 4].map((i) => (
        <li key={i} className="space-y-1">
          <div className="h-3 w-28 bg-ink-hairline-soft rounded-sm" />
          <div className="h-2 w-40 bg-ink-hairline-soft rounded-sm" />
        </li>
      ))}
    </ul>
  );
}
