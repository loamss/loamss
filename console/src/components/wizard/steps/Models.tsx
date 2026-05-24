"use client";

import { useEffect, useState } from "react";
import { Choice } from "@/components/primitives/Choice";
import { Field } from "@/components/primitives/Field";
import { Note } from "@/components/primitives/Note";
import { StepLayout, StepFooter } from "./Storage";
import { useWizard } from "@/lib/wizard-state";
import { probeOllama } from "@/lib/runtime-client";

/*
 * Models — optional. The most decision-heavy screen of the wizard.
 *
 * Three options:
 *
 *   skip      — explicit "no model right now"; preselected
 *   anthropic — paste an API key (Field with prefix)
 *   ollama    — auto-detect at localhost:11434; show detected/not
 *
 * Ollama detection runs as a real fetch to /api/tags. Three states:
 *   checking      — pulsing dot
 *   detected      — sage dot + the list of installed models
 *   not-detected  — amber dot + setup hint
 *   cors-blocked  — amber dot + an honest "browser CORS, runtime
 *                   may still reach it" note (this is the
 *                   common case in dev since Ollama doesn't ship
 *                   CORS headers)
 */
type OllamaStatus =
  | { state: "checking" }
  | { state: "detected"; models: string[] }
  | { state: "not-detected"; reason: string }
  | { state: "cors-blocked"; reason: string };

export function Models() {
  const { model, setModel, goTo } = useWizard();
  const [ollamaStatus, setOllamaStatus] = useState<OllamaStatus>({
    state: "checking",
  });

  useEffect(() => {
    // Honor the dev shortcut for offline testing.
    const params = new URLSearchParams(window.location.search);
    if (params.get("ollama") === "missing") {
      setOllamaStatus({
        state: "not-detected",
        reason: "(dev override: ?ollama=missing)",
      });
      return;
    }

    let cancelled = false;
    void probeOllama(model.ollamaUrl).then((result) => {
      if (cancelled) return;
      setOllamaStatus(result);
    });
    return () => {
      cancelled = true;
    };
  }, [model.ollamaUrl]);

  const nextDisabled =
    model.mode === "anthropic" && model.anthropicKey.trim().length < 8;

  return (
    <StepLayout
      eyebrow="03 — Models"
      title="Want Loamss to use a model?"
      sub="Models embed your data for fast search, summarize threads, and resolve entities across sources. Skip is a real choice — ingestion and browsing still work; only semantic search is gated on this."
    >
      <div className="space-y-3">
        <Choice
          selected={model.mode === "skip"}
          onSelect={() => setModel({ mode: "skip" })}
          title="Skip for now"
          description="No model configured. Ingestion + entity resolution still work; semantic queries fall back to exact-match. Add a model later under Settings."
          badge="No setup"
        />

        <Choice
          selected={model.mode === "anthropic"}
          onSelect={() => setModel({ mode: "anthropic" })}
          title="Anthropic Claude"
          description="Hosted. Strong on summaries + entity resolution. The runtime never sends your data here unless a capsule explicitly calls model.call."
          details={
            <div className="space-y-4">
              <Field
                label="API key"
                value={model.anthropicKey}
                onChange={(e) => setModel({ anthropicKey: e.target.value })}
                placeholder="paste your key"
                type="password"
                prefix="sk-ant-"
                mono
                help={
                  <>
                    Stored in your OS keychain — never written to disk in plain
                    text. Get a key at{" "}
                    <a
                      href="https://console.anthropic.com/settings/keys"
                      target="_blank"
                      rel="noreferrer"
                      className="underline underline-offset-2 text-ink-muted hover:text-brand"
                    >
                      console.anthropic.com
                    </a>
                    .
                  </>
                }
              />
            </div>
          }
        />

        <Choice
          selected={model.mode === "ollama"}
          onSelect={() => setModel({ mode: "ollama" })}
          title="Local model via Ollama"
          description="Runs entirely on your machine — no API key, no data leaves. Best for embeddings; usable for summaries depending on the model you've pulled."
          details={
            <div className="space-y-4">
              <OllamaStatusRow status={ollamaStatus} url={model.ollamaUrl} />
              {ollamaStatus.state === "not-detected" && (
                <Note kind="warn">
                  Ollama isn&rsquo;t reachable at{" "}
                  <span className="font-mono text-2xs">
                    {model.ollamaUrl}
                  </span>
                  . Install it from{" "}
                  <a
                    href="https://ollama.com/download"
                    target="_blank"
                    rel="noreferrer"
                    className="underline underline-offset-2 text-ink hover:text-brand"
                  >
                    ollama.com/download
                  </a>{" "}
                  and run{" "}
                  <span className="font-mono text-2xs">ollama serve</span>, then
                  return.
                </Note>
              )}
              {ollamaStatus.state === "cors-blocked" && (
                <Note kind="info">
                  The browser can&rsquo;t reach Ollama directly (CORS).
                  The runtime can still talk to it from your machine —
                  pick this option and we&rsquo;ll verify on the
                  runtime side at submit time.
                </Note>
              )}
              <Field
                label="Endpoint"
                value={model.ollamaUrl}
                onChange={(e) => setModel({ ollamaUrl: e.target.value })}
                placeholder="http://localhost:11434"
                mono
                help="Override only if you're running Ollama on a different host or port."
              />
            </div>
          }
        />
      </div>

      <StepFooter
        backLabel="Memory"
        onBack={() => goTo("memory")}
        nextLabel="Continue"
        onNext={() => goTo("connect")}
        nextDisabled={nextDisabled}
      />
    </StepLayout>
  );
}

/*
 * OllamaStatusRow — the live-feeling status indicator. Four states
 * from the real probe in runtime-client.ts:
 *
 *   checking      — pulsing dot
 *   detected      — sage dot + model count + "healthy" badge
 *   not-detected  — amber dot
 *   cors-blocked  — amber dot but "uncertain" framing (this is the
 *                   common case in dev because Ollama doesn't ship
 *                   CORS headers; the runtime CAN still reach it)
 */
function OllamaStatusRow({
  status,
  url,
}: {
  status: OllamaStatus;
  url: string;
}) {
  return (
    <div className="flex items-center gap-3 px-4 py-3 rounded border border-ink-hairline-soft bg-paper-raised">
      <StatusDot status={status} />
      <div className="flex-1 min-w-0">
        <div className="text-sm text-ink">
          {status.state === "checking" && "Checking for Ollama…"}
          {status.state === "detected" &&
            `Ollama detected · ${status.models.length} model${
              status.models.length === 1 ? "" : "s"
            }`}
          {status.state === "not-detected" && "Ollama not detected"}
          {status.state === "cors-blocked" && "Ollama reachability unknown"}
        </div>
        <div className="font-mono text-2xs text-ink-quiet truncate">
          {status.state === "detected" && status.models.length > 0
            ? status.models.slice(0, 3).join(" · ")
            : url}
        </div>
      </div>
      {status.state === "detected" && (
        <div className="smallcap text-sage flex-none">healthy</div>
      )}
    </div>
  );
}

function StatusDot({ status }: { status: OllamaStatus }) {
  if (status.state === "checking") {
    return (
      <span className="relative inline-flex h-2.5 w-2.5 flex-none">
        <span className="absolute inset-0 rounded-full bg-ink-hairline animate-pulse-soft" />
      </span>
    );
  }
  if (status.state === "detected") {
    return (
      <span className="relative inline-flex h-2.5 w-2.5 flex-none">
        <span className="absolute inset-0 rounded-full bg-sage" />
      </span>
    );
  }
  return (
    <span className="relative inline-flex h-2.5 w-2.5 flex-none">
      <span className="absolute inset-0 rounded-full bg-amber" />
    </span>
  );
}
