"use client";

import { create } from "zustand";

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

interface WizardState {
  // Navigation
  step: WizardStep;
  furthestStep: WizardStep;

  // Step state
  storage: StorageConfig;
  memory: MemoryMode;
  model: ModelConfig;
  connect: ConnectMode;

  // Submission state
  submitting: boolean;
  submitProgress: number; // 0..100

  // Actions
  goTo: (step: WizardStep) => void;
  setStorage: (patch: Partial<StorageConfig>) => void;
  setMemory: (mode: MemoryMode) => void;
  setModel: (patch: Partial<ModelConfig>) => void;
  setConnect: (mode: ConnectMode) => void;
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
  submitting: false,
  submitProgress: 0,
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
   * startSubmit simulates the network round-trip that posts the
   * wizard's collected config to the runtime. Real version will go
   * through @loamss/sdk; this mock returns canned progress over ~1.6s
   * so the user can see the loading state.
   */
  startSubmit: async () => {
    set({ submitting: true, submitProgress: 0 });
    // Walk progress in three steps to feel like real work.
    const steps = [
      { progress: 25, delay: 320 }, // "writing storage config"
      { progress: 60, delay: 440 }, // "writing memory config"
      { progress: 100, delay: 380 }, // "registering models"
    ];
    for (const { progress, delay } of steps) {
      await new Promise((r) => setTimeout(r, delay));
      set({ submitProgress: progress });
    }
    // Small settle before flipping to done.
    await new Promise((r) => setTimeout(r, 280));
    set({ submitting: false, step: "done", furthestStep: "done" });
  },

  reset: () => set({ ...initialState }),
}));
