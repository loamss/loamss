"use client";

import { Note } from "@/components/primitives/Note";
import type { ConsoleState } from "@/lib/runtime-client";

/*
 * ApprovalsPane — grants awaiting the user's OK.
 *
 * This is the runtime's pendi nig-approval queue: a capsule or
 * client asked for a capability that wasn't already granted, and
 * the runtime is holding the request until a human decides. The
 * dashboard only renders this pane when items.length > 0, so by
 * design its visual weight is loud the moment it appears.
 *
 * Today this is read-only. Approving / denying from the console
 * lands in the approval surface commit (separate work — needs an
 * approve/deny HTTP endpoint behind the same localhost contract).
 * The pane points the user at the CLI command that exists.
 */

interface ApprovalsPaneProps {
  items: ConsoleState["approvals_pending"]["items"];
}

export function ApprovalsPane({ items }: ApprovalsPaneProps) {
  return (
    <section className="border-l-2 border-amber bg-amber-tint/30 pl-5 pr-5 py-5 rounded-r-sm">
      <header className="flex items-baseline justify-between gap-3">
        <div>
          <div className="smallcap text-amber">Pending approvals</div>
          <div className="mt-1 font-serif text-2xl text-ink">
            {items.length === 1
              ? "One request needs your OK."
              : `${items.length} requests need your OK.`}
          </div>
        </div>
        <Note kind="warn" className="!py-1.5 !px-2.5 max-w-prose">
          Approve from a terminal:{" "}
          <span className="font-mono text-2xs">loamss approve list</span>
        </Note>
      </header>
      <ul className="mt-5 space-y-3">
        {items.map((a) => (
          <li
            key={a.id}
            className="border border-ink-hairline-soft bg-paper rounded-sm px-4 py-3"
          >
            <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
              <span className="font-mono text-xs text-ink">
                {a.principal_kind}:{a.principal_id}
              </span>
              <span className="text-ink-quiet">requests</span>
              <span className="font-mono text-xs text-ink">
                {a.capability}
              </span>
            </div>
            {a.rationale && (
              <div className="mt-1 text-sm text-ink-muted leading-relaxed">
                {a.rationale}
              </div>
            )}
            <div className="mt-1 font-mono text-2xs text-ink-quiet">
              requested {relativeAge(a.requested_at)} · approve with{" "}
              <span className="text-ink-muted">loamss approve {a.id}</span>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}

function relativeAge(iso: string): string {
  const elapsed = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
  if (elapsed < 60) return `${elapsed}s ago`;
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m ago`;
  if (elapsed < 86400) return `${Math.floor(elapsed / 3600)}h ago`;
  return `${Math.floor(elapsed / 86400)}d ago`;
}
