// Re-showing a parked chat forces at most one layout: the gesture forces none, the outgoing view's
// park reads before any write, and the restore's scroll write is the one forced access, with every
// later read (anchor, edge distance, rail) after it. That holds when the switch changes the
// composer area's height too: the park reads from the click, before the outgoing transcript is laid
// out at the new size. A chat parked while an older page loads parks without the page's skeleton
// slot, so the restore lands on the reader's turn in its own task. Real store, renderer and scroll
// controller; layouts counted by `layout-spy.ts`.
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
  loadCSS("01-tokens.css") +
  loadCSS("13-messages.css") +
  loadCSS("29-turns.css") +
  loadCSS("40-a11y.css") +
  "#messages-wrap-outer { height: 400px; }";
document.head.appendChild(style);

const store = await import("./store.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");
const cover = await import("./transcript-cover.js");
const { KEY_ATTR } = await import("./reconcile.js");
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
 *  at the click's clean layout, then the active chat and the cover. `steerRow` stands for the
 *  target's composer area: a queued prompt's row takes 50 px from the scroller. */
function click(id: string, steerRow: boolean): void {
  cover.takeParkReads();
  select(id);
  cover.holdCover(id);
  outer.style.height = steerRow ? "350px" : "400px";
}

/** What the reader sees at the scroller's top: the first turn reaching into the viewport and how
 *  far its top sits from the scroller's. Pixel offsets alone shift with the older-page skeleton,
 *  which a park leaves out. */
function readerAnchor(): { id: string; offset: number } {
  const scroller = scroll.getScrollEl();
  const top = scroller.getBoundingClientRect().top;
  const view = scroller.querySelector(".transcript-view.is-active");
  const turn = [...(view?.children ?? [])].find(
    (t) => t.hasAttribute(KEY_ATTR) && t.getBoundingClientRect().bottom > top,
  );
  return {
    id: turn?.getAttribute(KEY_ATTR) ?? "",
    offset: Math.round((turn?.getBoundingClientRect().top ?? 0) - top),
  };
}

/** Transitions and animations running on elements inside `view`. */
function runningIn(view: HTMLElement | null): string[] {
  return document
    .getAnimations()
    .filter((a) => {
      const target = (a.effect as KeyframeEffect | null)?.target;
      return a.playState === "running" && target instanceof Element && view?.contains(target);
    })
    .map((a) => `${a.constructor.name}@${(a.effect as KeyframeEffect).target?.className ?? ""}`);
}

/** The activation half (`tabs.ts` `runActivation`): the transcript back in layout, then the
 *  painted chat moves. */
async function activate(id: string): Promise<void> {
  cover.showTranscript();
  show(id);
  await Promise.resolve();
}

/** Two 30-turn chats, x shown and read at 600 px once its turns' first-frame work (the footer
 *  fits, the entry transitions) has settled. */
async function readingX(): Promise<HTMLElement> {
  store.setSessions([chatOf("pl-x", 30), chatOf("pl-y", 30)]);
  select("pl-x");
  show("pl-x");
  store.bumpMessages("pl-x", "load");
  await Promise.resolve();
  await frames();
  await new Promise((r) => setTimeout(r, 400));
  const scroller = scroll.getScrollEl();
  scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
  scroller.scrollTo({ top: 600, behavior: "instant" });
  scroller.dispatchEvent(new Event("scroll"));
  await frames();
  expect(scroll.readingState()).toBe("reading");
  expect(readerAnchor().id, "no turn at the reader's position").not.toBe("");
  return scroller;
}

/** Away to y (whose composer area shows a steer row) and shown there. */
async function awayToY(): Promise<void> {
  click("pl-y", true);
  await frames();
  await activate("pl-y");
  store.bumpMessages("pl-y", "load");
  await Promise.resolve();
  await frames();
  await new Promise((r) => setTimeout(r, 400));
}

describe("a chat-to-chat switch whose composer area changes height", () => {
  it("forces no layout in the click, only the restore in the activation, and lands the parked chat exactly on return", async () => {
    await readingX();
    const parkedAt = readerAnchor();
    await awayToY();

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
    expect(readerAnchor(), "the parked chat moved").toEqual(parkedAt);
  });

  it("replays no entry transition in the parked chat's view on return", async () => {
    await readingX();
    const xView = messages.transcriptViewFor("pl-x");
    expect(
      xView?.querySelectorAll(".turn-footer, .turn-face").length,
      "x renders no turn with an entry transition, so the case proves nothing",
    ).toBeGreaterThan(0);
    expect(runningIn(xView), "x's entry transitions were still running before the switch").toEqual(
      [],
    );
    const parkedAt = readerAnchor();
    await awayToY();

    click("pl-x", false);
    await frames();
    await activate("pl-x");
    await frames();
    expect(
      runningIn(xView),
      "the parked chat's turns replayed their entry on return: its view lost its rendering state",
    ).toEqual([]);
    expect(readerAnchor(), "the parked chat moved").toEqual(parkedAt);
  });

  it("keeps the reader where they were on a return to the painted chat before the other showed", async () => {
    const scroller = await readingX();
    const at = readerAnchor();
    const top = scroller.scrollTop;

    click("pl-y", true);
    await frames();
    expect(scroller.scrollTop, "the covered scroller moved under the cover").toBe(top);
    // Back to x before y's activation: x is still the painted, laid-out chat, so the click lifts
    // y's cover over it (`tabs.ts` `beginActivation`).
    select("pl-x");
    cover.releaseCoverExceptFor("pl-x");
    outer.style.height = "400px";
    await frames();
    expect(readerAnchor(), "the painted chat moved under the cover").toEqual(at);
  });
});

/** Asks for the older page from the reader's position, so its skeleton stands above the turns
 *  while the page is out. */
async function olderPageLoading(): Promise<HTMLElement | null> {
  scroll.setLoadMore(() => undefined, true);
  scroll.noteHistoryEdge(true);
  await frames();
  return document.getElementById("load-more-skeleton");
}

describe("a chat parked while an older page loads", () => {
  it("parks at the reader's position less the skeleton's slot the park removes", async () => {
    const scroller = await readingX();
    expect(await olderPageLoading(), "no older-page skeleton, so the case proves nothing").not.toBe(
      null,
    );
    expect(scroll.readingState()).toBe("reading");
    const turn = (): number => {
      const view = scroller.querySelector(".transcript-view.is-active");
      const first = [...(view?.children ?? [])].find((t) => t.hasAttribute(KEY_ATTR));
      return first?.getBoundingClientRect().top ?? 0;
    };
    const top = scroller.scrollTop;
    const before = turn();
    const parked = scroll.detach();
    const shift = before - turn();
    expect(shift, "the park removed no skeleton slot").toBeGreaterThan(0);
    expect(Math.abs(parked.scrollTop - (top - shift))).toBeLessThanOrEqual(1);
  });

  it("lands the reader's turn and offset in the return's own activation task", async () => {
    await readingX();
    expect(await olderPageLoading(), "no older-page skeleton, so the case proves nothing").not.toBe(
      null,
    );
    const parkedAt = readerAnchor();
    click("pl-y", false);
    await frames();
    await activate("pl-y");
    store.bumpMessages("pl-y", "load");
    await Promise.resolve();
    await frames();
    await new Promise((r) => setTimeout(r, 400));

    click("pl-x", false);
    await frames();
    await activate("pl-x");
    expect(readerAnchor(), "the restore wrote a position the reader was never at").toEqual(
      parkedAt,
    );
    await frames();
    expect(readerAnchor(), "the parked chat moved").toEqual(parkedAt);
  });
});
