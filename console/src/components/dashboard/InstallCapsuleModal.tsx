"use client";

import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/primitives/Button";
import { Note } from "@/components/primitives/Note";
import {
  type CapsuleManifestSummary,
  installCapsule,
} from "@/lib/runtime-client";

/*
 * InstallCapsuleModal — two-step flow.
 *
 *   Step 1: Path entry. User pastes an absolute path to a capsule
 *           directory or capsule.yaml.
 *
 *   Step 2: Permission slip review. After install succeeds the
 *           runtime returns the parsed manifest with every
 *           declared permission. We render that as a "this is
 *           what just happened" slip — the capsule's name, version,
 *           every capability it received, the rationale the
 *           capsule author wrote for each one. User clicks Done
 *           to dismiss.
 *
 * Important design choice: the install is a one-shot at the
 * runtime — the grants are ALREADY issued by the time we render
 * step 2. We don't ask "approve / cancel" because there's nothing
 * to undo at the slip stage. If the user is unhappy they click
 * Uninstall on the row, which reverts everything. This matches
 * how `loamss capsule install` works at the CLI today.
 *
 * (Future: a "preview only" dry-run mode that returns the parsed
 * manifest WITHOUT committing grants/files, so the slip can act
 * as a pre-flight check. capsule-spec.md leaves room for this.)
 */

interface InstallCapsuleModalProps {
  onClose: () => void;
  onInstalled: () => void;
}

type Stage = "path" | "installing" | "review";

interface ReviewState {
  manifest: CapsuleManifestSummary;
  grants: string[];
  note?: string;
}

export function InstallCapsuleModal({
  onClose,
  onInstalled,
}: InstallCapsuleModalProps) {
  const [stage, setStage] = useState<Stage>("path");
  const [path, setPath] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [review, setReview] = useState<ReviewState | null>(null);

  const firstFieldRef = useRef<HTMLInputElement>(null);

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
    if (stage !== "path") return;
    setError(null);
    setStage("installing");

    const result = await installCapsule(path.trim());
    if (!result.ok) {
      setError(humaniseInstallError(result));
      setStage("path");
      return;
    }
    // Tell the parent to refresh so the row appears in the
    // background while the user reads the slip.
    onInstalled();
    setReview({
      manifest: result.manifest,
      grants: result.grants,
      note: result.note,
    });
    setStage("review");
  }

  function done() {
    onClose();
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-ink/30 p-4 animate-fade-in"
      role="dialog"
      aria-modal="true"
      aria-labelledby="install-capsule-title"
      onClick={(e) => {
        if (e.target === e.currentTarget && stage === "path") onClose();
      }}
    >
      <div className="bg-paper border border-ink-hairline rounded-md shadow-raise max-w-xl w-full">
        {stage === "review" && review ? (
          <PermissionSlip
            review={review}
            onDone={done}
          />
        ) : (
          <PathEntry
            stage={stage}
            path={path}
            setPath={setPath}
            error={error}
            inputRef={firstFieldRef}
            onSubmit={submit}
            onCancel={onClose}
          />
        )}
      </div>
    </div>
  );
}

interface PathEntryProps {
  stage: Stage;
  path: string;
  setPath: (v: string) => void;
  error: string | null;
  inputRef: React.RefObject<HTMLInputElement | null>;
  onSubmit: (e: React.FormEvent) => void;
  onCancel: () => void;
}

function PathEntry({
  stage,
  path,
  setPath,
  error,
  inputRef,
  onSubmit,
  onCancel,
}: PathEntryProps) {
  const installing = stage === "installing";
  return (
    <>
      <header className="px-6 py-5 border-b border-ink-hairline-soft">
        <div className="smallcap text-ink-quiet">Install capsule</div>
        <h2
          id="install-capsule-title"
          className="mt-1 font-serif text-2xl text-ink leading-tight"
          style={{ fontVariationSettings: "'opsz' 96, 'wght' 400" }}
        >
          Give it a path.
        </h2>
      </header>

      <form onSubmit={onSubmit} className="px-6 py-5 space-y-5">
        <div>
          <label
            htmlFor="capsule-path"
            className="smallcap text-ink-quiet block mb-2"
          >
            Path to capsule
          </label>
          <input
            ref={inputRef}
            id="capsule-path"
            type="text"
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder="/Users/you/code/my-capsule"
            required
            disabled={installing}
            className="w-full font-mono text-sm bg-paper border border-ink-hairline-soft focus:border-brand focus:ring-1 focus:ring-brand/20 rounded-sm px-3 py-2 outline-none transition-colors disabled:bg-paper-deep/30"
          />
          <p className="mt-1 text-xs text-ink-quiet font-sans">
            Absolute path to a capsule directory or its
            <span className="font-mono text-2xs"> capsule.yaml</span>. The
            runtime parses the manifest, issues the grants it declares,
            and (when this binary is the live daemon) starts the
            subprocess.
          </p>
        </div>

        {error && <Note kind="warn">{error}</Note>}

        <div className="flex items-center justify-end gap-3 pt-3 border-t border-ink-hairline-soft">
          <button
            type="button"
            onClick={onCancel}
            disabled={installing}
            className="text-sm text-ink-quiet hover:text-ink-muted underline underline-offset-2 disabled:opacity-50"
          >
            Cancel
          </button>
          <Button
            type="submit"
            size="sm"
            variant="primary"
            disabled={installing || !path.trim()}
          >
            {installing ? "Installing…" : "Install"}
          </Button>
        </div>
      </form>
    </>
  );
}

function PermissionSlip({
  review,
  onDone,
}: {
  review: ReviewState;
  onDone: () => void;
}) {
  const { manifest, grants, note } = review;
  return (
    <>
      <header className="px-6 py-5 border-b border-ink-hairline-soft">
        <div className="smallcap text-sage">Installed</div>
        <h2
          className="mt-1 font-serif text-2xl text-ink leading-tight"
          style={{ fontVariationSettings: "'opsz' 96, 'wght' 400" }}
        >
          {manifest.name}
          <span className="font-mono text-base text-ink-quiet ml-3">
            v{manifest.version}
          </span>
        </h2>
        {manifest.description && (
          <p className="mt-2 text-sm text-ink-muted leading-relaxed">
            {manifest.description}
          </p>
        )}
        {manifest.author && (
          <p className="mt-1 font-mono text-2xs text-ink-quiet">
            by {manifest.author}
          </p>
        )}
      </header>

      <div className="px-6 py-5 space-y-5 max-h-[60vh] overflow-y-auto">
        {note && <Note kind="warn">{note}</Note>}

        <section>
          <div className="smallcap text-ink-quiet mb-2">Permission slip</div>
          <p className="text-sm text-ink-muted leading-relaxed mb-3">
            This capsule received {manifest.permissions.length} grant
            {manifest.permissions.length === 1 ? "" : "s"} from the
            runtime. Every grant is recorded in the audit log and
            revocable by uninstalling.
          </p>
          <ul className="space-y-2.5">
            {manifest.permissions.map((p, i) => (
              <li
                key={`${p.capability}-${i}`}
                className="border border-ink-hairline-soft rounded-sm px-4 py-3 bg-paper-deep/30"
              >
                <div className="flex items-baseline gap-3 flex-wrap">
                  <span className="font-mono text-xs text-ink">
                    {p.capability}
                  </span>
                  {p.requires_user_approval && (
                    <span className="font-mono text-2xs text-amber">
                      asks before every use
                    </span>
                  )}
                </div>
                {p.rationale && (
                  <p className="mt-1.5 text-sm text-ink-muted leading-relaxed">
                    {p.rationale.trim()}
                  </p>
                )}
                {p.scope && Object.keys(p.scope).length > 0 && (
                  <pre className="mt-2 font-mono text-2xs text-ink-quiet bg-paper border border-ink-hairline-soft rounded-sm px-2 py-1.5 overflow-x-auto">
                    {JSON.stringify(p.scope, null, 2)}
                  </pre>
                )}
              </li>
            ))}
          </ul>
        </section>

        {manifest.tools && manifest.tools.length > 0 && (
          <section>
            <div className="smallcap text-ink-quiet mb-2">Tools exposed</div>
            <ul className="space-y-1.5">
              {manifest.tools.map((t) => (
                <li key={t.name} className="text-sm">
                  <span className="font-mono text-xs text-ink">{t.name}</span>
                  {t.description && (
                    <span className="text-ink-muted"> — {t.description}</span>
                  )}
                </li>
              ))}
            </ul>
          </section>
        )}

        <section>
          <div className="smallcap text-ink-quiet mb-2">Grant IDs</div>
          <ul className="space-y-1">
            {grants.map((g) => (
              <li key={g} className="font-mono text-2xs text-ink-quiet">
                {g}
              </li>
            ))}
          </ul>
        </section>
      </div>

      <footer className="px-6 py-4 border-t border-ink-hairline-soft flex items-center justify-end">
        <Button onClick={onDone} size="sm" variant="primary">
          Done
        </Button>
      </footer>
    </>
  );
}

function humaniseInstallError(
  result: Exclude<Awaited<ReturnType<typeof installCapsule>>, { ok: true }>,
): string {
  switch (result.kind) {
    case "conflict":
      return "A capsule with that name is already installed. Uninstall it first to install a new version.";
    case "rejected":
      return result.reason;
    default:
      return result.reason;
  }
}
