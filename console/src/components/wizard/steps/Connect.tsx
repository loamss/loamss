"use client";

import { Button } from "@/components/primitives/Button";
import { Choice } from "@/components/primitives/Choice";
import { Note } from "@/components/primitives/Note";
import { StepLayout, StepFooter } from "./Storage";
import { useWizard } from "@/lib/wizard-state";

/*
 * Connect — the optional last step. The wizard's deliberate
 * "skip is valid" moment.
 *
 * Two real options:
 *   skip  — preselected; lands the user on the dashboard with zero
 *           sources, which is fine
 *   gmail — the first reference connector
 *
 * The Gmail option, when selected, doesn't expand into the full
 * 4-step Gmail wizard inside the first-run flow — that would
 * make the wizard feel infinite. Instead, the wizard's Finish
 * button becomes "Finish + Connect Gmail", and on submit we land
 * on the dashboard with a sticky "Connect Gmail" panel ready.
 */
export function Connect() {
  const { connect, setConnect, goTo, startSubmit, submitting, submitProgress } =
    useWizard();

  return (
    <StepLayout
      eyebrow="04 — Connect"
      title="Pull something in?"
      sub="Optional — Loamss is useful without any sources, but most users connect at least one. You can do this now or any time later from the Sources tab."
    >
      <div className="space-y-3">
        <Choice
          selected={connect === "skip"}
          onSelect={() => setConnect("skip")}
          title="Skip — I&rsquo;ll connect sources later"
          description="The dashboard will be quiet for now. Add a source any time from Sources → Add."
          badge="Default"
        />

        <Choice
          selected={connect === "gmail"}
          onSelect={() => setConnect("gmail")}
          title="Gmail"
          description="The first reference connector. Read-only message sync via Gmail v1. We&rsquo;ll walk you through the OAuth handshake after setup completes."
          meta="source:gmail · requires a Google OAuth client (one-time)"
          details={
            <div className="space-y-4">
              <Note kind="info">
                <strong className="text-ink">One-time setup tip.</strong>{" "}
                Gmail&rsquo;s OAuth flow will say &ldquo;App isn&rsquo;t
                verified&rdquo; the first time — that&rsquo;s expected for a
                self-hosted client. Click <em>Advanced → Go to Loamss</em>{" "}
                when you see it. We&rsquo;ll show you again at the right moment.
              </Note>
              <div className="text-xs text-ink-quiet">
                We&rsquo;ll do this after Finish so you&rsquo;re not jumping
                between Google Cloud Console and this wizard.
              </div>
            </div>
          }
        />

        <Choice
          selected={false}
          onSelect={() => {}}
          title="Other sources"
          description="Calendar, Drive, Slack, GitHub, Notion, …"
          disabled
          badge="Coming soon"
        />
      </div>

      <FinishFooter
        onBack={() => goTo("models")}
        onFinish={startSubmit}
        connect={connect}
        submitting={submitting}
        submitProgress={submitProgress}
      />
    </StepLayout>
  );
}

/*
 * FinishFooter — like StepFooter but the right button is "Finish setup"
 * and triggers the mocked submission. While submitting, we show a
 * thin progress bar in place of the footer's actions.
 */
interface FinishFooterProps {
  onBack: () => void;
  onFinish: () => void;
  connect: string;
  submitting: boolean;
  submitProgress: number;
}

function FinishFooter({
  onBack,
  onFinish,
  connect,
  submitting,
  submitProgress,
}: FinishFooterProps) {
  if (submitting) {
    return (
      <div className="pt-8 mt-2 border-t border-ink-hairline-soft">
        <div className="smallcap text-ink-quiet mb-3">Finishing up…</div>
        <div className="relative h-1 w-full bg-ink-hairline-soft rounded-full overflow-hidden">
          <div
            className="absolute inset-y-0 left-0 bg-brand transition-all duration-500 ease-out"
            style={{ width: `${submitProgress}%` }}
          />
        </div>
        <div className="mt-3 text-xs text-ink-quiet font-mono tabular">
          {submitProgress < 30 && "writing storage configuration"}
          {submitProgress >= 30 && submitProgress < 70 &&
            "writing memory configuration"}
          {submitProgress >= 70 && submitProgress < 100 &&
            "registering models"}
          {submitProgress === 100 && "done"}
        </div>
      </div>
    );
  }

  return (
    <div className="flex items-center justify-between pt-8 mt-2 border-t border-ink-hairline-soft">
      <Button variant="ghost" size="md" onClick={onBack}>
        <BackGlyph />
        Back to Models
      </Button>
      <Button onClick={onFinish}>
        {connect === "gmail" ? "Finish + Connect Gmail" : "Finish setup"}
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
