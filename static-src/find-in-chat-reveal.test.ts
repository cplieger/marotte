import { describe, it, expect, afterAll, beforeAll, beforeEach, vi } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import type { TurnState } from "./types.js";
import type { Entry, Hit } from "./wire/types.gen.js";
import type * as ApiClient from "./api-client.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: vi.fn(),
}));

// Nested as static/index.html nests them: `#messages-wrap` is both the scroller and the transcript's
// `offsetParent`.
for (const id of [
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
  "find-btn",
  "shell-panel",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  document.body.appendChild(d);
}
const scrollerEl = document.createElement("div");
scrollerEl.id = "messages-wrap";
document.getElementById("messages-wrap-outer")?.appendChild(scrollerEl);
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
scrollerEl.appendChild(messagesEl);

const { setSessions, setActive, bumpMessages, appendEntry, getActive, applyToolProgress } =
  await import("./store.js");
const { mountChatView, teardownAll } = await import("./messages.js");
const { scrollToBottom, readingState } = await import("./scroll.js");
const { setPinSettleMs } = await import("./scroll-controller.js");
const { resetFoldState } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");
const { forgetHeights } = await import("./block-heights.js");
const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { handleFindHotkey, toggleChatFind, closeChatFind, _isChatFindOpen } =
  await import("./find-in-chat.js");
const api = await import("./api-client.js");

const VIEWPORT_PX = 720;
const NEEDLE = "chartreuse";
/** Not `NEEDLE`, which find keeps across a close: a new query makes the first Enter search, not step. */
const FOLD_NEEDLE = "vermilion";
const COMMAND_NEEDLE = "saffron";
const ENTRIES = 200;
const HIT_AT = 101;
/** A second match above the hit, far enough that the two rows are never laid out together. */
const ABOVE_AT = 71;
const ABOVE_TEXT = `prose ${String(ABOVE_AT)} a ${NEEDLE} that sits well above the hit`;

function sealed(at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `big-e${String(at)}`, turn: "big", kind, seq: at, ts: at + 1, payload } as Entry;
}

/** Twelve paragraphs with the needle in the last: taller than the scrollport, so centring the
 *  needle is a second move after the jump that centres the entry. */
function hitText(): string {
  const paragraphs = Array.from(
    { length: 12 },
    (_, i) => `paragraph ${String(i)} ${"the quick brown fox jumps over the lazy dog ".repeat(4)}`,
  );
  return [...paragraphs, `the ${NEEDLE} lives at the very end`].join("\n\n");
}

/** Wrapping prose rows split by `thinking` entries, so each is its own `content-visibility: auto`
 *  row. The cold load lays out the tail only, so the rows around the hit still stand at their
 *  3rem placeholder and take their real height as the jump brings them near. A `live` turn has no
 *  close, so it can stream. */
function proseTurn(live: boolean, matchAbove = false): Entry[] {
  const out: Entry[] = [
    sealed(0, "turn_open", { prompt: { id: "big-p", text: "go" }, source: "prompt", n: 1 }),
  ];
  for (let at = 1; at <= ENTRIES; at++) {
    if (at === HIT_AT) {
      out.push(sealed(at, "text", { text: hitText() }));
    } else if (matchAbove && at === ABOVE_AT) {
      out.push(sealed(at, "text", { text: ABOVE_TEXT }));
    } else if (at % 2 === 1) {
      out.push(
        sealed(at, "text", {
          text: `prose ${String(at)} ${"a sentence that wraps across the column more than once ".repeat(5)}`,
        }),
      );
    } else {
      out.push(sealed(at, "thinking", { text: `step ${String(at)}` }));
    }
  }
  if (!live) {
    out.push(sealed(ENTRIES + 1, "turn_close", { outcome: "completed" }));
  }
  return out;
}

function streamChunk(chat: string, turn = "big"): void {
  const seq = getActive()?.turns.get(turn)?.entries.length ?? 0;
  appendEntry(chat, {
    id: `${turn}-e${String(seq)}`,
    turn,
    kind: "text",
    seq,
    ts: seq + 1,
    payload: { text: `streamed ${String(seq)}` },
  } as Entry);
}

function inTurn(turn: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turn}-e${String(at)}`, turn, kind, seq: at, ts: at + 1, payload } as Entry;
}

/** A turn that ran a tool, so it offers the fold. `text` precedes the tool, keeping it out of the folded face, which
 *  shows the final prose. A `live` turn has no close, so it can stream. */
function toolTurn(turn: string, n: number, text: string, live = false): Entry[] {
  const out = [
    inTurn(turn, 0, "turn_open", { prompt: { id: `${turn}-p`, text: "go" }, source: "prompt", n }),
    inTurn(turn, 1, "text", { text }),
    inTurn(turn, 2, "tool_call", {
      id: `${turn}-tc`,
      title: "Read File",
      kind: "read",
      status: "completed",
      ts: 1,
    }),
    inTurn(turn, 3, "text", { text: `done ${turn}` }),
  ];
  if (!live) {
    out.push(inTurn(turn, 4, "turn_close", { outcome: "completed" }));
  }
  return out;
}

function turnCard(turn: string): HTMLElement {
  const card = messagesEl.querySelector<HTMLElement>(`.turn[${KEY_ATTR}="${turn}"]`);
  if (card === null) {
    throw new Error(`no card for turn ${turn}`);
  }
  return card;
}

function foldToggle(turn: string): HTMLButtonElement {
  const btn = turnCard(turn).querySelector<HTMLButtonElement>(".turn-fold-toggle");
  if (btn === null) {
    throw new Error(`turn ${turn} offers no fold`);
  }
  return btn;
}

function hitIn(at: number, text: string, excerpt: string): Hit {
  return {
    turn_id: "big",
    entry_id: `big-e${String(at)}`,
    excerpt,
    role: "assistant",
    segment_kind: "content",
    turn: 1,
    offset: text.indexOf(NEEDLE),
    segment_len: text.length,
  } as unknown as Hit;
}

function hit(): Hit {
  return hitIn(HIT_AT, hitText(), `the ${NEEDLE} lives at the very end`);
}

function aboveHit(): Hit {
  return hitIn(ABOVE_AT, ABOVE_TEXT, ABOVE_TEXT);
}

function entryEl(at: number): HTMLElement {
  return entryById(`big-e${String(at)}`);
}

function entryById(id: string): HTMLElement {
  const found = messagesEl.querySelector<HTMLElement>(
    `[data-entry-id="${id}"], [data-entries~="${id}"]`,
  );
  if (found === null) {
    throw new Error(`entry ${id} is not mounted`);
  }
  return found;
}

/** The hit's own paragraph, the last of its entry; found by position, since its marks may be gone. */
function hitParagraph(): HTMLElement {
  const last = [...entryEl(HIT_AT).querySelectorAll<HTMLElement>("p")].at(-1);
  if (last === undefined) {
    throw new Error("the hit entry has no paragraph");
  }
  return last;
}

function currentHitEntry(): string | null {
  const mark = messagesEl.querySelector("mark.find-hit-current");
  return mark?.closest("[data-entry-id]")?.getAttribute("data-entry-id") ?? null;
}

/** The reader's own scroll to `target`: an input first, which is what tells the controller the
 *  reader moved, then an instant write. */
function readerScrollsTo(target: HTMLElement, deltaY: number): void {
  scrollerEl.dispatchEvent(new WheelEvent("wheel", { deltaY, bubbles: true }));
  target.scrollIntoView({ block: "center", behavior: "instant" });
}

function twoFrames(): Promise<void> {
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

/** The server's search reply, run through the caller's decoder so it is held to the wire shape. */
function stageServerHits(hits: Hit[]): void {
  vi.mocked(api.apiGetTyped).mockImplementation(((path: string, decode: (v: unknown) => unknown) =>
    Promise.resolve(
      path.includes("/search?q=")
        ? decode({ matches: hits, scanned: 1, matched: hits.length, truncated: false })
        : null,
    )) as typeof api.apiGetTyped);
}

function countText(): string {
  return document.getElementById("chat-find-count")?.textContent ?? "";
}

async function findAndStep(
  query: string,
  beforeStep: () => void = () => undefined,
  back = false,
): Promise<void> {
  handleFindHotkey(new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true }));
  const input = document.getElementById("chat-find-input") as HTMLInputElement;
  const enter = (shiftKey: boolean): void => {
    input.value = query;
    input.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", shiftKey, bubbles: true, cancelable: true }),
    );
  };
  enter(false);
  await vi.waitFor(() => {
    expect(countText()).toMatch(/in chat|matched, not shown here/);
  });
  // The shell's render, which records the navigable hits, lands a few microtask hops after the counter.
  await new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
  beforeStep();
  enter(back);
}

async function loadAtLiveEdge(chat: string, live: boolean, matchAbove = false): Promise<void> {
  forgetHeights(["big"]);
  const turns = new Map<string, TurnState>([
    ["big", { entries: proseTurn(live, matchAbove), openEntries: new Map() }],
  ]);
  setSessions([
    { ...makeSession({ id: chat, name: chat }), turns, turn_order: ["big"], turn_count: 1 },
  ]);
  setActive(chat);
  bumpMessages(chat, "load");
  await vi.waitFor(() => {
    expect(messagesEl.querySelectorAll("[data-entry-seq]").length).toBeGreaterThan(ENTRIES / 4);
  });
  const real = setPinSettleMs(20);
  try {
    scrollToBottom();
  } finally {
    setPinSettleMs(real);
  }
  await new Promise((resolve) => {
    setTimeout(resolve, 150);
  });
  stageServerHits(matchAbove ? [aboveHit(), hit()] : [hit()]);
}

async function findFromLiveEdge(chat: string): Promise<void> {
  await loadAtLiveEdge(chat, false);
  await findAndStep(NEEDLE);
}

/** Close find once the step's first jump is issued: the walk-miss jump, which the reveal follows
 *  only after a rendered frame. A microtask, so the close lands inside that frame wait. */
function closeAfterFirstJump(): void {
  const real = Element.prototype.scrollIntoView;
  vi.spyOn(Element.prototype, "scrollIntoView").mockImplementationOnce(function (
    this: Element,
    arg?: boolean | ScrollIntoViewOptions,
  ) {
    real.call(this, arg);
    queueMicrotask(closeChatFind);
  });
}

function atLiveEdge(): boolean {
  return (
    scrollerEl.scrollTop + scrollerEl.clientHeight >= scrollerEl.scrollHeight - VIEWPORT_PX / 4
  );
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}

/** Find closed mid-step leaves the reader where the jump took them: parked, and not pinned back
 *  to the live edge by the next streamed chunk, before or after the flight would have landed. */
async function staysParkedThroughStream(chat: string): Promise<void> {
  await vi.waitFor(() => {
    expect(_isChatFindOpen()).toBe(false);
  });
  // Past the jump's quiet period, so a follow write is licensed, and while a smooth flight still flies.
  await sleep(400);
  streamChunk(chat);
  // After any flight has landed.
  await sleep(1200);
  streamChunk(chat);
  await sleep(300);
  expect({ state: readingState(), atLiveEdge: atLiveEdge() }).toEqual({
    state: "reading",
    atLiveEdge: false,
  });
}

function offCentre(): number {
  const box = document.querySelector("mark.find-hit-current")?.getBoundingClientRect();
  if (box === undefined) {
    return Number.POSITIVE_INFINITY;
  }
  return Math.abs(
    (box.top + box.bottom) / 2 - scrollerEl.getBoundingClientRect().top - VIEWPORT_PX / 2,
  );
}

/** Centred, then still centred and still Reading after `holdMs`, so a correction that lands late is
 *  seen. `block: "center"` within a quarter of the scrollport: the reveal's own move is ~300px here. */
async function staysCentred(holdMs: number): Promise<void> {
  await vi.waitFor(
    () => {
      expect(offCentre()).toBeLessThan(VIEWPORT_PX / 4);
    },
    { timeout: 5000 },
  );
  await new Promise((resolve) => {
    setTimeout(resolve, holdMs);
  });
  expect({ centred: offCentre() < VIEWPORT_PX / 4, state: readingState() }).toEqual({
    centred: true,
    state: "reading",
  });
}

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  const outer = document.getElementById("messages-wrap-outer");
  if (outer !== null) {
    outer.style.height = `${String(VIEWPORT_PX)}px`;
  }
});

afterAll(() => {
  style.remove();
});

beforeEach(() => {
  teardownAll();
  mountChatView();
  localStorage.clear();
  resetFoldState();
  setSessions([]);
  setActive("");
});

describe("find-in-chat's reveal under reduced motion", () => {
  // Every scroll instant, so the reveal's write lands inside the frame callback that issues it.
  beforeAll(async () => {
    await emulateA11yMedia({ reducedMotion: "reduce" });
  });

  afterAll(async () => {
    await resetA11yMedia();
  });

  it("leaves the hit centred while the rows around it take their real height", async () => {
    try {
      await findFromLiveEdge("c-reveal-reduced");
      await staysCentred(300);
    } finally {
      toggleChatFind();
    }
  });

  it("parks the reader where the step jumped when find closes before the reveal", async () => {
    await loadAtLiveEdge("c-close-reduced", true);
    await findAndStep(NEEDLE, closeAfterFirstJump);
    await staysParkedThroughStream("c-close-reduced");
  });

  it("keeps the current hit when the transcript streams while the reader is away from it", async () => {
    try {
      await loadAtLiveEdge("c-away-reduced", true, true);
      await findAndStep(NEEDLE, undefined, true);
      await vi.waitFor(() => {
        expect(currentHitEntry()).toBe(`big-e${String(HIT_AT)}`);
      });
      readerScrollsTo(entryEl(ABOVE_AT), -1);
      await vi.waitFor(() => {
        expect(hitParagraph().checkVisibility({ contentVisibilityAuto: true })).toBe(false);
      });
      streamChunk("c-away-reduced");
      // The re-run has walked once the match the reader is at carries a mark.
      await vi.waitFor(() => {
        expect(entryEl(ABOVE_AT).querySelector("mark.find-hit")).not.toBeNull();
      });
      readerScrollsTo(hitParagraph(), 1);
      await twoFrames();
      expect(currentHitEntry()).toBe(`big-e${String(HIT_AT)}`);
    } finally {
      toggleChatFind();
    }
  });

  it("makes the next hit current once the reader folds the turn holding the current hit", async () => {
    const chat = "c-fold-reduced";
    const turns = new Map<string, TurnState>(
      [
        toolTurn("f1", 1, `one ${FOLD_NEEDLE}`),
        toolTurn("f2", 2, `a ${FOLD_NEEDLE} and a second ${FOLD_NEEDLE}`),
        toolTurn("f3", 3, `a ${FOLD_NEEDLE} first, then a ${FOLD_NEEDLE} again`, true),
      ].map((entries) => [entries[0]?.turn ?? "", { entries, openEntries: new Map() }]),
    );
    try {
      setSessions([
        {
          ...makeSession({ id: chat, name: chat }),
          turns,
          turn_order: ["f1", "f2", "f3"],
          turn_count: 3,
        },
      ]);
      setActive(chat);
      bumpMessages(chat, "load");
      // Older turns load folded; the reader opens both.
      await vi.waitFor(() => {
        foldToggle("f2");
      });
      foldToggle("f1").click();
      foldToggle("f2").click();
      await vi.waitFor(() => {
        expect(entryById("f2-e1").checkVisibility({ contentVisibilityAuto: true })).toBe(true);
        expect(entryById("f1-e1").checkVisibility({ contentVisibilityAuto: true })).toBe(true);
      });
      stageServerHits([]);
      handleFindHotkey(new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true }));
      const input = document.getElementById("chat-find-input") as HTMLInputElement;
      input.value = FOLD_NEEDLE;
      for (const counter of ["1 of 5", "2 of 5", "3 of 5"]) {
        input.dispatchEvent(
          new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
        );
        await vi.waitFor(() => {
          expect(countText()).toBe(counter);
        });
      }
      expect(currentHitEntry()).toBe("f2-e1");

      foldToggle("f2").click();
      // `content-visibility` flips to hidden only at the end of the fold's transition delay, with no mutation, so the
      // walk that drops the hit is the next streamed chunk's.
      await vi.waitFor(() => {
        expect(entryById("f2-e1").checkVisibility({ contentVisibilityAuto: true })).toBe(false);
      });
      streamChunk(chat, "f3");
      await vi.waitFor(() => {
        expect(countText()).toBe("2 of 3");
      });
      expect(messagesEl.querySelector("mark.find-hit-current")).toBe(
        entryById("f3-e1").querySelector("mark.find-hit"),
      );
    } finally {
      toggleChatFind();
    }
  });

  it("keeps the current hit in a running command's output when its next frame lands", async () => {
    const chat = "c-command-reduced";
    const output = ["one", "two", "three"].map((n) => `${COMMAND_NEEDLE} ${n}`).join("\n");
    const entries = [
      inTurn("cmd", 0, "turn_open", {
        prompt: { id: "cmd-p", text: "go" },
        source: "prompt",
        n: 1,
      }),
      inTurn("cmd", 1, "text", { text: "running the build" }),
      inTurn("cmd", 2, "tool_call", {
        id: "cmd-tc",
        title: "Run make",
        kind: "execute",
        status: "in_progress",
        ts: 1,
        output,
      }),
    ];
    try {
      setSessions([
        {
          ...makeSession({ id: chat, name: chat }),
          turns: new Map([["cmd", { entries, openEntries: new Map() }]]),
          turn_order: ["cmd"],
          turn_count: 1,
        },
      ]);
      setActive(chat);
      bumpMessages(chat, "load");
      await vi.waitFor(() => {
        entryById("cmd-e2");
      });
      const card = entryById("cmd-e2");
      // A running card's output region is collapsed, so walked only once the reader opens it.
      card.querySelector<HTMLElement>(".tool-disclosure")?.click();
      await vi.waitFor(() => {
        expect(card.querySelector(".tool-output pre")?.checkVisibility()).toBe(true);
      });
      stageServerHits([]);
      handleFindHotkey(new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true }));
      const input = document.getElementById("chat-find-input") as HTMLInputElement;
      input.value = COMMAND_NEEDLE;
      for (const counter of ["1 of 3", "2 of 3", "3 of 3"]) {
        input.dispatchEvent(
          new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
        );
        await vi.waitFor(() => {
          expect(countText()).toBe(counter);
        });
      }

      applyToolProgress(chat, "cmd", {
        turn: "cmd",
        tool_call_id: "cmd-tc",
        output_delta: "\nstill building",
      });
      const pre = card.querySelector(".tool-output pre");
      await vi.waitFor(() => {
        expect(pre?.textContent).toContain("still building");
        expect(pre?.querySelectorAll("mark.find-hit")).toHaveLength(3);
      });
      expect({
        counter: countText(),
        current: [...(pre?.querySelectorAll("mark.find-hit") ?? [])].findIndex((m) =>
          m.classList.contains("find-hit-current"),
        ),
      }).toEqual({ counter: "3 of 3", current: 2 });
    } finally {
      toggleChatFind();
    }
  });
});

describe("find-in-chat's reveal with motion", () => {
  it("flies up from the live edge to the hit and stays there", async () => {
    try {
      await findFromLiveEdge("c-reveal-motion");
      await staysCentred(500);
    } finally {
      toggleChatFind();
    }
  });

  it("parks the reader where the step's flight lands when find closes before the reveal", async () => {
    await loadAtLiveEdge("c-close-motion", true);
    await findAndStep(NEEDLE, closeAfterFirstJump);
    await staysParkedThroughStream("c-close-motion");
  });

  it("keeps the current-hit highlight when the transcript streams during the reveal's flight", async () => {
    // One chunk as the highlight is set, which is the moment the reveal's flight is issued.
    const streamOnHighlight = new MutationObserver(() => {
      if (messagesEl.querySelector("mark.find-hit-current") !== null) {
        streamOnHighlight.disconnect();
        streamChunk("c-stream-motion");
      }
    });
    try {
      await loadAtLiveEdge("c-stream-motion", true);
      streamOnHighlight.observe(messagesEl, { subtree: true, attributes: true, childList: true });
      await findAndStep(NEEDLE);
      await staysCentred(500);
      expect(countText()).toBe("1 of 1");
    } finally {
      streamOnHighlight.disconnect();
      toggleChatFind();
    }
  });

  it("keeps the current-hit highlight when the reader moves during the reveal's flight while the transcript streams", async () => {
    const streamOnHighlight = new MutationObserver(() => {
      if (messagesEl.querySelector("mark.find-hit-current") !== null) {
        streamOnHighlight.disconnect();
        streamChunk("c-input-motion");
        setTimeout(() => {
          scrollerEl.dispatchEvent(new WheelEvent("wheel", { deltaY: -1, bubbles: true }));
        }, 250);
      }
    });
    try {
      await loadAtLiveEdge("c-input-motion", true);
      streamOnHighlight.observe(messagesEl, { subtree: true, attributes: true, childList: true });
      await findAndStep(NEEDLE);
      // Past the flight, wherever the input left it.
      await sleep(1200);
      readerScrollsTo(hitParagraph(), -1);
      await twoFrames();
      expect(currentHitEntry()).toBe(`big-e${String(HIT_AT)}`);
    } finally {
      streamOnHighlight.disconnect();
      toggleChatFind();
    }
  });

  it("marks a match added beside the hit during the reveal's flight once the flight lands", async () => {
    // Beside the hit, because the walker prunes skipped rows: a chunk streamed at the live edge is skipped, so not
    // walkable, while the reader is at this hit.
    const addOnHighlight = new MutationObserver(() => {
      const current = messagesEl.querySelector("mark.find-hit-current");
      if (current !== null) {
        addOnHighlight.disconnect();
        const added = document.createElement("p");
        added.textContent = `a second ${NEEDLE} beside the first`;
        current.parentElement?.after(added);
      }
    });
    try {
      await loadAtLiveEdge("c-add-motion", true);
      addOnHighlight.observe(messagesEl, { subtree: true, attributes: true, childList: true });
      await findAndStep(NEEDLE);
      await staysCentred(500);
      await vi.waitFor(() => {
        expect(
          [...messagesEl.querySelectorAll("mark.find-hit")].map((m) => ({
            hit: m.getAttribute("data-hit"),
            current: m.classList.contains("find-hit-current"),
          })),
        ).toEqual([
          { hit: "0", current: true },
          { hit: "1", current: false },
        ]);
      });
    } finally {
      addOnHighlight.disconnect();
      toggleChatFind();
    }
  });
});
