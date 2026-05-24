"use client";

import { Button } from "@/components/primitives/Button";
import { Note } from "@/components/primitives/Note";
import { useWizard } from "@/lib/wizard-state";

/*
 * Done — the "you're set up" landing.
 *
 * Editorial moment: this is NOT a victory lap. It's a small
 * directory of next actions, framed honestly ("or just leave it").
 *
 * Composition: a serif checkmark headline, a brief config summary
 * (so the user can verify what they just confirmed), three primary
 * next-action cards, and a quiet "or do nothing" reassurance.
 */
export function Done() {
  const {
    storage,
    memory,
    model,
    connect,
    reset,
    submitResult,
    startSubmit,
    submitting,
  } = useWizard();
  const handedOff = submitResult?.ok ?? false;
  const conflict = submitResult && !submitResult.ok ? submitResult.conflict : undefined;

  // The eyebrow color/label tracks the actual outcome:
  //   sage  — config written to disk
  //   amber — needs the user to confirm overwrite
  //   amber — generic error (network / runtime)
  const eyebrow = handedOff
    ? "Setup complete"
    : conflict
      ? "Existing config — confirm to overwrite"
      : "Setup not yet persisted";
  const eyebrowTone = handedOff ? "text-sage" : "text-amber";

  return (
    <div className="max-w-panel mx-auto pt-8 sm:pt-12">
      <div className="space-y-9">
        <div
          className={["smallcap animate-stagger-1", eyebrowTone].join(" ")}
          style={{ opacity: 0 }}
        >
          {eyebrow} ·{" "}
          {new Date().toLocaleString("en-US", { dateStyle: "medium" })}
        </div>

        <h1
          className="font-serif text-4xl sm:text-5xl text-ink leading-tight tracking-tight animate-stagger-2"
          style={{
            fontVariationSettings: "'opsz' 144, 'wght' 400",
            opacity: 0,
          }}
        >
          {handedOff ? "Your Loamss is configured." : "Your Loamss is running."}
        </h1>

        <p
          className="max-w-prose text-base text-ink-muted leading-relaxed animate-stagger-3"
          style={{ opacity: 0 }}
        >
          The runtime is bound to{" "}
          <span className="font-mono text-sm text-ink">127.0.0.1:7777</span>{" "}
          and isn&rsquo;t reachable from anywhere else on the network. Nothing
          leaves your machine until you grant something access.
        </p>

        {/* Success — show where the file landed + the restart hint. */}
        {handedOff && submitResult?.writtenTo && (
          <div className="animate-stagger-3" style={{ opacity: 0 }}>
            <Note kind="info">
              Configuration written to{" "}
              <span className="font-mono text-2xs">
                {submitResult.writtenTo}
              </span>
              .{" "}
              {submitResult.nextStep ??
                "Restart the runtime to apply."}
            </Note>
          </div>
        )}

        {/* Conflict — an existing config blocked the write. Offer a
         * one-click overwrite; the runtime renames the existing file
         * to a timestamped .bak so nothing is lost without consent. */}
        {conflict && (
          <div className="animate-stagger-3 space-y-3" style={{ opacity: 0 }}>
            <Note kind="warn">
              A config file already exists at{" "}
              <span className="font-mono text-2xs">{conflict.path}</span>.{" "}
              {conflict.hint}
            </Note>
            <button
              type="button"
              disabled={submitting}
              onClick={() => {
                void startSubmit(true);
              }}
              className="text-sm text-brand hover:text-brand-deep underline underline-offset-2 disabled:opacity-50"
            >
              Back up the existing file and write the new one
            </button>
          </div>
        )}

        {/* Generic error — surface the runtime's reason verbatim. */}
        {submitResult && !submitResult.ok && !conflict && (
          <div className="animate-stagger-3" style={{ opacity: 0 }}>
            <Note kind="warn">
              The runtime couldn&rsquo;t persist your selections:{" "}
              {submitResult.reason ?? "unknown error"}.{" "}
              Your selections are preserved below — try again, or apply
              them by hand via{" "}
              <span className="font-mono text-2xs">loamss config</span>.
            </Note>
          </div>
        )}

        {/* Config summary — a small editorial recap. */}
        <ConfigSummary
          storage={storage}
          memory={memory}
          model={model}
          connect={connect}
        />

        {/* Next actions — three cards, restrained. */}
        <div
          className="space-y-4 pt-6 animate-stagger-4"
          style={{ opacity: 0 }}
        >
          <div className="smallcap text-ink-quiet">What&rsquo;s next</div>
          <NextActions hasGmail={connect === "gmail"} />
        </div>

        <div
          className="pt-8 border-t border-ink-hairline-soft animate-stagger-4 flex items-center justify-between"
          style={{ opacity: 0 }}
        >
          <span className="text-sm text-ink-muted">
            Or just leave it. The runtime stays out of your way.
          </span>
          <button
            type="button"
            onClick={reset}
            className="text-xs text-ink-quiet hover:text-ink-muted transition-colors underline underline-offset-2"
          >
            Re-run wizard
          </button>
        </div>
      </div>
    </div>
  );
}

interface ConfigSummaryProps {
  storage: ReturnType<typeof useWizard.getState>["storage"];
  memory: ReturnType<typeof useWizard.getState>["memory"];
  model: ReturnType<typeof useWizard.getState>["model"];
  connect: ReturnType<typeof useWizard.getState>["connect"];
}

function ConfigSummary({ storage, memory, model, connect }: ConfigSummaryProps) {
  const lines = [
    {
      label: "Storage",
      value:
        storage.mode === "default"
          ? "encrypted local folder"
          : storage.mode === "custom"
            ? `custom · ${storage.encrypt ? "encrypted" : "plain"}`
            : "cloud",
      detail:
        storage.mode === "default"
          ? "~/.loamss/storage"
          : storage.mode === "custom"
            ? storage.customPath || "(unset)"
            : "",
    },
    {
      label: "Memory",
      value:
        memory === "sqlite"
          ? "SQLite"
          : memory === "postgres"
            ? "Postgres"
            : "Chroma",
      detail:
        memory === "sqlite" ? "memory:sqlite · ~/.loamss/memory.db" : "",
    },
    {
      label: "Models",
      value:
        model.mode === "skip"
          ? "None — add later"
          : model.mode === "anthropic"
            ? "Anthropic Claude"
            : "Ollama (local)",
      detail:
        model.mode === "anthropic"
          ? "model:anthropic"
          : model.mode === "ollama"
            ? `model:ollama · ${model.ollamaUrl}`
            : "",
    },
    {
      label: "Sources",
      value:
        connect === "gmail" ? "Gmail (pending connect)" : "None — add later",
      detail: "",
    },
  ];
  return (
    <div
      className="border border-ink-hairline rounded-md bg-paper-raised animate-stagger-3"
      style={{ opacity: 0 }}
    >
      {lines.map((line, idx) => (
        <div
          key={line.label}
          className={[
            "flex items-baseline gap-4 px-5 py-3",
            idx > 0 && "border-t border-ink-hairline-soft",
          ]
            .filter(Boolean)
            .join(" ")}
        >
          <div className="smallcap text-ink-quiet w-22 flex-none">
            {line.label}
          </div>
          <div className="flex-1 min-w-0">
            <div className="text-sm text-ink">{line.value}</div>
            {line.detail && (
              <div className="font-mono text-xs text-ink-quiet truncate">
                {line.detail}
              </div>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}

function NextActions({ hasGmail }: { hasGmail: boolean }) {
  return (
    <div className="space-y-3">
      {hasGmail && (
        <ActionRow
          eyebrow="Pending"
          title="Finish connecting Gmail"
          description="We saved your intent. Click to walk through the OAuth flow now."
          actionLabel="Continue"
          primary
        />
      )}
      <ActionRow
        eyebrow="Sources"
        title="Connect a source"
        description="Pull data into your storage from Gmail, files, a calendar, or anything else."
        actionLabel="Open Sources"
      />
      <ActionRow
        eyebrow="Apps"
        title="Pair an app"
        description="Let ChatGPT, an inbox UI, or anything else with MCP support read your memory."
        actionLabel="Open Apps"
      />
      <ActionRow
        eyebrow="Capsules"
        title="Install a capsule"
        description="Extensions that run inside the runtime — daily briefings, organizers, custom tools."
        actionLabel="Open Capsules"
      />
    </div>
  );
}

interface ActionRowProps {
  eyebrow: string;
  title: string;
  description: string;
  actionLabel: string;
  primary?: boolean;
}

function ActionRow({
  eyebrow,
  title,
  description,
  actionLabel,
  primary = false,
}: ActionRowProps) {
  return (
    <div
      className={[
        "group flex items-start gap-5 px-5 py-4 rounded-md border transition-all duration-200",
        primary
          ? "border-brand bg-brand-tint/30 hover:bg-brand-tint/50"
          : "border-ink-hairline bg-paper-raised hover:border-ink-muted/40 hover:bg-paper-raised",
      ].join(" ")}
    >
      <div className="flex-1 min-w-0">
        <div
          className={[
            "smallcap",
            primary ? "text-brand" : "text-ink-quiet",
          ].join(" ")}
        >
          {eyebrow}
        </div>
        <div className="mt-1 font-sans text-base text-ink">{title}</div>
        <div className="mt-1 text-sm text-ink-muted leading-relaxed">
          {description}
        </div>
      </div>
      <Button
        size="sm"
        variant={primary ? "primary" : "secondary"}
        onClick={() => {}}
      >
        {actionLabel}
      </Button>
    </div>
  );
}
