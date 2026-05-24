"use client";

import type { ReactNode } from "react";
import { Stepper, type StepDef } from "./Stepper";
import { Wordmark } from "@/components/primitives/Wordmark";
import { useWizard, stepIndex, type WizardStep } from "@/lib/wizard-state";
import type { RuntimeProbe } from "@/lib/runtime-client";

/*
 * WizardShell — the persistent frame the wizard steps render into.
 *
 * Three regions:
 *
 *   top    — wordmark + a small "first-run setup" tag.
 *   middle — the Stepper (chapter-index style)
 *   below  — the active step, animated with a fade-up on transition
 *
 * The shell takes a `step` key so children can animate their entry
 * cleanly. It avoids any "did you mean to leave?" prompts — this is
 * configuration; if the user closes the tab, the runtime is in the
 * exact same state, and they can resume from welcome.
 */
const steps: StepDef[] = [
  { id: "welcome", numeral: "00", label: "Welcome" },
  { id: "storage", numeral: "01", label: "Storage" },
  { id: "memory", numeral: "02", label: "Memory" },
  { id: "models", numeral: "03", label: "Models" },
  { id: "connect", numeral: "04", label: "Connect" },
];

interface WizardShellProps {
  children: ReactNode;
  // The current step's content key — used to trigger the page-enter
  // animation on transition.
  stepKey: WizardStep;
  // Whether to show the stepper. Welcome and Done hide it for visual
  // weight.
  showStepper?: boolean;
}

export function WizardShell({
  children,
  stepKey,
  showStepper = true,
}: WizardShellProps) {
  const { step, goTo, furthestStep, runtimeProbe } = useWizard();

  return (
    <div className="min-h-screen flex flex-col">
      {/* Header — small, considered. The wordmark is the only logo
       * moment in the wizard. The runtime-status badge on the right
       * tells the user whether their daemon is reachable; it updates
       * every 20s in the background. */}
      <header className="px-6 sm:px-10 py-6 sm:py-8 flex items-center justify-between gap-4">
        <Wordmark size="md" />
        <div className="flex items-center gap-4">
          <RuntimeBadge probe={runtimeProbe} />
          <span className="smallcap text-ink-quiet hidden sm:inline">
            First-run setup
          </span>
        </div>
      </header>

      {/* Stepper — sits below the header, full-width, hairline rule
       * above it. Hidden on welcome and done for those screens'
       * editorial weight. */}
      {showStepper && (
        <div className="px-6 sm:px-10 mb-12">
          <Stepper
            steps={steps}
            current={step}
            currentIndex={stepIndex(step)}
            onNavigate={(target) => {
              // Only allow nav back to past or current steps.
              if (stepIndex(target) <= stepIndex(furthestStep)) {
                goTo(target);
              }
            }}
          />
        </div>
      )}

      {/* Active step — keyed on stepKey so React remounts and the
       * page-enter animation plays. Centered horizontally with
       * generous side padding. */}
      <main className="flex-1 px-6 sm:px-10 pb-16">
        <div key={stepKey} className="page-enter">
          {children}
        </div>
      </main>

      {/* Footer — just a small reassurance + the project link.
       * Visually anchored at the bottom but never feels heavy. */}
      <footer className="px-6 sm:px-10 py-6 flex flex-wrap items-center justify-between gap-3 text-xs text-ink-quiet font-sans">
        <span>
          The runtime is running on your machine. Nothing leaves until
          you grant something.
        </span>
        <span className="font-mono text-2xs">
          v0.1 prototype · localhost:7777
        </span>
      </footer>
    </div>
  );
}

/**
 * RuntimeBadge surfaces the live state of the runtime daemon in the
 * header. Three visual states:
 *
 *   detected      — sage dot + version badge ("runtime v0.1")
 *   not-reachable — amber dot + helpful label
 *   probing       — quiet dot pulsing
 *
 * The badge is small + monospace for version numbers; it's part of
 * the "state is visible" design principle (see console-design.md).
 */
function RuntimeBadge({ probe }: { probe: RuntimeProbe | null }) {
  if (probe === null) {
    return (
      <span className="flex items-center gap-2 text-xs text-ink-quiet">
        <span className="inline-block w-1.5 h-1.5 rounded-full bg-amber" />
        <span className="font-mono text-2xs">runtime not reachable</span>
      </span>
    );
  }
  return (
    <span className="flex items-center gap-2 text-xs text-ink-muted">
      <span className="inline-block w-1.5 h-1.5 rounded-full bg-sage" />
      <span className="font-mono text-2xs">
        runtime · {probe.health.version}
      </span>
    </span>
  );
}
