// Settings persistence. Debounces PATCHes to coalesce rapid writes (e.g. theme toggle +
// notification toggle changes into one round-trip), and owns the settings reads. The settings
// state and every rule over it are settings-state.ts's; this module runs it.

import { showSaving, showSaved, showError } from "./save-indicator.js";
import { patchAppSettings, loadSettings as loadSettingsAction } from "./actions/settings.js";
import { registerCleanup } from "./actions/index.js";
import { paintSettingLocks, writeSwitch } from "./governance.js";
import {
  changes,
  initialSettingsState,
  isCurrent,
  ownKeys,
  reduce,
  shownSettings,
  switchShown,
  type Payload,
  type SettingsEvent,
  type SettingsState,
  type Stamp,
} from "./settings-state.js";
import type { EffectiveSettings } from "./wire/types.gen.js";

// The GET /api/settings payload. GENERATED from the Go struct marotte.EffectiveSettings (see
// internal/wirespec), so it is not maintained here and cannot drift from what the server sends.
export type { EffectiveSettings } from "./wire/types.gen.js";

/** A write's answer: the committed settings, null when it failed, or "superseded" when it landed
 *  but the page already holds newer settings, which the caller must not overwrite with its own. */
export type PatchAnswer = EffectiveSettings | null | "superseded";

/** A switch a write moves: checked exactly when key holds on. Each settle repaints it from the
 *  settings state. */
export interface SettingsSwitch {
  readonly el: HTMLInputElement;
  readonly key: string;
  readonly on: unknown;
}

let state: SettingsState = initialSettingsState;
let writeSeq = 0;
let patchTimer: ReturnType<typeof setTimeout> | undefined;
let patchResolvers: ((r: PatchAnswer) => void)[] = [];
/** The switches of the edit, and of each write in flight by id. */
let editSwitches: SettingsSwitch[] = [];
const writeSwitches = new Map<number, readonly SettingsSwitch[]>();
/** What each switch showed before its first unsettled write: a settle puts it back when nothing
 *  was adopted yet to say what the server holds. */
const bases = new Map<HTMLInputElement, boolean>();
/** The settings events this page took. */
let eventsNoted = 0;

function step(event: SettingsEvent): void {
  state = reduce(state, event);
}

function asPayload(s: EffectiveSettings): Payload {
  return s as unknown as Payload;
}

/** The adopted settings with this page's edit and writes in flight laid over them
 *  (settings-state.shownSettings), or null before any. For painting controls only: a consumer
 *  that acts on a setting takes a read's answer, which is what the server holds. */
export function settingsAsShown(): EffectiveSettings | null {
  return shownSettings(state) as unknown as EffectiveSettings | null;
}

function unsettled(el: HTMLInputElement): boolean {
  return (
    editSwitches.some((s) => s.el === el) ||
    [...writeSwitches.values()].some((list) => list.some((s) => s.el === el))
  );
}

function repaint(switches: readonly SettingsSwitch[]): void {
  for (const { el, key, on } of switches) {
    const checked = switchShown(state, key, on) ?? bases.get(el);
    if (checked !== undefined) {
      writeSwitch(el, checked);
    }
  }
  for (const { el } of switches) {
    if (!unsettled(el)) {
      bases.delete(el);
    }
  }
  if (switches.length > 0) {
    paintSettingLocks();
  }
}

/** @internal Reset module state for tests. */
// deadset:ignore DS1004 -- test seam: resets the settings state, the queue, its resolvers and switches, the debounce timer and the event count
export function __testResetTracking(): void {
  state = initialSettingsState;
  writeSeq = 0;
  patchResolvers = [];
  editSwitches = [];
  writeSwitches.clear();
  bases.clear();
  reading = null;
  rereading = null;
  eventsNoted = 0;
  if (patchTimer !== undefined) {
    clearTimeout(patchTimer);
    patchTimer = undefined;
  }
}

/** Shared dispatch body: sends the edit as one PATCH and resolves its callers. Called by the
 *  debounce timer and, for a still-debounced write, by the unload cleanup below. */
function executePatch(): void {
  const id = ++writeSeq;
  const resolvers = patchResolvers;
  patchResolvers = [];
  writeSwitches.set(id, editSwitches);
  editSwitches = [];
  step({ type: "send", id });
  const body = state.pending.find((w) => w.id === id)?.body ?? {};
  let answer: EffectiveSettings | null = null;
  void patchAppSettings.dispatch(
    {
      body: { ...body },
      onStart: () => {
        step({ type: "start", id });
      },
    },
    {
      silent: true,
      onSuccess: (a) => {
        answer = a;
        const mine = ownKeys(state, id);
        if (mine.length > 0) {
          showSaved(mine);
        }
      },
      onError: () => {
        const mine = ownKeys(state, id);
        if (mine.length > 0) {
          showError(mine);
        }
      },
      onSettled: () => {
        const settled: EffectiveSettings | null = answer;
        const current = settled !== null && isCurrent(state, settled);
        step({ type: "settle", id, answer: settled === null ? null : asPayload(settled) });
        const switches = writeSwitches.get(id) ?? [];
        writeSwitches.delete(id);
        repaint(switches);
        const result: PatchAnswer = settled === null ? null : current ? settled : "superseded";
        for (const resolve of resolvers) {
          resolve(result);
        }
      },
    },
  );
}

registerCleanup(() => {
  // An armed timer means a NON-EMPTY edit, so this needs no emptiness test of its own:
  // `patchSettings` fills the edit before it arms the timer, and the only two paths that empty it
  // — `executePatch` itself and `__testResetTracking` — clear the timer with it.
  if (patchTimer !== undefined) {
    clearTimeout(patchTimer);
    patchTimer = undefined;
    executePatch();
  }
});

/** Queue the keys of patch a click changes (settings-state.changes). Each switch is repainted from
 *  the settings state when the write settles, so a failed write leaves it at what the server holds. */
export function patchSettings(
  patch: Partial<EffectiveSettings>,
  ...switches: SettingsSwitch[]
): Promise<PatchAnswer> {
  const changed = changes(state, patch);
  if (Object.keys(changed).length === 0) {
    // No-op: nothing to send. Resolve immediately with null so callers that await the promise don't
    // hang.
    return Promise.resolve(null);
  }
  for (const { el } of switches) {
    if (!unsettled(el)) {
      bases.set(el, !el.checked);
    }
  }
  step({ type: "click", patch: changed });
  editSwitches.push(...switches);
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

/** Take a settings payload from any source. True when it is not older than settings this page
 *  holds, so the caller may apply it; false when it is. A deleted or restored config.json serves a
 *  lower version, refused here until the write after it starts a later epoch. */
export function adoptSettings(s: EffectiveSettings): boolean {
  const current = isCurrent(state, s);
  step({ type: "adopt", payload: asPayload(s) });
  return current;
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

/** Load the settings the server holds, or null when the fetch failed or answered settings older
 *  than ones this page holds. A read in flight is shared; one a
 *  newer settings event made stale is answered by a read sent after that event. */
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

/** The adopted settings when nothing newer was announced since, else refreshSettings. */
export function newestSettings(): Promise<EffectiveSettings | null> {
  const adopted = state.adopted;
  return adopted !== null && isCurrent(state, adopted.at)
    ? Promise.resolve(adopted.value as unknown as EffectiveSettings)
    : refreshSettings();
}

/** Take a settings_updated event's version. An older one changes nothing held, but still counts as
 *  an event, so a read sent before it is followed by one sent after. */
export function noteSettingsEvent(version: Stamp): void {
  eventsNoted++;
  step({ type: "announce", at: version });
}
