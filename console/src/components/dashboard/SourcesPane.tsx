"use client";

import { useState } from "react";
import type { ConsoleState } from "@/lib/runtime-client";
import { deleteSource, syncSource } from "@/lib/runtime-client";
import { useDashboard } from "@/lib/dashboard-state";
import { AddSourceModal } from "./AddSourceModal";
import { EmptyState, Pane, UnavailablePane, statusDotColor } from "./Pane";

/*
 * SourcesPane — list of configured sources, now with mutations.
 *
 * Per row:
 *   <dot>  <name>  ·  <adapter>  ·  <last-sync hint>     [Sync] [Disconnect]
 *
 * Sync triggers POST /console/sources/{name}/sync, which is async
 * on the server side. We optimistically set `pending` on the row's
 * action state so the button shows a spinner immediately; the
 * dashboard's next /console/state poll picks up the real status
 * transition (running → success/error).
 *
 * Disconnect runs a small confirm prompt first. The audit log
 * keeps the record forever; the row is just gone from the UI on
 * success.
 */

interface SourcesPaneProps {
  block: ConsoleState["sources"] | undefined;
}

export function SourcesPane({ block }: SourcesPaneProps) {
  const [addOpen, setAddOpen] = useState(false);
  const refresh = useDashboard((s) => s.refresh);

  const action = (
    <button
      type="button"
      onClick={() => setAddOpen(true)}
      className="text-xs text-brand hover:text-brand-deep underline underline-offset-2"
    >
      + Add source
    </button>
  );

  if (!block) {
    return (
      <Pane eyebrow="Sources" action={action}>
        <PaneSkeleton rows={2} />
      </Pane>
    );
  }
  if (!block.available) {
    return (
      <Pane eyebrow="Sources" action={action}>
        <UnavailablePane />
      </Pane>
    );
  }

  return (
    <>
      <Pane eyebrow="Sources" action={action}>
        {block.items.length === 0 ? (
          <EmptyState
            message="No sources yet. A source pulls data into your storage — local files, a calendar, a mailbox — so the memory layer has something to organise. Click +Add source to wire one up."
            hint="loamss source add source:files --name docs --config root=$HOME/Documents"
          />
        ) : (
          <ul className="divide-y divide-ink-hairline-soft">
            {block.items.map((s) => (
              <SourceRow key={s.id} source={s} />
            ))}
          </ul>
        )}
      </Pane>
      {addOpen && (
        <AddSourceModal
          onClose={() => setAddOpen(false)}
          onAdded={() => {
            // Trigger an immediate refresh so the row appears
            // without waiting for the next 8s tick. Manual=true
            // shows the refresh spinner in the footer.
            void refresh({ manual: true });
          }}
        />
      )}
    </>
  );
}

interface SourceRowProps {
  source: ConsoleState["sources"]["items"][number];
}

type RowAction = "idle" | "syncing" | "deleting" | "error";

function SourceRow({ source }: SourceRowProps) {
  const refresh = useDashboard((s) => s.refresh);
  const [action, setAction] = useState<RowAction>("idle");
  const [actionError, setActionError] = useState<string | null>(null);

  const isRunning =
    source.last_sync_status === "running" || action === "syncing";

  async function handleSync() {
    setActionError(null);
    setAction("syncing");
    const result = await syncSource(source.name);
    if (!result.ok) {
      // The server didn't accept the sync. Surface the reason but
      // don't pin the row in syncing — the next refresh will reset
      // it to whatever the runtime says.
      setActionError(result.reason);
      setAction("error");
      // Trigger a refresh so the user sees the actual server-side
      // state immediately.
      void refresh({ manual: true });
      return;
    }
    // 202 accepted; the row's last_sync_status will flip via the
    // next /console/state poll. We keep the action label set to
    // "syncing" until the row's server-side status changes, but
    // also trigger an immediate refresh to shorten the gap.
    void refresh({ manual: true });
    setAction("idle"); // status pill will pick up "running" from server
  }

  async function handleDelete() {
    if (
      !window.confirm(
        `Disconnect source "${source.name}"? Its credentials will be cleared. ` +
          `The audit log keeps the record of every previous sync.`,
      )
    ) {
      return;
    }
    setActionError(null);
    setAction("deleting");
    const result = await deleteSource(source.name);
    if (!result.ok) {
      setActionError(result.reason);
      setAction("error");
      return;
    }
    void refresh({ manual: true });
    // No need to reset action; the row is about to vanish.
  }

  return (
    <li className="py-3">
      <div className="flex items-baseline gap-4">
        <span
          className={[
            "inline-block w-1.5 h-1.5 rounded-full flex-none translate-y-[-2px]",
            dotClass(statusDotColor(source.last_sync_status || "")),
          ].join(" ")}
          aria-label={source.last_sync_status || "never synced"}
        />
        <div className="flex-1 min-w-0">
          <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <span className="text-sm text-ink">{source.name}</span>
            <span className="font-mono text-2xs text-ink-quiet">
              {source.adapter}
            </span>
          </div>
          <div className="mt-0.5 font-mono text-2xs text-ink-quiet">
            {syncHint(source.last_sync_status, source.last_sync_at)}
            {renderSummary(source.summary)}
          </div>
        </div>
        <div className="flex items-center gap-3 flex-none">
          <button
            type="button"
            onClick={handleSync}
            disabled={isRunning || action === "deleting"}
            className="text-xs text-ink-muted hover:text-ink underline underline-offset-2 disabled:opacity-40 disabled:no-underline"
          >
            {isRunning ? "syncing…" : "sync"}
          </button>
          <button
            type="button"
            onClick={handleDelete}
            disabled={action === "deleting" || action === "syncing"}
            className="text-xs text-ink-quiet hover:text-brick underline underline-offset-2 disabled:opacity-40 disabled:no-underline"
          >
            {action === "deleting" ? "removing…" : "disconnect"}
          </button>
        </div>
      </div>
      {actionError && (
        <div className="mt-2 font-mono text-2xs text-brick">
          {actionError}
        </div>
      )}
    </li>
  );
}

function syncHint(status: string, at?: string): string {
  if (!at) return "never synced";
  const elapsed = Math.max(
    0,
    Math.floor((Date.now() - new Date(at).getTime()) / 1000),
  );
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
  const parts: string[] = [];
  for (const [k, v] of Object.entries(summary)) {
    if (parts.length >= 3) break;
    // Skip the verbose timestamp / error_message keys — they're
    // informative but blow up the line. The status hint already
    // carries the "is it ok?" signal.
    if (k === "started" || k === "finished" || k === "error_message") continue;
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
