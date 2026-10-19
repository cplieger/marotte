// The settings reducer against a model server: other devices' writes, this page's writes taken
// one at a time in send order, refusals, answers lost after a commit, events (late duplicates too),
// reads with and without the repaint a settings_updated read does, and a replaced config.json.
// Each switch is on exactly when its key is true; as in persist.ts, the page repaints a write's
// switches when it settles and every switch on an event's current read, from shownValue, and the
// state a consumer acts on takes that read's own payload.

import { describe, it, expect } from "vitest";
import fc from "fast-check";
import {
  changes,
  initialSettingsState,
  isCurrent,
  reduce,
  shownValue,
  type Payload,
  type SettingsState,
  type Stamp,
} from "./settings-state.js";

const KEYS = ["a", "b"] as const;
type Key = (typeof KEYS)[number];
type Values = Record<Key, boolean>;

type Action =
  | { t: "click"; keys: readonly Key[] }
  | { t: "timer" }
  | { t: "serve"; outcome: "commit" | "refuse" | "lost" }
  | { t: "answer" }
  | { t: "other"; key: Key; value: boolean }
  | { t: "reset" }
  | { t: "event" }
  | { t: "staleEvent" }
  | { t: "plainRead" }
  | { t: "serveRead" }
  | { t: "readAnswer" };

interface World {
  server: { at: Stamp; values: Values };
  /** What the server held at each version it served. */
  history: Map<string, Values>;
  client: SettingsState;
  /** What each switch shows. */
  display: Values;
  /** What a consumer acting on the settings holds: the payload of each event's current read. */
  behaviour: { at: Stamp; values: Values };
  /** The newest version the page received, by event or payload. */
  seen: Stamp;
  nextId: number;
  /** This page's writes the server has not answered to the page, oldest first: the head is on the wire. */
  sent: { id: number; keys: readonly Key[] }[];
  /** The head's outcome, once the server took it. */
  served: { answer: Payload | null } | null;
  events: Stamp[];
  reads: boolean[];
  answeredReads: { repaint: boolean; payload: Payload }[];
}

const tag = (s: Stamp): string => `${String(s.settings_epoch)}/${String(s.settings_revision)}`;
// The model's own order, apart from the reducer's: epoch first, then revision, each field up to
// Number.MAX_SAFE_INTEGER (53 bits).
const order = (s: Stamp): bigint => (BigInt(s.settings_epoch) << 53n) + BigInt(s.settings_revision);
const older = (a: Stamp, b: Stamp): boolean => order(a) < order(b);
const max = (a: Stamp, b: Stamp): Stamp => (older(a, b) ? b : a);

function snapshot(w: World): Payload {
  return { ...w.server.at, ...w.server.values };
}

function commit(w: World, values: Partial<Values>, at?: Stamp): void {
  w.server.at = at ?? {
    settings_epoch: w.server.at.settings_epoch,
    settings_revision: w.server.at.settings_revision + 1,
  };
  w.server.values = { ...w.server.values, ...values };
  w.history.set(tag(w.server.at), { ...w.server.values });
  w.events.push(w.server.at);
}

function shown(w: World, key: Key): unknown {
  return shownValue(w.client, key)?.value;
}

function repaint(w: World, keys: readonly Key[]): void {
  for (const k of keys) {
    w.display[k] = shown(w, k) === true;
  }
}

function newWorld(): World {
  const at = { settings_epoch: 1, settings_revision: 1 };
  const values = { a: false, b: false };
  const w: World = {
    server: { at, values },
    history: new Map([[tag(at), { ...values }]]),
    client: initialSettingsState,
    display: { ...values },
    behaviour: { at, values: { ...values } },
    seen: at,
    nextId: 1,
    sent: [],
    served: null,
    events: [],
    reads: [],
    answeredReads: [],
  };
  w.client = reduce(w.client, { type: "adopt", payload: snapshot(w) });
  return w;
}

/** Hand the reducer a payload, checking its verdict against the newest version the page received
 *  and that a payload it refuses changes nothing adopted. */
function take(
  w: World,
  event:
    | { type: "adopt"; payload: Payload }
    | {
        type: "settle";
        id: number;
        answer: Payload;
      },
): boolean {
  const payload = event.type === "adopt" ? event.payload : event.answer;
  const current = isCurrent(w.client, payload);
  expect(current, `verdict for ${tag(payload)} after ${tag(w.seen)}`).toBe(!older(payload, w.seen));
  w.seen = max(w.seen, payload);
  const before = w.client.adopted;
  w.client = reduce(w.client, event);
  if (!current) {
    expect(w.client.adopted, `the refused ${tag(payload)} changed what is adopted`).toBe(before);
  }
  return current;
}

/** The server's scope takes this page's writes one at a time: the head starts as the one before it
 *  settles. */
function startHead(w: World): void {
  const head = w.sent[0];
  if (head !== undefined) {
    w.client = reduce(w.client, { type: "start", id: head.id });
  }
}

function apply(w: World, a: Action): void {
  switch (a.t) {
    case "click": {
      const patch: Record<string, boolean> = {};
      for (const k of a.keys) {
        patch[k] = !w.display[k];
        w.display[k] = !w.display[k];
      }
      const kept = changes(w.client, patch);
      for (const k of a.keys) {
        if (!Object.hasOwn(kept, k)) {
          expect(shown(w, k), `a click of ${k} dropped though it changes what is shown`).toBe(
            patch[k],
          );
        }
      }
      w.client = reduce(w.client, { type: "click", patch });
      return;
    }
    case "timer": {
      const keys = Object.keys(w.client.edit) as Key[];
      if (keys.length > 0) {
        const id = w.nextId++;
        w.client = reduce(w.client, { type: "send", id });
        w.sent.push({ id, keys });
        if (w.sent.length === 1) {
          startHead(w);
        }
      }
      return;
    }
    case "serve": {
      const head = w.sent[0];
      if (head === undefined || w.served !== null) {
        return;
      }
      if (a.outcome === "refuse") {
        w.served = { answer: null };
        return;
      }
      const body = w.client.pending.find((p) => p.id === head.id)?.body ?? {};
      commit(w, body as Partial<Values>);
      w.served = { answer: a.outcome === "commit" ? snapshot(w) : null };
      return;
    }
    case "answer": {
      const head = w.sent[0];
      if (head === undefined || w.served === null) {
        return;
      }
      const answer = w.served.answer;
      const behaviour = w.behaviour;
      if (answer === null) {
        w.client = reduce(w.client, { type: "settle", id: head.id, answer });
      } else {
        take(w, { type: "settle", id: head.id, answer });
      }
      repaint(w, head.keys);
      if (answer === null) {
        expect(w.behaviour, "a failed write moved what a consumer acts on").toBe(behaviour);
      }
      w.sent.shift();
      w.served = null;
      startHead(w);
      return;
    }
    case "other":
      commit(w, { [a.key]: a.value });
      return;
    case "reset": {
      // config.json is put back to the first version served; the write after it starts an epoch.
      const first = w.history.values().next().value ?? w.server.values;
      w.server.values = { ...first };
      commit(w, {}, { settings_epoch: w.server.at.settings_epoch + 1, settings_revision: 1 });
      return;
    }
    case "event": {
      const e = w.events.shift();
      if (e !== undefined) {
        w.client = reduce(w.client, { type: "announce", at: e });
        w.seen = max(w.seen, e);
        w.reads.push(true);
      }
      return;
    }
    case "staleEvent":
      w.client = reduce(w.client, {
        type: "announce",
        at: { settings_epoch: 1, settings_revision: 1 },
      });
      w.reads.push(true);
      return;
    case "plainRead":
      w.reads.push(false);
      return;
    case "serveRead": {
      const repaints = w.reads.shift();
      if (repaints !== undefined) {
        w.answeredReads.push({ repaint: repaints, payload: snapshot(w) });
      }
      return;
    }
    case "readAnswer": {
      const r = w.answeredReads.shift();
      if (r === undefined) {
        return;
      }
      const current = take(w, { type: "adopt", payload: r.payload });
      if (r.repaint && current) {
        repaint(w, KEYS);
        // persist answers such a read with the payload it adopted.
        const adopted = w.client.adopted;
        if (adopted !== null) {
          w.behaviour = {
            at: adopted.at,
            values: { a: adopted.value["a"], b: adopted.value["b"] } as Values,
          };
        }
      }
      return;
    }
  }
}

/** Invariants 2 and 3 of settings-state.ts, and that a consumer acts only on what the server held,
 *  after every step. */
function check(w: World, before: SettingsState): void {
  expect(older(w.client.held, before.held), "the held version went back").toBe(false);
  const was = before.adopted?.at;
  const now = w.client.adopted?.at;
  expect(was !== undefined && (now === undefined || older(now, was)), "adopted went back").toBe(
    false,
  );
  expect(w.behaviour.values, `behaviour is not the server's at ${tag(w.behaviour.at)}`).toEqual(
    w.history.get(tag(w.behaviour.at)),
  );
  if (now === undefined) {
    return;
  }
  const held = w.history.get(tag(now));
  for (const k of KEYS) {
    const s = shown(w, k);
    const own =
      (Object.hasOwn(w.client.edit, k) && w.client.edit[k] === s) ||
      w.client.pending.some((p) => Object.hasOwn(p.body, k) && p.body[k] === s);
    expect(own || held?.[k] === s, `${k} shows ${String(s)}, not held at ${tag(now)}`).toBe(true);
  }
}

function step(w: World, a: Action): void {
  const before = w.client;
  apply(w, a);
  check(w, before);
}

/** Let every write, event and read play out in order, then require that the page shows what the
 *  server holds. */
function quiesce(w: World): void {
  for (let guard = 0; guard < 100; guard++) {
    step(w, { t: "timer" });
    if (w.sent.length > 0) {
      step(w, { t: "serve", outcome: "commit" });
      step(w, { t: "answer" });
    } else if (w.events.length > 0) {
      step(w, { t: "event" });
    } else if (w.reads.length > 0) {
      step(w, { t: "serveRead" });
    } else if (w.answeredReads.length > 0) {
      step(w, { t: "readAnswer" });
    } else {
      break;
    }
  }
  expect(w.client.pending).toEqual([]);
  expect(w.client.edit).toEqual({});
  expect(w.client.adopted?.at).toEqual(w.server.at);
  for (const k of KEYS) {
    expect(w.display[k], `the ${k} switch at rest`).toBe(w.server.values[k]);
    expect(shown(w, k), `${k} shown at rest`).toBe(w.server.values[k]);
    expect(w.behaviour.values[k], `${k} acted on at rest`).toBe(w.server.values[k]);
  }
}

function run(actions: readonly Action[]): void {
  const w = newWorld();
  for (const a of actions) {
    step(w, a);
  }
  quiesce(w);
}

const key = fc.constantFrom<Key>(...KEYS);
const action: fc.Arbitrary<Action> = fc.oneof(
  {
    weight: 4,
    arbitrary: fc
      .uniqueArray(key, { minLength: 1, maxLength: 2 })
      .map((keys) => ({ t: "click" as const, keys })),
  },
  { weight: 3, arbitrary: fc.constant({ t: "timer" as const }) },
  {
    weight: 3,
    arbitrary: fc
      .constantFrom("commit", "refuse", "lost")
      .map((outcome) => ({ t: "serve" as const, outcome })),
  },
  { weight: 3, arbitrary: fc.constant({ t: "answer" as const }) },
  {
    weight: 2,
    arbitrary: fc.record({ key, value: fc.boolean() }).map((o) => ({ t: "other" as const, ...o })),
  },
  { weight: 1, arbitrary: fc.constant({ t: "reset" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "event" as const }) },
  { weight: 1, arbitrary: fc.constant({ t: "staleEvent" as const }) },
  { weight: 2, arbitrary: fc.constant({ t: "plainRead" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "serveRead" as const }) },
  { weight: 3, arbitrary: fc.constant({ t: "readAnswer" as const }) },
);

const clickA: Action = { t: "click", keys: ["a"] };
const timer: Action = { t: "timer" };
const commitHead: Action = { t: "serve", outcome: "commit" };
const refuseHead: Action = { t: "serve", outcome: "refuse" };
const answer: Action = { t: "answer" };
const event: Action = { t: "event" };
const serveRead: Action = { t: "serveRead" };
const readAnswer: Action = { t: "readAnswer" };

function at(revision: number, values: Record<string, boolean>): Payload {
  return { settings_epoch: 1, settings_revision: revision, ...values };
}

describe("settings state", () => {
  it("holds its invariants and comes to rest at the server's settings, for any interleaving", () => {
    fc.assert(
      fc.property(fc.array(action, { maxLength: 12 }), (actions) => {
        run(actions);
      }),
      { numRuns: 2000, seed: 20261018 },
    );
  });

  it.each<[string, Action[]]>([
    [
      "adopts this page's answer that commits above another device's event",
      [clickA, timer, { t: "other", key: "b", value: true }, event, commitHead, answer],
    ],
    [
      "refuses an older answer after a late event older still",
      [
        clickA,
        timer,
        commitHead,
        { t: "other", key: "b", value: true },
        event,
        event,
        serveRead,
        serveRead,
        readAnswer,
        readAnswer,
        { t: "staleEvent" },
        answer,
      ],
    ],
    [
      "repaints a failed write's switch to settings read after it was sent",
      [
        clickA,
        timer,
        { t: "other", key: "a", value: true },
        event,
        serveRead,
        readAnswer,
        refuseHead,
        answer,
        clickA,
      ],
    ],
    [
      "sends a click that puts back what newer settings changed while a write was out",
      [
        clickA,
        timer,
        commitHead,
        { t: "other", key: "a", value: false },
        event,
        event,
        serveRead,
        serveRead,
        readAnswer,
        readAnswer,
        clickA,
        timer,
        answer,
        refuseHead,
        answer,
      ],
    ],
    [
      "repaints two failed clicks with a read between them to the server's value",
      [
        clickA,
        timer,
        { t: "plainRead" },
        serveRead,
        readAnswer,
        clickA,
        timer,
        refuseHead,
        answer,
        refuseHead,
        answer,
      ],
    ],
    [
      "keeps a write waiting behind another over settings read meanwhile",
      [
        clickA,
        timer,
        clickA,
        timer,
        { t: "other", key: "a", value: true },
        event,
        serveRead,
        readAnswer,
        commitHead,
        answer,
      ],
    ],
  ])("%s", (_name, actions) => {
    run(actions);
  });

  it("shows a write waiting behind another over settings read meanwhile, until it starts", () => {
    let s = reduce(initialSettingsState, { type: "adopt", payload: at(1, { a: false }) });
    s = reduce(s, { type: "click", patch: { a: true } });
    s = reduce(s, { type: "send", id: 1 });
    s = reduce(s, { type: "start", id: 1 });
    s = reduce(s, { type: "click", patch: { a: false } });
    s = reduce(s, { type: "send", id: 2 });
    s = reduce(s, { type: "announce", at: at(3, {}) });
    s = reduce(s, { type: "adopt", payload: at(3, { a: true }) });
    expect(shownValue(s, "a"), "the waiting write commits after revision 3").toEqual({
      value: false,
    });

    s = reduce(s, { type: "settle", id: 1, answer: at(2, { a: true }) });
    s = reduce(s, { type: "start", id: 2 });
    expect(shownValue(s, "a"), "started above revision 3").toEqual({ value: false });
    s = reduce(s, { type: "adopt", payload: at(4, { a: true }) });
    expect(shownValue(s, "a"), "revision 4 may follow it").toEqual({ value: true });
  });

  it("orders a later epoch above any revision of an earlier one", () => {
    const high = { settings_epoch: 1, settings_revision: 2 ** 32 + 1 };
    const next = { settings_epoch: 2, settings_revision: 1 };
    expect(older(high, next), "the model's order").toBe(true);
    const s = reduce(initialSettingsState, { type: "announce", at: next });
    expect(isCurrent(s, high), "the reducer's order").toBe(false);
  });

  it("adopts no answer below a version an event announced", () => {
    let s = reduce(initialSettingsState, { type: "adopt", payload: at(1, { a: false }) });
    s = reduce(s, { type: "click", patch: { a: true } });
    s = reduce(s, { type: "send", id: 1 });
    s = reduce(s, { type: "start", id: 1 });
    s = reduce(s, { type: "announce", at: at(3, {}) });
    s = reduce(s, { type: "settle", id: 1, answer: at(2, { a: true }) });
    expect(s.adopted?.at).toEqual(at(1, {}));
    expect(shownValue(s, "a")).toEqual({ value: false });
  });
});
