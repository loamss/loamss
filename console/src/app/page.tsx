"use client";

import { useEffect } from "react";
import { WizardShell } from "@/components/wizard/WizardShell";
import { Welcome } from "@/components/wizard/steps/Welcome";
import { Storage } from "@/components/wizard/steps/Storage";
import { Memory } from "@/components/wizard/steps/Memory";
import { Models } from "@/components/wizard/steps/Models";
import { Connect } from "@/components/wizard/steps/Connect";
import { Done } from "@/components/wizard/steps/Done";
import { useWizard, type WizardStep } from "@/lib/wizard-state";

/*
 * Console root.
 *
 * In v1 this routes to the dashboard once the user has finished
 * onboarding. For the prototype we drive everything off the wizard
 * store; the URL stays at "/" and the visible content swaps. A real
 * Next.js setup would use route groups so the wizard lives at
 * /onboarding and the dashboard at /, with a server-side check
 * deciding which to show.
 */
// Recognized step ids for the dev shortcut. Anything else is ignored.
const validSteps: WizardStep[] = [
  "welcome",
  "storage",
  "memory",
  "models",
  "connect",
  "done",
];

export default function ConsoleRoot() {
  const step = useWizard((s) => s.step);
  const goTo = useWizard((s) => s.goTo);
  const setModel = useWizard((s) => s.setModel);
  const refreshRuntimeProbe = useWizard((s) => s.refreshRuntimeProbe);

  // Probe the runtime on mount so the header can show
  // "runtime: connected" or "runtime: not reachable". Re-poll every
  // 20 seconds so the badge stays current if the daemon comes up or
  // goes down mid-wizard.
  useEffect(() => {
    void refreshRuntimeProbe();
    const interval = setInterval(() => {
      void refreshRuntimeProbe();
    }, 20_000);
    return () => clearInterval(interval);
  }, [refreshRuntimeProbe]);

  // Dev shortcut: ?step=models lands directly on a step. Useful for
  // screenshots and reviewing individual screens without click-
  // through. Removed in production via guarding on NODE_ENV. We also
  // pre-fill the anthropic key when landing on models so the next-
  // button isn't disabled for a quick visual check.
  useEffect(() => {
    if (process.env.NODE_ENV === "production") return;
    const params = new URLSearchParams(window.location.search);
    const target = params.get("step");
    if (target && validSteps.includes(target as WizardStep)) {
      goTo(target as WizardStep);
    }
    if (params.get("anthropic") === "1") {
      setModel({ mode: "anthropic", anthropicKey: "sk-ant-demo-key-12345" });
    }
    if (params.get("ollama") === "1") {
      setModel({ mode: "ollama" });
    }
  }, [goTo, setModel]);

  // Welcome + Done hide the stepper for their editorial weight.
  const hideStepper = step === "welcome" || step === "done";

  return (
    <WizardShell stepKey={step} showStepper={!hideStepper}>
      {step === "welcome" && <Welcome />}
      {step === "storage" && <Storage />}
      {step === "memory" && <Memory />}
      {step === "models" && <Models />}
      {step === "connect" && <Connect />}
      {step === "done" && <Done />}
    </WizardShell>
  );
}
