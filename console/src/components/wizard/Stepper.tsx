"use client";

import type { WizardStep } from "@/lib/wizard-state";

/*
 * Stepper — the wizard's primary nav indicator.
 *
 * Visual approach: an editorial chapter index. Serif numerals
 * (00, 01, 02, …) sit above small-caps labels, with a thin horizontal
 * rule connecting them. The current step's numeral is dark and large;
 * previous steps are muted but legible (you can click back to them);
 * future steps are quiet.
 *
 * Rejected alternatives:
 *   - "1 / 5" pill chips: too SaaS-marketing, no character
 *   - vertical stepper: takes too much horizontal real estate at
 *     this stage; we have lots of vertical space to use
 *   - dots-only: not enough information for a multi-step decision
 *     where each step has a name
 *
 * The horizontal rule isn't a progress bar — it's a continuous line
 * the numerals sit on, like a book's running header. Position on the
 * line communicates "where you are in the sequence."
 */
export interface StepDef {
  id: WizardStep;
  numeral: string; // "00", "01", … or "fin" for done
  label: string; // small-caps label
}

interface StepperProps {
  steps: StepDef[];
  current: WizardStep;
  // Allow navigating back to completed steps by clicking the numeral.
  onNavigate?: (step: WizardStep) => void;
  // The current step's name index (so we can derive past/future).
  currentIndex: number;
}

export function Stepper({
  steps,
  current,
  onNavigate,
  currentIndex,
}: StepperProps) {
  return (
    <nav aria-label="Setup progress" className="w-full">
      {/* The rule itself — sits above the numerals, runs full width. */}
      <div className="hairline" />
      <ol className="grid grid-cols-5 gap-0">
        {steps.map((step, idx) => {
          const isCurrent = step.id === current;
          const isPast = idx < currentIndex;
          const canNavigate = isPast && onNavigate;
          return (
            <li
              key={step.id}
              className={[
                "flex flex-col items-center pt-5 pb-2 text-center",
                "border-r border-ink-hairline-soft last:border-r-0",
                "transition-opacity duration-300",
              ].join(" ")}
            >
              <button
                type="button"
                onClick={() => canNavigate && onNavigate(step.id)}
                disabled={!canNavigate}
                aria-current={isCurrent ? "step" : undefined}
                className={[
                  "group flex flex-col items-center gap-1",
                  "transition-colors duration-300",
                  canNavigate &&
                    "cursor-pointer hover:text-brand",
                  !canNavigate && !isCurrent && "cursor-default",
                ].join(" ")}
              >
                <span
                  className={[
                    "font-serif text-3xl tracking-tight tabular leading-none",
                    "transition-all duration-400 ease-out",
                    isCurrent && "text-ink",
                    isPast && "text-ink-muted",
                    !isCurrent && !isPast && "text-ink-ghost",
                  ].join(" ")}
                  style={{
                    fontVariationSettings: isCurrent
                      ? "'opsz' 72, 'wght' 500"
                      : "'opsz' 36, 'wght' 400",
                  }}
                >
                  {step.numeral}
                </span>
                <span
                  className={[
                    "smallcap mt-0.5 transition-colors duration-300",
                    isCurrent && "!text-ink",
                    isPast && "!text-ink-muted",
                    !isCurrent && !isPast && "!text-ink-ghost",
                  ].join(" ")}
                >
                  {step.label}
                </span>
              </button>
            </li>
          );
        })}
      </ol>
    </nav>
  );
}
