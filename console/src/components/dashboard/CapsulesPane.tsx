"use client";

import type { ConsoleState } from "@/lib/runtime-client";
import { EmptyState, Pane, UnavailablePane } from "./Pane";

/*
 * CapsulesPane — installed capsules + which are currently running.
 *
 * Row layout: <name@version> on the left, a "running" / "stopped"
 * tag on the right, optional permission summary underneath (first
 * three capabilities + "+ N more" when there's more).
 *
 * We don't show permission count alone — counts are a poor proxy
 * for "what can this capsule do." Names like "memory.read" are
 * worth the line.
 */

interface CapsulesPaneProps {
  block: ConsoleState["capsules"] | undefined;
}

export function CapsulesPane({ block }: CapsulesPaneProps) {
  if (!block) {
    return (
      <Pane eyebrow="Capsules">
        <ul className="divide-y divide-ink-hairline-soft">
          {[1, 2].map((i) => (
            <li key={i} className="py-4">
              <div className="h-3 w-44 bg-ink-hairline-soft rounded-sm" />
            </li>
          ))}
        </ul>
      </Pane>
    );
  }
  if (!block.available) {
    return (
      <Pane eyebrow="Capsules">
        <UnavailablePane />
      </Pane>
    );
  }
  if (block.items.length === 0) {
    return (
      <Pane eyebrow="Capsules">
        <EmptyState
          message="No capsules installed. Capsules run inside the runtime under a permission slip — daily briefings, organisers, custom tools — anything you write to the SDK and the runtime gives a name to."
          hint="loamss capsule install ./path/to/capsule"
        />
      </Pane>
    );
  }

  return (
    <Pane eyebrow="Capsules">
      <ul className="divide-y divide-ink-hairline-soft">
        {block.items.map((c) => (
          <li key={c.id} className="py-3">
            <div className="flex items-baseline justify-between gap-4">
              <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 min-w-0">
                <span className="text-sm text-ink truncate">{c.name}</span>
                <span className="font-mono text-2xs text-ink-quiet">
                  v{c.version}
                </span>
                {c.author && (
                  <span className="font-mono text-2xs text-ink-quiet">
                    · {c.author}
                  </span>
                )}
              </div>
              <span
                className={[
                  "inline-flex items-center gap-1.5 text-2xs font-mono flex-none",
                  c.running ? "text-sage" : "text-ink-quiet",
                ].join(" ")}
              >
                <span
                  className={[
                    "inline-block w-1.5 h-1.5 rounded-full",
                    c.running ? "bg-sage" : "bg-ink-ghost",
                  ].join(" ")}
                />
                {c.running ? "running" : "stopped"}
              </span>
            </div>
            {c.permissions.length > 0 && (
              <div className="mt-1 font-mono text-2xs text-ink-quiet truncate">
                {summarizePermissions(c.permissions)}
              </div>
            )}
          </li>
        ))}
      </ul>
    </Pane>
  );
}

function summarizePermissions(perms: string[]): string {
  if (perms.length <= 3) return perms.join(" · ");
  const visible = perms.slice(0, 3);
  return `${visible.join(" · ")} · +${perms.length - 3} more`;
}
