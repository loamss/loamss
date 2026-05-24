"use client";

import type { ReactNode } from "react";

/*
 * Note — inline informational callouts.
 *
 *   info — neutral; default tone (e.g., "Stored in your OS keychain")
 *   warn — amber; the consequential-warning tone ("App isn't verified")
 *   alert — brick; rare; for actual error/danger explanations
 *
 * Visually: a subtle left rule in the semantic color, tinted
 * background, ink-muted text. Compact — these explain, they don't
 * shout. Iconography is a small inline glyph in the rule color.
 */
type NoteKind = "info" | "warn" | "alert";

interface NoteProps {
  kind?: NoteKind;
  children: ReactNode;
  className?: string;
}

const kindClasses: Record<
  NoteKind,
  { wrap: string; icon: ReactNode; iconClass: string }
> = {
  info: {
    wrap: "bg-paper-deep/40 border-l-2 border-ink-hairline",
    iconClass: "text-ink-quiet",
    // Lowercase 'i' in a circle — small, restrained.
    icon: (
      <svg
        viewBox="0 0 14 14"
        fill="none"
        className="h-3.5 w-3.5"
        aria-hidden="true"
      >
        <circle cx="7" cy="7" r="6" stroke="currentColor" strokeWidth="1.1" />
        <circle cx="7" cy="3.8" r="0.7" fill="currentColor" />
        <path
          d="M7 5.8V10.4"
          stroke="currentColor"
          strokeWidth="1.1"
          strokeLinecap="round"
        />
      </svg>
    ),
  },
  warn: {
    wrap: "bg-amber-tint/40 border-l-2 border-amber",
    iconClass: "text-amber",
    // Triangle with exclamation — small.
    icon: (
      <svg
        viewBox="0 0 14 14"
        fill="none"
        className="h-3.5 w-3.5"
        aria-hidden="true"
      >
        <path
          d="M7 1.5L13 12.5H1L7 1.5Z"
          stroke="currentColor"
          strokeWidth="1.1"
          strokeLinejoin="round"
        />
        <path
          d="M7 5.5V8.5"
          stroke="currentColor"
          strokeWidth="1.1"
          strokeLinecap="round"
        />
        <circle cx="7" cy="10.6" r="0.7" fill="currentColor" />
      </svg>
    ),
  },
  alert: {
    wrap: "bg-brick-tint/40 border-l-2 border-brick",
    iconClass: "text-brick",
    icon: (
      <svg
        viewBox="0 0 14 14"
        fill="none"
        className="h-3.5 w-3.5"
        aria-hidden="true"
      >
        <circle cx="7" cy="7" r="6" stroke="currentColor" strokeWidth="1.1" />
        <path
          d="M4.5 4.5L9.5 9.5M9.5 4.5L4.5 9.5"
          stroke="currentColor"
          strokeWidth="1.1"
          strokeLinecap="round"
        />
      </svg>
    ),
  },
};

export function Note({ kind = "info", children, className = "" }: NoteProps) {
  const styles = kindClasses[kind];
  return (
    <div
      className={[
        "flex gap-2.5 px-3.5 py-2.5 rounded-sm text-xs text-ink-muted leading-relaxed",
        styles.wrap,
        className,
      ].join(" ")}
    >
      <span className={`mt-0.5 flex-none ${styles.iconClass}`}>
        {styles.icon}
      </span>
      <div className="flex-1">{children}</div>
    </div>
  );
}
