"use client";

import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/primitives/Button";
import { Note } from "@/components/primitives/Note";
import { addSource } from "@/lib/runtime-client";

/*
 * AddSourceModal — opened from the SourcesPane's "+ Add source"
 * action. v1 only supports source:files because that's the
 * frictionless connector (no OAuth dance) and the canonical first-
 * source experience the wizard already gestures at.
 *
 * Other adapters (source:gmail, eventually source:calendar) need
 * an OAuth sub-flow before the row is useful — when those land we
 * make this modal route on adapter selection: files → fill-form-
 * and-save, gmail → "click here to connect via OAuth".
 *
 * Design notes:
 *
 *   - Dialog escape: Esc, click outside, or Cancel. Submit closes
 *     on success; error stays open with the runtime's message.
 *   - Name field has a sensible default ("my-files") so the
 *     user-who-just-clicks-Save still ships with something
 *     meaningful — matches the wizard's "defaults work" principle.
 *   - The form is intentionally simple. Power-config (encrypt:
 *     true / globs / exclusion lists / max file size) lives in
 *     CLI flags; the dashboard surfaces the most common path.
 */

interface AddSourceModalProps {
  onClose: () => void;
  onAdded: () => void; // called after a successful add so the parent can trigger an immediate /console/state refresh
}

const ADAPTERS = [
  {
    id: "source:files",
    label: "Local files",
    description:
      "Sync a directory on this machine into the memory layer — Markdown, text, anything with a recognisable extension. No OAuth, no external service.",
  },
];

export function AddSourceModal({ onClose, onAdded }: AddSourceModalProps) {
  const [adapter] = useState(ADAPTERS[0].id); // future: a picker once >1 adapter
  const [name, setName] = useState("my-files");
  const [root, setRoot] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const dialogRef = useRef<HTMLDivElement>(null);
  const firstFieldRef = useRef<HTMLInputElement>(null);

  // Focus the name field on open, and close on Esc.
  useEffect(() => {
    firstFieldRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (submitting) return;
    setError(null);
    setSubmitting(true);

    const config: Record<string, unknown> = { root: root.trim() };
    const result = await addSource({ adapter, name: name.trim(), config });

    if (result.ok) {
      setSubmitting(false);
      onAdded();
      onClose();
      return;
    }
    // Surface the runtime's message verbatim; users debugging "why
    // doesn't this directory work" need the actual error.
    setError(humaniseAddError(result));
    setSubmitting(false);
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-ink/30 p-4 animate-fade-in"
      role="dialog"
      aria-modal="true"
      aria-labelledby="add-source-title"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        className="bg-paper border border-ink-hairline rounded-md shadow-raise max-w-lg w-full"
      >
        <header className="px-6 py-5 border-b border-ink-hairline-soft">
          <div className="smallcap text-ink-quiet">Add source</div>
          <h2
            id="add-source-title"
            className="mt-1 font-serif text-2xl text-ink leading-tight"
            style={{ fontVariationSettings: "'opsz' 96, 'wght' 400" }}
          >
            Pull data into your memory.
          </h2>
        </header>

        <form onSubmit={submit} className="px-6 py-5 space-y-5">
          <div>
            <label className="smallcap text-ink-quiet block mb-2">
              Adapter
            </label>
            <div className="border border-ink-hairline rounded-sm bg-paper-deep/30 px-4 py-3">
              <div className="flex items-baseline gap-3">
                <span className="font-mono text-xs text-ink">{adapter}</span>
                <span className="text-sm text-ink-muted">
                  {ADAPTERS[0].label}
                </span>
              </div>
              <p className="mt-1 text-sm text-ink-muted leading-relaxed">
                {ADAPTERS[0].description}
              </p>
            </div>
          </div>

          <div>
            <label
              htmlFor="source-name"
              className="smallcap text-ink-quiet block mb-2"
            >
              Name
            </label>
            <input
              ref={firstFieldRef}
              id="source-name"
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-files"
              required
              pattern="[a-z0-9][a-z0-9_-]*"
              title="lowercase letters, digits, hyphens, underscores; must start with letter or digit"
              className="w-full font-mono text-sm bg-paper border border-ink-hairline-soft focus:border-brand focus:ring-1 focus:ring-brand/20 rounded-sm px-3 py-2 outline-none transition-colors"
            />
            <p className="mt-1 text-xs text-ink-quiet font-sans">
              Used as the principal id in the audit log and as the memory
              namespace this source writes into.
            </p>
          </div>

          <div>
            <label
              htmlFor="source-root"
              className="smallcap text-ink-quiet block mb-2"
            >
              Root directory
            </label>
            <input
              id="source-root"
              type="text"
              value={root}
              onChange={(e) => setRoot(e.target.value)}
              placeholder="/Users/you/Documents"
              required
              className="w-full font-mono text-sm bg-paper border border-ink-hairline-soft focus:border-brand focus:ring-1 focus:ring-brand/20 rounded-sm px-3 py-2 outline-none transition-colors"
            />
            <p className="mt-1 text-xs text-ink-quiet font-sans">
              Absolute path. The source walks it on each sync and writes
              an entry per recognised file.
            </p>
          </div>

          {error && <Note kind="warn">{error}</Note>}

          <div className="flex items-center justify-end gap-3 pt-3 border-t border-ink-hairline-soft">
            <button
              type="button"
              onClick={onClose}
              disabled={submitting}
              className="text-sm text-ink-quiet hover:text-ink-muted underline underline-offset-2 disabled:opacity-50"
            >
              Cancel
            </button>
            <Button
              type="submit"
              size="sm"
              variant="primary"
              disabled={submitting || !name.trim() || !root.trim()}
            >
              {submitting ? "Adding…" : "Add source"}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}

function humaniseAddError(
  result: Exclude<Awaited<ReturnType<typeof addSource>>, { ok: true }>,
): string {
  switch (result.kind) {
    case "conflict":
      return "A source with that name already exists. Pick a different name, or remove the existing one first.";
    case "rejected":
      return result.reason; // runtime's adapter-init message is usually precise enough to act on
    default:
      return result.reason;
  }
}
