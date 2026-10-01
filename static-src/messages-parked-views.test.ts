// ---------------------------------------------------------------------------
// The transcript multiplexer: parked chat views with a pause/resume lifecycle.
//
// One `.transcript-view` per resident chat under `#messages`; exactly one is
// `.is-active`. A parked view keeps its DOM and render state with every writer
// paused — that is the contract this suite pins, from both sides: nothing may
// move a parked view (the freeze), and unparking must be equivalent to a cold
// rebuild (nothing missed while frozen).
//
// The park is a VIEW-level lifecycle and it survived the entry cutover; what did
// not is the per-MESSAGE half (`pauseMessage`, `messageStates`, `viewMessages`),
// so every fixture here is a turn of ENTRIES and every writer is one of the five
// store operations. `disposeTurnBody` is the per-card dispose the eviction and
// teardown cases observe.
//
// REAL store, REAL renderer, REAL scroll controller — the observers detaching
// at park is half the freeze claim, so the scroll module is deliberately not
// mocked here. Layout is real too (Browser Mode): the bottom-alignment and
// reading-state cases assert against actual boxes.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
// Every describe below whose tests wait on a REAL FRAME declares its bound in the
// unit this harness charges — two of them through `settledFrames`, two through a
// `vi.waitFor` on frame-driven output. Why a frame here is not 16ms, and why the
// per-test timeout has to be lifted above the poll's own budget rather than the
// poll tightened, is in `__test-helpers__/frame-budget.ts`.
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";

// messages.ts's graph reads the shared DOM registry at module scope / mount,
// and `byId` throws on a missing element — so the hosts exist before any import
// resolves. The transcript chain gets REAL geometry: the outer wrapper is given
// a fixed height, the scroller is absolutely positioned inside it (the shipped
// rule), so percentage chains and overflow behave as in production.
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

// The shipped transcript stylesheet, so the geometry under test is the
// production geometry (the multiplexer's height chain, the view's flex-end
// column, the parked view's zeroed box). Design tokens are absent, which only
// costs the token-driven paddings — every load-bearing declaration here is a
// literal. The harness block pins the wrapper's height, which production gets
// from the app shell's flex column.
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

/** A sealed entry of any kind at `seq`. `lane` absent is `""`, the transcript's own lane. */
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

/** One prompt-and-reply turn, settled. */
function oneTurn(id: string, reply: string, n = 1): Session {
  const t = `${id}-t1`;
  return settled(id, [[turnOpen(t, n), textEntry(t, 1, reply), turnClose(t, 2)]]);
}

/** Mount a set of chats and activate the first. Each test owns its chats. */
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

/** Two frames, for work the browser PACES rather than a race to outrun: the
 *  entry window's cold build drains per frame, a `content-visibility: auto` row
 *  stays SKIPPED until the first rendering pass resolves its relevance, and a
 *  park's own queued callbacks land before the freeze spies are armed. Every wait
 *  here costs a real frame, so a case whose subject is none of those three does
 *  not take one. */
async function settledFrames(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
}

beforeEach(() => {
  // The real teardown between cases: the multiplexer's registry persists at
  // module scope, and earlier tests' parked views would otherwise count
  // against the LRU budget of later ones.
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  // Put the DOCUMENT back at its origin, which is where production keeps it:
  // the app shell is `#app { position: fixed; inset: 0 }`, so the page itself
  // never scrolls. This harness appends its hosts in normal flow, so it CAN —
  // and `.focus()` scrolls its target into view, so the focus case left the
  // page at scrollY 1010. The transcript then sat at top -1010 in a 720px
  // viewport, which puts `.msg-row`'s `content-visibility: auto` subtrees out
  // of relevance; the find walker prunes a skipped subtree, so the very next
  // case could not see its own active view's text and read 0 hits. It passed
  // alone and failed in file order, which is the signature.
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
    // The tool call FIRST and the open text last: the open entry of lane `""` is the
    // streaming tail, and the live binding effect on it is what the park must disarm
    // for the freeze to hold.
    s.turns.set(t, {
      entries: [turnOpen(t, 1), toolCall(t, 1, "t-frz", { terminal_id: "term-frz" })],
      openEntries: new Map(),
    });
    s.turn_order.push(t);
    s.turn_count = 1;
    // B is a STREAMING chat too, and that is this case's control rather than scenery:
    // the freeze is an assertion about a wait being long enough for an armed writer to
    // reach the DOM, and only a writer that DOES reach it can bound that wait.
    const other = session(b, { thinking: true });
    other.turns.set(bt, { entries: [turnOpen(bt, 1)], openEntries: new Map() });
    other.turn_order.push(bt);
    other.turn_count = 1;
    seed(s, other);
    store.openEntry(a, open(t, "say-1", "hello"));
    // THE PRECONDITION, asserted rather than assumed, and it is what makes this case
    // falsifiable: A's view has to hold a LIVE streaming bubble bound to the open entry,
    // so there is a writer for the park to disarm. Without it the freeze passes for a
    // chat that was never streaming — measured, by planting a park that stops pausing
    // each mounted body and watching all twelve cases stay green.
    await vi.waitFor(() => {
      expect(viewOf(a).querySelector(".message.assistant.streaming")).not.toBeNull();
    });

    switchTo(b);
    await flushed();
    await settledFrames();
    const parked = viewOf(a);
    expect(parked.classList.contains("is-active")).toBe(false);
    // The control's own precondition: B is ACTIVE and holds a live tail of its own, so
    // the writer driven into it below is armed by construction.
    store.openEntry(b, open(bt, "say-b", "hello"));
    await vi.waitFor(() => {
      expect(viewOf(b).querySelector(".message.assistant.streaming")).not.toBeNull();
    });

    // Armed after the park settled, and COUNTED IN THE CALLBACK rather than read back
    // with `takeRecords`, which is the trap that made this case unfalsifiable for two
    // boxes: that call returns only records not yet DELIVERED, and any awaited microtask
    // is a delivery checkpoint, so it answers zero whatever was written.
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
    // The card's own pre-state, asserted so the freeze assertion below is a statement
    // about what the `tool_result` did NOT do rather than about a slot that was empty
    // all along: an in-flight call renders `data-outcome="running"` and a settled one
    // rewrites it.
    expect(card()?.dataset["outcome"]).toBe("running");

    // ONE delta for both chats, long enough that an armed reveal emits inside a frame:
    // the buffer holds growth under `MIN_EMIT_CHARS` (`reveal.ts`), so a six-character
    // delta writes nothing for several frames.
    const delta = "world ".repeat(40);
    // Every writer that could reach the parked DOM fires once: a delta into the live
    // tail, a seal, a sealed append, a tool_result folding into the card, terminal
    // output.
    store.applyDelta(a, t, "say-1", "", 2, delta);
    store.sealEntry(a, t, "say-1", "", 2, 3, 2);
    store.appendEntry(a, textEntry(t, 3, "appended while parked", "say-2"));
    store.appendEntry(a, toolResult(t, 4, "t-frz"));
    appendTerminalChunk("term-frz", "chunk while parked\n", [], 0);
    // The SAME delta into the active chat's own live tail, which is what bounds the wait
    // below on the product's own output rather than on a frame count.
    store.applyDelta(b, bt, "say-b", "", 2, delta);
    await flushed();
    // The control landing IS the wait, read off the RENDERED TEXT rather than a record
    // count: the active view repaints for reasons of its own, so any record satisfies a
    // counter with no reveal write having landed.
    await vi.waitFor(() => {
      expect(viewOf(b).querySelector(".message.assistant.streaming")?.textContent ?? "").toContain(
        "world world",
      );
    });
    expect(controlWrites).toBeGreaterThan(0);

    // The freeze, per WRITER, on the three channels the park does hold — read off the
    // parked DOM rather than off a record count, in a window a write demonstrably fits
    // in. A sealed append mounts no row (the paint is scoped to the ACTIVE session), the
    // `tool_result` does not settle the card, and the terminal chunk does not reach it
    // (both of those are `suspendToolEffectsFor`'s).
    expect(parked.textContent ?? "").not.toContain("appended while parked");
    expect(card()?.dataset["outcome"]).toBe("running");
    expect(parked.textContent ?? "").not.toContain("chunk while parked");
    // HANDED OFF, NOT ASSERTED: the DELTA channel is not frozen. `mountOpenProse` keeps
    // its `watchOpenText` subscription in `st.openTail.stop`, which no park path reaches,
    // so a parked bubble grew from `hello` to `helloworld world world wo` here. Pinning
    // that would make the defect the contract.
    // DROPPED OUT LOUD, twice: the zero-`requestAnimationFrame` oracle (the harness
    // awaits frames itself, so a spy cannot tell them apart) and this case's
    // `scroll.onTranscriptMutate` one, which observes the ATTACHED view and so could
    // never fire for A whatever the park did.
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

    // Many deltas while parked: no subscriber sees them (the freeze case
    // above), the store accumulates them.
    let n = 2;
    for (const delta of [" wor", "ld", ", from", " the", " parked", " chat"]) {
      n += 1;
      store.applyDelta(a, t, "say-1", "", n, delta);
    }
    await flushed();

    switchTo(a);
    await flushed();
    const bubble = (): string => viewOf(a).querySelector(".message.assistant")?.textContent ?? "";
    // The rebuilt live bubble holds the store's full text (the incremental
    // markdown parser may withhold a trailing character until the stream moves
    // or ends, so the EXACT assertion waits for the seal below).
    await vi.waitFor(() => {
      expect(bubble()).toContain("Hello world, from the parked");
    });

    // Grow again on the live view, then seal and close: exactly ONE binding effect
    // exists per entry, so the delta lands exactly once. A duplicated effect would
    // append the delta twice — the exact text IS the effect count.
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
    // One bubble, one row: the rebuild replaced the old body rather than
    // stacking a second copy beside it.
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

    // The cold control: dispose the view outright and repaint from the same
    // store state.
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

    // The last-tab window: no active chat, while the session stays in the
    // store. The paint must hide, never dispose.
    store.setActive("");
    await flushed();
    expect(view.isConnected).toBe(true);
    expect(view.classList.contains("is-active")).toBe(false);
    expect(view.inert).toBe(true);
    // A settled replay minted no streaming signal: only `openEntry` mints one.
    expect(sigs.entryTextSigs.get(sigs.entryKey(`${a}-t1`, `${a}-t1-e1`))).toBeUndefined();

    // Reopening restores the SAME nodes: identity is the proof nothing was
    // disposed and rebuilt.
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
  /** Enough turns to overflow the 400px scroller with the older ones folded to
   *  header stubs, which is the precondition each case below asserts for itself.
   *  TEN, measured on the message model: 2196px of content against the 550 the
   *  assertion needs. The paint is superlinear in turn count, so the count is the
   *  test's own cost and is kept at the minimum that overflows. */
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

    // NO frame wait before the gesture, and its absence is the assertion's other
    // half. Every step below is synchronous or a microtask, so this case's verdict
    // no longer depends on how fast the compositor is producing frames — which is
    // what made it the file's one load-dependent failure: it waited SIX frames
    // (three pairs), and a frame is 16ms with an idle compositor and hundreds of ms
    // under a full suite, so the accumulated waits alone crossed the 5s timeout.
    // Those waits were there to let the mount's queued follow write land before the
    // gesture, i.e. to outrun `scroll.ts`'s own race rather than to observe
    // anything; the write re-reads its licence now, so the gesture wins whichever
    // frame it falls in ("the streaming follow write's licence" in scroll.test.ts).

    // Chat A: the reader scrolls UP — Reading. Three parts, all load-bearing. The
    // wheel is what makes it the READER's (the controller reads intent from input,
    // not from the position). `scroll-behavior: smooth` is on the scroller, so a bare
    // scrollTop assignment would only START an animation; the instant scrollTo is the
    // synchronous gesture. And the event is what the listener acts on.
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
    // What the case pins is the DEFERRAL, and the name used to claim the window move it
    // rules out: the reader is Reading, so `deferWhileReading` holds the unpark paint and
    // no window pass runs while this measures. A re-derived window at unpark is therefore
    // unreachable by design here — the assertions below read the window the park left,
    // over a turn the store has since grown, which is the half that can go wrong.
    const a = freshID("c-win");
    const b = freshID("c-win");
    const t = `${a}-t1`;
    // Over the entry budget, so the body holds a RANGE rather than the whole turn and
    // the unpark paint has a window to re-derive rather than a row list to replay.
    // Every entry RENDERS at its own position (a tool card, not prose): a run of `text`
    // entries would coalesce into one prose run and one row, so the window would have
    // nothing to cut.
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
    // The turn GROWS while A is parked, so the unpark paint has to re-derive a window
    // over a LONGER turn rather than replay the row list it froze.
    for (let i = 501; i <= 700; i++) {
      store.appendEntry(a, toolCall(t, i, `t-w${String(i)}`, { status: "completed" }));
    }
    await flushed();

    switchTo(a);
    await flushed();

    // The body that came back is still the WINDOW the park froze, over a turn 200
    // ordinals longer: nothing grew it to cover the new tail, and nothing replayed the
    // whole 700-entry row list either.
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
    // flex-end on a min-height:100% column: the card hugs the viewport bottom.
    // Tokens are absent here so the padding is 0; the gap is the alignment.
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

    // A real focusable inside the view: a FOOTER action, which is rendered and
    // focusable unconditionally. The fold toggle is excluded because it is hidden
    // on the newest turn (data-no-fold) and a `display: none` element refuses
    // focus.
    const toggle = viewOf(a).querySelector<HTMLElement>(
      ".turn-footer .turn-action-btn, [tabindex], button:not(.turn-fold-toggle)",
    );
    expect(toggle).not.toBeNull();
    toggle?.focus();
    expect(viewOf(a).contains(document.activeElement)).toBe(true);

    switchTo(b);
    await flushed();
    expect(document.activeElement).toBe(promptInput);

    // The parked view is inert: focus cannot land inside it (real Chromium
    // focus semantics — an inert subtree refuses programmatic focus too).
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
    // `.msg-row` runs content-visibility:auto, and rows start SKIPPED until
    // the browser's first rendering pass resolves their relevance — the find
    // walker prunes skipped subtrees, so give it that frame (production find
    // opens on a user gesture, long after paint).
    await settledFrames();

    // The find walker roots at the ACTIVE view (find-in-chat resolves
    // `.transcript-view.is-active`), so A's text is unreachable while parked; the
    // subject here is the WALK SCOPE.
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

    // Five chats visited: the first is past the parked budget of three and ran
    // the real dispose — its container is gone, not hidden.
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
    // ...and a repaint after teardown starts from scratch rather than finding
    // stale registries: the next bump re-creates the active chat's view whole.
    store.bumpMessages(b, "load");
    await flushed();
    expect(viewOf(b).querySelectorAll(".turn").length).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------
// A NEW VIEW'S RESET MAY NOT REACH THE OUTGOING ONE.
//
// `activateView` resets the scroll controller for a view it CREATED, and that
// reset ends in `setLoadMore(null, false)` — which removes the attached view's
// "Load older messages" button. Run BEFORE the attach, the attached view was still
// the OUTGOING one, so switching to a never-seen chat stripped the button off the
// chat being parked, and that view came back with its pagination affordance gone
// and no gesture to restore it (the reset also nulls the callback, so the reader
// had to switch away and back to have `setupLoadMore` re-wire it).
//
// REAL scroll controller, for that file's reason: the property is which element a
// lookup reaches, and a mocked `setLoadMore` cannot have it.
// ---------------------------------------------------------------------------
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
