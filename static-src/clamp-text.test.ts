// Real layout throughout: a detached element measures 0, which only exercises the character guess.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { attachClamp, releaseClamp, releaseClampsIn, clampObservationCount } from "./clamp-text.js";
import clampSource from "./clamp-text.ts?raw";
import messagesCSS from "./css/13-messages.css?raw";
import type { TurnState } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";

// Built before `messages.js` is imported: `scroll.ts` reads the scroller from the DOM registry at import.
for (const id of [
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}
const scrollerEl = document.createElement("div");
scrollerEl.id = "messages-wrap";
document.getElementById("messages-wrap-outer")?.appendChild(scrollerEl);
const transcriptEl = document.createElement("div");
transcriptEl.id = "messages";
scrollerEl.appendChild(transcriptEl);

const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { mountChatView, disposeChatView } = await import("./messages.js");

function sealed(turnID: string, seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(seq)}`,
    turn: turnID,
    lane: "",
    kind,
    seq,
    ts: seq + 1,
    payload,
  } as Entry;
}

function turnWithSteer(id: string, n: number, request: string, correction: string): Entry[] {
  return [
    sealed(id, 0, "turn_open", { prompt: { id: `${id}-p`, text: request }, source: "prompt", n }),
    sealed(id, 1, "steer", { text: correction, origin: "user", state: "read" }),
    sealed(id, 2, "text", { text: "reply" }),
    sealed(id, 3, "turn_close", { outcome: "completed" }),
  ];
}

function paintTurns(chat: string, turnRows: Entry[][]): void {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnRows) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    order.push(first.turn);
  }
  setSessions([
    {
      ...makeSession({ id: chat, name: "c" }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(chat);
  bumpMessages(chat, "load");
}

const CSS = `
  .ct-text { font: 16px/20px monospace; overflow-wrap: anywhere; }
  .ct-text[data-clamped] {
    display: -webkit-box;
    -webkit-line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
`;

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = document.createElement("style");
  styleEl.textContent = CSS;
  document.head.appendChild(styleEl);
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterAll(() => {
  styleEl.remove();
  host.remove();
});

afterEach(() => {
  host.replaceChildren();
});

interface Pair {
  readonly text: HTMLElement;
  readonly more: HTMLButtonElement;
}

function mount(body: string, width: number, lines = 3): Pair {
  host.style.inlineSize = `${String(width)}px`;
  const text = document.createElement("div");
  text.className = "ct-text";
  text.textContent = body;
  const more = document.createElement("button");
  more.type = "button";
  host.replaceChildren(text, more);
  attachClamp(text, more, { lines });
  return { text, more };
}

/** Only for a verdict that must CHANGE: a condition that already holds returns before the observer runs. */
async function settles(p: Pair, hidden: boolean, why: string): Promise<void> {
  const deadline = Date.now() + 2000;
  while (p.more.hidden !== hidden && Date.now() < deadline) {
    await observerRuns();
  }
  expect(p.more.hidden, why).toBe(hidden);
}

/** Three frames: two span one resize delivery, and the module defers its verdict one frame more. */
async function observerRuns(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    });
  });
}

const ONE_LINE = "target main";
const LONG = "the quick brown fox jumps over the lazy dog ".repeat(12);

describe("deciding whether the opener is needed", () => {
  it("hides the opener for a text that fits", async () => {
    const p = mount(ONE_LINE, 400);
    await observerRuns();
    expect(p.more.hidden).toBe(true);
    expect(p.text.hasAttribute("data-clamped"), "clamped even so, harmlessly").toBe(true);
  });

  it("shows the opener for a text that overflows", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered for a text past its line cap");
    expect(p.text.scrollHeight).toBeGreaterThan(p.text.clientHeight);
  });

  it("re-decides at every width, so narrowing cannot hide text with no way to open it", async () => {
    const body = "widen the existing front-matter struct with the missing field instead";
    const p = mount(body, 900);
    await settles(p, true, "fits wide");

    host.style.inlineSize = "120px";
    await settles(p, false, "offered once it no longer fits");

    host.style.inlineSize = "900px";
    await settles(p, true, "withdrawn again when it fits");
  });

  it("corrects the no-layout guess once the element is laid out", async () => {
    const body = "x".repeat(240);
    const text = document.createElement("div");
    text.className = "ct-text";
    text.textContent = body;
    const more = document.createElement("button");
    more.type = "button";
    attachClamp(text, more, { lines: 3 });
    expect(more.hidden, "the guess, while detached").toBe(false);

    host.style.inlineSize = "1200px";
    host.replaceChildren(text, more);
    await settles({ text, more }, true, "corrected once laid out");
  });
});

describe("opening and closing", () => {
  it("drops the clamp on the opener's click and restores it on the next", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");
    expect(p.more.textContent).toBe("Show more");
    expect(p.more.getAttribute("aria-expanded")).toBe("false");

    p.more.click();
    expect(p.text.hasAttribute("data-clamped")).toBe(false);
    expect(p.more.textContent).toBe("Show less");
    expect(p.more.getAttribute("aria-expanded")).toBe("true");

    p.more.click();
    expect(p.text.hasAttribute("data-clamped")).toBe(true);
    expect(p.more.textContent).toBe("Show more");
    expect(p.more.getAttribute("aria-expanded")).toBe("false");
  });

  it("leaves an expansion alone when the box resizes under it", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");
    p.more.click();

    host.style.inlineSize = "300px";
    await observerRuns();
    expect(p.text.hasAttribute("data-clamped"), "still open").toBe(false);
    expect(p.more.hidden, "and the opener stays reachable").toBe(false);
  });

  it("keeps the expanded flag where the caller stores it", async () => {
    host.style.inlineSize = "400px";
    const text = document.createElement("div");
    text.className = "ct-text";
    text.textContent = LONG;
    const more = document.createElement("button");
    more.type = "button";
    host.replaceChildren(text, more);
    const store = { open: false };
    const handle = attachClamp(text, more, {
      lines: 3,
      isExpanded: () => store.open,
      setExpanded: (on) => {
        store.open = on;
      },
    });
    await settles({ text, more }, false, "offered");

    more.click();
    expect(store.open, "written through to the caller").toBe(true);
    handle.sync();
    expect(text.hasAttribute("data-clamped")).toBe(false);
    expect(more.hidden).toBe(false);
  });
});

describe("the handle", () => {
  it("collapse() forgets an expansion, for content that has changed", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");
    const handle = attachClamp(p.text, p.more);
    p.more.click();
    expect(p.text.hasAttribute("data-clamped")).toBe(false);

    handle.collapse();
    expect(p.text.hasAttribute("data-clamped")).toBe(true);
    expect(p.more.textContent).toBe("Show more");
    expect(p.more.hidden, "and it is still offered, since the text still overflows").toBe(false);
  });

  it("disable() takes the clamp off and a resize cannot put it back", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");
    attachClamp(p.text, p.more).disable();
    expect(p.text.hasAttribute("data-clamped")).toBe(false);
    expect(p.more.hidden).toBe(true);

    host.style.inlineSize = "120px";
    await observerRuns();
    expect(p.text.hasAttribute("data-clamped"), "still off").toBe(false);
    expect(p.more.hidden).toBe(true);
  });

  it("is idempotent: a repeat attach wires no second listener", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");
    // A second attach must return the same state, or one click toggles twice.
    attachClamp(p.text, p.more, { lines: 3 });
    p.more.click();
    expect(p.text.hasAttribute("data-clamped")).toBe(false);
  });
});

describe("releasing", () => {
  it("unobserves an element that has left the document", async () => {
    const p = mount(LONG, 400);
    await settles(p, false, "offered");

    p.text.remove();
    p.more.remove();
    // The final zero-size change carries `isConnected === false`, which is the release.
    await observerRuns();
    p.more.hidden = true;
    host.style.inlineSize = "120px";
    await observerRuns();
    expect(p.more.hidden, "nothing re-decided it").toBe(true);
  });

  it("takes the observation count back to zero when the host subtree is released", async () => {
    const before = clampObservationCount();
    host.style.inlineSize = "400px";
    const texts: HTMLElement[] = [];
    for (let i = 0; i < 5; i++) {
      const text = document.createElement("div");
      text.className = "ct-text";
      text.textContent = LONG;
      const more = document.createElement("button");
      more.type = "button";
      host.append(text, more);
      attachClamp(text, more, { lines: 3 });
      texts.push(text);
    }
    await observerRuns();
    expect(clampObservationCount() - before, "five more watched").toBe(5);

    releaseClampsIn(host);
    expect(clampObservationCount() - before, "and none after the sweep").toBe(0);
    for (const text of texts) {
      text.remove();
    }
    host.style.inlineSize = "120px";
    await observerRuns();
    expect(clampObservationCount() - before).toBe(0);
  });

  it("releases one element without touching its siblings", async () => {
    const before = clampObservationCount();
    const a = mount(LONG, 400);
    const b = document.createElement("div");
    b.className = "ct-text";
    b.textContent = LONG;
    const bMore = document.createElement("button");
    bMore.type = "button";
    host.append(b, bMore);
    attachClamp(b, bMore, { lines: 3 });
    await observerRuns();
    expect(clampObservationCount() - before).toBe(2);

    releaseClamp(a.text);
    expect(clampObservationCount() - before, "only the named one went").toBe(1);
    releaseClamp(b);
    expect(clampObservationCount() - before).toBe(0);
  });

  it("releases a mounted chat view's clamps when the view is disposed", async () => {
    // `disposeChatView` is the one dispose every close path runs. Driven through steer notes: the turn header's
    // clamp is CSS-only, so it would count 0 before and after the sweep.
    const before = clampObservationCount();
    mountChatView();
    const chat = "c-clamp-dispose";
    const turnRows: Entry[][] = [];
    for (let t = 0; t < 4; t++) {
      turnRows.push(
        turnWithSteer(
          `t${String(t)}`,
          t + 1,
          `a request, number ${String(t)}`,
          `a correction long enough to be worth clamping, number ${String(t)}`,
        ),
      );
    }
    paintTurns(chat, turnRows);
    expect(clampObservationCount() - before, "one per steer note").toBe(4);

    disposeChatView(chat);
    expect(clampObservationCount() - before).toBe(0);
  });

  it("keeps the callback-inferred sweep as well as the explicit release", () => {
    // Source guard: deleting either release half leaves the suite above green and the leak invisible.
    expect([
      clampSource.includes("export function releaseClamp("),
      clampSource.includes("export function releaseClampsIn("),
      clampSource.includes("!entry.target.isConnected"),
    ]).toEqual([true, true, true]);
  });
});

describe("inside the transcript's own observer set", () => {
  it("re-decides on a width change with no observation left undelivered", async () => {
    // The reported Safari loop error: a width change re-decides the verdict inside a resize delivery, where the card
    // is observed at a shallower depth. Chromium 152: 1 loop error per width change written in the delivery, 0 deferred.
    // Uses a steer note (the header clamp is CSS-only) and the shipped clamp rule, without which nothing overflows.
    const loops: string[] = [];
    const onError = (e: ErrorEvent): void => {
      if (e.message.includes("ResizeObserver loop")) {
        loops.push(e.message);
      }
    };
    window.addEventListener("error", onError);
    const noteStyle = document.createElement("style");
    noteStyle.textContent = messagesCSS;
    document.head.appendChild(noteStyle);
    const chat = "c-clamp-ro-loop";
    try {
      mountChatView();
      const body = "the quick brown fox jumps over the lazy dog while ".repeat(5);
      paintTurns(chat, [turnWithSteer("u1", 1, "a request", body)]);

      const text = document.querySelector<HTMLElement>(".steer-note-text");
      const more = document.querySelector<HTMLButtonElement>(".steer-note-more");
      expect(text, "the steer note painted").not.toBeNull();
      expect(more, "with its opener").not.toBeNull();
      if (text === null || more === null) {
        return;
      }

      scrollerEl.style.inlineSize = "320px";
      await settles({ text, more }, false, "offered once the steer no longer fits");
      expect(text.scrollHeight, "and the measurement is what said so").toBeGreaterThan(
        text.clientHeight,
      );
      expect(loops, "narrowing left nothing undelivered").toEqual([]);

      scrollerEl.style.inlineSize = "1000px";
      await settles({ text, more }, true, "withdrawn again once it fits");
      expect(loops, "and neither did widening").toEqual([]);
    } finally {
      window.removeEventListener("error", onError);
      scrollerEl.style.inlineSize = "";
      noteStyle.remove();
      disposeChatView(chat);
    }
  });
});
