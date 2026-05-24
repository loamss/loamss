"use client";

import { Button } from "@/components/primitives/Button";
import { Choice } from "@/components/primitives/Choice";
import { Field } from "@/components/primitives/Field";
import { Note } from "@/components/primitives/Note";
import { useWizard } from "@/lib/wizard-state";

/*
 * Storage — where Loamss keeps your data.
 *
 * Three options:
 *   default — encrypted local folder (preselected)
 *   cloud   — coming soon (disabled but visible)
 *   custom  — expands with path + encryption toggle
 *
 * The encryption toggle is the "risky" control of the wizard; turning
 * it off triggers a Note warning ("Other users on this machine could
 * read them"). The wireframe specifically called this out.
 */
export function Storage() {
  const { storage, setStorage, goTo } = useWizard();

  return (
    <StepLayout
      eyebrow="01 — Storage"
      title="Where should Loamss keep your data?"
      sub="The runtime writes raw payloads, source credentials, and the audit log here. Encryption is on by default."
    >
      <div className="space-y-3">
        <Choice
          selected={storage.mode === "default"}
          onSelect={() => setStorage({ mode: "default" })}
          title="Encrypted local folder"
          description="The default. Files are written to your home directory and encrypted at rest with AES-256-GCM."
          meta="~/.loamss/storage"
          badge="Recommended"
        />

        <Choice
          selected={false}
          onSelect={() => {}}
          title="Cloud storage"
          description="S3, B2, R2, or any S3-compatible provider you already use. Data stays in your bucket."
          disabled
          badge="Coming soon"
        />

        <Choice
          selected={storage.mode === "custom"}
          onSelect={() => setStorage({ mode: "custom" })}
          title="Custom location"
          description="Pick a path. Useful if you keep a synced or encrypted volume mounted elsewhere."
          details={
            <div className="space-y-4">
              <Field
                label="Path"
                value={storage.customPath}
                onChange={(e) => setStorage({ customPath: e.target.value })}
                placeholder="/Users/me/Documents/MyLoamss"
                mono
              />
              <div className="flex items-start gap-3">
                <button
                  type="button"
                  onClick={() => setStorage({ encrypt: !storage.encrypt })}
                  role="switch"
                  aria-checked={storage.encrypt}
                  className={[
                    "relative flex-none h-5 w-9 rounded-full transition-colors duration-200",
                    "focus-visible:ring-2 focus-visible:ring-brand/30",
                    storage.encrypt ? "bg-brand" : "bg-ink-hairline",
                  ].join(" ")}
                >
                  <span
                    className={[
                      "absolute top-0.5 h-4 w-4 rounded-full bg-paper",
                      "shadow-sm transition-transform duration-200",
                      storage.encrypt ? "translate-x-4" : "translate-x-0.5",
                    ].join(" ")}
                  />
                </button>
                <div className="flex-1">
                  <div className="text-sm text-ink leading-snug">
                    Encrypt at rest with AES-256-GCM
                  </div>
                  <div className="text-xs text-ink-quiet mt-0.5">
                    Strongly recommended. Off only if you trust the underlying
                    volume.
                  </div>
                </div>
              </div>
              {!storage.encrypt && (
                <Note kind="warn">
                  Without encryption, anyone with read access to this volume
                  can see your raw data. The audit log will record this choice;
                  you can re-encrypt later via{" "}
                  <span className="font-mono text-2xs">Settings → Storage</span>
                  .
                </Note>
              )}
            </div>
          }
        />
      </div>

      <StepFooter
        backLabel="Welcome"
        onBack={() => goTo("welcome")}
        nextLabel="Continue"
        onNext={() => goTo("memory")}
        nextDisabled={storage.mode === "custom" && storage.customPath.trim() === ""}
      />
    </StepLayout>
  );
}

// --- shared step-layout helpers ---------------------------------------

interface StepLayoutProps {
  eyebrow: string;
  title: string;
  sub: string;
  children: React.ReactNode;
}

export function StepLayout({ eyebrow, title, sub, children }: StepLayoutProps) {
  return (
    <div className="max-w-panel mx-auto">
      <div className="mb-10">
        <div className="smallcap text-ink-quiet mb-3">{eyebrow}</div>
        <h1 className="font-serif text-3xl sm:text-4xl text-ink leading-tight tracking-tight">
          {title}
        </h1>
        <p className="mt-3 text-base text-ink-muted leading-relaxed max-w-prose">
          {sub}
        </p>
      </div>
      <div className="space-y-7">{children}</div>
    </div>
  );
}

interface StepFooterProps {
  backLabel: string;
  onBack: () => void;
  nextLabel: string;
  onNext: () => void;
  nextDisabled?: boolean;
}

export function StepFooter({
  backLabel,
  onBack,
  nextLabel,
  onNext,
  nextDisabled = false,
}: StepFooterProps) {
  return (
    <div className="flex items-center justify-between pt-8 mt-2 border-t border-ink-hairline-soft">
      <Button variant="ghost" size="md" onClick={onBack}>
        <BackGlyph />
        {backLabel}
      </Button>
      <Button onClick={onNext} disabled={nextDisabled}>
        {nextLabel}
        <ForwardGlyph />
      </Button>
    </div>
  );
}

function BackGlyph() {
  return (
    <svg viewBox="0 0 16 16" className="h-3 w-3" fill="none" aria-hidden="true">
      <path
        d="M13 8H3M3 8L7.5 3.5M3 8L7.5 12.5"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function ForwardGlyph() {
  return (
    <svg viewBox="0 0 16 16" className="h-3 w-3" fill="none" aria-hidden="true">
      <path
        d="M3 8H13M13 8L8.5 3.5M13 8L8.5 12.5"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
