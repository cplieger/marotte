// A landing page whose paint can only start the bodies in view keeps the transcript cover up until
// those bodies are built, then lifts it before the bodies out of view finish; a page whose owed
// bodies are all out of view lifts in the paint's task. Real store, renderer, scroll controller and
// layout.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import { loadCSS } from "./__test-helpers__/css-rules.js";
import type { Entry } from "./wire/types.gen.js";

for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  if (id === "messages-wrap") {
    document.getElementById("messages-wrap-outer")?.appendChild(d);
  } else if (id === "messages") {
    document.getElementById("messages-wrap")?.appendChild(d);
  } else {
    document.body.appendChild(d);
  }
}

const style = document.createElement("style");
style.textContent =
  loadCSS("13-messages.css") +
  `
  #messages-wrap-outer { height: 400px; }
  .ic-ui { inline-size: 16px; block-size: 16px; }
`;
document.head.appendChild(style);

const store = await import("./store.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");
const cover = await import("./transcript-cover.js");
const { mountedWindow } = await import("./messages-blocks.js");

messages.mountChatView();

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

function turnOpen(turnID: string, n: number): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text: `prompt ${String(n)}` },
    source: "prompt",
    n,
  });
}

/** A turn of `count` sealed thinking entries, one mounted row each. A failed turn never auto-folds,
 *  so its body is open and its unbuilt rows are a visible spacer. */
function heavyTurn(turnID: string, count: number, n: number, outcome = "completed"): Entry[] {
  const out: Entry[] = [turnOpen(turnID, n)];
  for (let at = 1; at <= count; at++) {
    out.push(sealed(turnID, at, "thinking", { text: `chunk ${String(at)}` }));
  }
  out.push(sealed(turnID, count + 1, "turn_close", { outcome }));
  return out;
}

/** One prose entry far taller than the 400px scrollport. */
function tallProseTurn(turnID: string, n: number): Entry[] {
  const text = Array.from({ length: 80 }, (_, i) => `paragraph ${String(i)}`).join("\n\n");
  return [
    turnOpen(turnID, n),
    sealed(turnID, 1, "text", { text }),
    sealed(turnID, 2, "turn_close", { outcome: "completed" }),
  ];
}

function chat(id: string, turns: readonly (readonly Entry[])[]): Session {
  const s = makeSession({ id, name: id });
  for (const entries of turns) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    s.turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    s.turn_order.push(first.turn);
  }
  s.turn_count = s.turn_order.length;
  return s;
}

/** A chat the server knows has turns, with none resident. */
function evicted(id: string): Session {
  return { ...makeSession({ id, name: id }), turn_count: 4, has_more: true };
}

function coverUp(): boolean {
  const c = document.getElementById("transcript-cover");
  return c !== null && !c.hidden && c.getAttribute("data-state") !== "released";
}

/** Whether a spacer standing for unbuilt rows shows inside the scrollport. */
function blankInView(): boolean {
  const port = scroll.getScrollEl().getBoundingClientRect();
  for (const sp of document.querySelectorAll("#messages .transcript-view.is-active .turn-space")) {
    const r = sp.getBoundingClientRect();
    if (Math.min(r.bottom, port.bottom) - Math.max(r.top, port.top) > 0.5) {
      return true;
    }
  }
  return false;
}

/** Whether every row of `turnID`'s `count` entries is mounted. */
function built(turnID: string, count: number): boolean {
  const w = mountedWindow(turnID);
  return w !== undefined && w.from <= 1 && w.to >= count;
}

async function flushed(): Promise<void> {
  await Promise.resolve();
}

async function nextFrame(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
}

/** Park every `scheduler.yield` the owed builds take until `releaseAll`, which lets each one and
 *  every yield the released work asks for run, then restores the scheduler. */
function holdYields(): { releaseAll: () => Promise<void> } {
  const held: (() => void)[] = [];
  const sched = (globalThis as { scheduler?: { yield?: () => Promise<void> } }).scheduler;
  expect(sched?.yield, "the owed builds yield through scheduler.yield").toBeTypeOf("function");
  const spy = vi
    .spyOn(sched as { yield: () => Promise<void> }, "yield")
    .mockImplementation(() => new Promise<void>((release) => held.push(release)));
  return {
    async releaseAll() {
      let released = 0;
      for (let round = 0; released < held.length; round++) {
        expect(round, "the owed builds kept yielding").toBeLessThan(200);
        while (released < held.length) {
          held[released++]?.();
        }
        await new Promise((r) => setTimeout(r, 0));
      }
      spy.mockRestore();
    },
  };
}

/** Switch to `to` under a cover held for it, from a painted one-turn chat, as an activation does. */
async function switchUnderCover(
  sessions: Session[],
  to: string,
  onHeld?: () => void,
): Promise<void> {
  const from = freshID("cr-from");
  store.setSessions([chat(from, [heavyTurn(`${from}-t`, 1, 1)]), ...sessions]);
  store.setActive(from);
  store.setPainted(from);
  store.bumpMessages(from, "load");
  await flushed();
  store.setActive(to);
  cover.holdCover(to);
  onHeld?.();
  store.setPainted(to);
  await flushed();
}

/** What the page looked like at the moment the cover was released. */
interface AtLift {
  blank: boolean;
  newestBuilt: boolean;
  olderBuilt: boolean;
}

/** Resolves with the page as it stood when the held cover was next released. */
function watchLift(newest: [string, number], older: [string, number]): Promise<AtLift> {
  return new Promise((resolve) => {
    const mo = new MutationObserver(() => {
      if (coverUp()) {
        return;
      }
      mo.disconnect();
      resolve({
        blank: blankInView(),
        newestBuilt: built(...newest),
        olderBuilt: built(...older),
      });
    });
    const node = document.getElementById("transcript-cover");
    if (node === null) {
      throw new Error("no cover");
    }
    mo.observe(node, { attributes: true, attributeFilter: ["data-state", "hidden"] });
  });
}

beforeEach(() => {
  cover.dropCover();
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  store.setPainted("");
});

afterEach(() => {
  vi.restoreAllMocks();
  cover.dropCover();
});

describe("the cover over a landing page", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("stays up until the body in view is built, and lifts before the bodies above finish", async () => {
    const b = freshID("cr-b");
    const older = `${b}-old`;
    const newest = `${b}-new`;
    let lift: Promise<AtLift> | undefined;
    await switchUnderCover(
      [chat(b, [heavyTurn(older, 120, 1, "failed"), heavyTurn(newest, 200, 2)])],
      b,
      () => {
        lift = watchLift([newest, 200], [older, 120]);
      },
    );
    expect(built(newest, 200), "the premise: the paint only started the newest body").toBe(false);
    expect(built(older, 120), "the premise: the older body is owed too").toBe(false);
    expect(scroll.readingState(), "the premise: the reader lands on the live edge").toBe(
      "following",
    );

    const at = await lift;
    if (at === undefined) {
      throw new Error("the switch never held the cover");
    }
    expect(at.blank, "a blank band showed at the lift").toBe(false);
    expect(at.newestBuilt, "the body in view was still being built at the lift").toBe(true);
    expect(at.olderBuilt, "the lift waited on a body out of view").toBe(false);
  });

  it("lifts by the first frame when every body still owed is out of view", async () => {
    const yields = holdYields();
    const b = freshID("cr-b");
    const older = `${b}-old`;
    await switchUnderCover(
      [chat(b, [heavyTurn(older, 200, 1, "failed"), tallProseTurn(`${b}-new`, 2)])],
      b,
    );
    await nextFrame();
    expect(built(older, 200), "the premise: the older body is still owed").toBe(false);
    expect(blankInView(), "the premise: its rows are out of view").toBe(false);
    expect(coverUp()).toBe(false);
    await yields.releaseAll();
  });

  it("leaves a cover handed on to a chat still loading when the landing's builds finish", async () => {
    const yields = holdYields();
    const b = freshID("cr-b");
    const newest = `${b}-new`;
    const c = freshID("cr-c");
    await switchUnderCover([chat(b, [heavyTurn(newest, 200, 1)]), evicted(c)], b);
    await nextFrame();
    expect(coverUp(), "the premise: B's cover waits on its body in view").toBe(true);

    store.setActive(c);
    cover.holdCover(c);
    store.setPainted(c);
    await flushed();
    await yields.releaseAll();
    expect(coverUp(), "B's builds released the cover held for C").toBe(true);
  });
});
