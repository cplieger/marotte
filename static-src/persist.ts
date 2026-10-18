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
let patchInputs: HTMLInputElement[] = [];
let patchResolvers: ((r: PatchAnswer) => void)[] = [];

/** A write's answer: the committed settings, null when it failed, or "superseded" when it landed
 *  but the page already holds newer settings, which the caller must not overwrite with its own. */
export type PatchAnswer = EffectiveSettings | null | "superseded";

const tracking: {
  /** The newest value the server is known to hold for each key, and the version it was known at. */
  confirmed: Map<string, { value: unknown; at: Stamp }>;
  /** Each key's newest local write not yet settled, and `reads` when it was made. It owns the key's
   *  indicator. */
  claims: Map<string, { claim: number; value: unknown; reads: number }>;
  /** Each checkbox's newest unsettled write: `before` is what a failure puts back, carried over
   *  from an older write still unsettled unless a read was adopted since, and `reads` when it was made. */
  boxes: Map<HTMLInputElement, { claim: number; before: boolean; after: boolean; reads: number }>;
} = { confirmed: new Map(), claims: new Map(), boxes: new Map() };
let claimSeq = 0;
/** The reads this page adopted. */
let reads = 0;

function confirm(key: string, value: unknown, at: Stamp): void {
  const known = tracking.confirmed.get(key);
  if (known === undefined || !below(at, known.at)) {
    tracking.confirmed.set(key, { value, at });
  }
}

/** Whether writing value to key changes nothing the server will hold. A read adopted after the
 *  key's pending write may predate or follow its commit, so value must then match both. */
function unchanged(key: string, value: unknown): boolean {
  const same = (v: unknown): boolean => JSON.stringify(v) === JSON.stringify(value);
  const pending = tracking.claims.get(key);
  const known = tracking.confirmed.get(key)?.value;
  if (pending === undefined) {
    return same(known);
  }
  return same(pending.value) && (pending.reads === reads || same(known));
}

/** Record every key of settings the page adopted as the server's value at their version. */
export function initSettingsTracking(s: EffectiveSettings): void {
  reads++;
  for (const [key, value] of Object.entries(s)) {
    confirm(key, value, s);
  }
}

/** @internal Reset module state for tests. */
// deadset:ignore DS1004 -- test seam: resets the patch dedup tracker, queue, resolvers, claims, debounce timer, settings reads and held revision
export function __testResetTracking(): void {
  patchQueue = {};
  patchInputs = [];
  patchResolvers = [];
  tracking.confirmed.clear();
  tracking.claims.clear();
  tracking.boxes.clear();
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
  const resolvers = patchResolvers;
  const keys = Object.keys(body) as (keyof EffectiveSettings)[];
  const sent = new Map(keys.map((k) => [k, tracking.claims.get(k)?.claim]));
  const boxes = patchInputs.flatMap((el) => {
    const c = tracking.boxes.get(el);
    return c === undefined ? [] : [{ el, claim: c.claim, after: c.after }];
  });
  const sentAt = held;
  patchQueue = {};
  patchInputs = [];
  patchResolvers = [];
  /** The keys of this write that no later write has claimed since. */
  const stillOurs = (): string[] =>
    keys.filter((k) => tracking.claims.get(k)?.claim === sent.get(k));
  let result: PatchAnswer = null;
  void patchAppSettings.dispatch(
    {
      body: body,
      // A read newer than this write was adopted; the settings_updated read of that version
      // repaints the controls.
      restore: () =>
        latest !== null && below(sentAt, latest)
          ? []
          : boxes.flatMap(({ el, claim }) => {
              const c = tracking.boxes.get(el);
              return c?.claim === claim ? [{ el, checked: c.before }] : [];
            }),
    },
    {
      silent: true,
      onSuccess: (answer) => {
        result = adoptSettings(answer) ? answer : "superseded";
        for (const k of keys) {
          confirm(k, answer[k], answer);
        }
        const mine = stillOurs();
        if (mine.length > 0) {
          showSaved(mine);
        }
      },
      onError: () => {
        const mine = stillOurs();
        if (mine.length > 0) {
          showError(mine);
        }
      },
      onSettled: () => {
        for (const k of stillOurs()) {
          tracking.claims.delete(k);
        }
        for (const { el, claim, after } of boxes) {
          const c = tracking.boxes.get(el);
          if (c?.claim === claim) {
            tracking.boxes.delete(el);
          } else if (result !== null && result !== "superseded" && c !== undefined) {
            // The server now holds this write's value, which a later write's failure puts back.
            c.before = after;
          }
        }
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

/** Queue the keys of patch that would change what the server holds. A checkbox among inputs flips
 *  back if the write fails, unless a later write owns it or newer settings were read since it was
 *  sent. */
export function patchSettings(
  patch: Partial<EffectiveSettings>,
  ...inputs: HTMLInputElement[]
): Promise<PatchAnswer> {
  const changed: Partial<EffectiveSettings> = {};
  for (const k of Object.keys(patch) as (keyof EffectiveSettings)[]) {
    if (!unchanged(k, patch[k])) {
      Object.assign(changed, { [k]: patch[k] });
    }
  }
  if (Object.keys(changed).length === 0) {
    // No-op: nothing to send. Resolve immediately with null so callers that await the promise don't
    // hang.
    return Promise.resolve(null);
  }
  const claim = ++claimSeq;
  for (const [k, value] of Object.entries(changed)) {
    tracking.claims.set(k, { claim, value, reads });
  }
  for (const input of inputs) {
    if (input.type === "checkbox") {
      const older = tracking.boxes.get(input);
      const before = older?.reads === reads ? older.before : !input.checked;
      tracking.boxes.set(input, { claim, before, after: input.checked, reads });
    }
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

/** The one ordering rule for every settings payload and event: true when s is not older than the
 *  version this page holds, which it then holds; false when it is. The held version never goes
 *  back: a deleted or restored config.json serves a lower version, refused here until the write
 *  after it starts a later epoch. */
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
      initSettingsTracking(s);
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
