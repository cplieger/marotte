// The steer stack's fold over every stack shape the dock draws (steers, queued rows, the running compaction, a
// stopped queue's paused head at any depth), folded and open, inside the holding area's
// cap: past three messages the reader sees the first two, a fold row counting the rest, and the paused
// queue's head with its Resume; open, every row in send order; the composer on screen either way.

import { describe, it, expect, afterAll, beforeAll, afterEach } from "vitest";
import fc from "fast-check";
import { page as browser } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  CHROME_PX,
  holds,
  mountPane,
  openStack,
  unmountView,
} from "./__test-helpers__/holding-area.js";
import { rowEntry, runAt, turnEntry } from "./__test-helpers__/run-order.js";
import type { SteerStackSource } from "./pending-steers.js";
import type { PendingSteer, RunEntryView } from "./types.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

interface Shape {
  steers: number;
  /** The queued rows in run order. */
  queued: number;
  /** The first queued row the reader's stop paused: rows from it on wait for Resume. */
  pausedFrom: number | null;
  /** A compaction runs in the chat (`ChatHeader.compacting`). */
  compacting: boolean;
  runs: number;
  runsOpen: boolean;
  open: boolean;
}

const MAX_ROWS = 20;

const shapeArb: fc.Arbitrary<Shape> = fc.integer({ min: 0, max: MAX_ROWS }).chain((queued) =>
  fc.record({
    steers: fc.integer({ min: 0, max: MAX_ROWS - queued }),
    queued: fc.constant(queued),
    pausedFrom: fc.option(fc.integer({ min: 0, max: Math.max(0, queued - 1) }), {
      nil: null,
    }),
    compacting: fc.boolean(),
    runs: fc.integer({ min: 0, max: 5 }),
    runsOpen: fc.boolean(),
    open: fc.boolean(),
  }),
);

/** Five queued rows ahead of a stopped queue's head over two, and fifteen steers ahead of one over three. */
const SEEDS: Shape[] = [
  {
    steers: 0,
    queued: 7,
    pausedFrom: 5,
    compacting: false,
    runs: 5,
    runsOpen: false,
    open: false,
  },
  {
    steers: 15,
    queued: 3,
    pausedFrom: 0,
    compacting: true,
    runs: 0,
    runsOpen: false,
    open: false,
  },
];

function isPaused(s: Shape, i: number): boolean {
  return s.pausedFrom !== null && i >= s.pausedFrom && i < s.queued;
}

/** The first paused row's index, or -1 for none. */
function stoppedAt(s: Shape): number {
  return s.pausedFrom !== null && s.pausedFrom < s.queued ? s.pausedFrom : -1;
}

/** The waiting messages' words in send order: the steers, then the queued rows in run order. */
function messagesOf(s: Shape): string[] {
  return [
    ...Array.from({ length: s.steers }, (_, i) => `s${String(i + 1)}`),
    ...Array.from({ length: s.queued }, (_, i) => `q${String(i + 1)}`),
  ];
}

function sourceOf(s: Shape): SteerStackSource {
  const stopped = stoppedAt(s) !== -1;
  const entries = Array.from({ length: s.queued }, (_, i): RunEntryView => {
    const id = `q${String(i + 1)}`;
    const wait = { after: "turns", turns: i + 1 } as const;
    return isPaused(s, i)
      ? rowEntry(id, { after: "paused" }, { state: "paused", stop_paused: true, send_now: "now" })
      : rowEntry(id, wait, { send_now: stopped ? "now" : "stop" });
  });
  const run = stopped
    ? runAt(1, entries, { send: "prompt" })
    : runAt(1, [turnEntry("t1", { origin: "prompt", kind: "row" }), ...entries], {
        cancel: "t1",
        send: "steer",
      });
  const steers = Array.from({ length: s.steers }, (_, i): PendingSteer => ({
    id: `steer-${String(i + 1)}`,
    text: `s${String(i + 1)}`,
    origin: "user",
  }));
  return {
    read: () => ({ id: "c1", steers, run, compacting: s.compacting }),
    edit: () => Promise.resolve(),
    editByClear: () => Promise.resolve(),
    remove: () => Promise.resolve(),
    discard: () => Promise.resolve(),
    waitingFor: "the agent",
  };
}

function shown(e: Element): boolean {
  return e.checkVisibility();
}

function textOf(e: Element): string {
  return e.querySelector(".steer-text")?.textContent ?? "";
}

/** The stack as the reader meets it, against what the shape's rows say it must show. */
function checkStack(s: Shape): void {
  const stack = document.getElementById("steer-stack")!;
  const children = Array.from(stack.children);
  const fold = stack.querySelector(":scope > .steer-fold");
  const messageRows = children.filter((c) => c.matches(".steer-row:not([data-kind])"));
  const messages = messagesOf(s);

  expect(messageRows.map(textOf), "the messages in send order").toEqual(messages);
  expect(fold !== null, "a fold row exactly past three messages").toBe(messages.length > 3);

  const compacting = stack.querySelector(':scope > [data-kind="compact"]');
  expect(compacting !== null, "a compaction row exactly while the call runs").toBe(s.compacting);
  if (compacting !== null) {
    expect(children[0], "the compaction row leads the stack").toBe(compacting);
    expect(shown(compacting), "the compaction row, never folded").toBe(true);
  }

  const head = stack.querySelector(':scope > [data-kind="paused"]');
  const firstPaused = stoppedAt(s);
  expect(head !== null, "a paused queue's head over its paused rows").toBe(firstPaused !== -1);
  if (head !== null) {
    expect(shown(head), "the paused queue's head").toBe(true);
    expect(shown(head.querySelector('[aria-label="Resume the queue"]')!), "its Resume").toBe(true);
    const below = children.slice(children.indexOf(head) + 1).find((c) => c !== fold);
    expect(below && textOf(below), "the head leads the rows the stop paused").toBe(
      `q${String(firstPaused + 1)}`,
    );
  }

  if (s.open || fold === null) {
    expect(
      children.filter((c) => !shown(c)),
      "every row shown",
    ).toEqual([]);
    return;
  }
  const hiddenMessages = messageRows.filter((r) => !shown(r));
  expect(messageRows.filter(shown).map(textOf), "the first two messages shown, folded").toEqual(
    messages.slice(0, 2),
  );
  expect(fold.textContent, "the fold row counts the messages it hides").toBe(
    `+${String(hiddenMessages.length)} more`,
  );
  const at = children.indexOf(fold);
  expect(
    children.slice(0, at).filter((c) => !shown(c)),
    "nothing hidden above the fold row",
  ).toEqual([]);
}

let style: HTMLStyleElement | null = null;

beforeAll(() => {
  style = mountAppCSS();
});

afterEach(() => {
  unmountView();
  localStorage.removeItem(LS_UI_STATE_KEY);
});

afterAll(async () => {
  style?.remove();
  await browser.viewport(1280, 720);
});

describe("the steer stack's fold inside the holding area", () => {
  for (const [w, h] of [
    [1440, 900],
    [390, 844],
  ] as const) {
    it(`shows two messages, the fold row and a paused head, or every row in order, at ${String(w)}x${String(h)}`, async () => {
      await browser.viewport(w, h);
      await fc.assert(
        fc.asyncProperty(shapeArb, async (s) => {
          openStack(s.open);
          const chat = await mountPane(h, s.runs, s.runsOpen, sourceOf(s));
          try {
            checkStack(s);
            holds(chat, h - CHROME_PX);
          } finally {
            unmountView();
          }
        }),
        { numRuns: 25, examples: SEEDS.map((s) => [s] as [Shape]) },
      );
    });
  }
});
