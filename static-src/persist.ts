// Settings persistence. Debounces PATCHes to coalesce rapid writes (e.g. theme toggle +
// notification toggle changes into one round-trip), and owns the settings reads and the
// revision every payload is adopted against.

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
// deadset:ignore DS1004 -- test seam: resets the patch dedup tracker, queue, resolvers, key generations, debounce timer, settings reads and held revision
export function __testResetTracking(): void {
  lastSentPatch = {};
  patchQueue = {};
  patchSnapshot = {};
  patchInputs = [];
  patchResolvers = [];
  keyGen.clear();
  reading = null;
  rereading = null;
  heldRevision = 0;
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

/** The settings GET in flight, and the one every refresh asked for meanwhile shares, sent once it settles. */
let reading: Promise<EffectiveSettings | null> | null = null;
let rereading: Promise<EffectiveSettings | null> | null = null;

function startRead(): Promise<EffectiveSettings | null> {
  const read = loadSettingsAction.dispatch(undefined);
  reading = read;
  // Registered before any rereading continuation, so that one finds no read in flight and sends its own.
  void read.then(() => {
    if (reading === read) {
      reading = null;
    }
  });
  return read;
}

/** Load the effective settings, or null when the fetch itself failed. A read already in flight is
 *  shared; a caller reacting to a change uses refreshSettings, since that read may predate it. */
export function loadSettings(): Promise<EffectiveSettings | null> {
  return reading ?? startRead();
}

/** Load the effective settings from a GET sent after this call, or null when it failed. Calls made
 *  while a read is in flight share one more, sent once it settles, so answers arrive in send order. */
export function refreshSettings(): Promise<EffectiveSettings | null> {
  if (reading === null) {
    return startRead();
  }
  rereading ??= reading.then(() => {
    rereading = null;
    return startRead();
  });
  return rereading;
}

/** The newest settings revision this page adopted. */
let heldRevision = 0;

/** Reports whether s is at or above the newest settings revision this page adopted, and holds s's
 *  revision if so. Every path adopting a settings payload asks first, so a late answer never paints
 *  over a newer one. */
export function adoptableSettings(s: EffectiveSettings): boolean {
  if (s.settings_revision < heldRevision) {
    return false;
  }
  heldRevision = s.settings_revision;
  return true;
}

/** Take a settings_updated event's revision. Events carry the server's current count, so one below
 *  the held revision means config.json was replaced or reset, and the held revision follows it
 *  down: otherwise no payload would be adopted again until a reload. */
export function noteSettingsEvent(revision: number): void {
  if (revision < heldRevision) {
    heldRevision = revision;
  }
}
