// ---------------------------------------------------------------------------
// Residency measured in ENTRIES: what a paint mounts inside ONE long turn.
//
// A turn-id set cannot refuse part of one turn, so a cold load of a 700-entry turn
// built all 700. The plan names an `EntryRange` now, and the ordinals outside it are
// ABSENT from the DOM rather than unpainted. The real `scroll.ts` is attached, not the
// shared mock, whose scroller has no geometry to read.
//
// THE FIXTURE IS `thinking` ENTRIES, and that is what the entry model forces rather
// than a preference: `sliceTurn` snaps a window down to a prose run's first entry and
// up past its last, so a body of N sealed `text` entries is ONE `.msg-row` and every
// mounted-ordinal assertion below would read 1. A `thinking` entry renders at its own
// position and joins no run, so the body has N ordinals a window can sit inside. Two
// other fixtures appear where the case needs them: `tool_call` entries where the
// narrower TOOL budget binds, and text stretches separated by a rendering entry where
// the case is about prose RUNS.
// ---------------------------------------------------------------------------

import { describe, it, expect, afterAll, beforeAll, beforeEach, vi } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { TurnState } from "./types.js";
import type { Turn } from "./turns.js";
import type { Entry, Hit } from "./wire/types.gen.js";

// The SERVER's search answer is the only thing stubbed for the navigation case:
// `chat-search.ts`, the reveal it injects, the renderer and the scroller all run for
// real, which is the point — a hit inside an unmounted entry needs a real build, and
// a mocked reveal would pass with none having happened. Spy-wrapped rather than
// replaced, because this module has a dozen other exports the graph links.
vi.mock("./api-client.js", { spy: true });

// NESTED as the shipped page nests them (static/index.html): `#messages-wrap` is
// `position: absolute; inset: 0` inside the outer wrapper, so it is the
// `offsetParent` of the whole transcript AND the scroller. Flat siblings give the
// anchor ladder a scroller with no content in it and card offsets measured against
// the body, which is every coordinate in the wrong space.
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
const { scrollToBottom, readingState, setPinSettleMs } = await import("./scroll.js");
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

/** FIXTURE (1), the general shape: one turn of `count` sealed `thinking` entries.
 *  `entryRenders` answers true for `thinking` through its default arm and `inProseRun`
 *  answers false, so each is its own mounted element charged against the entry budget
 *  alone — the exact analogue of the retired 700 text BLOCKS, and the only shape under
 *  which the overscan floor is falsifiable at all. */
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

/** FIXTURE (2): `count` consecutive settled TOOL cards, the shape the tool budget binds
 *  on, where the window is at its narrowest. WRONG as the general fixture —
 *  `RESIDENT_TOOL_CALLS` (96) binds before `RESIDENT_ENTRIES` (320), so a 320-ordinal
 *  assertion over one would fail for a reason the case is not about. */
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

/** FIXTURE (3): `runs` stretches of `per` sealed `text` entries, each stretch separated
 *  by a `thinking` entry. A prose RUN boundary is only reachable this way —
 *  `recordRowHeight`'s one production caller is a run LEAVING a body — and a single
 *  stretch would be one row whatever its length. */
function runsTurn(turnID: string, runs: number, per: number): Entry[] {
  const out: Entry[] = [turnOpen(turnID)];
  let at = 1;
  for (let r = 0; r < runs; r++) {
    if (r > 0) {
      out.push(sealed(turnID, at++, "thinking", { text: `between ${String(r)}` }));
    }
    for (let i = 0; i < per; i++) {
      // Long enough to WRAP, so a real row measures well past the per-entry estimate
      // and the height a departing run records is worth something the document can be
      // short by.
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

/** The ordinal the two tool-bearing fixtures put their card at: HEAD-ward of the cold
 *  window and inside the one the reader's own drag reaches, so the card is mounted, then
 *  dropped, then mounted again — the sequence both cases are about. The turn's own head
 *  is deliberately NOT used: the window converges one budget head-ward and stops there
 *  (`dragUpAWindow` carries the measurement), so a card at ordinal 1 is unreachable and
 *  the case would be testing the ladder rather than the drop. */
const TOOL_AT = 250;

/** `hugeTurn` with a todo list at `TOOL_AT`: the entry kind whose mount arms an effect
 *  of its own, so a drop that fails to drain it leaves a signal subscribed to a detached
 *  list. */
function todoHeadTurn(turnID: string, count: number): Entry[] {
  const out = hugeTurn(turnID, count);
  out[TOOL_AT] = sealed(turnID, TOOL_AT, "tool_call", {
    id: `${turnID}-todo`,
    title: "todo_list",
    kind: "other",
    status: "completed",
    ts: TOOL_AT,
    input: { todos: [{ content: "first", status: "completed" }] },
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

/** `hugeTurn` with one entry carrying a needle no other entry holds, so a query for it
 *  marks NOTHING while that entry is unmounted — which is what routes stepping to the
 *  server hits instead of to the resident marks. */
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

/** The entry ordinals this turn's body actually holds, ascending. A prose RUN is stamped
 *  for its first member alone, which is why the ROW cases read `data-entries` instead. */
function mountedSeqs(turnID: string): number[] {
  return [...card(turnID).querySelectorAll<HTMLElement>("[data-entry-seq]")]
    .map((e) => Number(e.dataset["entrySeq"]))
    .sort((a, b) => a - b);
}

function seqEl(turnID: string, at: number): HTMLElement | null {
  return card(turnID).querySelector<HTMLElement>(`[data-entry-seq="${String(at)}"]`);
}

/** This turn's mounted prose RUNS, in document order. */
function bodyRows(turnID: string): HTMLElement[] {
  return [...bodyOf(turnID).querySelectorAll<HTMLElement>(":scope > .msg-row")];
}

function spacer(turnID: string, side: "head" | "tail"): HTMLElement | null {
  return card(turnID).querySelector<HTMLElement>(`:scope > .turn-space[data-space="${side}"]`);
}

// ---------------------------------------------------------------------------
// Search navigation onto an entry that is not in the DOM. The overlay, the server
// pre-pass, the injected reveal and the walker all run for real; only the server's
// ANSWER is staged.
// ---------------------------------------------------------------------------

/** The server's search reply, for any search these cases run, run through the caller's
 *  own decoder so the staged bytes are held to the wire shape. Everything else
 *  `api-client.js` serves keeps its real behaviour. */
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
    // The window is 320 ordinals of ONE turn, so a builder that could only cut between
    // turns would mount all of them in the paint that created the card — the frame the
    // yielded builder exists to protect.
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
    // A rail jump calls this on a turn that already HAS a body, and a caller that named
    // no ordinal asks for the turn's head — which no slice can mount until positional
    // insertion lands. The build has to give up rather than spin: a wedged loop holds
    // `hasPendingBuild` true for the turn's lifetime, and its yields starve the timer
    // queue, so a spin takes the whole run down instead of reporting here.
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
    // The case's premise, and the whole difficulty: the hit's entry is ABSENT, so there
    // is nothing for the walker to mark and no node for `jumpTo` to reach. One rendered
    // frame cannot fix that, which is why the build is awaited.
    expect(seqEl("big", 200)).toBeNull();

    await findAndStep(SENTINEL);

    await vi.waitFor(() => {
      expect(document.querySelector("mark.find-hit-current")).not.toBeNull();
    });
    const current = document.querySelector("mark.find-hit-current");
    // ON the entry the hit named — not the notice, and not a neighbouring entry.
    // `find-in-chat.ts` resolves a hit's host by `[data-entry-id]`, which is the
    // coordinate that replaced the block index.
    expect(current?.closest("[data-entry-id]")?.getAttribute("data-entry-id")).toBe("big-e200");
    // The position in the server's one-row list, with no whole-chat figure beside it:
    // the list holds every occurrence, so there is nothing more to say.
    expect(countText()).toBe("1 of 1");
    toggleChatFind();
  });
});

// ---------------------------------------------------------------------------
// Scrolling moves the window, and every case below needs REAL geometry: the
// shipped stylesheet, a sized scrollport, and the real `scroll.ts` listener the
// window pass hangs off. The shared mock cannot serve any of it — its scroller is
// a detached div and its `onViewportChange` never fires.
// ---------------------------------------------------------------------------

describe("scrolling moves the window", () => {
  let style: HTMLStyleElement;

  /** The scrollport's own height. */
  const VIEWPORT_PX = 720;

  beforeAll(() => {
    style = mountAppCSS();
    // `#messages-wrap-outer` is `flex: 1` of a column this fixture does not build,
    // so without a height the absolutely-positioned scroller inside it is 0 tall
    // and the ladder answers the live edge for every position.
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

  /** Wait until the cold build has FINISHED: two consecutive polls agreeing on the
   *  mounted count. The window pass refuses to move a body that is still filling, so
   *  a case that scrolls has to let the builder finish first. */
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

  /** Put the reader back on the live edge and wait out the bottom pin's own settle
   *  window: a gesture inside it is undone before any pass sees it. The pin is
   *  armed short and the real window is back in force before the case acts. */
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

  /** Drag the scrollbar to `top`, and report how far the reader actually moved.
   *
   *  The wheel's DIRECTION is load-bearing, not decoration: the controller enters
   *  Reading from the aim of the reader's input, so a bare positional write is the
   *  shape of the platform's own clamp and stays Following on purpose.
   *  `behavior: "instant"`, not `scrollTop =`: the scroller declares
   *  `scroll-behavior: smooth` (css/13-messages.css), so an assignment only starts an
   *  animation. Measured immediately, so the number excludes later compensation. */
  async function dragTo(top: number): Promise<number> {
    const scrollEl = scroller();
    const was = scrollEl.scrollTop;
    scrollEl.dispatchEvent(new WheelEvent("wheel", { deltaY: top < was ? -1 : 1 }));
    scrollEl.scrollTo({ top: Math.max(0, top), behavior: "instant" });
    const moved = scrollEl.scrollTop - was;
    for (let f = 0; f < 4; f++) {
      await frame();
    }
    return moved;
  }

  /** Drag to the top, wait for the window to move HEAD-ward, and report the ordinal it
   *  reached.
   *
   *  It converges in ONE pass on the per-ENTRY fixture and cannot be walked further from
   *  here, which is production's own arithmetic rather than a flake: the anchor ladder
   *  resolves its position from MOUNTED elements, so at `scrollTop` 0 the anchor is the
   *  window's own first ordinal, the plan seeds there, and the compensation leaves the
   *  reader at 0 again — where a second `scrollTo({top: 0})` moves nothing and emits no
   *  scroll event, so no later pass is armed. Measured on this fixture: `[381, 700]`
   *  becomes `[221, 541]` and stays, and a 40-iteration drag loop reaches the same 221.
   *  So every case below is written about the window having MOVED and the tail having
   *  been DROPPED — which is the oracle — rather than about reaching ordinal 1. The
   *  prose-RUN fixture is cheap enough per box that the same drag does reach its first
   *  run, which is what `dragToHead` is for. */
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

  /** Wait until the window stops MOVING: two consecutive polls agreeing on the
   *  ordinals mounted. Distinct from `settled`, which waits for a cold build to
   *  finish filling one window. */
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

  /** `least` is the mounted-ordinal floor the cold build has to clear before the case
   *  scrolls. It is a parameter rather than a constant because a PROSE fixture stamps
   *  one ordinal per RUN — five runs of 120 text entries mount five rows — so a floor
   *  written for the per-entry shape times out on the row axis. */
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

    // Previously absent ordinals are present…
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

    // The OVERLAP is the subject, not one chosen ordinal, and that is what makes this
    // clamp-proof: `content-visibility: auto` re-measures with no mutation, the browser
    // clamps `scrollTop` itself, and the listener reads that clamp as a gesture — so
    // however many passes run and however far the window ends up, every ordinal present
    // both before and after has to be the identical node.
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
      // An element the window still wants is neither rebuilt nor re-created, which is
      // what positional insertion buys and what keeps a selection and a reader-set
      // disclosure inside it. Per-element `toBe`, never a `toEqual` over the two node
      // lists: that compares structurally and passes for a rebuilt element holding the
      // same markup, which is the one thing this case exists to detect.
      expect(after.get(i)?.el, `ordinal ${String(i)}`).toBe(before.get(i)?.el);
    }

    // And the reader followed their own travel rather than the window's height change.
    // Measured against the TOTAL `scrollTop` delta rather than one drag's, so every
    // compensation the convergence wrote is accounted for. A BOUND on the residue, not
    // zero — the head batch also grows the body's tail, so a pass's `scrollHeight` delta
    // is not purely above the reader.
    const travelled = scroller().scrollTop - wasTop;
    const mid = kept[Math.floor(kept.length / 2)] ?? 0;
    const drift = (after.get(mid)?.top ?? 0) - (before.get(mid)?.top ?? 0) + travelled;
    expect(Math.abs(drift)).toBeLessThan(0.25 * Math.abs(travelled));
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
    // Eight frames with no further gesture. The compensation WRITES scrollTop, which
    // emits a scroll of its own, so without the re-entrancy latch and the
    // plan-equality exit the window would keep chasing its own correction.
    for (let i = 0; i < 8; i++) {
      await frame();
    }
    expect(mountedSeqs("big").join(",")).toBe(first);
    expect(scroller().scrollTop).toBeCloseTo(landed, -1);
  });

  for (const shape of ["reasoning", "tool_call"] as const) {
    it(`keeps one overscan mounted each side of the anchor, ${shape}`, async () => {
      // TWO fixtures, because the two budgets bind on different shapes: the reasoning
      // turn is the general case that motivates the feature, and a run of consecutive
      // tool cards is where the TOOL budget binds and the window is narrowest.
      await coldLoad("big", shape === "reasoning" ? hugeTurn("big", HUGE) : toolTurn("big", HUGE));
      // To the head, then back down past what it mounted, which is what puts the
      // anchor MID-turn: at either end of the sequence one side latches and the other
      // takes the whole budget.
      await dragUpAWindow("big");
      const to = mountedSeqs("big").at(-1) ?? 0;
      await dragTo(mountedBottom("big"));
      await vi.waitFor(() => {
        expect(mountedSeqs("big").at(-1)).toBeGreaterThan(to);
      });
      await windowSettled("big");
      // INTO the region the move produced, or there is no anchor to measure: a tail-ward
      // move seats the new window BELOW the reader, so `anchorSeq` answers -1 — every
      // mounted element sits past `scrollTop` — and the floor is unreadable rather than
      // absent. A reader who scrolled down to read what arrived is inside it.
      await dragTo(Math.round((mountedTop("big") + mountedBottom("big")) / 2));

      // Anchor and window read TOGETHER and re-read until they agree, because the pair
      // is what the floor is a property of. `windowSettled` alone is not enough: the
      // compensation writes `scrollTop`, and `content-visibility: auto` re-measuring
      // with no mutation makes the browser clamp it again, which the listener reads as
      // a gesture — so a window can start moving after two polls agreed. A retry cannot
      // rescue a MISSING floor: no settled pair would satisfy it and this times out red.
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

  /** The prose runs a body holds, keyed by the run's first `seq`. A run is stamped for
   *  that entry alone and carries the rest in `data-entries`, so the ROW cases read the
   *  rows rather than `mountedSeqs`. */
  function runRows(turnID: string): Map<number, HTMLElement> {
    return new Map(bodyRows(turnID).map((r) => [Number(r.dataset["entrySeq"]), r] as const));
  }

  it("drops a whole prose RUN when the window moves off it, and keeps the rest in order", async () => {
    // 5 runs of 120 text entries, separated by a rendering entry, so the 320-ordinal
    // window covers about two runs and a move to the head has to take whole rows OUT of
    // the body. The row axis has no other fixture: `recordRowHeight`'s one production
    // caller is a run LEAVING a body, and a turn whose body is one run has no boundary.
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
    // And the runs that stayed read in the turn's own order — the reconcile's OUTCOME,
    // whichever arm produced it. The length floor is what makes "in order" expressible:
    // a one-row survivor set satisfies any sort.
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
    // THE SIMPLIFICATION, with its reason at the site: the old suite needed a
    // `shape()` helper pairing each row key with how many of ITS blocks were mounted,
    // because a block index was per MESSAGE and two rows of 200 blocks both numbered
    // theirs 0..199. An entry `seq` is per TURN and unique, so the mounted-seq set IS
    // the shape and the helper goes.
    const was = mountedSeqs("keep").join(",");

    // All the way back down, which is the shortest gesture that moves this window at
    // all: measured, a drag to three fifths of the document leaves the plan unchanged,
    // because five runs of 120 entries put that position inside the mounted region and
    // the anchor never leaves it. The tail is what drops the first run.
    await dragTo(scroller().scrollHeight);
    await vi.waitFor(() => {
      expect(mountedSeqs("keep").join(",")).not.toBe(was);
    });

    // A run the move KEPT is neither rebuilt nor re-created: the row key is what the
    // reconcile matches on, so a key carrying the window would re-create every row on
    // every move.
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

    // THE FLIGHT the jump produces: ~50 scroll events in quick succession, which is what
    // a smooth `scrollIntoView` emits, and clearing the pin on any of them cancels the
    // navigation the reader asked for. NOT three settled drags with frames between them
    // — measured, each of those lets a whole window pass run to completion, and a grant
    // released after the reader has settled somewhere else is a different claim that this
    // case would fail for the wrong reason.
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
    const id = await coldLoad("todo", todoHeadTurn("todo", HUGE));
    const key = toolCallSigKey(id, "todo-todo");
    // Mounted first: the head is outside the cold window, so the reader has to reach
    // it before there is anything to release.
    await dragUpAWindow("todo");
    expect(seqEl("todo", TOOL_AT)).not.toBeNull();
    expect(toolCallSigs.get(key)).toBeDefined();

    // Back to the live edge, which retracts the head the reader left.
    await dragTo(scroller().scrollHeight);
    await vi.waitFor(() => {
      expect(seqEl("todo", TOOL_AT)).toBeNull();
    });
    // The ELEMENT going is half of it. Its effect is subscribed to the store, so a
    // drop that leaves it armed writes into a detached list for the rest of the page.
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

    // Away, so the card is dropped, and back. `aria-expanded` is the ONLY record a
    // reader opened a tool card — the boxes have registry keys and this has nothing —
    // so a drop that does not carry it hands back a collapsed card.
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

    // The rail jump's exact shape: scroll first, then build, and nothing after it. No
    // `bumpMessages` here on purpose — the grant is head-ward of everything mounted, so
    // the build itself can insert nothing and the pass that does has to come from the
    // build's own settle. A flight of zero distance emits no scroll event at all.
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

    // The store bump lands while the build is in FLIGHT, which is the common case
    // during streaming. That paint refuses the building turn and must not record its
    // plan as applied — the build's own settle pass is the only thing left to insert
    // the grant, and it exits on a plan already recorded.
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

    // The reader folds it from the rail's side of the store and stays put. Reading is
    // what makes `deferWhileReading` hold `setCardFolded` and the unmount, so the card
    // is still unfolded and bodied on screen while the store has already stopped
    // calling it openable.
    setTurnOpen(id, "t2", false);
    bumpMessages(id, "shape");

    // The card the reader is on is not openable now, so the ladder steps FORWARD to
    // t3 and seeds at its HEAD. Testing `data-folded` instead would pick t2's own
    // ordinals, which `planResidency` cannot place, take its absent-turn fallback,
    // and window t3's TAIL — the live edge, hundreds of ordinals from the reader.
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

  // -------------------------------------------------------------------------
  // The running turn: what the reader watching a live run sees.
  // -------------------------------------------------------------------------

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

    // THE FIXTURE DECISION, recorded where it is made: the arrivals are SEALED
    // `thinking` entries, so each mounts its own element and "every arrival is mounted"
    // stays a per-element claim. A run of `text` deltas coalesces into one growing prose
    // run, where the same oracle would only be observable as that row's text plus a
    // bounded mounted-seq count — a weaker reading of the same case.
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
    // The premise, ASSERTED rather than enforced: every arrival above was in window
    // because the reader never left the live edge, and nothing here touched the
    // scroller.
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

    // THE ORACLE THIS CASE INHERITED IS UNREACHABLE, and the measurement is why it is
    // this case instead. It was "re-mounts a live tail from the STORE, still streaming,
    // when the reader comes back": the reader scrolls away, the live BLOCK is dropped by
    // the window, the run keeps writing, and coming back re-mounts it from the store with
    // nothing lost. Under the entry model an open entry is a separate type with no `seq`
    // at all — `syncOpenTail` mounts it at the TAIL of its lane's view whenever the turn
    // has an open entry and no `turn_close`, with no window gate anywhere in that path —
    // so no window move can drop it and the premise cannot be arranged. What IS still
    // true is asserted here: the surface survives a window move that drops the ordinals
    // around it, and the text a delta writes while the reader is elsewhere is on screen
    // when they return, because it comes from the store rather than from the DOM. The
    // half that genuinely needs a re-mount belongs to the PARK path, where the whole body
    // is disposed and rebuilt (`messages-parked-views.test.ts`); the reopening condition
    // for a scroll-driven version is `syncOpenTail` gaining a window gate.
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

  // The shipped stylesheet, so the gap `.turn-body` declares is MEASURED here
  // rather than restated. Scoped to this block: the cases above assert DOM
  // presence and want no cascade at all.
  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  it("prices a spacer at the run its rows occupy, the parent's gaps included", async () => {
    const id = chatID();
    // Four one-entry prose runs, each separated by a rendering entry, so `.turn-body`'s
    // own flex `gap` sits between seven boxes and a spacer standing in for the lot has
    // to carry those gaps.
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
    // Nothing mounted, so the tail spacer stands for every box: their own measured
    // heights plus one gap per box — the boundary gap the parent no longer supplies
    // beside the gaps between them.
    expect(spacerHeight(t as Turn, { from: 0, to: 0 }, "tail", "")).toBe(
      measured + boxes.length * gap,
    );
  });
});
