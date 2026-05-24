"use client";

import { forwardRef } from "react";
import type { ButtonHTMLAttributes } from "react";

/*
 * Three button registers:
 *
 *   primary   — forest-green fill, paper text. The single
 *               "continue / approve / confirm" action per view.
 *   secondary — paper background with hairline border. Sits beside
 *               primary as "back" or "alternate path."
 *   ghost     — no background, deep-ink text. For tertiary actions
 *               (Cancel, dismiss, inline links-in-disguise).
 *   danger    — brick-red text on subtle tint. Used only in the
 *               "danger zone" pattern (remove, revoke).
 *
 * Sizes are restrained — md is the default, sm for compact rows,
 * lg only for the wizard's "Let's go" moment.
 */
type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
type ButtonSize = "sm" | "md" | "lg";

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
}

const variantClasses: Record<ButtonVariant, string> = {
  primary:
    "bg-brand text-paper hover:bg-brand-deep active:bg-brand-deep " +
    "disabled:bg-ink-ghost disabled:text-paper disabled:cursor-not-allowed " +
    "shadow-soft hover:shadow-raise transition-shadow",
  secondary:
    "bg-paper text-ink border border-ink-hairline hover:border-ink-muted " +
    "hover:bg-paper-raised active:bg-paper-deep " +
    "disabled:opacity-50 disabled:cursor-not-allowed",
  ghost:
    "bg-transparent text-ink-muted hover:text-ink hover:bg-paper-deep " +
    "disabled:opacity-40 disabled:cursor-not-allowed",
  danger:
    "bg-brick-tint text-brick border border-brick/30 hover:border-brick " +
    "hover:bg-brick hover:text-paper transition-colors",
};

const sizeClasses: Record<ButtonSize, string> = {
  sm: "px-3 py-1.5 text-sm",
  md: "px-5 py-2.5 text-sm",
  lg: "px-7 py-3.5 text-base",
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    { variant = "primary", size = "md", className = "", children, ...rest },
    ref,
  ) {
    return (
      <button
        ref={ref}
        className={[
          // Shared: rounded corners are softer than square, never pillowy.
          "inline-flex items-center justify-center gap-2 rounded font-sans",
          "font-medium tracking-tight",
          // Calm transitions — no spring, no bounce.
          "transition-colors duration-150 ease-out",
          // Focus state is handled globally in globals.css; nothing extra here.
          variantClasses[variant],
          sizeClasses[size],
          className,
        ].join(" ")}
        {...rest}
      >
        {children}
      </button>
    );
  },
);
