"use client";

import type { ReactNode } from "react";

/*
 * Choice — a radio-card option used throughout the wizard.
 *
 * The visual language: each option is a card with a hairline border;
 * the selected card has a brand-tinted background and a deeper border;
 * the radio indicator is rendered as a small circle that fills with
 * the brand color when selected.
 *
 * Disabled options ("Coming soon") show a muted label with no border
 * change on hover.
 *
 * The component supports an optional `details` slot — when the option
 * is selected, additional configuration UI can appear below the title.
 * This is how "Custom location" expands its path + encryption controls.
 */
interface ChoiceProps {
  selected: boolean;
  onSelect: () => void;
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode; // small line of metadata under the description
  disabled?: boolean;
  badge?: string; // e.g. "Coming soon", "Recommended"
  details?: ReactNode; // shown only when selected
}

export function Choice({
  selected,
  onSelect,
  title,
  description,
  meta,
  disabled = false,
  badge,
  details,
}: ChoiceProps) {
  return (
    <label
      className={[
        "group block cursor-pointer",
        "rounded-md transition-all duration-200 ease-out",
        "border",
        selected
          ? "border-brand bg-brand-tint/40"
          : "border-ink-hairline bg-paper-raised hover:border-ink-muted/40",
        disabled && "cursor-not-allowed opacity-50 hover:border-ink-hairline",
      ]
        .filter(Boolean)
        .join(" ")}
    >
      <div className="flex items-start gap-4 px-5 py-4">
        {/* Radio indicator — restrained circle that fills with brand on
         * select. No animation beyond the color transition. */}
        <span
          aria-hidden="true"
          className={[
            "mt-1 inline-flex h-4 w-4 flex-none items-center justify-center rounded-full",
            "border transition-colors duration-200",
            selected
              ? "border-brand bg-paper"
              : "border-ink-hairline bg-paper",
          ].join(" ")}
        >
          <span
            className={[
              "h-2 w-2 rounded-full transition-all duration-200",
              selected ? "bg-brand scale-100" : "bg-transparent scale-0",
            ].join(" ")}
          />
        </span>

        <div className="flex-1 min-w-0">
          <div className="flex items-baseline justify-between gap-3">
            <div className="font-sans text-base text-ink leading-snug">
              {title}
            </div>
            {badge && (
              <span className="smallcap text-ink-quiet shrink-0">{badge}</span>
            )}
          </div>
          {description && (
            <div className="mt-1 text-sm text-ink-muted leading-relaxed">
              {description}
            </div>
          )}
          {meta && (
            <div className="mt-1.5 font-mono text-xs text-ink-quiet">
              {meta}
            </div>
          )}
        </div>

        <input
          type="radio"
          className="sr-only"
          checked={selected}
          onChange={onSelect}
          disabled={disabled}
        />
      </div>

      {/* Expandable details section — only renders when selected and
       * details are provided. Uses a smooth max-height transition. */}
      {details && (
        <div
          className={[
            "overflow-hidden transition-all duration-300 ease-out",
            selected ? "max-h-[400px] opacity-100" : "max-h-0 opacity-0",
          ].join(" ")}
        >
          <div className="px-5 pb-5 pl-12">
            <div className="hairline mb-4" />
            {details}
          </div>
        </div>
      )}
    </label>
  );
}
