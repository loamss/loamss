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
  const { storage, memory, model, connect, reset, submitResult } = useWizard();
  const handedOff = submitResult?.ok ?? false;

  return (
    <div className="max-w-panel mx-auto pt-8 sm:pt-12">
      <div className="space-y-9">
        <div
          className={[
            "smallcap animate-stagger-1",
            handedOff ? "text-sage" : "text-amber",
          ].join(" ")}
          style={{ opacity: 0 }}
        >
          {handedOff ? "Setup complete" : "Setup recorded locally"} ·{" "}
          {new Date().toLocaleString("en-US", { dateStyle: "medium" })}
        </div>

        <h1
          className="font-serif text-4xl sm:text-5xl text-ink leading-tight tracking-tight animate-stagger-2"
          style={{
            fontVariationSettings: "'opsz' 144, 'wght' 400",
            opacity: 0,
          }}
        >
          Your Loamss is running.
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

        {/* Honest status of what the runtime actually did with the
         * config the wizard sent. v0.1: the runtime accepts the POST
         * but doesn't write the config file yet — this Note makes
         * that explicit rather than pretending the setup persisted.
         * Removed when the writer ships. */}
        {submitResult && !handedOff && (
          <div className="animate-stagger-3" style={{ opacity: 0 }}>
            <Note kind="warn">
              The runtime accepted your config but the file-writer
              isn&rsquo;t shipped yet (v0.1 stub).{" "}
              {submitResult.reason ?? ""}{" "}
              Your selections are preserved below — you can apply them
              manually via{" "}
              <span className="font-mono text-2xs">loamss config</span>{" "}
              while the writer is in flight.
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
