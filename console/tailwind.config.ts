import type { Config } from "tailwindcss";

/*
 * The Loamss console's visual identity, encoded as a Tailwind theme.
 *
 * Direction: editorial-technical minimalism. Warm paper, deep ink,
 * serif headers, technical sans body, monospace for IDs and paths.
 * Hairlines instead of borders. Sage / dusty amber / brick red for
 * semantic colors. One brand accent: a deep forest green that reads
 * as "vault" and "thriving" rather than "tech startup."
 *
 * If you're tweaking this, the principle is: it should look like a
 * field manual or logbook, not a SaaS dashboard. Bias toward
 * restraint at every choice.
 */
const config: Config = {
  content: ["./src/**/*.{ts,tsx}"],
  darkMode: "media",
  theme: {
    extend: {
      colors: {
        // Paper + ink: the foundational warm-neutral pairing.
        paper: {
          DEFAULT: "#F6F2EA", // primary background — warm off-white
          deep: "#EDE7D9", // recessed surfaces (subtle wells)
          raised: "#FBF8F1", // raised cards (very subtle lift)
          edge: "#E5DECF", // a touch deeper for visual breaks
        },
        ink: {
          DEFAULT: "#1B1814", // body text — deep brown-black, never #000
          muted: "#5A554C", // secondary text
          quiet: "#8E877B", // tertiary; hints, captions
          ghost: "#B4ADA0", // disabled / placeholder
          hairline: "#D6CFC0", // 1px rule color
          "hairline-soft": "#E5DECF", // softest divider
        },
        // The one brand accent. Used sparingly: selected radios,
        // primary button background, focus ring. Almost-black green.
        brand: {
          DEFAULT: "#1F3D2C",
          deep: "#162C20",
          muted: "#445F4E",
          tint: "#D9E0DA", // for subtle background washes
        },
        // Semantic accents. Earned colors, not startup neon.
        sage: {
          DEFAULT: "#3D7B5C", // ok — success, healthy, complete
          tint: "#E0EBE4",
        },
        amber: {
          DEFAULT: "#A87838", // warn — needs attention, pending
          tint: "#F0E5D0",
        },
        brick: {
          DEFAULT: "#94392C", // error — broken, danger zone
          tint: "#F0DDD8",
        },
      },
      fontFamily: {
        // Serif: editorial, characterful, used for display + numbers
        // in section headers. Fraunces is variable with optical
        // sizing — it adapts to size, which gives the wizard step
        // numerals visual weight without going heavy.
        serif: [
          "var(--font-fraunces)",
          "Georgia",
          "Cambria",
          "Times New Roman",
          "Times",
          "serif",
        ],
        // Sans: technical character, not sterile. IBM Plex Sans was
        // designed for IBM's infrastructure documentation — exactly
        // the register we want.
        sans: [
          "var(--font-plex-sans)",
          "ui-sans-serif",
          "system-ui",
          "-apple-system",
          "BlinkMacSystemFont",
          "Helvetica Neue",
          "sans-serif",
        ],
        // Mono: for IDs, paths, hashes. Plex Mono pairs with Plex Sans.
        mono: [
          "var(--font-plex-mono)",
          "ui-monospace",
          "SFMono-Regular",
          "Menlo",
          "Monaco",
          "Consolas",
          "Liberation Mono",
          "monospace",
        ],
      },
      fontSize: {
        // Editorial scale — slightly wider gaps than Tailwind's default.
        // Sub-base sizes used for labels, captions, small caps.
        "2xs": ["0.6875rem", { lineHeight: "1rem", letterSpacing: "0.05em" }],
        xs: ["0.75rem", { lineHeight: "1.1rem", letterSpacing: "0.02em" }],
        sm: ["0.875rem", { lineHeight: "1.35rem" }],
        base: ["1rem", { lineHeight: "1.6rem" }],
        lg: ["1.125rem", { lineHeight: "1.75rem" }],
        xl: ["1.375rem", { lineHeight: "1.9rem", letterSpacing: "-0.01em" }],
        "2xl": ["1.75rem", { lineHeight: "2.2rem", letterSpacing: "-0.015em" }],
        "3xl": [
          "2.25rem",
          { lineHeight: "2.6rem", letterSpacing: "-0.02em" },
        ],
        "4xl": ["3rem", { lineHeight: "3.4rem", letterSpacing: "-0.025em" }],
        // Display: for the big serif numeral / title moments.
        display: [
          "4rem",
          { lineHeight: "4.4rem", letterSpacing: "-0.03em" },
        ],
      },
      spacing: {
        // Generous: this is for breathing room around dense info.
        "0.75": "0.1875rem",
        "18": "4.5rem",
        "22": "5.5rem",
        "30": "7.5rem",
        "120": "30rem",
        "140": "35rem",
      },
      maxWidth: {
        // Content widths: most wizard cards 32rem; some panels 44rem.
        prose: "32rem",
        panel: "44rem",
        wide: "60rem",
      },
      borderRadius: {
        // Restrained roundness — slightly softer than square, never
        // pillowy. Cards 6px, buttons 4px, inputs 4px.
        sm: "3px",
        DEFAULT: "4px",
        md: "6px",
        lg: "8px",
        xl: "12px",
      },
      boxShadow: {
        // Shadows are rare; when used they're soft and tinted with
        // the paper warmth, not generic black-with-alpha.
        soft: "0 1px 0 rgba(27, 24, 20, 0.04), 0 1px 2px rgba(27, 24, 20, 0.04)",
        raise:
          "0 1px 0 rgba(27, 24, 20, 0.06), 0 4px 12px rgba(27, 24, 20, 0.04)",
        ring: "0 0 0 1px rgba(31, 61, 44, 0.18)",
        "ring-focus": "0 0 0 3px rgba(31, 61, 44, 0.15)",
      },
      letterSpacing: {
        smallcap: "0.12em",
      },
      animation: {
        // Calm, ease-out everywhere. Nothing bouncy.
        "fade-in": "fade-in 320ms cubic-bezier(0.16, 1, 0.3, 1) both",
        "fade-up": "fade-up 380ms cubic-bezier(0.16, 1, 0.3, 1) both",
        "stagger-1": "fade-up 380ms 60ms cubic-bezier(0.16, 1, 0.3, 1) both",
        "stagger-2": "fade-up 380ms 120ms cubic-bezier(0.16, 1, 0.3, 1) both",
        "stagger-3": "fade-up 380ms 180ms cubic-bezier(0.16, 1, 0.3, 1) both",
        "stagger-4": "fade-up 380ms 240ms cubic-bezier(0.16, 1, 0.3, 1) both",
        "pulse-soft": "pulse-soft 2s cubic-bezier(0.4, 0, 0.6, 1) infinite",
        "progress-indeterminate":
          "progress-indeterminate 1.6s ease-in-out infinite",
      },
      keyframes: {
        "fade-in": {
          from: { opacity: "0" },
          to: { opacity: "1" },
        },
        "fade-up": {
          from: { opacity: "0", transform: "translateY(6px)" },
          to: { opacity: "1", transform: "translateY(0)" },
        },
        "pulse-soft": {
          "0%, 100%": { opacity: "1" },
          "50%": { opacity: "0.55" },
        },
        "progress-indeterminate": {
          "0%": { transform: "translateX(-100%)" },
          "100%": { transform: "translateX(100%)" },
        },
      },
    },
  },
  plugins: [],
};

export default config;
