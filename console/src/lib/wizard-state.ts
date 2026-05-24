"use client";

import { create } from "zustand";
import {
	applyConsoleInit,
	type ConsoleInitPayload,
	probeRuntime,
	type RuntimeProbe,
} from "./runtime-client";

/*
 * Wizard state.
 *
 * Five visible steps plus the "done" landing state.
 *
 *   welcome  → storage → memory → models → connect → done
 *
 * Each step keeps the user's selections; the final "Finish setup"
 * call posts them all to the runtime in one go (mocked here).
 */
export type WizardStep =
  | "welcome"
  | "storage"
  | "memory"
  | "models"
  | "connect"
  | "done";

// --- Storage step -----------------------------------------------------

export type StorageMode = "default" | "cloud" | "custom";

export interface StorageConfig {
  mode: StorageMode;
  customPath: string;
  encrypt: boolean;
}

// --- Memory step ------------------------------------------------------

export type MemoryMode = "sqlite" | "postgres" | "chroma";

// --- Models step ------------------------------------------------------

export type ModelMode = "skip" | "anthropic" | "ollama";

export interface ModelConfig {
  mode: ModelMode;
  anthropicKey: string;
  ollamaUrl: string;
}

// --- Connect step -----------------------------------------------------

export type ConnectMode = "skip" | "gmail";

// --- Store ------------------------------------------------------------

/**
 * Result of the wizard's submission. Carries the payload the wizard
 * built + whether the runtime accepted it. Surfaced on the Done
 * screen so the user sees what was actually written (or, for the
 * v0.1 stub, what would have been written).
 */
export interface SubmitResult {
	ok: boolean;
	payload: ConsoleInitPayload;
	reason?: string; // non-empty when ok=false; explains why
	note?: string; // optional runtime-side explanation (e.g., "stub")
	at: string; // ISO timestamp
}

interface WizardState {
  // Navigation
  step: WizardStep;
  furthestStep: WizardStep;

  // Step state
  storage: StorageConfig;
  memory: MemoryMode;
  model: ModelConfig;
  connect: ConnectMode;

  // Runtime probe — populated on mount, surfaced in the WizardShell
  // header so the user sees "runtime: connected" or "runtime: not
  // reachable" without having to dig.
  runtimeProbe: RuntimeProbe | null;
  runtimeProbedAt: string | null;

  // Submission state
  submitting: boolean;
  submitProgress: number; // 0..100
  submitResult: SubmitResult | null;

  // Actions
  goTo: (step: WizardStep) => void;
  setStorage: (patch: Partial<StorageConfig>) => void;
  setMemory: (mode: MemoryMode) => void;
  setModel: (patch: Partial<ModelConfig>) => void;
  setConnect: (mode: ConnectMode) => void;
  refreshRuntimeProbe: () => Promise<void>;
  startSubmit: () => Promise<void>;
  reset: () => void;
}

const initialState = {
  step: "welcome" as WizardStep,
  furthestStep: "welcome" as WizardStep,
  storage: {
    mode: "default" as StorageMode,
    customPath: "",
    encrypt: true,
  },
  memory: "sqlite" as MemoryMode,
  model: {
    mode: "skip" as ModelMode,
    anthropicKey: "",
    ollamaUrl: "http://localhost:11434",
  },
  connect: "skip" as ConnectMode,
  runtimeProbe: null,
  runtimeProbedAt: null,
  submitting: false,
  submitProgress: 0,
  submitResult: null,
};

// Order of steps for "furthest reached" tracking.
const stepOrder: WizardStep[] = [
  "welcome",
  "storage",
  "memory",
  "models",
  "connect",
  "done",
];

export const stepIndex = (s: WizardStep): number => stepOrder.indexOf(s);

export const useWizard = create<WizardState>((set, get) => ({
  ...initialState,

  goTo: (step) =>
    set((state) => ({
      step,
      furthestStep:
        stepIndex(step) > stepIndex(state.furthestStep)
          ? step
          : state.furthestStep,
    })),

  setStorage: (patch) =>
    set((state) => ({ storage: { ...state.storage, ...patch } })),

  setMemory: (mode) => set({ memory: mode }),

  setModel: (patch) =>
    set((state) => ({ model: { ...state.model, ...patch } })),

  setConnect: (mode) => set({ connect: mode }),

  /*
   * refreshRuntimeProbe pings GET /healthz on the runtime so the
   * wizard can show "runtime: connected | not reachable" in the
   * header. Called on mount + at major step transitions; cheap
   * enough to repeat.
   */
  refreshRuntimeProbe: async () => {
    const probe = await probeRuntime();
    set({
      runtimeProbe: probe,
      runtimeProbedAt: new Date().toISOString(),
    });
  },

  /*
   * startSubmit POSTs the wizard's collected config to /console/init.
   * The runtime currently runs as a stub that echoes the payload
   * (writes_config_file=false in the capability response) — we treat
   * any 2xx as "received" and surface the runtime's note on the
   * Done screen.
   *
   * The progress bar is driven by the actual stages (build payload
   * → POST → settle), not a canned animation. Stages are short so
   * the user perceives a single submit click rather than five
   * sub-steps.
   */
  startSubmit: async () => {
    set({ submitting: true, submitProgress: 0, submitResult: null });
    const state = get();
    const payload = buildPayload(state);
    set({ submitProgress: 30 });

    const result = await applyConsoleInit(payload);
    set({ submitProgress: 90 });

    // Brief settle so users see the bar fill rather than snap to done.
    await new Promise((r) => setTimeout(r, 220));

    const submitResult: SubmitResult = {
      ok: result.ok,
      payload,
      reason: result.ok ? undefined : result.reason,
      // The runtime's v0.1 response includes a "note" field
      // explaining the stub status; applyConsoleInit doesn't surface
      // it yet but we can fetch on Done.
      at: new Date().toISOString(),
    };

    set({
      submitting: false,
      submitProgress: 100,
      submitResult,
      step: "done",
      furthestStep: "done",
    });
  },

  reset: () => set({ ...initialState }),
}));

/**
 * Build the /console/init payload from the wizard's collected state.
 * Pure function; tested separately.
 */
export function buildPayload(state: {
  storage: StorageConfig;
  memory: MemoryMode;
  model: ModelConfig;
  connect: ConnectMode;
}): ConsoleInitPayload {
  // Storage.
  const storageEntry: ConsoleInitPayload["storage"] =
    state.storage.mode === "custom"
      ? {
          adapter: "storage:fs-encrypted",
          config: {
            root: state.storage.customPath || "~/.loamss/storage",
            encrypt: state.storage.encrypt,
          },
        }
      : {
          adapter: "storage:fs-encrypted",
          config: {
            root: "~/.loamss/storage",
            encrypt: true,
          },
        };

  // Memory.
  const memoryEntry: ConsoleInitPayload["memory"] = {
    adapter: "memory:sqlite",
    config: { path: "~/.loamss/memory.db" },
  };

  // Models — skip mode emits an empty array; the runtime falls back
  // to model:none which surfaces graceful "no model" errors on calls.
  const models: ConsoleInitPayload["models"] = [];
  if (state.model.mode === "anthropic") {
    models.push({
      adapter: "model:anthropic",
      config: {
        // For the v0.1 stub we ship the key inline; production
        // routes it through the OS keychain (the wizard would call
        // a separate "save secret" endpoint and reference it here
        // by handle).
        api_key: state.model.anthropicKey,
      },
    });
  } else if (state.model.mode === "ollama") {
    models.push({
      adapter: "model:ollama",
      config: { url: state.model.ollamaUrl },
    });
  }

  // Source intents — the wizard's "Connect" step might select Gmail
  // but doesn't actually run the OAuth flow inside the wizard. The
  // runtime sees the intent and the dashboard surfaces it as a
  // pending action.
  const source_intents: ConsoleInitPayload["source_intents"] = [];
  if (state.connect === "gmail") {
    source_intents.push({
      adapter: "source:gmail",
      name: "gmail-personal",
    });
  }

  return {
    storage: storageEntry,
    memory: memoryEntry,
    models,
    source_intents,
  };
}
