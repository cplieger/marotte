// The page's settings as one pure reducer over the server's answers, the writes in flight and the
// edit not yet sent; persist.ts runs it for the patchSettings controls. Invariants:
// 1. Every patchSettings control shows `shownValue`: the edit, else the newest write still waiting
//    to start or started at or above the adopted version, else the adopted settings.
// 2. `held` and `adopted.at` never go back, and only a payload `accepts` passes is adopted.
// 3. A shown value is the server's at `adopted.at` unless it is this page's own edit or write in flight.
// 4. A click is dropped only when it equals the shown value and the newest write in flight of its key.
// 5. A click carries the notification switch the kinds it leaves imply (notifyRule).

import { KEYED_PUSH_KINDS } from "./push-kinds.js";

/** A settings version, ordered epoch first: config.json counts its writes within an epoch, and
 *  only a later epoch counts again from 1. */
export interface Stamp {
  readonly settings_epoch: number;
  readonly settings_revision: number;
}

type Values = Readonly<Record<string, unknown>>;

/** A settings payload: every key the server holds, at its version. */
export type Payload = Stamp & Values;

interface Write {
  readonly id: number;
  readonly body: Values;
  /** The version held when this write started, which it commits above; null while it waits behind
   *  an earlier write, when it commits above every version the page has seen. */
  readonly after: Stamp | null;
}

export interface SettingsState {
  /** The newest version adopted or announced by a settings event. */
  readonly held: Stamp;
  /** The last payload adopted, from a read or a write's answer. */
  readonly adopted: { readonly at: Stamp; readonly value: Values } | null;
  /** Writes sent and not settled, in send order. */
  readonly pending: readonly Write[];
  /** Changes clicked and not sent yet. */
  readonly edit: Values;
}

/** Where a payload comes from: a settings read, or a write's answer. */
export type Source = "read" | "write";

export type SettingsEvent =
  | { readonly type: "adopt"; readonly payload: Payload; readonly from: Source }
  | { readonly type: "announce"; readonly at: Stamp }
  | { readonly type: "click"; readonly patch: Values }
  | { readonly type: "send"; readonly id: number }
  | { readonly type: "start"; readonly id: number }
  | { readonly type: "settle"; readonly id: number; readonly answer: Payload | null };

const ZERO: Stamp = { settings_epoch: 0, settings_revision: 0 };

export const initialSettingsState: SettingsState = {
  held: ZERO,
  adopted: null,
  pending: [],
  edit: {},
};

function below(s: Stamp, than: Stamp): boolean {
  return (
    s.settings_epoch < than.settings_epoch ||
    (s.settings_epoch === than.settings_epoch && s.settings_revision < than.settings_revision)
  );
}

function stampOf(s: Stamp): Stamp {
  return { settings_epoch: s.settings_epoch, settings_revision: s.settings_revision };
}

function later(a: Stamp, b: Stamp): Stamp {
  return below(a, b) ? stampOf(b) : stampOf(a);
}

function same(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

/** Whether a payload at `at` is not older than settings this page holds: a consumer applies only
 *  such a payload. */
export function isCurrent(state: SettingsState, at: Stamp): boolean {
  return !below(at, state.held);
}

/** Whether a payload at `at` from `from` is adopted: one not older than held, except a write's answer
 *  at the version already adopted. One version can answer two payloads, because each also resolves
 *  what config.json does not hold (kiro-cli's defaults, each directory's verdict), and a write's
 *  answer is resolved as its write commits, so a read at that version was in practice resolved later. */
export function accepts(state: SettingsState, at: Stamp, from: Source): boolean {
  if (below(at, state.held)) {
    return false;
  }
  return from === "read" || state.adopted === null || below(state.adopted.at, at);
}

function adopt(state: SettingsState, payload: Payload, from: Source): SettingsState {
  if (!accepts(state, payload, from)) {
    return state;
  }
  const at = stampOf(payload);
  return { ...state, held: at, adopted: { at, value: payload } };
}

/** The value the page shows for key, or undefined when nothing adopted, sent or clicked says. */
export function shownValue(state: SettingsState, key: string): { value: unknown } | undefined {
  if (Object.hasOwn(state.edit, key)) {
    return { value: state.edit[key] };
  }
  const adopted = state.adopted;
  for (let i = state.pending.length - 1; i >= 0; i--) {
    const w = state.pending[i];
    if (
      w !== undefined &&
      Object.hasOwn(w.body, key) &&
      (w.after === null || adopted === null || !below(w.after, adopted.at))
    ) {
      return { value: w.body[key] };
    }
  }
  if (adopted !== null && Object.hasOwn(adopted.value, key)) {
    return { value: adopted.value[key] };
  }
  return undefined;
}

/** The adopted payload with every shown value laid over it, or null before any payload. */
export function shownSettings(state: SettingsState): Payload | null {
  if (state.adopted === null) {
    return null;
  }
  const out: Record<string, unknown> = { ...state.adopted.value };
  for (const key of Object.keys(out)) {
    out[key] = shownValue(state, key)?.value;
  }
  return out as Payload;
}

/** The newest value this page has clicked or sent for key and not seen settle. */
function newestUnsettled(state: SettingsState, key: string): { value: unknown } | undefined {
  if (Object.hasOwn(state.edit, key)) {
    return { value: state.edit[key] };
  }
  const w = state.pending.findLast((p) => Object.hasOwn(p.body, key));
  return w === undefined ? undefined : { value: w.body[key] };
}

const NOTIFY_MASTER = "notifications_enabled";
const NOTIFY_KINDS: readonly string[] = Object.values(KEYED_PUSH_KINDS);

/** What a click sends: the keys of patch it changes, and the notification switch they imply. */
export function changes(state: SettingsState, patch: Values): Values {
  return notifyRule(state, changedKeys(state, patch));
}

/** Invariant 5: a click turning the last notification kind off turns notifications off with it, and
 *  one leaving a kind on turns them back on while they show off only by this page's own click, the
 *  server still holding them on. A click of the switch alone leaves the kinds. */
function notifyRule(state: SettingsState, changed: Values): Values {
  if (!NOTIFY_KINDS.some((k) => Object.hasOwn(changed, k))) {
    return changed;
  }
  const after = (k: string): unknown =>
    Object.hasOwn(changed, k) ? changed[k] : shownValue(state, k)?.value;
  let master: boolean;
  if (NOTIFY_KINDS.every((k) => after(k) === false)) {
    master = false;
  } else if (after(NOTIFY_MASTER) === false && state.adopted?.value[NOTIFY_MASTER] === true) {
    master = true;
  } else {
    return changed;
  }
  return { ...changed, ...changedKeys(state, { [NOTIFY_MASTER]: master }) };
}

/** The keys of patch a click changes (invariant 4). A write in flight may commit before or after
 *  settings read since it was sent, so a click must match both to change nothing. */
function changedKeys(state: SettingsState, patch: Values): Values {
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(patch)) {
    const shown = shownValue(state, key);
    const unsettled = newestUnsettled(state, key);
    const dropped =
      shown !== undefined &&
      same(shown.value, value) &&
      (unsettled === undefined || same(unsettled.value, value));
    if (!dropped) {
      out[key] = value;
    }
  }
  return out;
}

/** The keys of write id that no later write or the edit carries: the ones its outcome reports. */
export function ownKeys(state: SettingsState, id: number): string[] {
  const at = state.pending.findIndex((w) => w.id === id);
  const w = state.pending[at];
  if (w === undefined) {
    return [];
  }
  const laterWrites = state.pending.slice(at + 1);
  return Object.keys(w.body).filter(
    (k) => !Object.hasOwn(state.edit, k) && !laterWrites.some((l) => Object.hasOwn(l.body, k)),
  );
}

/** Whether a switch that is on exactly when key holds on shows checked, or undefined when nothing
 *  says what key holds. */
export function switchShown(state: SettingsState, key: string, on: unknown): boolean | undefined {
  const shown = shownValue(state, key);
  return shown === undefined ? undefined : same(shown.value, on);
}

export function reduce(state: SettingsState, event: SettingsEvent): SettingsState {
  switch (event.type) {
    case "adopt":
      return adopt(state, event.payload, event.from);
    case "announce":
      return { ...state, held: later(state.held, event.at) };
    case "click": {
      const changed = changes(state, event.patch);
      return Object.keys(changed).length === 0
        ? state
        : { ...state, edit: { ...state.edit, ...changed } };
    }
    case "send":
      if (Object.keys(state.edit).length === 0) {
        return state;
      }
      return {
        ...state,
        pending: [...state.pending, { id: event.id, body: state.edit, after: null }],
        edit: {},
      };
    case "start": {
      const held = stampOf(state.held);
      return {
        ...state,
        pending: state.pending.map((w) => (w.id === event.id ? { ...w, after: held } : w)),
      };
    }
    case "settle": {
      if (!state.pending.some((w) => w.id === event.id)) {
        return state;
      }
      const settled = { ...state, pending: state.pending.filter((w) => w.id !== event.id) };
      return event.answer === null ? settled : adopt(settled, event.answer, "write");
    }
  }
}
