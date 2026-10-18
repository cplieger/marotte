// Settings persistence. Debounces PATCHes to coalesce rapid writes (e.g. theme toggle +
// notification toggle changes into one round-trip), and owns the settings reads and the
// version every payload is adopted against.

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
let patchResolvers: ((r: PatchAnswer) => void)[] = [];

/** A write's answer: the committed settings, null when it failed, or "superseded" when it landed
 *  but the page already holds newer settings, which the caller must not overwrite with its own. */
export type PatchAnswer = Record<string, unknown> | null | "superseded";

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
  held = { settings_epoch: 0, settings_revision: 0 };
  latest = null;
  eventsNoted = 0;
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
  let result: PatchAnswer = null;
  void patchAppSettings.dispatch(
    {
      body: body,
      ...(allInputs.length > 0 ? { inputs: allInputs } : {}),
    },
    {
      silent: true,
      onSuccess: (r) => {
        const version = stampOf(r);
        result =
          version === null || adoptSettings(version)
            ? (r as Record<string, unknown>)
            : "superseded";
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
): Promise<PatchAnswer> {
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
  const p = new Promise<PatchAnswer>((resolve) => {
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

/** A settings payload's version, ordered epoch first: config.json counts its writes within an
 *  epoch, and only a later epoch counts again from 1. */
type Stamp = Pick<EffectiveSettings, "settings_epoch" | "settings_revision">;

/** The newest version this page adopted or a settings event announced. */
let held: Stamp = { settings_epoch: 0, settings_revision: 0 };
/** The newest settings a read answered, for newestSettings. */
let latest: EffectiveSettings | null = null;
/** The settings events this page took. */
let eventsNoted = 0;

function below(s: Stamp, than: Stamp): boolean {
  return (
    s.settings_epoch < than.settings_epoch ||
    (s.settings_epoch === than.settings_epoch && s.settings_revision < than.settings_revision)
  );
}

function stampOf(r: unknown): Stamp | null {
  if (typeof r !== "object" || r === null) {
    return null;
  }
  const { settings_epoch: epoch, settings_revision: revision } = r as Record<string, unknown>;
  return typeof epoch === "number" && typeof revision === "number"
    ? { settings_epoch: epoch, settings_revision: revision }
    : null;
}

/** The one ordering rule for every settings payload and event: true when s is not older than the
 *  version this page holds, which it then holds; false when it is. The held version never goes
 *  back, since the server sends events in version order and a reset starts a later epoch. */
export function adoptSettings(s: Stamp): boolean {
  if (below(s, held)) {
    return false;
  }
  held = { settings_epoch: s.settings_epoch, settings_revision: s.settings_revision };
  return true;
}

/** The settings GET in flight: its raw answer, that answer once checked, and eventsNoted when it was sent. */
let reading: {
  sent: Promise<EffectiveSettings | null>;
  answer: Promise<EffectiveSettings | null>;
  afterEvents: number;
} | null = null;
/** The GET every refresh made while `reading` predates the newest event shares, sent once it settles. */
let rereading: Promise<EffectiveSettings | null> | null = null;

function startRead(): Promise<EffectiveSettings | null> {
  const afterEvents = eventsNoted;
  const sent = loadSettingsAction.dispatch(undefined);
  const answer = sent.then((s) => {
    if (s === null) {
      return null;
    }
    if (adoptSettings(s)) {
      latest = s;
      return s;
    }
    // Older than an event taken since this read was sent: the read sent after that event answers.
    return afterEvents < eventsNoted ? refreshSettings() : null;
  });
  const read = { sent, answer, afterEvents };
  reading = read;
  void sent.then(() => {
    if (reading === read) {
      reading = null;
    }
  });
  return answer;
}

/** Load the effective settings, or null when the fetch failed or answered settings older than ones
 *  this page holds. A read in flight is shared; one a newer settings event made stale is answered
 *  by a read sent after that event. */
export function loadSettings(): Promise<EffectiveSettings | null> {
  return reading?.answer ?? rereading ?? startRead();
}

/** loadSettings from a GET sent after the newest event noteSettingsEvent took. Calls made while an
 *  older read is in flight share one more, sent once it settles, so answers arrive in send order. */
export function refreshSettings(): Promise<EffectiveSettings | null> {
  if (reading === null) {
    return rereading ?? startRead();
  }
  if (reading.afterEvents === eventsNoted) {
    return reading.answer;
  }
  // On the raw answer: the checked one can wait for this very read.
  rereading ??= reading.sent.then(() => {
    rereading = null;
    return startRead();
  });
  return rereading;
}

/** The newest settings a read answered when nothing newer was announced since, else refreshSettings. */
export function newestSettings(): Promise<EffectiveSettings | null> {
  return latest !== null && !below(latest, held) ? Promise.resolve(latest) : refreshSettings();
}

/** Take a settings_updated event's version through adoptSettings. An older one changes nothing
 *  held, but still counts as an event, so a read sent before it is followed by one sent after. */
export function noteSettingsEvent(version: Stamp): void {
  eventsNoted++;
  adoptSettings(version);
}
