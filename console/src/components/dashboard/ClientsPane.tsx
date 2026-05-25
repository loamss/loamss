"use client";

import { useState } from "react";
import type { ConsoleState } from "@/lib/runtime-client";
import { revokeClient } from "@/lib/runtime-client";
import { useDashboard } from "@/lib/dashboard-state";
import { PairAppModal } from "./PairAppModal";
import { EmptyState, Pane, UnavailablePane } from "./Pane";

/*
 * ClientsPane — paired apps (Claude Desktop, ChatGPT, custom MCP
 * clients) that hold a credential against this runtime.
 *
 * Per row:
 *   <dot>  <name>  ·  paired/last-seen   ·  active|revoked   [revoke]
 *
 * Revoked clients stay in the list (the audit log promise: nothing
 * is hidden). Their dot is muted so they can't be mistaken for
 * attached ones, and the "revoke" action disappears (there's
 * nothing left to do).
 */

interface ClientsPaneProps {
  block: ConsoleState["clients"] | undefined;
}

export function ClientsPane({ block }: ClientsPaneProps) {
  const [pairOpen, setPairOpen] = useState(false);
  const refresh = useDashboard((s) => s.refresh);

  const action = (
    <button
      type="button"
      onClick={() => setPairOpen(true)}
      className="text-xs text-brand hover:text-brand-deep underline underline-offset-2"
    >
      + Pair an app
    </button>
  );

  if (!block) {
    return (
      <Pane eyebrow="Apps" action={action}>
        <PaneSkeleton />
      </Pane>
    );
  }
  if (!block.available) {
    return (
      <Pane eyebrow="Apps" action={action}>
        <UnavailablePane />
      </Pane>
    );
  }

  return (
    <>
      <Pane eyebrow="Apps" action={action}>
        {block.items.length === 0 ? (
          <EmptyState
            message="No apps paired yet. Pairing gives an external client — Claude, ChatGPT, a custom MCP-speaking tool — a credential to read your memory under the permissions you grant."
            hint="loamss client pair  (or click + Pair an app)"
          />
        ) : (
          <ul className="divide-y divide-ink-hairline-soft">
            {block.items.map((c) => (
              <ClientRow key={c.id} client={c} />
            ))}
          </ul>
        )}
      </Pane>
      {pairOpen && (
        <PairAppModal
          onClose={() => setPairOpen(false)}
          onPaired={() => void refresh({ manual: true })}
        />
      )}
    </>
  );
}

interface ClientRowProps {
  client: ConsoleState["clients"]["items"][number];
}

type RowAction = "idle" | "revoking";

function ClientRow({ client }: ClientRowProps) {
  const refresh = useDashboard((s) => s.refresh);
  const [action, setAction] = useState<RowAction>("idle");
  const [error, setError] = useState<string | null>(null);

  async function handleRevoke() {
    if (
      !window.confirm(
        `Revoke app "${client.name}"? Its bearer token stops working immediately, ` +
          `and every grant attached to it is revoked. The audit log keeps the trail.`,
      )
    ) {
      return;
    }
    setError(null);
    setAction("revoking");
    const result = await revokeClient(client.id);
    if (!result.ok) {
      setError(result.reason);
      setAction("idle");
      return;
    }
    void refresh({ manual: true });
  }

  return (
    <li className="py-3">
      <div className="flex items-baseline gap-4">
        <span
          className={[
            "inline-block w-1.5 h-1.5 rounded-full flex-none translate-y-[-2px]",
            client.active ? "bg-sage" : "bg-ink-ghost",
          ].join(" ")}
          aria-label={client.active ? "active" : "revoked"}
        />
        <div className="flex-1 min-w-0">
          <div className="flex flex-wrap items-baseline gap-x-3">
            <span className="text-sm text-ink">{client.name}</span>
            {!client.active && (
              <span className="font-mono text-2xs text-ink-quiet">
                revoked
              </span>
            )}
          </div>
          <div className="mt-0.5 font-mono text-2xs text-ink-quiet">
            paired {relativeAge(client.paired_at)}
            {client.last_seen_at &&
              ` · last seen ${relativeAge(client.last_seen_at)}`}
          </div>
        </div>
        {client.active && (
          <button
            type="button"
            onClick={handleRevoke}
            disabled={action === "revoking"}
            className="text-xs text-ink-quiet hover:text-brick underline underline-offset-2 disabled:opacity-40 disabled:no-underline flex-none"
          >
            {action === "revoking" ? "revoking…" : "revoke"}
          </button>
        )}
      </div>
      {error && (
        <div className="mt-2 font-mono text-2xs text-brick">{error}</div>
      )}
    </li>
  );
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
