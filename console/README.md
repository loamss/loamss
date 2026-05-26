# @loamss/console

The user-facing console for the Loamss runtime. Embedded into the `loamss` binary via Go `embed.FS` and served at the runtime's listen address — there's no separate server in production.

## Status

**Phase 1+, interactive.** First-run wizard and all five dashboard panes (Sources, Capsules, Apps, Approvals, Activity) talk to the live runtime over HTTP. The Settings tab is the last cluster waiting on UI work; the OAuth surface (Phase 1 of the OAuth UI build plan) is being added next.

## Run it

```bash
cd console
bun install
bun dev
# open http://localhost:3000
```

In dev mode the console runs against a separate origin from the runtime. Set `NEXT_PUBLIC_LOAMSS_BASE=http://127.0.0.1:7777` if your runtime isn't on the default port.

For the production embed:

```bash
bun build         # produces a static export to out/
# The runtime's Makefile (../runtime/Makefile) copies out/ into
# internal/console/dist/ and serves it from the Go binary via
# embed.FS. The runtime build path is the one that ships.
```

## What's built

### First-run wizard (`/`)

1. **Welcome** — editorial invitation
2. **01 Storage** — encrypted local folder (default), S3 / GCS, or custom path
3. **02 Memory** — `sqlite-vec` (default), `pgvector`, `chroma`, `qdrant`
4. **03 Models** — skip, Anthropic Claude, OpenAI, or Ollama (auto-detected at `localhost:11434`)
5. **04 Connect** — optional first source. Skip or files/Gmail
6. **Done** — config summary, directory of next actions

Submission writes a real config file via `POST /console/init`; the daemon re-reads it without a restart (hot-reload covers everything except data-dir / listen-address / primary-adapter changes).

### Dashboard (`/`, post-wizard)

| Pane | What it does |
|---|---|
| **Sources** | List + add + sync + remove. Capsule ingestors and in-tree sources appear together |
| **Capsules** | Install (from local path or registry — registry MVP in progress), start / stop, view permission slip |
| **Apps** | Pair new MCP client via one-time code, revoke paired clients |
| **Approvals** | Pending capability checks the user has to decide on. The most important surface — one-click approve/deny |
| **Activity** | Audit log made human; filter by actor, type, outcome |

### What's still mocked

- **Memory tab** — entities + threads detail views. The SDK side is real (`runtime.tools.call("entities.list" / "threads.list" / ...)`) but no console screen exists yet.
- **Settings tab** — config edits go through `POST /console/init` today, but there's no in-console editor for them yet.
- **OAuth surface** — the runtime's `/console/oauth/*` endpoints exist; the console UI on top of them is the active build (see the OAuth-UI plan in the repo-root chat).

## Tech

- **Framework**: Next.js 15 (App Router) with `output: "export"` for static deploy
- **Language**: TypeScript strict
- **Style**: Tailwind v3 with a custom theme (`tailwind.config.ts`)
- **State**: Zustand for cross-component stores (`src/lib/wizard-state.ts`, `src/lib/dashboard-state.ts`)
- **Fonts**: Fraunces (variable serif, display) + IBM Plex Sans (body) + IBM Plex Mono (IDs/paths)
- **Package manager**: Bun
- **No third-party UI kit** — every primitive lives in `src/components/primitives/`. This is on purpose.

## Project layout

```
src/
├── app/
│   ├── layout.tsx              font loading + html shell
│   ├── page.tsx                wizard root (pre-config) / dashboard root (post-config)
│   ├── globals.css             paper texture, focus rings, scrollbar
│   └── (dashboard)/            post-wizard panes
│       ├── sources/
│       ├── capsules/
│       ├── apps/
│       ├── approvals/
│       └── activity/
├── components/
│   ├── primitives/             Button, Choice, Field, Note, Wordmark
│   ├── wizard/                 WizardShell, Stepper, six step components
│   └── dashboard/              SourceRow, CapsuleCard, ApprovalChip, ActivityEntry, …
└── lib/
    ├── wizard-state.ts         Zustand wizard store
    ├── dashboard-state.ts      Zustand dashboard store
    └── runtime-client.ts       Thin wrapper over fetch + /console/* + the MCP client
```

## Visual identity

Direction: **editorial-technical minimalism** — infrastructure software a thoughtful adult would trust with their data. Not a SaaS dashboard.

- Paper background `#F6F2EA` with barely-visible grain
- Deep brown-black ink `#1B1814` (never `#000`)
- Brand: deep forest green `#1F3D2C` — "vault" / "thriving", never startup-tech-green
- Semantic: sage `#3D7B5C` / dusty amber `#A87838` / brick red `#94392C`
- Serif (Fraunces) for display + numerals — italic terminal `s` on the Loamss wordmark
- 1px hairline dividers — whisper, don't shout
- 250ms ease-out transitions — patient, never bouncy

If a new component feels visually loud, it's probably wrong. Bias toward restraint. The full design rationale lives in [`../console-design.md`](../console-design.md).

## Dev shortcuts

In `process.env.NODE_ENV !== "production"`:

- `?step=<id>` — jump to any wizard step (welcome / storage / memory / models / connect / done)
- `?anthropic=1` — pre-select Anthropic with a demo key
- `?ollama=1` — pre-select Ollama
- `?ollama=missing` — flip Ollama detection to "not found" to inspect the warn state
- `?dashboard=1` — bypass wizard, jump straight to dashboard (handy when iterating on a single pane)

Inert in production builds.

## Embed pipeline

The runtime's `Makefile` runs `bun install --frozen-lockfile && bun run build` against this directory and copies the resulting `out/` into `runtime/internal/console/dist/`, which is then included via Go `embed.FS`. The single binary `loamss` ships with the console baked in — there's no separate console server in production. CI builds both sides on every push.

## Related

- [`../console-design.md`](../console-design.md) — design exploration (IA, flows, screens, decisions)
- [`../sdk/typescript/`](../sdk/typescript/) — `@loamss/sdk`, the MCP client library the dashboard uses
- [`../cli.md`](../cli.md) — every console action maps to a CLI command
- [`../permission-model.md`](../permission-model.md) — the model behind permission slips
- [`CLAUDE.md`](CLAUDE.md) — coding conventions specific to this subtree
