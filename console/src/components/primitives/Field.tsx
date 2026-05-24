"use client";

import { forwardRef } from "react";
import type { InputHTMLAttributes, ReactNode } from "react";

/*
 * Field — labeled input with optional help text and inline error.
 *
 * The visual approach: a small-caps label above, a generously-padded
 * input with a hairline bottom border (and a full border on hover/
 * focus), help text below in muted ink. Errors take over the help-
 * text slot with brick-red color.
 */
interface FieldProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, "prefix"> {
  label: string;
  help?: ReactNode;
  error?: string;
  mono?: boolean; // monospace input (for paths, IDs, API keys)
  prefix?: ReactNode; // e.g., "sk-ant-" prefix marker
}

export const Field = forwardRef<HTMLInputElement, FieldProps>(function Field(
  { label, help, error, mono = false, prefix, className = "", id, ...rest },
  ref,
) {
  const inputId = id || `field-${label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={inputId} className="smallcap">
        {label}
      </label>
      <div
        className={[
          "flex items-stretch rounded border bg-paper-raised",
          "transition-colors duration-150",
          error
            ? "border-brick/60"
            : "border-ink-hairline hover:border-ink-muted/50 focus-within:border-brand",
        ].join(" ")}
      >
        {prefix && (
          <span className="flex items-center pl-3 pr-1.5 font-mono text-sm text-ink-quiet border-r border-ink-hairline-soft mr-1">
            {prefix}
          </span>
        )}
        <input
          ref={ref}
          id={inputId}
          className={[
            "flex-1 min-w-0 bg-transparent outline-none",
            "px-3 py-2.5 text-sm text-ink placeholder:text-ink-ghost",
            mono ? "font-mono" : "font-sans",
            className,
          ].join(" ")}
          {...rest}
        />
      </div>
      {error ? (
        <p className="text-xs text-brick font-sans">{error}</p>
      ) : (
        help && (
          <p className="text-xs text-ink-quiet font-sans leading-relaxed">
            {help}
          </p>
        )
      )}
    </div>
  );
});
