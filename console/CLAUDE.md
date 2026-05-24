# console/CLAUDE.md

Subsystem context for Claude Code sessions working in `console/`. Loaded lazily by Claude Code when files in this subtree are edited.

The repo-level `CLAUDE.md` covers the project's principles, the spec set, and the trust model. The repo-level `console-design.md` is the design source-of-truth (IA, flows, screens, decisions). This file adds the conventions specific to the Next.js code in this subtree.

## What lives here

The first-run wizard for Loamss. v0.1 prototype:

- `src/app/` — Next.js App Router pages (currently just `/` → wizard)
- `src/components/primitives/` — Button, Choice, Field, Note, Wordmark (built locally; no UI kit)
- `src/components/wizard/` — WizardShell, Stepper, six step components
- `src/lib/wizard-state.ts` — Zustand store + step ordering

The Welcome, Storage, Memory, Models, Connect, and Done screens are all functional with mocked async submission.

## Planned (not yet built)

The seven post-wizard tabs from `console-design.md`:

- **Dashboard** — composite of sources + apps + activity + pending approvals
- **Sources** — list + detail + Gmail-connect 4-step wizard
- **Apps** — list + per-app grant editor + pair-new flow
- **Capsules** — list + install (from path) + permission-slip review
- **Memory** — entities + threads with detail drill-in
- **Activity** — audit log made human
- **Settings** — storage / memory / models / backup / advanced

Each tab maps to existing CLI commands (`loamss source`, `loamss client`, etc.) and to MCP surfaces (`entities.list`, `threads.show`, etc.).

## Conventions specific to this subtree

### Style

- **Tailwind first**, with custom theme in `tailwind.config.ts`. Don't reach for `style={{ ... }}` unless a value is computed at runtime (e.g., font-variation-settings derived from current step).
- **No third-party UI kit** (no shadcn, no Radix, no Headless UI). Every primitive lives in `src/components/primitives/`.
- **No icon library**. SVGs are inline, minimal, paired with the action they sit next to. The wireframes warn against decorative iconography.
- **TypeScript strict** — `strict`, `noEmit`, `isolatedModules` on. Errors that creep past lint will fail the static export.

### Visual identity (read this before adding components)

The aesthetic is **editorial-technical minimalism**. "Infrastructure software a thoughtful adult would trust with their data." Not a SaaS dashboard.

- Paper-warm background (`bg-paper`), never `bg-white`
- Deep brown-black text (`text-ink`), never `text-black`
- Hairlines (`border-ink-hairline`, 1px), not heavy borders
- Serif (Fraunces, via `font-serif`) for display numerals and editorial headlines
- Sans (IBM Plex Sans, via `font-sans`) for body
- Mono (IBM Plex Mono, via `font-mono`) for IDs, paths, hashes, command names
- Small caps (`smallcap` utility) for section eyebrows and step labels
- Brand-green selection state (`border-brand bg-brand-tint/40` for radio cards)
- Sage / dusty-amber / brick-red for ok / warn / error
- 250ms ease-out transitions everywhere; no springs, no bounces

If a new component feels visually loud, it's probably wrong. Bias toward restraint.

### State

- **Wizard state** lives in `src/lib/wizard-state.ts` (Zustand). The store is small and focused; resist temptation to put non-wizard state here.
- **Tab state** (when we build the post-wizard tabs) should stay in component-local hooks until there's a real cross-component need.
- **Server state** (runtime data via `@loamss/sdk`) will eventually use TanStack Query or similar. For now everything is mocked.

### Component patterns

- Primitives accept `className` so callers can compose; they don't accept arbitrary style props (resist `as` polymorphism for now).
- Variant + size enums (Button) over a free-form class API.
- Choice supports a `details` slot that animates open when selected — used by Storage's custom path and Models' Anthropic / Ollama configurations.
- Steps share a `StepLayout` + `StepFooter` pair (in `Storage.tsx`) for consistent eyebrow / title / footer treatment.

### Animations

CSS-only via Tailwind keyframes (`tailwind.config.ts → keyframes`). The classes:

- `animate-fade-in` — opacity 0 → 1, 320ms
- `animate-fade-up` — opacity + 6px lift, 380ms
- `animate-stagger-{1,2,3,4}` — same as fade-up but with delays for sequential reveals (used on Welcome and Done)
- `animate-pulse-soft` — gentle 2s pulse for live-state indicators
- `animate-progress-indeterminate` — for any loading bar without a fixed progress

Use `page-enter` (in `globals.css`) on wizard step containers — keys the WizardShell so transitions feel intentional.

## Dev shortcuts

In `process.env.NODE_ENV !== "production"`:

- `?step=<id>` — jump to any wizard step (welcome / storage / memory / models / connect / done)
- `?anthropic=1` — pre-select Anthropic with a demo key
- `?ollama=1` — pre-select Ollama
- `?ollama=missing` — flip Ollama detection to "not found" to inspect the warn state

These are guarded in `src/app/page.tsx` and inert in production builds.

## What's planned to land next

Roughly in priority order:

1. Wire the wizard's mocked async to real runtime calls via `@loamss/sdk`
2. Build the post-wizard dashboard (the screen the user lands on after Done)
3. Build the Sources tab + Gmail-connect 4-step sub-wizard
4. Build the Apps tab + permission slip review
5. Build Memory (entities + threads) using `@loamss/sdk` against the live runtime
6. Build Activity (audit log viewer with filters)
7. Build Settings
8. Production embed via Go `embed.FS` (covered in the runtime's CLAUDE.md)

## Where to ask questions

- `/console-design.md` for design decisions (this file is implementation; that file is intent)
- Open an Issue for concrete bugs or feature requests
- Subsystem-level architecture lives in `/ARCHITECTURE.md`
