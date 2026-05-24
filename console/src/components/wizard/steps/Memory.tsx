"use client";

import { Choice } from "@/components/primitives/Choice";
import { StepLayout, StepFooter } from "./Storage";
import { useWizard } from "@/lib/wizard-state";

/*
 * Memory — pick the vector store.
 *
 * One real option in v0.1 (sqlite); the others exist so the user
 * knows there are options. The wireframe was direct about this: a
 * single default with two "Coming soon" entries.
 *
 * Editorial moment: the eyebrow says "02 — Memory" but the question
 * is asked in plainspoken terms ("organize what it knows about you"),
 * which is the same line the wireframe used.
 */
export function Memory() {
  const { memory, setMemory, goTo } = useWizard();

  return (
    <StepLayout
      eyebrow="02 — Memory"
      title="How should Loamss organize what it knows?"
      sub="Memory is the layer that lets capsules and apps ask questions like 'what threads have I had with Sarah?'. The default is fast and needs no extra setup."
    >
      <div className="space-y-3">
        <Choice
          selected={memory === "sqlite"}
          onSelect={() => setMemory("sqlite")}
          title="Local SQLite"
          description="Embedded, single-file, zero configuration. Fast enough for tens of thousands of entries with vector search."
          meta="memory:sqlite · ~/.loamss/memory.db"
          badge="Recommended"
        />

        <Choice
          selected={false}
          onSelect={() => {}}
          title="Postgres + pgvector"
          description="For setups where memory needs to live in a Postgres instance you already run. Same query semantics; bigger ceiling."
          disabled
          badge="Coming soon"
        />

        <Choice
          selected={false}
          onSelect={() => {}}
          title="Chroma or Qdrant"
          description="Dedicated vector databases. Best when you're scaling past what a single SQLite file handles, or you want sharded recall."
          disabled
          badge="Coming soon"
        />
      </div>

      <StepFooter
        backLabel="Storage"
        onBack={() => goTo("storage")}
        nextLabel="Continue"
        onNext={() => goTo("models")}
      />
    </StepLayout>
  );
}
