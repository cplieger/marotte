// Settings persistence. Debounces PATCHes to coalesce rapid writes (e.g. theme toggle +
// notification toggle changes into one round-trip).

import { showSaving, showSaved, showError } from "./save-indicator.js";
import { patchAppSettings, loadSettings as loadSettingsAction } from "./actions/settings.js";
import { registerCleanup } from "./actions/index.js";
import type { EffectiveSettings } from "./wire/types.gen.js";

// The GET /api/settings payload. GENERATED from the Go struct marotte.EffectiveSettings (see
// internal/wirespec), so it is not maintained here and cannot drift from what the server sends.
export type { EffectiveSettings } from "./wire/types.gen.js";

let patchTimer: ReturnType<typeof setTimeout> | undefined;
let patchQueue: Partial<EffectiveSettings> = {};
let patchSnapshot: Partial<EffectiveSettings> = {};
let patchInputs: HTMLInputElement[] = [];
let patchResolvers: ((r: Record<string, unknown> | null) => void)[] = [];

// The indicator belongs to the NEWEST write of each key.
let writeSeq = 0;
const keyGen = new Map<string, number>();

/** Last-known value per settings key. Seeded by initSettingsTracking() on app boot from
 *  /api/settings; updated by patchSettings() as we send writes. */
let lastSentPatch: Partial<EffectiveSettings> = {};

/** Seed the dedup tracker from the loaded settings. Called once at app boot before any
 *  patchSettings() can fire. Without this, the first patch for any key after page load is
 *  treated as a change even when the value matches the server. */
export function initSettingsTracking(s: EffectiveSettings): void {
  lastSentPatch = { ...s };
}

/** @internal Reset module state for tests. */
export function __testResetTracking(): void {
  lastSentPatch = {};
  patchQueue = {};
  patchSnapshot = {};
  patchInputs = [];
  patchResolvers = [];
  keyGen.clear();
  if (patchTimer !== undefined) {
    clearTimeout(patchTimer);
    patchTimer = undefined;
  }
}

/** Shared dispatch body: drains the queue, dispatches the PATCH, and resolves all pending
 *  promises. Called by the debounce timer and, for a still-debounced write, by the unload
 *  cleanup below. */
function executePatch(): void {
  const body = patchQueue;
  const allInputs = patchInputs;
  const resolvers = patchResolvers;
  const rollback = patchSnapshot;
  patchQueue = {};
  patchSnapshot = {};
  patchInputs = [];
  patchResolvers = [];
  const keys = Object.keys(body);
  const gen = ++writeSeq;
  for (const k of keys) {
    keyGen.set(k, gen);
  }
  /** The keys of this write that no later write has claimed since. */
  const stillOurs = (): string[] => keys.filter((k) => keyGen.get(k) === gen);
  let result: Record<string, unknown> | null = null;
  void patchAppSettings.dispatch(
    {
      body: body,
      ...(allInputs.length > 0 ? { inputs: allInputs } : {}),
    },
    {
      silent: true,
      onSuccess: (r) => {
        result = r as Record<string, unknown>;
        const mine = stillOurs();
        if (mine.length > 0) {
          showSaved(mine);
        }
      },
      onError: () => {
        Object.assign(lastSentPatch, rollback);
        const mine = stillOurs();
        if (mine.length > 0) {
          showError(mine);
        }
      },
      onSettled: () => {
        for (const resolve of resolvers) {
          resolve(result);
        }
      },
    },
  );
}

registerCleanup(() => {
  // An armed timer means a NON-EMPTY queue, so this needs no emptiness test of its own:
  // `patchSettings` fills the queue before it arms the timer, and the only two paths that empty the
  // queue — `executePatch` itself and `__testResetTracking` — clear the timer with it.
  if (patchTimer !== undefined) {
    clearTimeout(patchTimer);
    patchTimer = undefined;
    executePatch();
  }
});

export function patchSettings(
  patch: Partial<EffectiveSettings>,
  ...inputs: HTMLInputElement[]
): Promise<Record<string, unknown> | null> {
  // Filter out keys whose value matches the last-sent value.
  const changed: Partial<EffectiveSettings> = {};
  for (const k of Object.keys(patch) as (keyof EffectiveSettings)[]) {
    if (JSON.stringify(patch[k]) !== JSON.stringify(lastSentPatch[k])) {
      if (!(k in patchSnapshot)) {
        Object.assign(patchSnapshot, { [k]: lastSentPatch[k] });
      }
      Object.assign(changed, { [k]: patch[k] });
      Object.assign(lastSentPatch, { [k]: patch[k] });
    }
  }
  if (Object.keys(changed).length === 0) {
    // No-op: nothing to send. Resolve immediately with null so callers that await the promise don't
    // hang.
    return Promise.resolve(null);
  }
  Object.assign(patchQueue, changed);
  for (const input of inputs) {
    if (!patchInputs.includes(input)) {
      patchInputs.push(input);
    }
  }
  const p = new Promise<Record<string, unknown> | null>((resolve) => {
    patchResolvers.push(resolve);
  });
  // Announced per call rather than per batch: each changed key has its own slot, so a setting
  // flipped while an earlier one is still inside the debounce window has to raise its own spinner.
  showSaving(Object.keys(changed));
  if (patchTimer !== undefined) {
    return p;
  }
  patchTimer = setTimeout(() => {
    patchTimer = undefined;
    executePatch();
  }, 300);
  return p;
}

/** Load the effective settings, or null when the fetch itself failed. That fallback handed every
 *  caller an empty object indistinguishable from a real payload, so a network failure rendered
 *  as a full set of client-invented defaults — and it needed an eslint suppression to write,
 *  because the declared type promised what the wire did not. */
export async function loadSettings(): Promise<EffectiveSettings | null> {
  return await loadSettingsAction.dispatch(undefined);
}
