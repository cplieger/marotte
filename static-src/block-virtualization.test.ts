// Residency in ENTRIES: what a paint mounts inside ONE long turn. Ordinals outside the
// plan's `EntryRange` are ABSENT from the DOM, and the real `scroll.ts` is attached. The
// fixture is `thinking` entries, because `sliceTurn` snaps to whole prose runs and N text
// entries would be ONE row; tool-call and multi-run fixtures appear where a case needs
// the tool budget or prose runs.

import { describe, it, expect, afterAll, beforeAll, beforeEach, vi } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { TurnState } from "./types.js";
import type { Turn } from "./turns.js";
import type { Entry, Hit } from "./wire/types.gen.js";

// Only the server's search ANSWER is stubbed: a hit in an unmounted entry needs a real
// build. Spy-wrapped, since the graph links the module's other exports.
vi.mock("./api-client.js", { spy: true });

// NESTED as static/index.html nests them: `#messages-wrap` is the transcript's
// `offsetParent` AND the scroller, so flat siblings put every coordinate in the wrong space.
for (const id of [
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
  // The transcript find box's trigger and the panel its context guard reads: the
  // search-navigation case drives the real overlay.
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

const { RESIDENT_ENTRIES, OVERSCAN_ENTRIES } = await import("./block-window.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { openEntry, applyDelta, appendEntry } = await import("./store.js");
const { mountChatView, mountTurnBody, activeTranscriptView, teardownAll } =
  await import("./messages.js");
const { scrollToBottom, readingState } = await import("./scroll.js");
const { setPinSettleMs } = await import("./scroll-controller.js");
const { setTurnOpen, resetFoldState } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");
const { projectTurns } = await import("./turns.js");
const { forgetHeights, recordEntryHeight, recordRowHeight, spacerHeight } =
  await import("./block-heights.js");
const { toolCallSigs, toolCallSigKey } = await import("./store-signals.js");
const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { handleFindHotkey, toggleChatFind } = await import("./find-in-chat.js");
const api = await import("./api-client.js");

/** Entries in the fixture turn: comfortably past the entry budget, so a window is the
 *  only way it can mount. */
const HUGE = 700;

/** `messages.ts`' own per-slice cap, restated here because the module keeps it
 *  private and the frame assertion below is about that number. */
const BUILD_BATCH_ENTRIES = 32;

let seq = 0;
function chatID(): string {
  seq++;
  return `c-virt-${String(seq)}`;
}

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    kind,
    seq: at,
    ts: at + 1,
    payload,
  } as Entry;
}

function turnOpen(turnID: string, text = "go"): Entry {
  return sealed(turnID, 0, "turn_open", {
    prompt: { id: `${turnID}-p`, text },
    source: "prompt",
    n: 1,
  });
}

function turnClose(turnID: string, at: number): Entry {
  return sealed(turnID, at, "turn_close", { outcome: "completed" });
}

/**
 * FIXTURE (1): one turn of `count` sealed `thinking` entries, each its own element charged
 * to the entry budget alone, the only shape where the overscan floor is falsifiable.
 */
function hugeTurn(turnID: string, count: number, close = true): Entry[] {
  const out: Entry[] = [turnOpen(turnID)];
  for (let at = 1; at <= count; at++) {
    out.push(sealed(turnID, at, "thinking", { text: `step ${String(at)}` }));
  }
  if (close) {
    out.push(turnClose(turnID, count + 1));
  }
  return out;
}

/**
 * FIXTURE (2): `count` consecutive settled TOOL cards, where the tool budget binds and the
 * window is narrowest. Not the general fixture: `RESIDENT_TOOL_CALLS` binds first.
 */
function toolTurn(turnID: string, count: number): Entry[] {
  const out: Entry[] = [turnOpen(turnID)];
  for (let at = 1; at <= count; at++) {
    out.push(
      sealed(turnID, at, "tool_call", {
        id: `${turnID}-tc${String(at)}`,
        title: "Run Command",
        kind: "execute",
        status: "completed",
        ts: at,
      }),
    );
  }
  out.push(turnClose(turnID, count + 1));
  return out;
}

/**
 * FIXTURE (3): `runs` stretches of `per` sealed `text` entries split by a `thinking`
 * entry: a prose RUN boundary is only reachable this way.
 */
function runsTurn(turnID: string, runs: number, per: number): Entry[] {
  const out: Entry[] = [turnOpen(turnID)];
  let at = 1;
  for (let r = 0; r < runs; r++) {
    if (r > 0) {
      out.push(sealed(turnID, at++, "thinking", { text: `between ${String(r)}` }));
    }
    for (let i = 0; i < per; i++) {
      // Long enough to WRAP, so a departing run's recorded height is worth something.
      out.push(
        sealed(turnID, at++, "text", {
          text: `run ${String(r)} chunk ${String(i)}: ${"the quick brown fox jumps over the lazy dog and keeps going ".repeat(6)}`,
        }),
      );
    }
  }
  out.push(turnClose(turnID, at));
  return out;
}

/**
 * The ordinal the tool-bearing fixtures put their card at: head-ward of the cold window
 * and inside the one a drag reaches, so it mounts, drops and mounts again. Not the turn's
 * head, which the window cannot reach (see `dragUpAWindow`).
 */
const TOOL_AT = 250;

/**
 * `hugeTurn` with a delegate invocation at `TOOL_AT`, whose card arms its own effect: a
 * drop that fails to drain it leaves a signal subscribed to a detached card.
 */
function delegateHeadTurn(turnID: string, count: number): Entry[] {
  const out = hugeTurn(turnID, count);
  out[TOOL_AT] = sealed(turnID, TOOL_AT, "tool_call", {
    id: `${turnID}-inv`,
    title: "Sub-agent: helper",
    kind: "other",
    status: "completed",
    ts: TOOL_AT,
    agent_subtask_id: `${turnID}-sub`,
  });
  return out;
}

/** `hugeTurn` with a tool call carrying OUTPUT at `TOOL_AT`, so that card has something
 *  to reveal and therefore a disclosure control at all. */
function outputHeadTurn(turnID: string, count: number): Entry[] {
  const out = hugeTurn(turnID, count);
  out[TOOL_AT] = sealed(turnID, TOOL_AT, "tool_call", {
    id: `${turnID}-out`,
    title: "Run Command",
    kind: "execute",
    status: "completed",
    ts: TOOL_AT,
    output: "line one\nline two",
  });
  return out;
}

const SENTINEL = "chartreuse";

/**
 * `hugeTurn` with one entry holding a unique needle, so an unmounted hit routes stepping
 * to the server hits.
 */
function sentinelTurn(turnID: string, count: number, at: number): Entry[] {
  const out = hugeTurn(turnID, count);
  out[at] = sealed(turnID, at, "thinking", { text: sentinelText(at) });
  return out;
}

function sentinelText(at: number): string {
  return `the ${SENTINEL} lives at ${String(at)}`;
}

/** A chat holding whole turns, the shape a page GET lands. `live` makes the newest turn
 *  RUNNING, which the streaming cases need and every other case must not have. */
function activate(chat: string, turnEntries: readonly (readonly Entry[])[], live = false): void {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const entries of turnEntries) {
    const first = entries[0];
    if (first === undefined) {
      continue;
    }
    turns.set(first.turn, { entries: [...entries], openEntries: new Map() });
    order.push(first.turn);
  }
  setSessions([
    {
      ...makeSession({ id: chat, name: chat, thinking: live }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(chat);
  bumpMessages(chat, "load");
}

function root(): HTMLElement {
  return activeTranscriptView() ?? document.getElementById("messages")!;
}

function card(turnID: string): HTMLElement {
  for (const child of root().children) {
    if (child.getAttribute(KEY_ATTR) === turnID) {
      return child as HTMLElement;
    }
  }
  throw new Error(`no card for turn ${turnID}`);
}

function bodyOf(turnID: string): HTMLElement {
  const body = card(turnID).querySelector<HTMLElement>(":scope > .turn-body");
  if (body === null) {
    throw new Error(`no body for turn ${turnID}`);
  }
  return body;
}

/** A prose RUN is stamped for its first member alone, which is why the ROW cases read `data-entries`
 *  instead. */
function mountedSeqs(turnID: string): number[] {
  return [...card(turnID).querySelectorAll<HTMLElement>("[data-entry-seq]")]
    .map((e) => Number(e.dataset["entrySeq"]))
    .sort((a, b) => a - b);
}

function seqEl(turnID: string, at: number): HTMLElement | null {
  return card(turnID).querySelector<HTMLElement>(`[data-entry-seq="${String(at)}"]`);
}

function bodyRows(turnID: string): HTMLElement[] {
  return [...bodyOf(turnID).querySelectorAll<HTMLElement>(":scope > .msg-row")];
}

/** The head spacer is the body's first child; the tail is the body's next sibling. */
function spacer(turnID: string, side: "head" | "tail"): HTMLElement | null {
  const host = side === "head" ? ":scope > .turn-body" : ":scope";
  return card(turnID).querySelector<HTMLElement>(`${host} > .turn-space[data-space="${side}"]`);
}

// Search navigation onto an entry not in the DOM: everything runs for real except the
// server's staged ANSWER.

/**
 * The server's search reply, run through the caller's decoder so it is held to the wire
 * shape.
 */
function stageServerHits(hits: Hit[]): void {
  vi.mocked(api.apiGetTyped).mockImplementation(((path: string, decode: (v: unknown) => unknown) =>
    Promise.resolve(
      path.includes("/search?q=")
        ? decode({ matches: hits, scanned: 1, matched: hits.length, truncated: false })
        : null,
    )) as typeof api.apiGetTyped);
}

function hitOn(turnID: string, at: number): Hit {
  const text = sentinelText(at);
  return {
    turn_id: turnID,
    entry_id: `${turnID}-e${String(at)}`,
    excerpt: text,
    role: "assistant",
    segment_kind: "content",
    turn: 1,
    offset: text.indexOf(SENTINEL),
    segment_len: text.length,
  } as unknown as Hit;
}

function countText(): string {
  return document.getElementById("chat-find-count")?.textContent ?? "";
}

/** Open the find box, run the query, then step once — the reader's own gestures,
 *  because `navigateToHit` is reachable only from the stepping path. */
async function findAndStep(query: string): Promise<void> {
  handleFindHotkey(new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true }));
  const input = document.getElementById("chat-find-input") as HTMLInputElement;
  const enter = (): void => {
    input.value = query;
    input.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
    );
  };
  enter();
  // The server answer landed and the counter carries its figure — beside the marks
  // when there are some, as the empty state when the walker has nothing.
  await vi.waitFor(() => {
    expect(countText()).toMatch(/in chat|matched, not shown here/);
  });
  // The counter is painted synchronously by the query callback; the shell's render,
  // which records the navigable hits, lands a few microtask hops later.
  await new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
  enter();
}

beforeEach(() => {
  teardownAll();
  mountChatView();
  localStorage.clear();
  resetFoldState();
  setSessions([]);
  setActive("");
});

describe("a cold load of a 700-entry turn mounts a WINDOW", () => {
  it("bounds the mounted entry count by the budget, far below the turn's own", async () => {
    const id = chatID();
    activate(id, [hugeTurn("big", HUGE)]);
    await vi.waitFor(() => {
      expect(mountedSeqs("big").length).toBeGreaterThan(RESIDENT_ENTRIES / 2);
    });
    const mounted = mountedSeqs("big");
    expect(mounted.length).toBeLessThanOrEqual(RESIDENT_ENTRIES);
    expect(mounted.length).toBeLessThan(HUGE / 2);
  });

  it("leaves the ordinals OUTSIDE the window absent from the DOM, by query", async () => {
    const id = chatID();
    activate(id, [hugeTurn("big", HUGE)]);
    await vi.waitFor(() => {
      expect(mountedSeqs("big").length).toBeGreaterThan(RESIDENT_ENTRIES / 2);
    });
    const mounted = mountedSeqs("big");
    const first = mounted[0] ?? 0;
    // Absence by QUERY, not by visibility: the head of the turn is not in the document
    // at all, so no `content-visibility` rule is doing this work.
    expect(first).toBeGreaterThan(1);
    for (const at of [1, 2, Math.floor(first / 2), first - 1]) {
      expect(seqEl("big", at), `entry ${String(at)}`).toBeNull();
    }
    // And the window is one contiguous run ending at the live edge.
    expect(mounted.at(-1)).toBe(HUGE);
    expect(mounted).toEqual(Array.from({ length: mounted.length }, (_, k) => first + k));
  });

  it("takes at most ONE slice on the paint frame, even where the window is one turn", async () => {
    const id = chatID();
    activate(id, [hugeTurn("big", HUGE)]);
    // The window is 320 ordinals of ONE turn, so a builder cutting only between turns would
    // mount them all in one paint.
    expect(mountedSeqs("big").length).toBeLessThanOrEqual(BUILD_BATCH_ENTRIES);
    // Nothing is lost: the drain finishes the window off the frame.
    await vi.waitFor(() => {
      expect(mountedSeqs("big").length).toBeGreaterThan(BUILD_BATCH_ENTRIES);
    });
  });

  it("stands the unmounted ordinals behind a keyed spacer, so the body keeps its height", async () => {
    const id = chatID();
    activate(id, [hugeTurn("big", HUGE)]);
    await vi.waitFor(() => {
      expect(mountedSeqs("big").length).toBeGreaterThan(RESIDENT_ENTRIES / 2);
    });
    const head = spacer("big", "head");
    expect(head).not.toBeNull();
    // Real geometry: the spacer stands in for ~380 dropped ordinals, so it is priced
    // well past one row rather than being a zero-height placeholder.
    expect(Number.parseFloat(head?.style.blockSize ?? "0")).toBeGreaterThan(100);
  });

  it("settles a demand build whose grant reaches BELOW the mounted window", async () => {
    const id = chatID();
    activate(id, [hugeTurn("big", HUGE)]);
    await vi.waitFor(() => {
      expect(mountedSeqs("big").length).toBeGreaterThan(RESIDENT_ENTRIES / 2);
    });
    // A rail jump on a bodied turn with no ordinal asks for the head, which no slice can mount
    // yet: the build must give up rather than spin and starve the timer queue.
    const settled = await Promise.race([
      mountTurnBody(id, "big").then(() => "settled"),
      new Promise((resolve) => {
        setTimeout(() => resolve("spinning"), 500);
      }),
    ]);
    expect(settled).toBe("settled");
  });

  it("navigates a search hit onto an entry that is not in the DOM, and marks it", async () => {
    const id = chatID();
    activate(id, [sentinelTurn("big", HUGE, 200)]);
    await vi.waitFor(() => {
      expect(mountedSeqs("big").length).toBeGreaterThan(RESIDENT_ENTRIES / 2);
    });
    stageServerHits([hitOn("big", 200)]);
    // The premise: the hit's entry is ABSENT, so the build is awaited.
    expect(seqEl("big", 200)).toBeNull();

    await findAndStep(SENTINEL);

    await vi.waitFor(() => {
      expect(document.querySelector("mark.find-hit-current")).not.toBeNull();
    });
    const current = document.querySelector("mark.find-hit-current");
    // ON the hit's entry, resolved by `[data-entry-id]`.
    expect(current?.closest("[data-entry-id]")?.getAttribute("data-entry-id")).toBe("big-e200");
    // The position in the server's one-row list, with no whole-chat figure beside it:
    // the list holds every occurrence, so there is nothing more to say.
    expect(countText()).toBe("1 of 1");
    toggleChatFind();
  });
});

// Window moves need REAL geometry: the stylesheet, a sized scrollport, the real
// `scroll.ts` listener. The shared mock has none.

describe("scrolling moves the window", () => {
  let style: HTMLStyleElement;

  /** The scrollport's own height. */
  const VIEWPORT_PX = 720;

  beforeAll(() => {
    style = mountAppCSS();
    // Without a height the absolutely-positioned scroller is 0 tall.
    const outer = document.getElementById("messages-wrap-outer");
    if (outer !== null) {
      outer.style.height = `${String(VIEWPORT_PX)}px`;
    }
  });

  afterAll(() => {
    style.remove();
  });

  function scroller(): HTMLElement {
    return document.getElementById("messages-wrap")!;
  }

  function frame(): Promise<void> {
    return new Promise((resolve) => {
      requestAnimationFrame(() => resolve());
    });
  }

  /**
   * Wait until the cold build FINISHED (two polls agree): the window pass refuses to move a
   * filling body.
   */
  async function settled(turnID: string, least = 2 * OVERSCAN_ENTRIES): Promise<void> {
    let last = -1;
    await vi.waitFor(() => {
      const n = mountedSeqs(turnID).length;
      const was = last;
      last = n;
      expect(n).toBeGreaterThan(least);
      expect(n).toBe(was);
    });
  }

  /**
   * Put the reader back on the live edge and wait out the bottom pin's settle window, which
   * would undo a gesture inside it.
   */
  async function atLiveEdge(): Promise<void> {
    const real = setPinSettleMs(20);
    try {
      scrollToBottom();
    } finally {
      setPinSettleMs(real);
    }
    await new Promise((resolve) => {
      setTimeout(resolve, 100);
    });
  }

  /**
   * Drag the scrollbar to `top` and report how far the reader moved. The wheel's DIRECTION
   * matters: Reading is entered from the input's aim. `behavior: "instant"`, because the
   * scroller is `scroll-behavior: smooth`. Measured before any anchoring.
   */
  async function dragTo(top: number): Promise<number> {
    const scrollEl = scroller();
    const was = scrollEl.scrollTop;
    scrollEl.dispatchEvent(new WheelEvent("wheel", { deltaY: top < was ? -1 : 1 }));
    scrollEl.scrollTo({ top: Math.max(0, top), behavior: "instant" });
    const moved = scrollEl.scrollTop - was;
    for (let f = 0; f < 4; f++) {
      await frame();
    }
    // Resolve on a task, where input arrives. Inside a frame callback a following drag's write lands after
    // that frame's scroll events and before its ResizeObserver delivery, so `scroll.ts` holds the reading
    // line against a move whose scroll event it has not seen yet and undoes the drag.
    await new Promise((resolve) => {
      setTimeout(resolve, 0);
    });
    return moved;
  }

  /**
   * Drag to the top, wait for the window to move HEAD-ward, and report the ordinal reached. The anchor prices
   * the head spacer, so scrollTop 0 seeds the window at the turn's first ordinal in one pass.
   * `dragToHead` serves prose runs.
   */
  async function dragUpAWindow(turnID: string): Promise<number> {
    const was = mountedSeqs(turnID)[0] ?? 0;
    await dragTo(0);
    await vi.waitFor(() => {
      expect(mountedSeqs(turnID)[0] ?? was).toBeLessThan(was);
    });
    await windowSettled(turnID);
    return mountedSeqs(turnID)[0] ?? 0;
  }

  /** The prose-RUN fixture's own head walk: few enough boxes that the window reaches the
   *  turn's first run, which the row cases need by key. */
  async function dragToHead(turnID: string): Promise<void> {
    await dragTo(0);
    await vi.waitFor(() => {
      expect(mountedSeqs(turnID)[0]).toBe(1);
    });
  }

  /** Wait until the window stops MOVING (two polls agree); `settled` waits for a cold fill. */
  async function windowSettled(turnID: string): Promise<void> {
    // Seeded with a reading no window can produce, or an EMPTY window satisfies the
    // agreement on the first poll and the helper reports settled having seen nothing.
    let last = "<none>";
    await vi.waitFor(() => {
      const now = mountedSeqs(turnID).join(",");
      const was = last;
      last = now;
      expect(now).not.toBe("");
      expect(now).toBe(was);
    });
  }

  /** The mounted ordinal the viewport top sits at. */
  function anchorSeq(turnID: string): number {
    const top = scroller().scrollTop;
    let at = -1;
    for (const e of card(turnID).querySelectorAll<HTMLElement>("[data-entry-seq]")) {
      if (e.offsetTop <= top) {
        at = Number(e.dataset["entrySeq"]);
      }
    }
    return at;
  }

  /** The top of the mounted region, in the scroller's own space. */
  function mountedTop(turnID: string): number {
    let px = Number.POSITIVE_INFINITY;
    for (const e of card(turnID).querySelectorAll<HTMLElement>("[data-entry-seq]")) {
      px = Math.min(px, e.offsetTop);
    }
    return Number.isFinite(px) ? px : 0;
  }

  /** The bottom of the mounted region, in the scroller's own space. */
  function mountedBottom(turnID: string): number {
    let px = 0;
    for (const e of card(turnID).querySelectorAll<HTMLElement>("[data-entry-seq]")) {
      px = Math.max(px, e.offsetTop + e.offsetHeight);
    }
    return px;
  }

  /**
   * `least` is the mounted-ordinal floor the cold build must clear before scrolling; a
   * parameter because a prose fixture stamps one ordinal per RUN.
   */
  async function coldLoad(
    turnID: string,
    entries: readonly Entry[],
    least = 2 * OVERSCAN_ENTRIES,
  ): Promise<string> {
    const id = chatID();
    forgetHeights([turnID]);
    activate(id, [entries]);
    await settled(turnID, least);
    await atLiveEdge();
    return id;
  }

  it("mounts what the reader scrolls TO and drops what they scrolled away from", async () => {
    await coldLoad("big", hugeTurn("big", HUGE));
    const before = mountedSeqs("big");
    // At the live edge the tail latches immediately and the head takes the whole
    // budget, so the window ends at the newest ordinal and the HEAD is what is absent.
    expect(before[0]).toBeGreaterThan(1);
    expect(before.at(-1)).toBe(HUGE);

    const head = await dragUpAWindow("big");

    expect(head).toBeLessThan(before[0] ?? 0);
    expect(seqEl("big", head)).not.toBeNull();
    const after = mountedSeqs("big");
    // …and the ones the reader scrolled away from are ABSENT, by query: the window
    // MOVED rather than growing.
    expect(after.at(-1)).toBeLessThan(HUGE);
    expect(seqEl("big", HUGE)).toBeNull();
    expect(after.length).toBeLessThanOrEqual(RESIDENT_ENTRIES);
  });

  /** Every mounted ordinal's element and its viewport top, for the identity case. */
  function mountedSnapshot(turnID: string): Map<number, { el: HTMLElement; top: number }> {
    const out = new Map<number, { el: HTMLElement; top: number }>();
    for (const e of card(turnID).querySelectorAll<HTMLElement>("[data-entry-seq]")) {
      out.set(Number(e.dataset["entrySeq"]), { el: e, top: e.getBoundingClientRect().top });
    }
    return out;
  }

  it("keeps every ordinal the window KEPT on the same node, and the reader near it", async () => {
    await coldLoad("big", hugeTurn("big", HUGE));
    await dragUpAWindow("big");

    // The OVERLAP is the subject, which makes it clamp-proof: however far the window ends up,
    // every ordinal present before and after must be the identical node.
    const before = mountedSnapshot("big");
    const wasTop = scroller().scrollTop;
    const to = mountedSeqs("big").at(-1) ?? 0;

    // Down into the tail spacer, which extends the tail and retracts the head.
    await dragTo(mountedBottom("big"));
    await vi.waitFor(() => {
      expect(mountedSeqs("big").at(-1)).toBeGreaterThan(to);
    });
    await windowSettled("big");

    const after = mountedSnapshot("big");
    const kept = [...after.keys()].filter((i) => before.has(i)).sort((a, b) => a - b);
    // Or the identity assertion is vacuous: a move that kept nothing proves nothing
    // about rebuilding. The plan's own overscan floor guarantees this much overlap.
    expect(kept.length).toBeGreaterThanOrEqual(OVERSCAN_ENTRIES);
    for (const i of kept) {
      // Per-element `toBe`, never `toEqual` (which passes for a rebuilt element with the same
      // markup): a kept element keeps its selection and disclosure.
      expect(after.get(i)?.el, `ordinal ${String(i)}`).toBe(before.get(i)?.el);
    }

    // The reader followed their own travel, against the TOTAL `scrollTop` delta. A bound, not
    // zero: the head batch also grows the tail.
    const travelled = scroller().scrollTop - wasTop;
    const mid = kept[Math.floor(kept.length / 2)] ?? 0;
    const drift = (after.get(mid)?.top ?? 0) - (before.get(mid)?.top ?? 0) + travelled;
    expect(Math.abs(drift)).toBeLessThan(0.25 * Math.abs(travelled));
  });

  it("holds a reader parked just past the mounted rows where they are while the window moves", async () => {
    await coldLoad("big", hugeTurn("big", HUGE));
    await dragUpAWindow("big");
    const to = mountedSeqs("big").at(-1) ?? 0;

    // The viewport top on the body's last edge, every row above it: the body is the only box in view.
    const parked = mountedBottom("big");
    await dragTo(parked);
    await vi.waitFor(() => {
      expect(mountedSeqs("big").at(-1)).toBeGreaterThan(to);
    });
    await windowSettled("big");

    // The head retracted above and the tail mounted in place of its spacer at the price it stood for.
    expect(Math.abs(scroller().scrollTop - parked)).toBeLessThanOrEqual(1);
  });

  it("mounts what a reader dropped deep into a spacer sees, with no further gesture", async () => {
    // Long enough that the middle of the head spacer lies more than one window move from the mounted rows.
    await coldLoad("big", hugeTurn("big", 4 * HUGE));
    const head = spacer("big", "head");
    expect(head).not.toBeNull();
    const box = scroller().getBoundingClientRect();
    const r = head?.getBoundingClientRect() ?? box;
    const deep = Math.round(scroller().scrollTop + r.top - box.top + r.height / 2);
    expect(r.height).toBeGreaterThan(8 * VIEWPORT_PX);
    // A scrollbar drag, then stillness.
    await dragTo(deep);
    await windowSettled("big");

    const inView = [...card("big").querySelectorAll<HTMLElement>("[data-entry-seq]")].filter(
      (row) => {
        const rr = row.getBoundingClientRect();
        return rr.bottom > box.top && rr.top < box.bottom;
      },
    );
    expect(inView.length).toBeGreaterThan(0);
    // One window, not everything between where it was and where it went.
    expect(mountedSeqs("big").length).toBeLessThanOrEqual(RESIDENT_ENTRIES);
  });

  it("mounts what a reader dropped deep into the TAIL spacer sees, as one window", async () => {
    await coldLoad("big", hugeTurn("big", 4 * HUGE));
    await dragTo(0);
    await windowSettled("big");
    const tail = spacer("big", "tail");
    expect(tail).not.toBeNull();
    const box = scroller().getBoundingClientRect();
    const r = tail?.getBoundingClientRect() ?? box;
    expect(r.height).toBeGreaterThan(8 * VIEWPORT_PX);
    await dragTo(Math.round(scroller().scrollTop + r.top - box.top + r.height / 2));
    await windowSettled("big");

    const inView = [...card("big").querySelectorAll<HTMLElement>("[data-entry-seq]")].filter(
      (row) => {
        const rr = row.getBoundingClientRect();
        return rr.bottom > box.top && rr.top < box.bottom;
      },
    );
    expect(inView.length).toBeGreaterThan(0);
    expect(mountedSeqs("big").length).toBeLessThanOrEqual(RESIDENT_ENTRIES);
  });

  it("lets a reader's own scroll supersede the turn they opened, however far they go", async () => {
    // Opened by the reader, which pins its first ordinal as a standing request.
    const id = await coldLoad("big", hugeTurn("big", 4 * HUGE));
    setTurnOpen(id, "big", true);
    await mountTurnBody(id, "big", 0);
    await windowSettled("big");
    const tail = spacer("big", "tail");
    expect(tail).not.toBeNull();
    const box = scroller().getBoundingClientRect();
    const r = tail?.getBoundingClientRect() ?? box;
    await dragTo(Math.round(scroller().scrollTop + r.top - box.top + r.height / 2));
    await windowSettled("big");

    const inView = [...card("big").querySelectorAll<HTMLElement>("[data-entry-seq]")].filter(
      (row) => {
        const rr = row.getBoundingClientRect();
        return rr.bottom > box.top && rr.top < box.bottom;
      },
    );
    expect(inView.length).toBeGreaterThan(0);
  });

  it("keeps the reader's position addressable when the tail's window empties", async () => {
    await coldLoad("big", hugeTurn("big", HUGE));
    await dragUpAWindow("big");
    expect(mountedSeqs("big").at(-1)).toBeLessThan(HUGE);

    // The ordinals the drop took still hold their height, so the document is long
    // enough to hold the reader where they are — the resume chip's own promise.
    expect(scroller().scrollHeight - scroller().clientHeight).toBeGreaterThanOrEqual(
      scroller().scrollTop,
    );
    expect(scroller().scrollHeight).toBeGreaterThan(4 * VIEWPORT_PX);
  });

  it("settles one drag into ONE window move, with no oscillation", async () => {
    await coldLoad("big", hugeTurn("big", HUGE));
    await dragUpAWindow("big");

    const first = mountedSeqs("big").join(",");
    const landed = scroller().scrollTop;
    // Eight idle frames: anchoring's own scroll must not re-arm the window, thanks to
    // the re-entrancy latch and the plan-equality exit.
    for (let i = 0; i < 8; i++) {
      await frame();
    }
    expect(mountedSeqs("big").join(",")).toBe(first);
    expect(scroller().scrollTop).toBeCloseTo(landed, -1);
  });

  for (const shape of ["reasoning", "tool_call"] as const) {
    it(`mounts a head extension below the head spacer, ${shape}`, async () => {
      await coldLoad("big", shape === "reasoning" ? hugeTurn("big", HUGE) : toolTurn("big", HUGE));
      // Just inside the head spacer's end: a move that overlaps the mounted rows, so the head grows in place.
      const box = scroller().getBoundingClientRect();
      const end = spacer("big", "head")?.getBoundingClientRect().bottom ?? box.top;
      // Watched while it moves: a row seated above the spacer re-anchors the next pass, which can sort it out.
      const misplaced: string[] = [];
      const body = bodyOf("big");
      const watch = new MutationObserver(() => {
        const first = body.firstElementChild;
        if (spacer("big", "head") !== null && first?.classList.contains("turn-space") !== true) {
          misplaced.push((first as HTMLElement | null)?.dataset["entrySeq"] ?? "?");
        }
      });
      watch.observe(body, { childList: true });
      await dragTo(Math.round(scroller().scrollTop + end - box.top - VIEWPORT_PX / 4));
      await windowSettled("big");
      watch.disconnect();
      expect(misplaced).toEqual([]);
      const head = spacer("big", "head");
      // Or the order is vacuous: the window must still stop short of the turn's first ordinal.
      expect(head).not.toBeNull();
      const above = [...card("big").querySelectorAll<HTMLElement>("[data-entry-seq]")].filter(
        (row) =>
          head !== null &&
          (head.compareDocumentPosition(row) & Node.DOCUMENT_POSITION_PRECEDING) !== 0,
      );
      expect(above.map((row) => row.dataset["entrySeq"])).toEqual([]);
    });
  }

  for (const shape of ["reasoning", "tool_call"] as const) {
    it(`keeps one overscan mounted each side of the anchor, ${shape}`, async () => {
      // TWO fixtures: the reasoning turn, and consecutive tool cards where the tool budget binds.
      await coldLoad("big", shape === "reasoning" ? hugeTurn("big", HUGE) : toolTurn("big", HUGE));
      // To the head and back down past it, which puts the anchor MID-turn.
      await dragUpAWindow("big");
      const to = mountedSeqs("big").at(-1) ?? 0;
      await dragTo(mountedBottom("big"));
      await vi.waitFor(() => {
        expect(mountedSeqs("big").at(-1)).toBeGreaterThan(to);
      });
      await windowSettled("big");
      // INTO the moved region, or `anchorSeq` answers -1 and the floor is unreadable.
      await dragTo(Math.round((mountedTop("big") + mountedBottom("big")) / 2));

      // Anchor and window read TOGETHER until they agree: a clamp after the move can
      // move the window after two polls agreed. A MISSING floor still times out red.
      await vi.waitFor(
        () => {
          const mounted = mountedSeqs("big");
          const at = anchorSeq("big");
          expect(at).toBeGreaterThan(mounted[0] ?? 0);
          expect(at - (mounted[0] ?? 0)).toBeGreaterThanOrEqual(OVERSCAN_ENTRIES);
          expect((mounted.at(-1) ?? 0) - at).toBeGreaterThanOrEqual(OVERSCAN_ENTRIES);
        },
        { timeout: 4000 },
      );
    });
  }

  /**
   * The prose runs a body holds, keyed by first `seq`; a run carries the rest in
   * `data-entries`, so ROW cases read rows.
   */
  function runRows(turnID: string): Map<number, HTMLElement> {
    return new Map(bodyRows(turnID).map((r) => [Number(r.dataset["entrySeq"]), r] as const));
  }

  it("drops a whole prose RUN when the window moves off it, and keeps the rest in order", async () => {
    // 5 runs of 120 text entries: the window covers about two, so a head move takes whole rows
    // OUT. The only row-axis fixture.
    const id = await coldLoad("multi", runsTurn("multi", 5, 120), 2);
    expect(id).not.toBe("");
    const heads = (): number[] => [...runRows("multi").keys()].sort((a, b) => a - b);
    // At the live edge the window is the tail, so the LAST run is mounted and the first
    // is not — the premise, or the drop below has nothing to remove.
    const first = heads();
    expect(first.length).toBeGreaterThanOrEqual(2);
    expect(first.length).toBeLessThan(5);
    const wasLast = first.at(-1) ?? 0;

    await dragToHead("multi");

    // A run LEFT the body, which is the only production caller `recordRowHeight` has.
    expect(runRows("multi").has(wasLast)).toBe(false);
    // The kept runs stay in turn order; the length floor makes "in order" meaningful.
    const order = heads();
    expect(order.length).toBeGreaterThanOrEqual(2);
    expect(order).toEqual([...order].sort((a, b) => a - b));
    // The departed runs still hold their height, gaps included, so the document is long
    // enough to stand the reader where they are.
    expect(scroller().scrollHeight - scroller().clientHeight).toBeGreaterThanOrEqual(
      scroller().scrollTop,
    );
    expect(mountedSeqs("multi").length).toBeLessThanOrEqual(RESIDENT_ENTRIES);
    // A TAIL spacer stands for the entries that left, priced at what they measured.
    const tail = spacer("multi", "tail");
    expect(tail).not.toBeNull();
    expect(Number.parseFloat(tail?.style.blockSize ?? "0")).toBeGreaterThan(VIEWPORT_PX);
  });

  it("keeps every prose RUN the window still wants on the same node", async () => {
    await coldLoad("keep", runsTurn("keep", 5, 120), 2);
    await dragToHead("keep");

    const before = runRows("keep");
    const was = mountedSeqs("keep").join(",");

    // All the way down, the shortest gesture that moves this window; the tail drops the first run.
    await dragTo(scroller().scrollHeight);
    await vi.waitFor(() => {
      expect(mountedSeqs("keep").join(",")).not.toBe(was);
    });

    // A kept run is not re-created: the reconcile matches on the row key.
    const after = runRows("keep");
    const kept = [...after.keys()].filter((k) => before.has(k));
    expect(kept.length).toBeGreaterThan(0);
    for (const head of kept) {
      expect(after.get(head), String(head)).toBe(before.get(head));
    }
  });

  it("holds a navigation pin's grant through the flight the jump itself produces", async () => {
    const id = await coldLoad("big", hugeTurn("big", HUGE));

    // A jump to the far HEAD of the turn, from the live edge. The grant REPLACES the
    // window's slice for a turn the two contest, which is what the reader asked for.
    await mountTurnBody(id, "big", 20);
    bumpMessages(id, "shape");
    await vi.waitFor(() => {
      expect(seqEl("big", 20)).not.toBeNull();
    });

    // THE FLIGHT: ~50 scroll events from a smooth `scrollIntoView`; clearing the pin on any
    // cancels the navigation. Settled drags would test a different claim.
    const from = scroller().scrollTop;
    for (let i = 1; i <= 50; i++) {
      scroller().scrollTo({
        top: Math.max(0, Math.round(from * (1 - i / 50))),
        behavior: "instant",
      });
      expect(seqEl("big", 20), `event ${String(i)}`).not.toBeNull();
    }
    await frame();
    expect(seqEl("big", 20)).not.toBeNull();
  });

  it("drains a dropped entry's own effect, not just its element", async () => {
    const id = await coldLoad("dlg", delegateHeadTurn("dlg", HUGE));
    const key = toolCallSigKey(id, "dlg-inv");
    // Mounted first: the head is outside the cold window, so the reader has to reach
    // it before there is anything to release.
    await dragUpAWindow("dlg");
    expect(seqEl("dlg", TOOL_AT)).not.toBeNull();
    expect(toolCallSigs.get(key)).toBeDefined();

    // Back to the live edge, which retracts the head the reader left.
    await dragTo(scroller().scrollHeight);
    await vi.waitFor(() => {
      expect(seqEl("dlg", TOOL_AT)).toBeNull();
    });
    // The ELEMENT going is half of it. Its effect is subscribed to the store, so a
    // drop that leaves it armed writes into a detached card for the rest of the page.
    expect(toolCallSigs.get(key)).toBeUndefined();
  });

  it("brings a tool card back OPEN when the reader had opened it before the drop", async () => {
    await coldLoad("open", outputHeadTurn("open", HUGE));
    const toggle = (): HTMLElement | null =>
      card("open").querySelector<HTMLElement>(
        `[data-entry-seq="${String(TOOL_AT)}"] .tool-disclosure`,
      );
    await dragUpAWindow("open");
    await vi.waitFor(() => {
      expect(toggle()).not.toBeNull();
    });
    toggle()?.click();
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");

    // Away and back: `aria-expanded` is the only record a reader opened a tool card.
    await dragTo(scroller().scrollHeight);
    await vi.waitFor(() => {
      expect(toggle()).toBeNull();
    });
    // Out through the live edge's own settle window, or the drag back is a gesture
    // inside the bottom pin's hold and gets undone before any pass sees it.
    await atLiveEdge();
    await dragUpAWindow("open");
    await vi.waitFor(() => {
      expect(toggle()).not.toBeNull();
    });
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
  });

  it("mounts a HEAD-ward grant with no repaint behind it, which is all a rail jump gives", async () => {
    const id = await coldLoad("big", hugeTurn("big", HUGE));
    expect(seqEl("big", 20)).toBeNull();

    // The rail jump's exact shape: scroll, then build. The grant is head-ward of everything
    // mounted, so the build's own settle must insert it.
    await mountTurnBody(id, "big", 20);
    await vi.waitFor(() => {
      expect(seqEl("big", 20)).not.toBeNull();
    });
    // And the window MOVED to the grant rather than growing to cover it.
    expect(mountedSeqs("big").length).toBeLessThanOrEqual(RESIDENT_ENTRIES);
    expect(seqEl("big", HUGE)).toBeNull();
  });

  it("mounts a HEAD-ward grant with a repaint landing mid-build", async () => {
    const id = await coldLoad("big", hugeTurn("big", HUGE));
    expect(seqEl("big", 20)).toBeNull();

    // A store bump during the build refuses the building turn and must not record its plan as
    // applied, or the settle pass exits early.
    const built = mountTurnBody(id, "big", 20);
    bumpMessages(id, "shape");
    await built;

    await vi.waitFor(() => {
      expect(seqEl("big", 20)).not.toBeNull();
    });
  });

  it("steps the anchor to the nearest OPENABLE turn while a fold sits deferred", async () => {
    const id = chatID();
    // t2 is tool-bearing, so its fold really hides something; hand-opened, so it is
    // openable, bodied and unfolded with a newer turn after it.
    setTurnOpen(id, "t2", true);
    activate(id, [hugeTurn("t1", 6), toolTurn("t2", 60), hugeTurn("t3", HUGE)]);
    await settled("t3");
    await atLiveEdge();
    await dragTo(card("t2").offsetTop + Math.floor(card("t2").offsetHeight / 2));
    expect(card("t2").hasAttribute("data-folded")).toBe(false);

    // Folded from the rail while Reading: `deferWhileReading` keeps the card bodied on screen.
    setTurnOpen(id, "t2", false);
    bumpMessages(id, "shape");

    // The ladder steps FORWARD to t3's HEAD; testing `data-folded` would window t3's TAIL.
    await vi.waitFor(() => {
      expect(mountedSeqs("t3")[0]).toBe(1);
    });
    // The case's own premise, or it would pass for the wrong reason: the viewport top
    // is inside the card the store has stopped calling openable.
    const t2 = card("t2");
    expect(scroller().scrollTop).toBeGreaterThanOrEqual(t2.offsetTop);
    expect(scroller().scrollTop).toBeLessThan(t2.offsetTop + t2.offsetHeight);
    // And the body under the reader survives the pass: its unmount is a deferred
    // REQUEST, held until they return to Following.
    expect(t2.hasAttribute("data-folded")).toBe(false);
    expect(t2.querySelector(":scope > .turn-body")).not.toBeNull();
  });

  // The running turn: what a reader watching a live run sees.

  /** One paint's worth of settling: the store coalesces a delta on a microtask and the
   *  paint runs inside the store effect, so one macrotask turn covers both. */
  function tick(): Promise<void> {
    return new Promise((resolve) => {
      setTimeout(resolve, 0);
    });
  }

  it("mounts every ordinal a running turn appends, and stays bounded past the budget", async () => {
    const id = chatID();
    // Running and short: everything is mounted, and with the reader FOLLOWING the
    // anchor is the live tail, so each arrival is in window by construction.
    activate(id, [hugeTurn("live", 6, false)], true);
    await vi.waitFor(() => {
      expect(mountedSeqs("live")).toHaveLength(6);
    });
    await atLiveEdge();

    // Arrivals are SEALED `thinking` entries, so "every arrival is mounted" is a per-element
    // claim (text deltas would coalesce into one row).
    const total = RESIDENT_ENTRIES + 2 * OVERSCAN_ENTRIES;
    for (let at = 7; at <= total; at++) {
      appendEntry(id, sealed("live", at, "thinking", { text: `step ${String(at)}` }));
      await tick();
      // EACH arrival, not a sample: a reader watching a run sees every entry land.
      expect(seqEl("live", at), `entry ${String(at)}`).not.toBeNull();
      expect(mountedSeqs("live").length, `at ${String(at)}`).toBeLessThanOrEqual(RESIDENT_ENTRIES);
    }
    // Bounded means the HEAD left, by query: the turn is past the budget.
    expect(seqEl("live", 1)).toBeNull();
    expect(mountedSeqs("live").at(-1)).toBe(total);
    // The premise, asserted: the reader never left the live edge.
    expect(readingState()).toBe("following");
  }, 30_000);

  it("keeps the lane's OPEN entry mounted whatever the window drops", async () => {
    const id = chatID();
    activate(id, [hugeTurn("live", HUGE, false)], true);
    await settled("live");
    // The lane's OPEN entry is what streams, and it is the one thing that subscribes:
    // its text still grows, so it carries the caret and the streaming wash.
    openEntry(id, {
      turn: "live",
      id: "live-open",
      lane: "",
      kind: "text",
      text: "the run is writing",
      n: 1,
    });
    await atLiveEdge();
    const liveRow = (): HTMLElement | null =>
      bodyOf("live").querySelector<HTMLElement>(".msg-row .message.streaming");
    await vi.waitFor(() => {
      expect(liveRow()).not.toBeNull();
    });

    // An open entry has no `seq`, and `syncOpenTail` mounts it at its lane's tail with no window
    // gate, so no window move can drop it. Asserted: the surface survives a move that drops
    // its neighbours, and text written while away comes from the store. The re-mount case is
    // the PARK path's (`messages-parked-views.test.ts`).
    await dragUpAWindow("live");
    expect(liveRow()).not.toBeNull();
    expect(seqEl("live", HUGE)).toBeNull();

    // The run keeps writing while the reader is a window away.
    applyDelta(id, "live", "live-open", "", 2, " and it kept growing while nobody watched");
    await tick();
    await vi.waitFor(() => {
      expect(liveRow()?.textContent).toContain("and it kept growing");
    });
    // And it is STILL the live entry: the caret is what tells the reader the run is
    // going, and a surface that sealed it would say the turn had ended.
    expect(liveRow()?.classList.contains("streaming")).toBe(true);
  });
});

describe("unmounted ordinals hold the height their rows occupied", () => {
  let style: HTMLStyleElement;

  // The shipped stylesheet, so `.turn-body`'s gap is MEASURED; scoped to this block.
  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  it("prices a spacer at the run its rows occupy, the parent's gaps included", async () => {
    const id = chatID();
    // Four one-entry runs split by rendering entries: seven boxes whose gaps a spacer carries.
    const entries = runsTurn("gaps", 4, 1);
    forgetHeights(["gaps"]);
    activate(id, [entries]);
    await vi.waitFor(() => {
      expect(bodyRows("gaps")).toHaveLength(4);
    });
    const boxes = [...bodyOf("gaps").querySelectorAll<HTMLElement>("[data-entry-seq]")];
    expect(boxes).toHaveLength(7);
    let measured = 0;
    for (const box of boxes) {
      const at = Number(box.dataset["entrySeq"]);
      measured += box.offsetHeight;
      if (box.classList.contains("msg-row")) {
        recordRowHeight("gaps", { from: at, to: at + 1 }, box.offsetHeight);
      } else {
        recordEntryHeight("gaps", at, box.offsetHeight);
      }
    }
    const first = boxes[0];
    const second = boxes[1];
    const gap = (second?.offsetTop ?? 0) - ((first?.offsetTop ?? 0) + (first?.offsetHeight ?? 0));
    // Or the case proves nothing: a parent adding no gap loses none.
    expect(gap).toBeGreaterThan(0);
    const t = projectTurns({
      turns: new Map<string, TurnState>([["gaps", { entries, openEntries: new Map() }]]),
      turn_order: ["gaps"],
    }).find((x: Turn) => x.id === "gaps");
    expect(t).not.toBeUndefined();
    // Nothing mounted: the tail spacer is the boxes' heights plus one gap per box.
    expect(spacerHeight(t as Turn, { from: 0, to: 0 }, "tail", "")).toBe(
      measured + boxes.length * gap,
    );
  });
});
