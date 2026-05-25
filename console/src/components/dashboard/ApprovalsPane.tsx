"use client";

import { useState } from "react";
import { Button } from "@/components/primitives/Button";
import type { ConsoleState } from "@/lib/runtime-client";
import { resolveApproval } from "@/lib/runtime-client";
import { useDashboard } from "@/lib/dashboard-state";

/*
 * ApprovalsPane — pending grants awaiting the user's OK.
 *
 * This is the design's most consequential surface: the moment a
 * capsule or client asks for a capability that wasn't pre-granted,
 * the runtime holds the request and waits for a human decision.
 * Making this one-click (from the dashboard) instead of CLI-only
 * is what the dashboard was for.
 *
 * Layout:
 *
 *   <eyebrow + headline>                  [terminal command hint]
 *
 *   ┌ principal:id   requests   capability ┐
 *   │ rationale (capsule author's words)    │
 *   │ scope details when present            │
 *   │ requested 3m ago                      │
 *   │ [optional note input]                 │
 *   │ [Deny] [Approve]                      │
 *   └────────────────────────────────────────┘
 *
 * The decision buttons are intentionally small — this is a thing
 * the user does, not a thing the design promotes. The Approve
 * button is brand-coloured; Deny is the muted "this needs deliberate
 * action" treatment so accidental approval is less likely than
 * accidental denial.
 *
 * Race handling: if two deciders click at the same time (or a user
 * double-clicks), the second hits 409. We surface that inline as
 * "already resolved" and trigger an immediate refresh so the row
 * vanishes from the pane on the next render.
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
        <span className="font-mono text-2xs text-ink-quiet hidden sm:inline">
          or: loamss approve list
        </span>
      </header>
      <ul className="mt-5 space-y-3">
        {items.map((a) => (
          <ApprovalRow key={a.id} approval={a} />
        ))}
      </ul>
    </section>
  );
}

interface ApprovalRowProps {
  approval: ConsoleState["approvals_pending"]["items"][number];
}

type RowAction = "idle" | "approving" | "denying" | "done";

function ApprovalRow({ approval }: ApprovalRowProps) {
  const refresh = useDashboard((s) => s.refresh);
  const [action, setAction] = useState<RowAction>("idle");
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState("");
  const [noteOpen, setNoteOpen] = useState(false);

  async function decide(decision: "approve" | "deny") {
    setError(null);
    setAction(decision === "approve" ? "approving" : "denying");
    const result = await resolveApproval(approval.id, decision, {
      note: note.trim() || undefined,
    });
    if (!result.ok) {
      setError(humaniseError(result));
      setAction("idle");
      // On conflict, refresh anyway so the user sees the row vanish
      // (someone else resolved it).
      if (result.kind === "conflict" || result.kind === "not-found") {
        void refresh({ manual: true });
      }
      return;
    }
    setAction("done");
    void refresh({ manual: true });
  }

  const busy = action === "approving" || action === "denying";

  return (
    <li className="border border-ink-hairline-soft bg-paper rounded-sm px-4 py-3">
      <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <span className="font-mono text-xs text-ink">
          {approval.principal_kind}:{approval.principal_id}
        </span>
        <span className="text-ink-quiet">requests</span>
        <span className="font-mono text-xs text-ink">
          {approval.capability}
        </span>
      </div>
      {approval.rationale && (
        <div className="mt-1 text-sm text-ink-muted leading-relaxed">
          {approval.rationale}
        </div>
      )}
      {approval.scope && Object.keys(approval.scope).length > 0 && (
        <pre className="mt-2 font-mono text-2xs text-ink-quiet bg-paper-deep/30 border border-ink-hairline-soft rounded-sm px-2 py-1.5 overflow-x-auto">
          {JSON.stringify(approval.scope, null, 2)}
        </pre>
      )}
      <div className="mt-1 font-mono text-2xs text-ink-quiet">
        requested {relativeAge(approval.requested_at)}
        {!noteOpen && (
          <>
            {" · "}
            <button
              type="button"
              onClick={() => setNoteOpen(true)}
              className="text-ink-quiet hover:text-ink-muted underline underline-offset-2"
              disabled={busy || action === "done"}
            >
              add note
            </button>
          </>
        )}
      </div>
      {noteOpen && (
        <div className="mt-2">
          <label
            htmlFor={`note-${approval.id}`}
            className="smallcap text-ink-quiet block mb-1"
          >
            Decision note (optional)
          </label>
          <input
            id={`note-${approval.id}`}
            type="text"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="why?"
            disabled={busy || action === "done"}
            className="w-full font-mono text-xs bg-paper border border-ink-hairline-soft focus:border-brand focus:ring-1 focus:ring-brand/20 rounded-sm px-2 py-1.5 outline-none transition-colors"
          />
        </div>
      )}
      {error && (
        <div className="mt-2 font-mono text-2xs text-brick">{error}</div>
      )}
      <div className="mt-3 flex items-center justify-end gap-2">
        <button
          type="button"
          onClick={() => decide("deny")}
          disabled={busy || action === "done"}
          className="text-xs text-ink-quiet hover:text-brick underline underline-offset-2 disabled:opacity-40 disabled:no-underline px-2"
        >
          {action === "denying" ? "denying…" : "deny"}
        </button>
        <Button
          type="button"
          size="sm"
          variant="primary"
          onClick={() => decide("approve")}
          disabled={busy || action === "done"}
        >
          {action === "approving"
            ? "approving…"
            : action === "done"
              ? "resolved"
              : "approve"}
        </Button>
      </div>
    </li>
  );
}

function humaniseError(
  result: Exclude<Awaited<ReturnType<typeof resolveApproval>>, { ok: true }>,
): string {
  switch (result.kind) {
    case "conflict":
      return "Already resolved — possibly by another decider or via the CLI. Refreshing.";
    case "not-found":
      return "This request no longer exists. Refreshing.";
    default:
      return result.reason;
  }
}

function relativeAge(iso: string): string {
  const elapsed = Math.max(
    0,
    Math.floor((Date.now() - new Date(iso).getTime()) / 1000),
  );
  if (elapsed < 60) return `${elapsed}s ago`;
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m ago`;
  if (elapsed < 86400) return `${Math.floor(elapsed / 3600)}h ago`;
  return `${Math.floor(elapsed / 86400)}d ago`;
}
