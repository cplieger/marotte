// The page's settings as one pure reducer over what the server answered, the writes in flight and
// the edit not yet sent. persist.ts runs it against the network and the controls.
//
// Invariants, held by `reduce` and the helpers below:
// 1. Every control shows `shownValue`: the edit, else the newest write in flight known to commit
//    after the adopted settings, else the adopted settings.
// 2. `held` and `adopted.at` never go back: a payload below either is not newer than one taken.
// 3. A shown value is the server's at `adopted.at` unless it is this page's own edit or write in flight.
// 4. A click is dropped only when it equals the shown value and the newest write in flight of its key.

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
  /** A version this write commits above: the one held when it was sent, raised by the answer of
   *  each earlier write, since the server takes this page's writes in send order. */
  readonly after: Stamp;
}

export interface SettingsState {
  /** The newest version adopted or announced by a settings event. */
  readonly held: Stamp;
  /** The newest payload received, from a read or a write's answer. */
  readonly adopted: { readonly at: Stamp; readonly value: Values } | null;
  /** Writes sent and not settled, in send order. */
  readonly pending: readonly Write[];
  /** Changes clicked and not sent yet. */
  readonly edit: Values;
}

export type SettingsEvent =
  | { readonly type: "adopt"; readonly payload: Payload }
  | { readonly type: "announce"; readonly at: Stamp }
  | { readonly type: "click"; readonly patch: Values }
  | { readonly type: "send"; readonly id: number }
  | { readonly type: "settle"; readonly id: number; readonly answer: Payload | null };

const ZERO: Stamp = { settings_epoch: 0, settings_revision: 0 };

export const initialSettingsState: SettingsState = {
  held: ZERO,
  adopted: null,
  pending: [],
  edit: {},
};

export function below(s: Stamp, than: Stamp): boolean {
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

function adopt(state: SettingsState, payload: Payload): SettingsState {
  const newer = state.adopted === null || !below(payload, state.adopted.at);
  return {
    ...state,
    held: later(state.held, payload),
    adopted: newer ? { at: stampOf(payload), value: payload } : state.adopted,
  };
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
      (adopted === null || !below(w.after, adopted.at))
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

/** The keys of patch a click changes (invariant 4). A write in flight may commit before or after
 *  settings read since it was sent, so a click must match both to change nothing. */
export function changes(state: SettingsState, patch: Values): Values {
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
      return adopt(state, event.payload);
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
        pending: [...state.pending, { id: event.id, body: state.edit, after: stampOf(state.held) }],
        edit: {},
      };
    case "settle": {
      const at = state.pending.findIndex((w) => w.id === event.id);
      if (at < 0) {
        return state;
      }
      const answer = event.answer;
      const pending = state.pending.flatMap((w, i) => {
        if (i === at) {
          return [];
        }
        return i > at && answer !== null ? [{ ...w, after: later(w.after, answer) }] : [w];
      });
      const settled = { ...state, pending };
      return answer === null ? settled : adopt(settled, answer);
    }
  }
}
