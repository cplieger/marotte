// Re-showing a parked chat forces at most one layout: the gesture forces none, the outgoing view's
// park reads before any write, and the restore's scroll write is the one forced access, with every
// later read (anchor, edge distance, rail) after it. That holds when the switch changes the
// composer area's height too, which re-lays out a transcript left in layout. Real store, renderer
// and scroll controller; layouts counted by `layout-spy.ts`.
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry } from "./wire/types.gen.js";
import type { Session } from "./types.js";
import { loadCSS } from "./__test-helpers__/css-rules.js";
import { installLayoutSpy, type LayoutSpy } from "./__test-helpers__/layout-spy.js";

for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
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
for (const id of ["send-btn", "prompt-input"]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "button");
  d.id = id;
  document.body.appendChild(d);
}
const style = document.createElement("style");
style.textContent =
  loadCSS("13-messages.css") + loadCSS("40-a11y.css") + "#messages-wrap-outer { height: 400px; }";
document.head.appendChild(style);

const store = await import("./store.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");
const cover = await import("./transcript-cover.js");
const outer = document.getElementById("messages-wrap-outer") as HTMLElement;

messages.mountChatView();

/** The gesture half's store write. */
function select(id: string): void {
  store.setActive(id);
}

/** The activation half's store write: the painted chat moves (the active one already did). */
function show(id: string): void {
  (store as unknown as { setPainted?: (id: string) => void }).setPainted?.(id);
}

function chatOf(id: string, n: number): Session {
  const s = makeSession({ id, name: id });
  for (let i = 0; i < n; i++) {
    const t = `${id}-t${String(i)}`;
    const e = (at: number, kind: Entry["kind"], payload: unknown): Entry => ({
      id: `${t}-e${String(at)}`,
      turn: t,
      kind,
      seq: at,
      ts: at + 1,
      payload,
    });
    s.turns.set(t, {
      entries: [
        e(0, "turn_open", {
          prompt: { id: `${t}-p`, text: `prompt ${String(i)}` },
          source: "prompt",
          n: i + 1,
        }),
        e(1, "text", { text: `reply ${String(i)} `.repeat(12) }),
        e(2, "turn_close", { outcome: "completed" }),
      ],
      openEntries: new Map(),
    });
    s.turn_order.push(t);
  }
  s.turn_count = n;
  return s;
}

async function frames(): Promise<void> {
  for (let i = 0; i < 2; i++) {
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  }
}

let spy: LayoutSpy | null = null;

beforeEach(() => {
  outer.style.height = "";
  cover.dropCover();
  cover.showTranscript();
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  show("");
});

afterEach(() => {
  spy?.dispose();
  spy = null;
});

describe("the parked re-show", () => {
  it("forces at most one layout, the restore's scroll write, and reads nothing in the click", async () => {
    store.setSessions([chatOf("pl-x", 30), chatOf("pl-y", 2)]);
    select("pl-x");
    show("pl-x");
    store.bumpMessages("pl-x", "load");
    await Promise.resolve();
    await frames();
    const scroller = scroll.getScrollEl();
    expect(scroller.scrollHeight).toBeGreaterThan(scroller.clientHeight * 3);
    scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
    scroller.scrollTo({ top: 600, behavior: "instant" });
    scroller.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
    const snapshot = scroller.scrollTop;

    select("pl-y");
    show("pl-y");
    await Promise.resolve();
    await frames();

    spy = installLayoutSpy();
    spy.begin();
    select("pl-x");
    await Promise.resolve();
    const click = spy.end();
    expect(click.forced, "layouts forced by the gesture's store write").toBe(0);

    spy.begin();
    show("pl-x");
    await Promise.resolve();
    const task = spy.end();

    const forced = task.log.filter((a) => a.forced);
    expect(
      forced.map((a) => `${a.kind}:${a.what}`),
      "the activation task forced more than the restore write",
    ).toHaveLength(1);
    const restore = forced[0];
    expect(restore?.kind).toBe("write");
    const at = restore === undefined ? task.log.length : task.log.indexOf(restore);
    expect(
      task.log.slice(at + 1).some((a) => a.kind === "read"),
      "no read followed the restore write",
    ).toBe(true);
    expect(scroll.readingState()).toBe("reading");
    expect(Math.abs(scroller.scrollTop - snapshot)).toBeLessThanOrEqual(1);
  });
});

/** The gesture half as `tabs.ts` `beginActivation` runs it toward a covered chat: the park reads
 *  at the click's clean layout, then the active chat, the cover and the transcript out of layout.
 *  `steerRow` stands for the target's composer area: a queued prompt's row takes 50 px from the
 *  scroller. */
function click(id: string, steerRow: boolean): void {
  cover.takeParkReads();
  select(id);
  cover.holdCover(id);
  cover.hideTranscript();
  outer.style.height = steerRow ? "350px" : "400px";
}

/** The activation half (`tabs.ts` `runActivation`): the transcript back in layout, then the
 *  painted chat moves. */
async function activate(id: string): Promise<void> {
  cover.showTranscript();
  show(id);
  await Promise.resolve();
}

describe("a chat-to-chat switch whose composer area changes height", () => {
  it("lays out the outgoing transcript in neither half, and lands the parked chat exactly on return", async () => {
    store.setSessions([chatOf("pl-x", 30), chatOf("pl-y", 30)]);
    select("pl-x");
    show("pl-x");
    store.bumpMessages("pl-x", "load");
    await Promise.resolve();
    await frames();
    const scroller = scroll.getScrollEl();
    scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
    scroller.scrollTo({ top: 600, behavior: "instant" });
    scroller.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
    const parkedAt = scroller.scrollTop;

    click("pl-y", true);
    await frames();
    await activate("pl-y");
    store.bumpMessages("pl-y", "load");
    await Promise.resolve();
    await frames();

    spy = installLayoutSpy();
    spy.begin();
    click("pl-x", false);
    const gesture = spy.end();
    expect(gesture.forced, "layouts forced by the click").toBe(0);
    await frames();

    spy.begin();
    await activate("pl-x");
    const task = spy.end();
    const forced = task.log.filter((a) => a.forced);
    expect(
      forced.map((a) => `${a.kind}:${a.what}`),
      "the activation laid the outgoing transcript out again to read its park",
    ).toHaveLength(1);
    expect(forced[0]?.kind, "the one forced layout is the restore's write").toBe("write");
    await frames();
    expect(scroll.readingState()).toBe("reading");
    expect(Math.abs(scroller.scrollTop - parkedAt), "the parked chat moved").toBeLessThanOrEqual(1);
  });
});
