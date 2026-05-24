# @loamss/console

The user-facing console for the Loamss runtime. v0.1 prototype.

This is the seamless-configuration UI that sits above the `loamss` CLI. The first-run wizard is the cardinal test — a new user should land on a working runtime in under five minutes without reading docs or opening a terminal.

> **Status: prototype.** First-run wizard is functional; the seven tabs (Dashboard, Sources, Apps, Capsules, Memory, Activity, Settings) live in `console-design.md` at the repo root and ship incrementally.

## Run it

```bash
cd console
bun install
bun dev
# open http://localhost:3000
```

`bun build` produces a static export to `out/` that gets embedded into the Go runtime binary via `embed.FS` for the production deploy.

## What's built

The first-run wizard:

1. **Welcome** — editorial invitation, "begin setup" or "import existing config"
2. **01 Storage** — encrypted local folder (default), cloud (coming soon), or custom path with optional encryption toggle
3. **02 Memory** — SQLite (default), pgvector / Chroma (coming soon)
4. **03 Models** — skip, Anthropic Claude (with API key in OS keychain), or Ollama (auto-detected at `localhost:11434`)
5. **04 Connect** — optional first source. Skip (default) or Gmail with an "App isn't verified" preempt
6. **Done** — config summary + directory of next actions, never a victory lap

What's mocked:

- The "Finish setup" submission runs canned progress over ~1.6s instead of calling the runtime. Real version goes through `@loamss/sdk`.
- The Ollama detection waits 700ms and reports "detected" — flip with `?ollama=missing` to see the alternate "not detected" state.
- The Gmail-connect flow doesn't actually start; the wizard saves the user's intent for the dashboard to pick up.

## What's next

Per the design doc (`/console-design.md` at the repo root):

- The seven tabs the wizard hands off to (Dashboard, Sources, Apps, Capsules, Memory, Activity, Settings)
- Real runtime wiring via `@loamss/sdk` (the wizard's mocked async calls become real bearer-token-authenticated HTTP)
- Dashboard with live source + app cards, recent activity, pending approvals
- The Gmail-connect 4-step wizard (referenced from Sources → Add)

## Tech

- **Framework**: Next.js 15 (App Router) with `output: "export"` for static deploy
- **Language**: TypeScript strict
- **Style**: Tailwind v3 with a custom theme (see `tailwind.config.ts`)
- **State**: Zustand for wizard state (`src/lib/wizard-state.ts`)
- **Fonts**: Fraunces (variable serif, display) + IBM Plex Sans (body) + IBM Plex Mono (IDs/paths)
- **Package manager**: Bun

No third-party UI kit — every primitive is built locally (`src/components/primitives/`). This is on purpose: the visual identity matters more than feature coverage at this stage.

## Project layout

```
src/
├── app/
│   ├── layout.tsx        # font loading + html shell
│   ├── page.tsx          # wizard root
│   └── globals.css       # paper texture, focus rings, scrollbar
├── components/
│   ├── primitives/       # Button, Choice, Field, Note, Wordmark
│   └── wizard/
│       ├── WizardShell.tsx
│       ├── Stepper.tsx
│       └── steps/        # Welcome, Storage, Memory, Models, Connect, Done
└── lib/
    └── wizard-state.ts   # Zustand store + step ordering
```

## Visual identity

Direction: **editorial-technical minimalism** — infrastructure software a thoughtful adult would trust with their data. The aesthetic borrows from technical manuals and serious editorial design rather than SaaS dashboards.

- Paper background `#F6F2EA` with a barely-visible grain
- Deep brown-black ink `#1B1814` (never `#000`)
- Brand: deep forest green `#1F3D2C` — "vault" / "thriving", never startup-tech-green
- Semantic: sage `#3D7B5C` / dusty amber `#A87838` / brick red `#94392C`
- Serif (Fraunces) for display + numerals — italic terminal `s` on the Loamss wordmark
- 1px hairline dividers — whisper, don't shout
- 250ms ease-out transitions — patient, never bouncy

The full visual rationale is in `/console-design.md` at the repo root.

## Development shortcuts

In dev mode only:

- `?step=storage` (or any step id) — jump to a specific wizard step
- `?anthropic=1` — pre-select Anthropic with a demo key
- `?ollama=1` — pre-select Ollama
- `?ollama=missing` — show the "Ollama not detected" warn state on the models step

These are stripped in production builds.

## Related

- `/console-design.md` — the full design exploration (IA, flows, screens, decisions)
- `/sdk/typescript/` — `@loamss/sdk`, the MCP client library the console will use
- `/cli.md` — every console action maps to an existing CLI command
- `/permission-model.md` — the model behind permission slips
