"use client";

import { useEffect, useState } from "react";
import { DashboardShell } from "@/components/dashboard/DashboardShell";
import { WizardShell } from "@/components/wizard/WizardShell";
import { Welcome } from "@/components/wizard/steps/Welcome";
import { Storage } from "@/components/wizard/steps/Storage";
import { Memory } from "@/components/wizard/steps/Memory";
import { Models } from "@/components/wizard/steps/Models";
import { Connect } from "@/components/wizard/steps/Connect";
import { Done } from "@/components/wizard/steps/Done";
import { getConsoleState } from "@/lib/runtime-client";
import { captureSetupTokenFromURL } from "@/lib/setup-token";
import { useWizard, type WizardStep } from "@/lib/wizard-state";

/*
 * Console root.
 *
 * Two distinct surfaces share the path `/`:
 *
 *   - Wizard:    first-run setup. Shown when no config file has
 *                been written yet (the runtime advertises
 *                config.available=false in /console/state).
 *   - Dashboard: the post-wizard landing — runtime state at a
 *                glance, with the five panes.
 *
 * The route-selection logic runs once on mount, then hands off to
 * the chosen surface. Forcing the wizard via `?step=...` (dev) or
 * the user clicking "Re-run wizard" from the Done screen flips
 * the override flag so we don't bounce them back to the dashboard
 * on the next render.
 */

// Recognised wizard step ids for the dev shortcut. Anything else is ignored.
const validSteps: WizardStep[] = [
  "welcome",
  "storage",
  "memory",
  "models",
  "connect",
  "done",
];

type Surface = "loading" | "wizard" | "dashboard";

export default function ConsoleRoot() {
  const step = useWizard((s) => s.step);
  const goTo = useWizard((s) => s.goTo);
  const setModel = useWizard((s) => s.setModel);
  const refreshRuntimeProbe = useWizard((s) => s.refreshRuntimeProbe);

  // Surface decides which top-level component renders. `loading`
  // is the brief window between mount and the first /console/state
  // response. The wizard rendering won't show inappropriate state
  // (e.g., a Welcome flash before the dashboard) because we hold
  // here until the probe answers.
  const [surface, setSurface] = useState<Surface>("loading");

  // forceWizard overrides the auto-detected surface. Set by the
  // ?step=... dev shortcut and by the "Re-run wizard" button on
  // the dashboard. Without this, completing the wizard and then
  // re-running it would bounce back to the dashboard the moment
  // mount detected an existing config.
  const [forceWizard, setForceWizard] = useState(false);

  // Initial surface decision. We probe /console/state once; if
  // config is present, we land on the dashboard. If the runtime
  // is unreachable (returns null) we default to the wizard — the
  // most useful thing to show someone whose daemon isn't running
  // is the path to start it.
  useEffect(() => {
    // Capture ?setup=<token> first, *before* any /console/* fetch.
    // The function strips the param from the URL via replaceState so
    // it doesn't end up in browser history. Idempotent — safe to
    // call on every mount.
    captureSetupTokenFromURL();

    const params = new URLSearchParams(window.location.search);
    if (params.get("step") || params.get("wizard") === "1") {
      setForceWizard(true);
      setSurface("wizard");
      return;
    }

    let cancelled = false;
    void (async () => {
      const state = await getConsoleState();
      if (cancelled) return;
      // wizard_complete is the honest "the user has completed
      // setup at least once" signal. config.available is true
      // from defaults the moment the daemon starts; it'd send
      // every fresh install straight to the dashboard.
      setSurface(state?.config.wizard_complete ? "dashboard" : "wizard");
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // Dev shortcut: ?step=models lands on a specific wizard step.
  // Only fires when we've forced the wizard surface (so we don't
  // accidentally re-trigger after the dashboard takes over).
  useEffect(() => {
    if (process.env.NODE_ENV === "production") return;
    if (!forceWizard) return;
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
  }, [forceWizard, goTo, setModel]);

  // Probe the runtime continuously for the wizard's runtime-status
  // badge. The dashboard owns its own polling via the dashboard
  // store; we don't double-poll when it's mounted.
  useEffect(() => {
    if (surface !== "wizard") return;
    void refreshRuntimeProbe();
    const interval = setInterval(() => {
      void refreshRuntimeProbe();
    }, 20_000);
    return () => clearInterval(interval);
  }, [surface, refreshRuntimeProbe]);

  if (surface === "loading") {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <span className="font-mono text-xs text-ink-quiet">loading…</span>
      </div>
    );
  }

  if (surface === "dashboard") {
    return <DashboardShell />;
  }

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
