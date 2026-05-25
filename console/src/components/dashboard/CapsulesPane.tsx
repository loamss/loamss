"use client";

import { useState } from "react";
import type { ConsoleState } from "@/lib/runtime-client";
import {
  startCapsule,
  stopCapsule,
  uninstallCapsule,
} from "@/lib/runtime-client";
import { useDashboard } from "@/lib/dashboard-state";
import { InstallCapsuleModal } from "./InstallCapsuleModal";
import { EmptyState, Pane, UnavailablePane } from "./Pane";

/*
 * CapsulesPane — installed capsules + lifecycle controls.
 *
 * Per row layout:
 *
 *   <name@version>  [running tag]                     [stop|start] [uninstall]
 *   permissions...
 *   inline error if last action failed
 *
 * The Stop/Start label flips based on the capsule's running state;
 * Uninstall asks for confirmation because the audit log keeps the
 * record but the grants + files are unrecoverable.
 */

interface CapsulesPaneProps {
  block: ConsoleState["capsules"] | undefined;
}

export function CapsulesPane({ block }: CapsulesPaneProps) {
  const [installOpen, setInstallOpen] = useState(false);
  const refresh = useDashboard((s) => s.refresh);

  const action = (
    <button
      type="button"
      onClick={() => setInstallOpen(true)}
      className="text-xs text-brand hover:text-brand-deep underline underline-offset-2"
    >
      + Install capsule
    </button>
  );

  if (!block) {
    return (
      <Pane eyebrow="Capsules" action={action}>
        <PaneSkeleton />
      </Pane>
    );
  }
  if (!block.available) {
    return (
      <Pane eyebrow="Capsules" action={action}>
        <UnavailablePane />
      </Pane>
    );
  }

  return (
    <>
      <Pane eyebrow="Capsules" action={action}>
        {block.items.length === 0 ? (
          <EmptyState
            message="No capsules installed. Capsules run inside the runtime under a permission slip — daily briefings, organisers, custom tools, anything you write to the SDK and the runtime gives a name to."
            hint="loamss capsule install ./path/to/capsule  (or click + Install capsule)"
          />
        ) : (
          <ul className="divide-y divide-ink-hairline-soft">
            {block.items.map((c) => (
              <CapsuleRow key={c.id} capsule={c} />
            ))}
          </ul>
        )}
      </Pane>
      {installOpen && (
        <InstallCapsuleModal
          onClose={() => setInstallOpen(false)}
          onInstalled={() => void refresh({ manual: true })}
        />
      )}
    </>
  );
}

interface CapsuleRowProps {
  capsule: ConsoleState["capsules"]["items"][number];
}

type RowAction = "idle" | "starting" | "stopping" | "uninstalling";

function CapsuleRow({ capsule }: CapsuleRowProps) {
  const refresh = useDashboard((s) => s.refresh);
  const [action, setAction] = useState<RowAction>("idle");
  const [error, setError] = useState<string | null>(null);

  async function handleStartStop() {
    setError(null);
    if (capsule.running) {
      setAction("stopping");
      const result = await stopCapsule(capsule.name);
      if (!result.ok) {
        setError(result.reason);
        setAction("idle");
        return;
      }
    } else {
      setAction("starting");
      const result = await startCapsule(capsule.name);
      if (!result.ok) {
        setError(result.reason);
        setAction("idle");
        return;
      }
    }
    void refresh({ manual: true });
    setAction("idle");
  }

  async function handleUninstall() {
    if (
      !window.confirm(
        `Uninstall capsule "${capsule.name}"? Its grants will be revoked and its files removed. ` +
          `The audit log keeps every prior action.`,
      )
    ) {
      return;
    }
    setError(null);
    setAction("uninstalling");
    const result = await uninstallCapsule(capsule.name);
    if (!result.ok) {
      setError(result.reason);
      setAction("idle");
      return;
    }
    void refresh({ manual: true });
    // Row is about to vanish — no need to reset state.
  }

  const lifecycleBusy = action === "starting" || action === "stopping";
  const lifecycleLabel = capsule.running
    ? action === "stopping"
      ? "stopping…"
      : "stop"
    : action === "starting"
      ? "starting…"
      : "start";

  return (
    <li className="py-3">
      <div className="flex items-baseline justify-between gap-4">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 min-w-0 flex-1">
          <span className="text-sm text-ink truncate">{capsule.name}</span>
          <span className="font-mono text-2xs text-ink-quiet">
            v{capsule.version}
          </span>
          {capsule.author && (
            <span className="font-mono text-2xs text-ink-quiet">
              · {capsule.author}
            </span>
          )}
        </div>
        <div className="flex items-center gap-3 flex-none">
          <span
            className={[
              "inline-flex items-center gap-1.5 text-2xs font-mono",
              capsule.running ? "text-sage" : "text-ink-quiet",
            ].join(" ")}
          >
            <span
              className={[
                "inline-block w-1.5 h-1.5 rounded-full",
                capsule.running ? "bg-sage" : "bg-ink-ghost",
              ].join(" ")}
            />
            {capsule.running ? "running" : "stopped"}
          </span>
          <button
            type="button"
            onClick={handleStartStop}
            disabled={lifecycleBusy || action === "uninstalling"}
            className="text-xs text-ink-muted hover:text-ink underline underline-offset-2 disabled:opacity-40 disabled:no-underline"
          >
            {lifecycleLabel}
          </button>
          <button
            type="button"
            onClick={handleUninstall}
            disabled={action === "uninstalling" || lifecycleBusy}
            className="text-xs text-ink-quiet hover:text-brick underline underline-offset-2 disabled:opacity-40 disabled:no-underline"
          >
            {action === "uninstalling" ? "removing…" : "uninstall"}
          </button>
        </div>
      </div>
      {capsule.permissions.length > 0 && (
        <div className="mt-1 font-mono text-2xs text-ink-quiet truncate">
          {summarizePermissions(capsule.permissions)}
        </div>
      )}
      {error && (
        <div className="mt-2 font-mono text-2xs text-brick">{error}</div>
      )}
    </li>
  );
}

function summarizePermissions(perms: string[]): string {
  if (perms.length <= 3) return perms.join(" · ");
  const visible = perms.slice(0, 3);
  return `${visible.join(" · ")} · +${perms.length - 3} more`;
}

function PaneSkeleton() {
  return (
    <ul className="divide-y divide-ink-hairline-soft">
      {[1, 2].map((i) => (
        <li key={i} className="py-4">
          <div className="h-3 w-44 bg-ink-hairline-soft rounded-sm" />
        </li>
      ))}
    </ul>
  );
}
