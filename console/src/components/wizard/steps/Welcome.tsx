"use client";

import { Button } from "@/components/primitives/Button";
import { useWizard } from "@/lib/wizard-state";

/*
 * Welcome — no choices, just an invitation.
 *
 * Visual approach: a large serif headline, a paragraph that sets
 * tone and expectation (three minutes; data sovereignty), one big
 * primary action, one quiet text-link alternative.
 *
 * Composition: title + body sit in the upper-left third of the
 * available canvas — not centered, not full-width. This breaks the
 * "welcome modal" cliché and reads more like the opening page of a
 * book.
 */
export function Welcome() {
  const goTo = useWizard((s) => s.goTo);

  return (
    <div className="max-w-panel mx-auto pt-12 sm:pt-20">
      <div className="space-y-9">
        {/* The tiny pre-title — small caps, ink-quiet, sits above
         * the headline like a bookmark. */}
        <div
          className="smallcap text-ink-quiet animate-stagger-1"
          style={{ opacity: 0 }}
        >
          Loamss · v0.1 · first run
        </div>

        {/* Headline. Big, italic terminal, optical-size big. */}
        <h1
          className="font-serif text-4xl sm:text-display text-ink leading-[1.05] tracking-tight animate-stagger-2"
          style={{
            fontVariationSettings: "'opsz' 144, 'wght' 400, 'SOFT' 0",
            opacity: 0,
          }}
        >
          Welcome to a place
          <br />
          <em
            className="not-italic"
            style={{ fontVariationSettings: "'opsz' 144, 'wght' 400, 'WONK' 1" }}
          >
            <span className="italic">that&apos;s yours.</span>
          </em>
        </h1>

        {/* Subhead — body sans, generous line height. Two short
         * paragraphs, not one long one. */}
        <div
          className="max-w-prose space-y-4 text-base text-ink-muted leading-relaxed animate-stagger-3"
          style={{ opacity: 0 }}
        >
          <p>
            Loamss is personal data infrastructure: a runtime that
            keeps what you connect to it under <em>your</em> control,
            with an audit log you can read.
          </p>
          <p>
            We&rsquo;re going to set up storage, memory, and an optional
            model in about three minutes. The defaults are reasonable.
            You can change anything later.
          </p>
        </div>

        {/* Primary action + the secondary path. */}
        <div
          className="flex flex-wrap items-center gap-x-6 gap-y-3 pt-2 animate-stagger-4"
          style={{ opacity: 0 }}
        >
          <Button
            size="lg"
            onClick={() => goTo("storage")}
            aria-label="Begin setup"
          >
            Begin setup
            <ArrowGlyph />
          </Button>
          <button
            type="button"
            className="text-sm text-ink-quiet hover:text-ink-muted transition-colors underline-offset-4 hover:underline"
            onClick={() => {
              // In the real console, this opens the raw-config editor
              // (Settings → Advanced → Edit config). For now, just a
              // no-op with a tooltip-feeling message.
              alert(
                "In the real console this opens the raw config editor (Settings → Advanced → Edit config).",
              );
            }}
          >
            Have a config file already? Import it.
          </button>
        </div>
      </div>

      {/* A small piece of editorial flourish at the bottom — a brief
       * "what you'll set up" preview that previews the chapters.
       * Restrained: no boxes, just typography. */}
      <div
        className="mt-22 sm:mt-30 pt-8 border-t border-ink-hairline-soft animate-stagger-4"
        style={{ opacity: 0 }}
      >
        <div className="smallcap text-ink-quiet mb-5">
          What we&rsquo;ll set up
        </div>
        <ol className="space-y-2 text-sm text-ink-muted">
          {[
            { num: "01", title: "Storage", note: "where Loamss keeps things" },
            { num: "02", title: "Memory", note: "how it organizes them" },
            {
              num: "03",
              title: "Models",
              note: "optional — for embedding + summaries",
            },
            {
              num: "04",
              title: "Connect",
              note: "optional — pull data in from somewhere",
            },
          ].map((item) => (
            <li key={item.num} className="flex items-baseline gap-4">
              <span className="font-serif text-ink-quiet text-sm tabular w-8">
                {item.num}
              </span>
              <span className="font-sans text-ink">{item.title}</span>
              <span className="text-ink-quiet font-sans">— {item.note}</span>
            </li>
          ))}
        </ol>
      </div>
    </div>
  );
}

function ArrowGlyph() {
  return (
    <svg
      viewBox="0 0 16 16"
      className="h-3.5 w-3.5"
      aria-hidden="true"
      fill="none"
    >
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
