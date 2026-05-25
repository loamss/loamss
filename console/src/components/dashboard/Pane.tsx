"use client";

import type { ReactNode } from "react";

/*
 * Pane — the shared frame every dashboard tile renders inside.
 *
 * Visual contract:
 *
 *   eyebrow   — smallcap, ink-quiet, top-left.
 *   action    — optional, top-right (e.g. "open Sources →"). Quiet
 *               anchor styling, never a heavy CTA.
 *   children  — the pane body. Caller controls layout below the
 *               header strip.
 *
 * We keep this primitive thin on purpose. Different panes have
 * different content shapes (list, hero-summary, feed) and forcing
 * them into a single template would either inflate the contract
 * or impoverish the design. The Pane just handles the eyebrow +
 * the hairline above the content.
 */

interface PaneProps {
  eyebrow: string;
  action?: ReactNode;
  children: ReactNode;
}

export function Pane({ eyebrow, action, children }: PaneProps) {
  return (
    <section>
      <header className="flex items-baseline justify-between gap-3 pb-3 border-b border-ink-hairline-soft">
        <span className="smallcap text-ink-quiet">{eyebrow}</span>
        {action && <div className="text-xs">{action}</div>}
      </header>
      <div className="pt-5">{children}</div>
    </section>
  );
}

/**
 * EmptyState — the "no items yet" message. Calm, suggestive, never
 * a marketing-style hero. The optional `hint` is mono text that
 * tells a power user how to add the missing thing via CLI.
 */
interface EmptyStateProps {
  message: string;
  hint?: string;
}

export function EmptyState({ message, hint }: EmptyStateProps) {
  return (
    <div className="py-6 text-sm text-ink-muted leading-relaxed">
      <p>{message}</p>
      {hint && (
        <p className="mt-2 font-mono text-2xs text-ink-quiet">{hint}</p>
      )}
    </div>
  );
}

/**
 * UnavailablePane — the "this build doesn't expose this" message.
 * Different from empty: empty means "no entries yet", unavailable
 * means "the runtime can't tell us." Visually quieter so the
 * distinction is legible without being loud.
 */
export function UnavailablePane({ reason }: { reason?: string }) {
  return (
    <div className="py-4 text-sm text-ink-quiet leading-relaxed">
      <p>Not exposed in this build.</p>
      {reason && (
        <p className="mt-1 font-mono text-2xs text-ink-quiet">{reason}</p>
      )}
    </div>
  );
}

/**
 * statusDotColor maps the runtime's status strings to our semantic
 * palette. Single source of truth so every pane's dots agree on
 * what sage vs amber vs brick means.
 */
export function statusDotColor(
  status: string,
): "sage" | "amber" | "brick" | "quiet" {
  switch (status) {
    case "success":
    case "active":
      return "sage";
    case "running":
    case "pending":
      return "amber";
    case "error":
    case "denied":
    case "revoked":
      return "brick";
    default:
      return "quiet";
  }
}
