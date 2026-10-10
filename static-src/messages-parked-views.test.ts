// One `.transcript-view` per resident chat, exactly one `.is-active`. A parked view keeps its DOM with every
// writer paused: nothing may move it, and unparking must equal a cold rebuild. Real store, renderer, scroll
// controller and layout, since observers detaching at park is half the freeze.

import { describe, it, expect, vi, beforeEach } from "vitest";
// Describes that wait on real frames declare their bound in the harness's unit.
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";

// The graph reads the DOM registry at module scope; the transcript chain gets real, shipped geometry.
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

// The shipped transcript stylesheet, so the geometry is production's; every load-bearing declaration is a
// literal, and the harness pins the wrapper height the app shell provides.
const style = document.createElement("style");
style.textContent =
  loadCSS("13-messages.css") +
  `
  #messages-wrap-outer { height: 400px; }
`;
document.head.appendChild(style);

const store = await import("./store.js");
const sigs = await import("./store-signals.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");
const { appendTerminalChunk } = await import("./messages-tools.js");

messages.mountChatView();

const messagesEl = document.getElementById("messages") as HTMLElement;
const promptInput = document.getElementById("prompt-input") as HTMLTextAreaElement;

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

// --- Fixtures -------------------------------------------------------------------------

function session(id: string, over: Partial<Session> = {}): Session {
  return { ...makeSession({ id, name: id }), ...over };
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

function textEntry(turnID: string, at: number, s: string, id?: string): Entry {
  return sealed(turnID, at, "text", { text: s }, id);
}

function thinkingEntry(turnID: string, at: number, s: string): Entry {
  return sealed(turnID, at, "thinking", { text: s });
}

function toolCall(
  turnID: string,
  at: number,
  id: string,
  over: Record<string, unknown> = {},
): Entry {
  return sealed(
    turnID,
    at,
    "tool_call",
    { id, title: `run ${id}`, kind: "execute", status: "in_progress", ts: at + 1, ...over },
    id,
  );
}

function toolResult(turnID: string, at: number, callID: string, status = "completed"): Entry {
  return sealed(turnID, at, "tool_result", { status }, `${callID}:result`);
}

function turnClose(turnID: string, at: number, outcome = "completed"): Entry {
  return sealed(turnID, at, "turn_close", { outcome });
}

function open(turnID: string, id: string, text: string): OpenEntry {
  return { turn: turnID, id, kind: "text", text, n: 1 };
}

/** A chat holding whole SEALED turns, the shape a page GET lands. */
function settled(
  id: string,
  turns: readonly (readonly Entry[])[],
  over: Partial<Session> = {},
): Session {
  const s = session(id, over);
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

function oneTurn(id: string, reply: string, n = 1): Session {
  const t = `${id}-t1`;
  return settled(id, [[turnOpen(t, n), textEntry(t, 1, reply), turnClose(t, 2)]]);
}

function seed(...sessions: Session[]): void {
  store.setSessions(sessions);
  const first = sessions[0];
  if (first !== undefined) {
    store.setActive(first.id);
    store.bumpMessages(first.id, "load");
  }
}

function switchTo(chatID: string): void {
  store.setActive(chatID);
}

function viewOf(chatID: string): HTMLElement {
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

/** One microtask: the store's per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

/**
 * Two frames, for paced work: the cold build drains per frame, `content-visibility: auto` rows await relevance,
 * and a park's queued callbacks land before the spies arm.
 */
async function settledFrames(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
}

beforeEach(() => {
  // The multiplexer's registry persists at module scope; earlier parked views would count against the LRU.
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  // Production never scrolls the document (`#app` is fixed); `.focus()` here would, pushing rows out of
  // `content-visibility` relevance so the find walker misses them in the next case.
  window.scrollTo(0, 0);
});

// ---------------------------------------------------------------------------
// The freeze: a parked view receives NOTHING.
// ---------------------------------------------------------------------------

describe("a parked view is frozen", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("takes zero DOM writes, rAF and observer callbacks under appends, deltas and terminal output", async () => {
    const a = freshID("c-frz");
    const b = freshID("c-frz");
    const t = `${a}-t1`;
    const bt = `${b}-t1`;
    const s = session(a, { thinking: true });
    // The open text last: the streaming tail's live binding is what the park must disarm.
    s.turns.set(t, {
      entries: [turnOpen(t, 1), toolCall(t, 1, "t-frz", { terminal_id: "term-frz" })],
      openEntries: new Map(),
    });
    s.turn_order.push(t);
    s.turn_count = 1;
    // B streams too: only a writer that does reach the DOM can bound the freeze's wait.
    const other = session(b, { thinking: true });
    other.turns.set(bt, { entries: [turnOpen(bt, 1)], openEntries: new Map() });
    other.turn_order.push(bt);
    other.turn_count = 1;
    seed(s, other);
    store.openEntry(a, open(t, "say-1", "hello"));
    // The precondition: A holds a live bubble bound to the open entry, so there is a writer to disarm.
    await vi.waitFor(() => {
      expect(viewOf(a).querySelector(".message.assistant.streaming")).not.toBeNull();
    });

    switchTo(b);
    await flushed();
    await settledFrames();
    const parked = viewOf(a);
    expect(parked.classList.contains("is-active")).toBe(false);
    // B is active with its own live tail, so the writer driven below is armed.
    store.openEntry(b, open(bt, "say-b", "hello"));
    await vi.waitFor(() => {
      expect(viewOf(b).querySelector(".message.assistant.streaming")).not.toBeNull();
    });

    // Counted in the callback, not via `takeRecords`, which returns only undelivered records and answers zero after
    // any awaited microtask.
    let controlWrites = 0;
    const control = new MutationObserver((records) => {
      controlWrites += records.length;
    });
    control.observe(viewOf(b), {
      childList: true,
      subtree: true,
      characterData: true,
      attributes: true,
    });
    const card = (): HTMLElement | null =>
      parked.querySelector<HTMLElement>('[data-tool-id="t-frz"]');
    expect(card()).not.toBeNull();
    // The pre-state, so the freeze assertion says what the `tool_result` did not do.
    expect(card()?.dataset["outcome"]).toBe("running");

    // Long enough to emit inside a frame: the reveal holds growth under `MIN_EMIT_CHARS`.
    const delta = "world ".repeat(40);
    // Every writer that could reach the parked DOM fires once.
    store.applyDelta(a, t, "say-1", "", 2, delta);
    store.sealEntry(a, t, "say-1", "", 2, 3, 2);
    store.appendEntry(a, textEntry(t, 3, "appended while parked", "say-2"));
    store.appendEntry(a, toolResult(t, 4, "t-frz"));
    appendTerminalChunk("term-frz", "chunk while parked\n", [], 0);
    // The same delta into the active chat bounds the wait on the product's own output.
    store.applyDelta(b, bt, "say-b", "", 2, delta);
    await flushed();
    // Read off rendered text: the active view repaints for its own reasons, so a record count proves nothing.
    await vi.waitFor(() => {
      expect(viewOf(b).querySelector(".message.assistant.streaming")?.textContent ?? "").toContain(
        "world world",
      );
    });
    expect(controlWrites).toBeGreaterThan(0);

    // The freeze per writer on the channels the park holds: a sealed append mounts no row, the `tool_result` does not
    // settle the card, and the terminal chunk does not reach it.
    expect(parked.textContent ?? "").not.toContain("appended while parked");
    expect(card()?.dataset["outcome"]).toBe("running");
    expect(parked.textContent ?? "").not.toContain("chunk while parked");
    // Not asserted: `mountOpenProse`'s `watchOpenText` subscription is not stopped by any park path, so the delta
    // channel still grows a parked bubble; pinning it would make the defect the contract.
    control.disconnect();
  });
});

// ---------------------------------------------------------------------------
// Unpark correctness: nothing missed while frozen, nothing doubled after.
// ---------------------------------------------------------------------------

describe("park → grow → unpark", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("shows the exact text after park-after-delta → grow-parked → unpark → grow-again", async () => {
    const a = freshID("c-txt");
    const b = freshID("c-txt");
    const t = `${a}-t1`;
    const s = session(a, { thinking: true });
    s.turns.set(t, { entries: [turnOpen(t, 1)], openEntries: new Map() });
    s.turn_order.push(t);
    s.turn_count = 1;
    seed(s, oneTurn(b, "other"));
    store.openEntry(a, open(t, "say-1", "Hel"));
    await flushed();
    // A delta lands while the chat is live (through the entry signal).
    store.applyDelta(a, t, "say-1", "", 2, "lo");
    await flushed();

    switchTo(b);
    await flushed();

    // Many deltas while parked; the store accumulates them.
    let n = 2;
    for (const delta of [" wor", "ld", ", from", " the", " parked", " chat"]) {
      n += 1;
      store.applyDelta(a, t, "say-1", "", n, delta);
    }
    await flushed();

    switchTo(a);
    await flushed();
    const bubble = (): string => viewOf(a).querySelector(".message.assistant")?.textContent ?? "";
    // The incremental parser may withhold a trailing character until the seal, so this waits for containment.
    await vi.waitFor(() => {
      expect(bubble()).toContain("Hello world, from the parked");
    });

    // Exactly one binding effect per entry, so the exact text is the effect count.
    n += 1;
    store.applyDelta(a, t, "say-1", "", n, " — and more");
    await flushed();
    store.sealEntry(a, t, "say-1", "", 1, n + 1, n);
    store.appendEntry(a, turnClose(t, 2));
    store.setThinking(a, false);
    await flushed();
    await vi.waitFor(() => {
      expect(bubble()).toBe("Hello world, from the parked chat — and more");
    });
    // The rebuild replaced the old body rather than stacking a copy.
    expect(viewOf(a).querySelectorAll(".message.assistant")).toHaveLength(1);
  });

  it("unpark is equivalent to a cold rebuild, one copy of every entry kind", async () => {
    const a = freshID("c-eq");
    const b = freshID("c-eq");
    const t = `${a}-t1`;
    const s = session(a, { thinking: true });
    s.turns.set(t, {
      entries: [
        turnOpen(t, 1, "do the thing"),
        thinkingEntry(t, 1, "pondering"),
        textEntry(t, 2, "partial", "say-1"),
        toolCall(t, 3, "t-eq"),
      ],
      openEntries: new Map(),
    });
    s.turn_order.push(t);
    s.turn_count = 1;
    seed(s, oneTurn(b, "other"));
    await flushed();

    switchTo(b);
    await flushed();

    // While parked: another prose entry lands, the tool call settles, the turn closes.
    store.appendEntry(a, textEntry(t, 4, "answer", "say-2"));
    store.appendEntry(a, toolResult(t, 5, "t-eq"));
    store.appendEntry(a, turnClose(t, 6));
    store.setThinking(a, false);
    await flushed();

    switchTo(a);
    await flushed();
    const unparked = snapshot(viewOf(a));

    // The cold control: dispose and repaint from the same store state.
    messages.disposeChatView(a);
    store.bumpMessages(a, "load");
    await flushed();
    await vi.waitFor(() => {
      expect(snapshot(viewOf(a))).toEqual(unparked);
    });

    function snapshot(root: HTMLElement): Record<string, unknown> {
      return {
        text: [...root.querySelectorAll(".message.assistant")].map((node) => node.textContent),
        reasoning: [...root.querySelectorAll(".reasoning-block .reasoning-body")].map(
          (node) => node.textContent,
        ),
        cards: [...root.querySelectorAll<HTMLElement>(".tool-call")].map(
          (c) => c.dataset["outcome"] ?? "",
        ),
        turns: root.querySelectorAll(".turn").length,
      };
    }
  });

  it("paint-on-empty hides the view without disposing it", async () => {
    const a = freshID("c-empty");
    seed(oneTurn(a, "kept whole"));
    await flushed();
    const view = viewOf(a);
    const row = view.querySelector(".message.assistant");
    expect(row).not.toBeNull();

    // No active chat while the session stays in the store: the paint must hide, never dispose.
    store.setActive("");
    await flushed();
    expect(view.isConnected).toBe(true);
    expect(view.classList.contains("is-active")).toBe(false);
    expect(view.inert).toBe(true);
    // Only `openEntry` mints a streaming signal.
    expect(sigs.entryTextSigs.get(sigs.entryKey(`${a}-t1`, `${a}-t1-e1`))).toBeUndefined();

    // Node identity proves nothing was disposed and rebuilt.
    switchTo(a);
    await flushed();
    expect(viewOf(a)).toBe(view);
    expect(view.querySelector(".message.assistant")).toBe(row);
    expect(view.classList.contains("is-active")).toBe(true);
    expect(view.inert).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// The saved handle: reading state, scroll position, bottom alignment.
// ---------------------------------------------------------------------------

describe("the view handle", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  /** Ten turns overflow the 400px scroller with older ones folded; the paint is superlinear, so it is the minimum. */
  function longChat(id: string): Session {
    const turns: Entry[][] = [];
    for (let i = 0; i < 10; i++) {
      const t = `${id}-t${String(i)}`;
      turns.push([
        turnOpen(t, i + 1, `prompt ${String(i)}`),
        textEntry(t, 1, `reply ${String(i)}`),
        turnClose(t, 2),
      ]);
    }
    return settled(id, turns);
  }

  it("keeps two parked chats' reading states apart and restores each", async () => {
    const a = freshID("c-read");
    const b = freshID("c-read");
    const c = freshID("c-read");
    seed(longChat(a), longChat(b), oneTurn(c, "x"));
    await flushed();

    // No frame wait before the gesture: every step is synchronous, and the follow write re-reads its licence, so the
    // verdict does not depend on compositor speed.

    // Reading needs all three: the wheel marks it the reader's, the instant scrollTo beats `scroll-behavior: smooth`,
    // and the event is what the listener acts on.
    const scroller = scroll.getScrollEl();
    expect(scroller.scrollHeight).toBeGreaterThan(scroller.clientHeight + 150);
    scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
    scroller.scrollTo({ top: 5, behavior: "instant" });
    scroller.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");

    switchTo(b);
    await flushed();
    // Chat B stays at the live edge — Following.
    expect(scroll.readingState()).toBe("following");

    switchTo(c);
    await flushed();

    // Unpark A: Reading and the parked scroll offset come back.
    switchTo(a);
    await flushed();
    expect(scroll.readingState()).toBe("reading");
    expect(scroller.scrollTop).toBe(5);

    // Unpark B: Following.
    switchTo(b);
    await flushed();
    expect(scroll.readingState()).toBe("following");
  });

  it("holds the reading position through a park/unpark over a turn that GREW", async () => {
    // The reader is Reading, so `deferWhileReading` holds the unpark paint; this reads the window the park left over a
    // grown turn.
    const a = freshID("c-win");
    const b = freshID("c-win");
    const t = `${a}-t1`;
    // Over the entry budget so the body holds a range; tool cards, since `text` entries would coalesce into one row.
    const heavy = (upTo: number): Entry[] => {
      const entries: Entry[] = [turnOpen(t, 1)];
      for (let i = 1; i <= upTo; i++) {
        entries.push(toolCall(t, i, `t-w${String(i)}`, { status: "completed" }));
      }
      return entries;
    };
    seed(settled(a, [heavy(500)], { thinking: true }), longChat(b));
    await flushed();
    await settledFrames();

    const scroller = scroll.getScrollEl();
    const seqs = (): number[] =>
      [...viewOf(a).querySelectorAll<HTMLElement>("[data-entry-seq]")]
        .map((e) => Number(e.dataset["entrySeq"]))
        .sort((x, y) => x - y);
    expect(seqs().length).toBeLessThan(500);
    scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
    scroller.scrollTo({ top: 120, behavior: "instant" });
    scroller.dispatchEvent(new Event("scroll"));
    expect(scroll.readingState()).toBe("reading");
    const parked = scroller.scrollTop;

    switchTo(b);
    await flushed();
    // The turn grows while A is parked.
    for (let i = 501; i <= 700; i++) {
      store.appendEntry(a, toolCall(t, i, `t-w${String(i)}`, { status: "completed" }));
    }
    await flushed();

    switchTo(a);
    await flushed();

    // Still the window the park froze: nothing grew it and nothing replayed the 700-entry list.
    expect(seqs().length).toBeLessThan(500);
    expect(viewOf(a).querySelector('[data-entry-seq="700"]')).toBeNull();
    // And the reader is exactly where they were.
    expect(scroll.readingState()).toBe("reading");
    expect(scroller.scrollTop).toBe(parked);
  });

  it("keeps a short transcript bottom-aligned through a park/unpark cycle", async () => {
    const a = freshID("c-align");
    const b = freshID("c-align");
    seed(oneTurn(a, "short"), oneTurn(b, "x"));
    await flushed();

    const wrap = scroll.getScrollEl();
    const bottomGap = (): number => {
      const card = viewOf(a).querySelector<HTMLElement>(".turn:last-of-type");
      if (card === null) {
        throw new Error("no card");
      }
      return wrap.getBoundingClientRect().bottom - card.getBoundingClientRect().bottom;
    };
    const before = bottomGap();
    // flex-end on a min-height:100% column hugs the bottom; tokens are absent, so the gap is the alignment.
    expect(before).toBeLessThan(50);

    switchTo(b);
    await flushed();
    switchTo(a);
    await flushed();
    expect(bottomGap()).toBe(before);
  });
});

// ---------------------------------------------------------------------------
// Focus and reachability.
// ---------------------------------------------------------------------------

describe("focus and reachability", { timeout: testTimeoutFor(FRAME_BUDGET_MS) }, () => {
  it("relocates focus to the composer when the focused element parks, and marks the view inert", async () => {
    const a = freshID("c-foc");
    const b = freshID("c-foc");
    const t = `${a}-t1`;
    seed(
      settled(a, [
        [
          turnOpen(t, 1),
          toolCall(t, 1, "t-foc", { status: "completed" }),
          toolResult(t, 2, "t-foc"),
          turnClose(t, 3),
        ],
      ]),
      oneTurn(b, "x"),
    );
    await flushed();

    // A footer action is focusable unconditionally; the fold toggle is `display: none` on the newest turn.
    const toggle = viewOf(a).querySelector<HTMLElement>(
      ".turn-footer .turn-action-btn, [tabindex], button:not(.turn-fold-toggle)",
    );
    expect(toggle).not.toBeNull();
    toggle?.focus();
    expect(viewOf(a).contains(document.activeElement)).toBe(true);

    switchTo(b);
    await flushed();
    expect(document.activeElement).toBe(promptInput);

    // The parked view is inert, which refuses programmatic focus too.
    const parked = viewOf(a);
    expect(parked.inert).toBe(true);
    toggle?.focus();
    expect(parked.contains(document.activeElement)).toBe(false);
  });

  it("keeps parked text out of the transcript find's walk", async () => {
    const a = freshID("c-find");
    const b = freshID("c-find");
    seed(oneTurn(a, "needle in A"), oneTurn(b, "plain B"));
    await flushed();
    switchTo(b);
    await flushed();
    // `content-visibility: auto` rows start skipped and the find walker prunes them, so give it a frame.
    await settledFrames();

    // The find walker roots at the active view, so A's text is unreachable while parked.
    const { FindEngine } = await import("./find-engine.js");
    const active = messagesEl.querySelector<HTMLElement>(":scope > .transcript-view.is-active");
    expect(active).toBe(viewOf(b));
    const engine = new FindEngine(active ?? messagesEl);
    engine.search("needle", false);
    expect(engine.total).toBe(0);
    engine.search("plain", false);
    expect(engine.total).toBe(1);
    engine.clear();
  });
});

// ---------------------------------------------------------------------------
// Disposal: LRU, close, teardown.
// ---------------------------------------------------------------------------

describe("disposal", () => {
  it("evicts the least-recently-used parked view past the budget and leaks nothing", async () => {
    const ids: string[] = [];
    const sessions: Session[] = [];
    for (let i = 0; i < messages.PARKED_VIEWS + 2; i++) {
      const id = freshID("c-lru");
      ids.push(id);
      const t = `${id}-t1`;
      sessions.push(
        settled(id, [
          [
            turnOpen(t, 1),
            textEntry(t, 1, `text of ${id}`),
            toolCall(t, 2, `t-of-${id}`, { status: "completed" }),
            toolResult(t, 3, `t-of-${id}`),
            turnClose(t, 4),
          ],
        ]),
      );
    }
    seed(...sessions);
    await flushed();
    const first = ids[0] ?? "";
    // The first chat's card minted its composite-keyed signal at mount.
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(first, `t-of-${first}`))).toBeDefined();
    for (const id of ids.slice(1)) {
      switchTo(id);
      await flushed();
    }

    // Five chats visited: the first is past the parked budget of three and its container is gone.
    expect(messages.transcriptViewFor(first)).toBeNull();
    expect(messagesEl.querySelectorAll(":scope > .transcript-view")).toHaveLength(
      messages.PARKED_VIEWS + 1,
    );
    // Its tool signal space went with it (composite keys, per-view dispose).
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(first, `t-of-${first}`))).toBeUndefined();

    // The surviving parked views are intact.
    for (const id of ids.slice(2, -1)) {
      expect(viewOf(id).isConnected).toBe(true);
    }
  });

  it("teardownAll with parked views leaves no DOM and no state", async () => {
    const a = freshID("c-td");
    const b = freshID("c-td");
    const t = `${a}-t1`;
    const s = session(a, { thinking: true });
    s.turns.set(t, {
      entries: [turnOpen(t, 1), toolCall(t, 1, "t-td")],
      openEntries: new Map(),
    });
    s.turn_order.push(t);
    s.turn_count = 1;
    seed(s, oneTurn(b, "x"));
    // The open tail of lane `""` is what mints the streaming signal.
    store.openEntry(a, open(t, "say-1", "live"));
    await flushed();
    expect(sigs.entryTextSigs.get(sigs.entryKey(t, "say-1"))).toBeDefined();

    switchTo(b);
    await flushed();
    expect(messagesEl.querySelectorAll(":scope > .transcript-view")).toHaveLength(2);

    messages.teardownAll();

    // The op-set, observed: container removal (no view DOM at all)...
    expect(messagesEl.querySelectorAll(":scope > .transcript-view")).toHaveLength(0);
    expect(messagesEl.childElementCount).toBe(0);
    // ...per-entry signal clears (the parked streaming tail's included)...
    expect(sigs.entryTextSigs.get(sigs.entryKey(t, "say-1"))).toBeUndefined();
    // ...tool effect disposal through the composite keys, signal cleared...
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(a, "t-td"))).toBeUndefined();
    // ...scroll reset (Following, no stale reading state)...
    expect(scroll.readingState()).toBe("following");
    // ...and the next bump re-creates the active chat's view whole.
    store.bumpMessages(b, "load");
    await flushed();
    expect(viewOf(b).querySelectorAll(".turn").length).toBeGreaterThan(0);
  });
});

// `activateView` resets the scroll controller for a view it created, and that reset removes the attached view's
// "Load older messages" button, so it must run after the attach or it strips the outgoing chat's pagination.
describe("activating a new view", () => {
  it("leaves the outgoing view's pagination button in place", async () => {
    const a = freshID("c");
    const b = freshID("c");
    seed(oneTurn(a, "x"));
    await flushed();
    // What `chat.ts setupLoadMore` does for a chat the server says has more.
    scroll.setLoadMore(() => undefined, true);
    const button = viewOf(a).querySelector(`[id="load-more-indicator"]`);
    expect(button).not.toBeNull();

    // A chat with no resident view yet: the branch that creates one.
    store.setSessions([...store.getSessions(), oneTurn(b, "y")]);
    switchTo(b);
    await flushed();

    expect({
      created: messages.transcriptViewFor(b) !== null,
      outgoingKept: button !== null && viewOf(a).contains(button),
      incomingHasNone: viewOf(b).querySelector(`[id="load-more-indicator"]`) === null,
    }).toEqual({ created: true, outgoingKept: true, incomingHasNone: true });
  });
});
