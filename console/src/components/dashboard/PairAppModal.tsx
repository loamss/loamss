"use client";

import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/primitives/Button";
import { Note } from "@/components/primitives/Note";
import { createPairingCode } from "@/lib/runtime-client";

/*
 * PairAppModal — two-step.
 *
 *   Step 1: Name the external client (e.g., "Claude Desktop",
 *           "Cursor laptop", "Sarah's iPad"). The runtime uses this
 *           label in the audit log and the Apps pane forever, so
 *           "be specific" guidance lives next to the field.
 *
 *   Step 2: Show the pairing code + expiry. The user copies the
 *           code into the external app. Once the external app
 *           POSTs to /pair with the code, a Client row appears in
 *           the dashboard on the next /console/state poll.
 *
 * No "click to redeem" button here — redemption happens elsewhere
 * (the external client). The modal's job is to surface the code
 * and let the user copy it.
 */

interface PairAppModalProps {
  onClose: () => void;
  onPaired: () => void; // called when the user closes the code-display step
}

type Stage = "name" | "creating" | "show";

interface ShowState {
  code: string;
  clientName: string;
  expiresAt: string;
}

export function PairAppModal({ onClose, onPaired }: PairAppModalProps) {
  const [stage, setStage] = useState<Stage>("name");
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [show, setShow] = useState<ShowState | null>(null);

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
    if (stage !== "name") return;
    setError(null);
    setStage("creating");
    const result = await createPairingCode(name.trim());
    if (!result.ok) {
      setError(result.reason);
      setStage("name");
      return;
    }
    setShow({
      code: result.code,
      clientName: result.clientName,
      expiresAt: result.expiresAt,
    });
    setStage("show");
  }

  function done() {
    onPaired();
    onClose();
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-ink/30 p-4 animate-fade-in"
      role="dialog"
      aria-modal="true"
      aria-labelledby="pair-app-title"
      onClick={(e) => {
        if (e.target === e.currentTarget && stage === "name") onClose();
      }}
    >
      <div className="bg-paper border border-ink-hairline rounded-md shadow-raise max-w-lg w-full">
        {stage === "show" && show ? (
          <ShowCode show={show} onDone={done} />
        ) : (
          <NameEntry
            stage={stage}
            name={name}
            setName={setName}
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

interface NameEntryProps {
  stage: Stage;
  name: string;
  setName: (v: string) => void;
  error: string | null;
  inputRef: React.RefObject<HTMLInputElement | null>;
  onSubmit: (e: React.FormEvent) => void;
  onCancel: () => void;
}

function NameEntry({
  stage,
  name,
  setName,
  error,
  inputRef,
  onSubmit,
  onCancel,
}: NameEntryProps) {
  const busy = stage === "creating";
  return (
    <>
      <header className="px-6 py-5 border-b border-ink-hairline-soft">
        <div className="smallcap text-ink-quiet">Pair an app</div>
        <h2
          id="pair-app-title"
          className="mt-1 font-serif text-2xl text-ink leading-tight"
          style={{ fontVariationSettings: "'opsz' 96, 'wght' 400" }}
        >
          What's connecting?
        </h2>
      </header>

      <form onSubmit={onSubmit} className="px-6 py-5 space-y-5">
        <div>
          <label
            htmlFor="client-name"
            className="smallcap text-ink-quiet block mb-2"
          >
            Client name
          </label>
          <input
            ref={inputRef}
            id="client-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Claude Desktop"
            required
            disabled={busy}
            className="w-full font-sans text-sm bg-paper border border-ink-hairline-soft focus:border-brand focus:ring-1 focus:ring-brand/20 rounded-sm px-3 py-2 outline-none transition-colors disabled:bg-paper-deep/30"
          />
          <p className="mt-1 text-xs text-ink-quiet font-sans">
            Be specific. The runtime's audit log refers to this client by
            this name forever; "Claude Desktop · Sarah's MacBook" is more
            useful than "Claude" when you have several.
          </p>
        </div>

        {error && <Note kind="warn">{error}</Note>}

        <div className="flex items-center justify-end gap-3 pt-3 border-t border-ink-hairline-soft">
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="text-sm text-ink-quiet hover:text-ink-muted underline underline-offset-2 disabled:opacity-50"
          >
            Cancel
          </button>
          <Button
            type="submit"
            size="sm"
            variant="primary"
            disabled={busy || !name.trim()}
          >
            {busy ? "Generating…" : "Generate code"}
          </Button>
        </div>
      </form>
    </>
  );
}

function ShowCode({ show, onDone }: { show: ShowState; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  const expiresAt = new Date(show.expiresAt);
  const expiresIn = Math.max(
    0,
    Math.floor((expiresAt.getTime() - Date.now()) / 1000 / 60),
  );

  async function copy() {
    try {
      await navigator.clipboard.writeText(show.code);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // No clipboard permission — the code is already on screen,
      // user can select-and-copy manually.
    }
  }

  return (
    <>
      <header className="px-6 py-5 border-b border-ink-hairline-soft">
        <div className="smallcap text-sage">Code generated</div>
        <h2
          className="mt-1 font-serif text-2xl text-ink leading-tight"
          style={{ fontVariationSettings: "'opsz' 96, 'wght' 400" }}
        >
          Hand this to {show.clientName}.
        </h2>
      </header>

      <div className="px-6 py-5 space-y-5">
        <div>
          <div className="smallcap text-ink-quiet mb-2">Pairing code</div>
          <button
            type="button"
            onClick={copy}
            className="w-full bg-paper-deep/30 border border-ink-hairline rounded-sm px-4 py-4 font-mono text-2xl text-ink text-center tracking-wider hover:bg-brand-tint/30 hover:border-brand transition-colors"
          >
            {show.code}
          </button>
          <div className="mt-1 text-xs text-ink-quiet font-sans text-center">
            {copied ? "Copied to clipboard." : "Click to copy."}
          </div>
        </div>

        <div className="text-sm text-ink-muted leading-relaxed">
          <p>
            Paste this code into{" "}
            <span className="font-mono text-2xs text-ink">
              {show.clientName}
            </span>{" "}
            to complete the pairing. The runtime will issue the client a
            bearer token; you'll see the new row appear in the Apps pane
            within a few seconds.
          </p>
          <p className="mt-2">
            The code expires in{" "}
            <span className="font-mono text-2xs text-ink">
              {expiresIn} minute{expiresIn === 1 ? "" : "s"}
            </span>
            . It's single-use — once redeemed, the code is dead.
          </p>
        </div>

        <Note kind="info">
          The code itself is the entire auth payload. Treat it like a
          password until the external app redeems it. After redemption,
          revocation lives in the Apps pane's disconnect button.
        </Note>
      </div>

      <footer className="px-6 py-4 border-t border-ink-hairline-soft flex items-center justify-end">
        <Button onClick={onDone} size="sm" variant="primary">
          Done
        </Button>
      </footer>
    </>
  );
}
