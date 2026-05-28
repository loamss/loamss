"use client";

import { useEffect, useState } from "react";
import {
  hasSetupToken,
  setSetupToken as persistSetupToken,
} from "@/lib/setup-token";

/*
 * SetupTokenAffordance
 *
 * Tiny, restrained UI for cloud-deployed runtimes. Shown on the
 * Welcome screen as a single text link ("Setup token") that expands
 * to a paste field on click. Hidden entirely on laptop installs and
 * once a token has already been captured (either from `?setup=` or
 * a previous paste).
 *
 * Visual approach: same hairline + ink-muted treatment as the rest
 * of the wizard. No emphasis colour, no card, no shadow — this is a
 * help-text-level affordance, not a primary surface.
 *
 * Reads `hasSetupToken()` on mount to decide initial visibility. We
 * deliberately don't poll — if the operator pastes a token, the
 * component re-renders via local state, and the rest of the wizard
 * picks up the new token via getSetupToken() on next fetch.
 */
export function SetupTokenAffordance() {
  // captured tracks whether a token is in storage. We initialize to
  // false to avoid a hydration mismatch (hasSetupToken touches
  // window.localStorage which is unavailable during SSR), then sync
  // on mount.
  const [captured, setCaptured] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [value, setValue] = useState("");
  const [showCapturedToast, setShowCapturedToast] = useState(false);

  useEffect(() => {
    setCaptured(hasSetupToken());
  }, []);

  // Brief "token loaded" confirmation when one is already present
  // (e.g., captured from the URL on first paint). Auto-dismisses
  // after a few seconds so it doesn't linger.
  useEffect(() => {
    if (!captured) return;
    setShowCapturedToast(true);
    const t = setTimeout(() => setShowCapturedToast(false), 4000);
    return () => clearTimeout(t);
  }, [captured]);

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = value.trim();
    if (!trimmed) return;
    persistSetupToken(trimmed);
    setCaptured(true);
    setExpanded(false);
    setValue("");
  }

  // Already-captured state: a single discreet line so the operator
  // can confirm the token reached the browser. No persistent banner.
  if (captured) {
    if (!showCapturedToast) return null;
    return (
      <div
        className="mt-6 inline-flex items-center gap-2 text-xs text-ok"
        role="status"
      >
        <CheckGlyph />
        <span className="font-mono">Setup token loaded.</span>
      </div>
    );
  }

  // Collapsed state: a small text link, indistinguishable in weight
  // from the wizard's "Have a config file already? Import it." sibling.
  if (!expanded) {
    return (
      <button
        type="button"
        onClick={() => setExpanded(true)}
        className="mt-6 text-xs text-ink-quiet hover:text-ink-muted transition-colors underline-offset-4 hover:underline"
      >
        Cloud deploy? Paste your setup token.
      </button>
    );
  }

  // Expanded state: input + submit + cancel. The input is
  // intentionally not type="password" — the token is high-entropy
  // (not a memorable secret) and operators commonly paste it
  // visually from log output.
  return (
    <form onSubmit={handleSubmit} className="mt-6 max-w-prose">
      <label className="smallcap text-ink-quiet mb-2 block">
        Setup token
      </label>
      <p className="text-xs text-ink-quiet mb-3 leading-relaxed">
        The runtime prints a one-time token in its startup logs (search
        for <span className="font-mono">Setup token:</span>). Paste it
        below; we&apos;ll send it with the wizard&apos;s submit.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <input
          autoFocus
          type="text"
          autoComplete="off"
          spellCheck={false}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder="abc123…"
          className="flex-1 min-w-[260px] font-mono text-xs px-3 py-2 bg-paper border border-ink-hairline focus:border-ink focus:outline-none transition-colors"
        />
        <button
          type="submit"
          disabled={!value.trim()}
          className="text-xs px-3 py-2 border border-ink-hairline hover:border-ink disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
        >
          Save
        </button>
        <button
          type="button"
          onClick={() => {
            setExpanded(false);
            setValue("");
          }}
          className="text-xs text-ink-quiet hover:text-ink-muted transition-colors"
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

function CheckGlyph() {
  return (
    <svg
      viewBox="0 0 16 16"
      className="h-3.5 w-3.5"
      aria-hidden="true"
      fill="none"
    >
      <path
        d="M3 8L7 12L13 4"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
