// A chat switch whose paint throws part-way (a card that fails to render) still lands the reader on
// the incoming chat's parked position, keeps the scroll owner's holds on, resumes the paused bodies,
// and the next paint finishes the pass without moving the reader back. Real store, renderer, scroll
// controller and layout; only the turn header builder is made to throw once. The cover held for the
// switch lifts with the throw, since a parked re-show has no later paint to lift it.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry } from "./wire/types.gen.js";

vi.mock("./fundamentals/turn-header.js", { spy: true });

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

import { loadCSS } from "./__test-helpers__/css-rules.js";

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
const { buildTurnHeader } = await import("./fundamentals/turn-header.js");

messages.mountChatView();

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

function sealed(
  turnID: string,
  at: number,
  kind: Entry["kind"],
  payload: unknown,
  id?: string,
): Entry {
  return { id: id ?? `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

function turnOpen(turnID: string, n: number, text = "go"): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text },
    source: "prompt",
    n,
  });
}

function settled(id: string, turns: readonly (readonly Entry[])[]): Session {
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

function longChat(id: string): Session {
  const turns: Entry[][] = [];
  for (let i = 0; i < 10; i++) {
    const t = `${id}-t${String(i)}`;
    turns.push([
      turnOpen(t, i + 1, `prompt ${String(i)}`),
      sealed(t, 1, "text", { text: `reply ${String(i)}` }),
      sealed(t, 2, "turn_close", { outcome: "completed" }),
    ]);
  }
  return settled(id, turns);
}

function oneTurn(id: string): Session {
  const t = `${id}-t1`;
  return settled(id, [
    [
      turnOpen(t, 1),
      sealed(t, 1, "text", { text: "x" }),
      sealed(t, 2, "turn_close", { outcome: "completed" }),
    ],
  ]);
}

function switchTo(chatID: string): void {
  store.setActive(chatID);
  store.setPainted(chatID);
}

function viewOf(chatID: string): HTMLElement {
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

async function flushed(): Promise<void> {
  await Promise.resolve();
}

async function settledFrames(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
}

/** The reader's own move: a wheel opens the gesture and the scroll event says where it went. */
function readTo(scroller: HTMLElement, top: number): void {
  scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
  scroller.scrollTo({ top, behavior: "instant" });
  scroller.dispatchEvent(new Event("scroll"));
}

/** A failed switch to `a`, which the reader left Reading at 5: a turn opened while it was parked,
 *  and that turn's card throws as it is built. `onActive` runs between the switch's two store writes. */
async function failedSwitch(a: string, b: string, onActive?: () => void): Promise<HTMLElement> {
  store.setSessions([longChat(a), oneTurn(b)]);
  switchTo(a);
  store.bumpMessages(a, "load");
  await flushed();
  const scroller = scroll.getScrollEl();
  expect(scroller.scrollHeight, "the premise: A scrolls").toBeGreaterThan(
    scroller.clientHeight + 150,
  );
  await settledFrames();
  // The chat opened on its end, so the top turns first lay out under the reader's move; the reader rests at 5 after.
  readTo(scroller, 5);
  await settledFrames();
  readTo(scroller, 5);
  expect(scroll.readingState()).toBe("reading");

  switchTo(b);
  await flushed();
  store.openTurn(a, turnOpen(`${a}-t10`, 11, "sent from another device"));
  await flushed();

  vi.mocked(buildTurnHeader).mockImplementationOnce(() => {
    throw new Error("render failed");
  });
  store.setActive(a);
  onActive?.();
  store.setPainted(a);
  await flushed();
  expect(
    vi.mocked(buildTurnHeader).mock.results.some((r) => r.type === "throw"),
    "the premise: the switch's pass threw",
  ).toBe(true);
  return scroller;
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  store.setPainted("");
  window.scrollTo(0, 0);
  vi.mocked(buildTurnHeader).mockClear();
  // The effect runner reports the throw on console.error.
  vi.spyOn(console, "error").mockImplementation(() => undefined);
});

afterEach(() => {
  cover.dropCover();
  vi.restoreAllMocks();
  scroll.getScrollEl().style.removeProperty("overflow-anchor");
});

describe("a chat switch whose paint throws", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("lands the reader on the incoming chat's parked position", async () => {
    const scroller = await failedSwitch(freshID("c-thr"), freshID("c-thr"));
    expect(scroll.readingState()).toBe("reading");
    expect(scroller.scrollTop).toBe(5);
  });

  it("lifts the cover the switch was made under", async () => {
    const a = freshID("c-thr");
    await failedSwitch(a, freshID("c-thr"), () => {
      cover.holdCover(a);
    });
    expect(cover.coverHeld(), "the transcript stayed under the cover").toBe(false);
  });

  it("keeps the scroll owner holding the reader through a later change above them", async () => {
    const a = freshID("c-thr");
    const scroller = await failedSwitch(a, freshID("c-thr"));
    // WebKit has no scroll anchoring, so the owner's hold is the only thing keeping the reader's line.
    scroller.style.overflowAnchor = "none";
    const before = scroller.scrollTop;
    scroll.heldMutation(() => {
      const block = document.createElement("div");
      block.style.blockSize = "200px";
      viewOf(a).prepend(block);
    });
    expect(Math.abs(scroller.scrollTop - (before + 200))).toBeLessThanOrEqual(1);
  });

  it("finishes the pass on the next paint without moving the reader back", async () => {
    const a = freshID("c-thr");
    const scroller = await failedSwitch(a, freshID("c-thr"));
    readTo(scroller, 40);
    store.bumpMessages(a, "shape");
    await flushed();
    await settledFrames();
    expect(scroller.scrollTop).toBe(40);
    expect(viewOf(a).querySelectorAll(".turn").length).toBe(11);
  });

  it("resumes the bodies the park paused", async () => {
    const a = freshID("c-thr");
    const b = freshID("c-thr");
    const t = `${a}-t0`;
    const call = "t-late";
    store.setSessions([
      settled(a, [
        [
          turnOpen(t, 1),
          sealed(
            t,
            1,
            "tool_call",
            { id: call, title: "before park", kind: "execute", status: "in_progress", ts: 2 },
            call,
          ),
          sealed(t, 2, "turn_close", { outcome: "completed" }),
        ],
      ]),
      oneTurn(b),
    ]);
    switchTo(a);
    store.bumpMessages(a, "load");
    const card = (): HTMLElement | null =>
      viewOf(a).querySelector<HTMLElement>(`[data-tool-id="${call}"]`);
    await vi.waitFor(() => {
      expect(card()?.textContent ?? "").toContain("before park");
    });
    switchTo(b);
    await flushed();
    store.applyToolProgress(a, t, { turn: t, tool_call_id: call, title: "while parked" });
    store.openTurn(a, turnOpen(`${a}-t1`, 2, "sent from another device"));
    await flushed();

    vi.mocked(buildTurnHeader).mockImplementationOnce(() => {
      throw new Error("render failed");
    });
    switchTo(a);
    await flushed();
    await settledFrames();
    expect(vi.mocked(buildTurnHeader).mock.results.some((r) => r.type === "throw")).toBe(true);
    expect(card()?.textContent ?? "").toContain("while parked");
  });
});
