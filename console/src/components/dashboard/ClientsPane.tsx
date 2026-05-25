"use client";

import type { ConsoleState } from "@/lib/runtime-client";
import { EmptyState, Pane, UnavailablePane } from "./Pane";

/*
 * ClientsPane — paired apps (Claude Desktop, ChatGPT, custom MCP
 * clients) that hold a credential against this runtime.
 *
 * We surface name + paired-at + last-seen + active/revoked. The
 * "active" flag is the runtime's authoritative state — a revoked
 * client stays in the list (you might want to re-pair) but renders
 * with a muted dot so it can't be mistaken for an attached one.
 */

interface ClientsPaneProps {
  block: ConsoleState["clients"] | undefined;
}

export function ClientsPane({ block }: ClientsPaneProps) {
  if (!block) {
    return (
      <Pane eyebrow="Apps">
        <PaneSkeleton />
      </Pane>
    );
  }
  if (!block.available) {
    return (
      <Pane eyebrow="Apps">
        <UnavailablePane />
      </Pane>
    );
  }
  if (block.items.length === 0) {
    return (
      <Pane eyebrow="Apps">
        <EmptyState
          message="No apps paired yet. Pairing gives an external client — Claude, ChatGPT, a custom MCP-speaking tool — a credential to read your memory under the permissions you grant."
          hint="loamss client pair  # produces a code the app redeems"
        />
      </Pane>
    );
  }

  return (
    <Pane eyebrow="Apps">
      <ul className="divide-y divide-ink-hairline-soft">
        {block.items.map((c) => (
          <li key={c.id} className="py-3 flex items-baseline gap-4">
            <span
              className={[
                "inline-block w-1.5 h-1.5 rounded-full flex-none translate-y-[-2px]",
                c.active ? "bg-sage" : "bg-ink-ghost",
              ].join(" ")}
              aria-label={c.active ? "active" : "revoked"}
            />
            <div className="flex-1 min-w-0">
              <div className="flex flex-wrap items-baseline gap-x-3">
                <span className="text-sm text-ink">{c.name}</span>
                {!c.active && (
                  <span className="font-mono text-2xs text-ink-quiet">
                    revoked
                  </span>
                )}
              </div>
              <div className="mt-0.5 font-mono text-2xs text-ink-quiet">
                paired {relativeAge(c.paired_at)}
                {c.last_seen_at &&
                  ` · last seen ${relativeAge(c.last_seen_at)}`}
              </div>
            </div>
          </li>
        ))}
      </ul>
    </Pane>
  );
}

function relativeAge(iso: string): string {
  const elapsed = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
  if (elapsed < 60) return `${elapsed}s ago`;
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m ago`;
  if (elapsed < 86400) return `${Math.floor(elapsed / 3600)}h ago`;
  return `${Math.floor(elapsed / 86400)}d ago`;
}

function PaneSkeleton() {
  return (
    <ul className="divide-y divide-ink-hairline-soft">
      {[1, 2].map((i) => (
        <li key={i} className="py-4">
          <div className="h-3 w-36 bg-ink-hairline-soft rounded-sm" />
        </li>
      ))}
    </ul>
  );
}
