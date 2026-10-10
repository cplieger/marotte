import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import indexHtml from "../static/index.html?raw";

// scroll.ts initialises a #messages singleton at import, so it is stubbed. The fake records the
// epoch and absolute landings and fires `scrollend` for programmatic scrolls as Chromium does.
// `top: scrollTop` on its rect keeps a card's measured top INVARIANT under scroll, as a real one is.
const { scrollable } = vi.hoisted(() => {
  const handlers = new Map<string, Set<() => void>>();
  const geometry = { reads: 0 };
  const el = {
    scrollTop: 0,
    get clientHeight() {
      geometry.reads++;
      return 600;
    },
    clientTop: 0,
    getBoundingClientRect: () => ({ top: el.scrollTop, bottom: el.scrollTop + 600, height: 600 }),
    addEventListener(type: string, fn: () => void) {
      const set = handlers.get(type) ?? new Set<() => void>();
      set.add(fn);
      handlers.set(type, set);
    },
    removeEventListener(type: string, fn: () => void) {
      handlers.get(type)?.delete(fn);
    },
  };
  return {
    scrollable: {
      /** The transcript's scroll room, which is the rail's own visibility gate. */
      by: 500,
      /** Scroller geometry reads (`scrollableBy()`, `clientHeight`), each a forced layout. */
      geometry,
      line: 0,
      atLiveEdge: false,
      el,
      landings: [] as { px: number; behavior: string }[],
      /** `begin` / `end`, so a case can see the epoch bracket the whole operation. */
      epochs: [] as string[],
      readerGesture: undefined as (() => void) | undefined,
      transcriptMutate: undefined as (() => void) | undefined,
      contentResize: undefined as (() => void) | undefined,
      attach: undefined as (() => void) | undefined,
      fire(type: string) {
        for (const fn of [...(handlers.get(type) ?? [])]) {
          fn();
        }
      },
      reset() {
        this.by = 500;
        this.line = 0;
        this.atLiveEdge = false;
        this.landings.length = 0;
        this.epochs.length = 0;
        el.scrollTop = 0;
      },
    },
  };
});
vi.mock("./scroll.js", () => ({
  scrollableBy: () => {
    scrollable.geometry.reads++;
    return scrollable.by;
  },
  readingLineOffset: () => scrollable.line,
  atLiveEdgeNow: () => scrollable.atLiveEdge,
  getScrollEl: () => scrollable.el,
  beginSelfScroll: () => {
    scrollable.epochs.push("begin");
  },
  endSelfScroll: () => {
    scrollable.epochs.push("end");
  },
  scrollToOffset: (px: number, behavior: string) => {
    scrollable.landings.push({ px, behavior });
    scrollable.el.scrollTop = px;
    setTimeout(() => {
      scrollable.fire("scrollend");
    }, 0);
  },
  onReaderGesture: (cb: () => void) => {
    scrollable.readerGesture = cb;
    return () => {
      scrollable.readerGesture = undefined;
    };
  },
  onTranscriptMutate: (cb: () => void) => {
    scrollable.transcriptMutate = cb;
    return () => {
      scrollable.transcriptMutate = undefined;
    };
  },
  onContentResize: (cb: () => void) => {
    scrollable.contentResize = cb;
    return () => {
      scrollable.contentResize = undefined;
    };
  },
  onAttach: (cb: () => void) => {
    scrollable.attach = cb;
    return () => {
      scrollable.attach = undefined;
    };
  },
}));
// The session-wide index is the rail's own fetch; the lifecycle cases below decide
// what it does with the answer, not how it asks. `apiGetTyped` is the in-chat
// search's fetch, linked through the rail's hit-turn reader and never asked here.
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(), apiGetTyped: vi.fn() }));

// The pagination door `navigateToTurn` walks when its target is off the resident
// window. Mocked because the real one is a network fetch, and what these cases
// assert is the rail's own sequencing around it.
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));

import {
  mountTurnRail,
  loadTurnRail,
  setResidentTurns,
  pointTurnRail,
  refreshTurnRail,
  invalidateTurnRails,
  resetTurnRail,
  initTurnRailCallbacks,
} from "./turn-rail.js";
import type { TurnSummary } from "./rail-merge.js";
import { apiGet } from "./api-client.js";
import { loadMessages } from "./store-load.js";
import { setSessions, setActive, get } from "./store.js";
import type { Entry, Session, TurnState } from "./types.js";
import { KEY_ATTR } from "@cplieger/reactive";
import type { TurnOutcome } from "./turns.js";

const MINUTE = 60_000;

// Browser Mode serves no CSS, and the map lays no rows until its track has measured a height. The
// track gets the height the stylesheet would give it, and one boot mount takes its first
// ResizeObserver delivery, so every case renders on its first paint as a measured map does.
const trackCSS = document.createElement("style");
trackCSS.textContent = ".turn-map-track{display:block;block-size:2000px}";
document.head.appendChild(trackCSS);
mountRail(document.createElement("div"));
await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));

// The gate is measured in the map's frame-coalesced pick, so a case that shut it reopens it here
// rather than leaving the next case's first paint behind a stale verdict.
afterEach(async () => {
  scrollable.by = 500;
  scrollable.fire("scroll");
  await frames();
});

function turn(n: number, over: Partial<TurnSummary> = {}): TurnSummary {
  return {
    id: `m${String(n)}`,
    n,
    outcome: "completed",
    // One minute apart by default. `ts` is still required by `TurnSummary`, so the
    // field stays; nothing renders it, since the rail reports no pause.
    ts: n * MINUTE,
    ...over,
  };
}

function turns(count: number, outcome: TurnOutcome = "completed"): TurnSummary[] {
  return Array.from({ length: count }, (_, i) => turn(i + 1, { outcome }));
}

function entry(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return {
    id: `${turnID}-e${String(at)}`,
    turn: turnID,
    lane: "",
    kind,
    seq: at,
    ts: at + 1,
    payload,
  } as Entry;
}

interface ResidentOpts {
  prompt?: string;
  elapsedMs?: number;
  outcome?: TurnOutcome;
  /** No `turn_close`: the turn is still streaming. */
  open?: boolean;
}

/** A turn as the STORE holds it: the `turn_open` the projection requires, whose id the
 *  rail's index joins on, plus the close carrying the stamps a marker's slot reads. */
function residentTurn(id: string, n: number, opts: ResidentOpts = {}): TurnState {
  const entries: Entry[] = [
    entry(id, 0, "turn_open", {
      prompt: { id: `${id}-p`, text: opts.prompt ?? "ask" },
      source: "prompt",
      n,
    }),
  ];
  if (opts.open !== true) {
    entries.push(
      entry(id, 1, "turn_close", {
        outcome: opts.outcome ?? "completed",
        ...(opts.elapsedMs === undefined ? {} : { elapsed_ms: opts.elapsedMs }),
      }),
    );
  }
  return { entries, openEntries: new Map() };
}

type Resident = Pick<Session, "turns" | "turn_order">;

/** The ids are `m<n>` because `turn(n)` mints the same one, which is the key the index merge joins
 *  on. */
function residentTurns(ids: string[], opts: ResidentOpts = {}): Resident {
  const turnStates = new Map<string, TurnState>();
  for (const id of ids) {
    turnStates.set(id, residentTurn(id, Number(id.slice(1)), opts));
  }
  return { turns: turnStates, turn_order: [...ids] };
}

/** An older page landing: the store PREPENDS its turns, which is why `turn_order` is an
 *  array rather than the Map's own insertion order. */
function prependTurns(s: Session, ids: string[]): void {
  const older = residentTurns(ids);
  for (const [id, state] of older.turns) {
    s.turns.set(id, state);
  }
  s.turn_order = [...ids, ...s.turn_order];
}

/** Mount the map. A case that needs binning gives the track a smaller height. A module SINGLETON,
 *  hence the document-wide resolve. */
function mountRail(host: HTMLElement): HTMLElement {
  document.body.appendChild(host);
  mountTurnRail(host);
  const el = document.querySelector<HTMLElement>(".turn-map");
  if (el === null) {
    throw new Error("turn map not mounted");
  }
  return el;
}

/** The turn number a row's link lands, off its `#turn-<n>` href. */
function turnOf(link: Element | null | undefined): string {
  return /#turn-(\d+)$/u.exec(link?.getAttribute("href") ?? "")?.[1] ?? "";
}

/** The links of the map's rows, in order. */
function rowLinks(root: ParentNode): HTMLAnchorElement[] {
  return [...root.querySelectorAll<HTMLAnchorElement>(".turn-map-stack .turn-pill-link")];
}

/** The row a link sits in, which carries the position and severity attributes. */
function rowOf(link: HTMLElement): HTMLElement {
  const li = link.parentElement;
  if (li === null) {
    throw new Error("a link outside its row");
  }
  return li;
}

/** Whether a row's jump is paging history in, which its preview says. */
function isPending(link: HTMLElement): boolean {
  return link.getAttribute("data-tooltip")?.includes("Loading\u2026") ?? false;
}

/** The map's stack, the box a click's pointer Y is read against. */
function stackOf(root: ParentNode): HTMLElement {
  const el = root.querySelector<HTMLElement>(".turn-map-stack");
  if (el === null) {
    throw new Error("no stack");
  }
  return el;
}

/** The map's track, the box binning is measured against. */
function trackOf(root: ParentNode): HTMLElement {
  const el = root.querySelector<HTMLElement>(".turn-map-track");
  if (el === null) {
    throw new Error("no track");
  }
  return el;
}

/** The turn the map marks current, or "" when none is. */
function currentOf(root: ParentNode): string {
  return turnOf(root.querySelector(".turn-pill[data-current] > .turn-pill-link"));
}

function fakeRect(top: number, height: number): DOMRect {
  return {
    x: 0,
    y: top,
    top,
    bottom: top + height,
    left: 0,
    right: 100,
    width: 100,
    height,
    toJSON: () => ({}),
  } as DOMRect;
}

/** Two animation frames: one for the rail's own coalesced pick, one for whatever it
 *  renders from it. */
function frames(): Promise<void> {
  return new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

// ---------------------------------------------------------------------------
// Which chat the rail belongs to: a session-wide singleton over a paginated store, so the pointed
// chat is state. Left unset either way, it leaves stale markers over an empty chat or discards a
// first-turn refresh.
// ---------------------------------------------------------------------------

describe("which chat the map belongs to", () => {
  const host = document.createElement("div");

  beforeAll(() => {
    mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  function rail(): HTMLElement {
    const el = document.querySelector<HTMLElement>(".turn-map");
    if (el === null) {
      throw new Error("turn map not mounted");
    }
    return el;
  }

  /** The marker labels currently painted, in order. */
  function markers(): string[] {
    return rowLinks(rail()).map(turnOf);
  }

  it("paints one row per turn once the index arrives", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-a");
    expect(markers()).toEqual(["1", "2"]);
  });

  it("empties itself when handed another chat", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-a");

    pointTurnRail("c-b");

    expect(markers()).toEqual([]);
    // `data-shown` is what the stylesheet keys the map's display on.
    expect(rail().hasAttribute("data-shown")).toBe(false);
  });

  it("asks the server nothing when it is only pointed", () => {
    // An empty chat has no turns by definition, and a client-minted id has no
    // record to ask about, so the fetch is skipped rather than 404'd.
    pointTurnRail("c-b");
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("adopts a later refresh for the chat it points at", async () => {
    pointTurnRail("c-b");
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });

    await refreshTurnRail("c-b");

    expect(markers()).toEqual(["1"]);
  });

  it("drops a refresh for a chat it no longer points at", async () => {
    pointTurnRail("c-a");
    vi.mocked(apiGet).mockImplementation(async () => {
      // The reader switches chats while the index is in flight.
      pointTurnRail("c-b");
      return { turns: [turn(1), turn(2)] };
    });

    await refreshTurnRail("c-a");

    expect(markers()).toEqual([]);
  });

  // `turn_closed` is the only index re-read, so a rail never pointed at the chat discards the refresh
  // that would draw its first marker.
  it("drops a refresh for a chat it was never pointed at", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });

    await refreshTurnRail("c-never-pointed");

    expect(markers()).toEqual([]);
  });

  it("keeps what it is showing when the fetch fails", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });
    await loadTurnRail("c-a");

    // A rail that empties itself on a transient failure is worse than one that
    // is briefly a turn behind.
    vi.mocked(apiGet).mockResolvedValue(null);
    await refreshTurnRail("c-a");

    expect(markers()).toEqual(["1"]);
  });
});

// ---------------------------------------------------------------------------
// When the rail is worth existing
// ---------------------------------------------------------------------------

// A NAVIGATOR has nothing to offer a transcript seen whole. Both directions, including the one
// activation cannot cover.
describe("the map only appears once the transcript can be scrolled", () => {
  const host = document.createElement("div");

  beforeAll(() => {
    mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  function rail(): HTMLElement {
    const el = document.querySelector<HTMLElement>(".turn-map");
    if (el === null) {
      throw new Error("turn map not mounted");
    }
    return el;
  }

  function markers(): string[] {
    return rowLinks(rail()).map(turnOf);
  }

  it("stays empty for a transcript that fits, however many turns it holds", async () => {
    scrollable.by = 0;
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });

    await loadTurnRail("c-short");
    await frames();

    // No rows, and no `data-shown` for the stylesheet to draw the map on.
    expect(markers()).toEqual([]);
    expect(rail().hasAttribute("data-shown")).toBe(false);
  });

  it("appears once a paint takes the transcript past the threshold", async () => {
    scrollable.by = 0;
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-grows");
    await frames();
    expect(markers()).toEqual([]);

    // The transcript grew — a streaming turn, or a page of history landing. This is
    // the case activation cannot see: the turn holding the reading line has not
    // changed, so only the paint's own re-render reaches it.
    scrollable.by = 500;
    setResidentTurns([]);
    await frames();

    expect(markers()).toEqual(["1", "2"]);
    expect(rail().hasAttribute("data-shown")).toBe(true);
  });

  it("goes away again when the transcript stops being scrollable", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-shrinks");
    expect(markers()).toEqual(["1", "2"]);

    // A window the reader just made taller, or turns folding away.
    scrollable.by = 0;
    setResidentTurns([]);
    await frames();

    expect(markers()).toEqual([]);
  });

  it("wants real scroll room, not one stray pixel", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1)] });

    // A transcript overflowing by a hair is not one anybody navigates, and
    // treating it as navigable would flip the rail on and off as its own content
    // settles.
    scrollable.by = 1;
    await loadTurnRail("c-hair");
    await frames();
    expect(markers()).toEqual([]);

    scrollable.by = 101;
    setResidentTurns([]);
    await frames();
    expect(markers()).toEqual(["1"]);
  });
});

// ---------------------------------------------------------------------------
// Which turn the reading line is in: the arithmetic is `rail-activation.ts`'s; these pin the
// wiring (table built from painted cards, scroll frames measure nothing, both end clamps).
// ---------------------------------------------------------------------------

describe("which turn the reading line is in", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;
  /** How many times a card has been measured, so a scroll frame's layout cost is
   *  observable rather than argued about. */
  let measures = 0;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
    measures = 0;
  });

  /** A turn card at a known top in the scroller's own frame. Detached and rect-faked:
   *  what the module needs from a card is its key and its box. */
  function card(n: number, top: number): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.setAttribute(KEY_ATTR, `m${String(n)}`);
    const rect = (): DOMRect => {
      measures++;
      return fakeRect(top, 400);
    };
    Object.defineProperty(e, "getBoundingClientRect", { configurable: true, value: rect });
    Object.defineProperty(e, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(top, 400)],
    });
    return e;
  }

  /** The label of the marker the rail marks current, or "" when none is. */
  function current(): string {
    return currentOf(rail);
  }

  async function seat(id: string, ns: number[]): Promise<Map<number, HTMLElement>> {
    vi.mocked(apiGet).mockResolvedValue({ turns: ns.map((n) => turn(n)) });
    await loadTurnRail(id);
    const cards = new Map(ns.map((n, i) => [n, card(n, i * 400)]));
    // A view attaches at its own restored offset, so seating a chat starts at the top
    // rather than inheriting whatever the previous case scrolled to.
    scrollable.el.scrollTop = 0;
    setResidentTurns([...cards.values()]);
    await frames();
    return cards;
  }

  function at(cards: Map<number, HTMLElement>, n: number): HTMLElement {
    const c = cards.get(n);
    if (c === undefined) {
      throw new Error(`no card for turn ${String(n)}`);
    }
    return c;
  }

  async function scrollTo(px: number): Promise<void> {
    scrollable.el.scrollTop = px;
    scrollable.fire("scroll");
    await frames();
  }

  it("names the turn the reading line has entered", async () => {
    await seat("c-a", [1, 2, 3]);

    await scrollTo(450);

    expect(current()).toBe("2");
  });

  // Written as the top CLAMP, not as "all three are visible, expect 1": at the top
  // of a transcript the reading line can sit above the first card entirely.
  it("names the first turn at the very top of the transcript", async () => {
    await seat("c-a", [1, 2, 3]);

    expect(current()).toBe("1");
  });

  it("names the last turn at the very bottom of the transcript", async () => {
    await seat("c-a", [1, 2, 3]);
    scrollable.atLiveEdge = true;

    await scrollTo(500);

    expect(current()).toBe("3");
  });

  it("answers from the cached table with no layout read per scroll frame", async () => {
    await seat("c-a", [1, 2, 3]);
    const afterBuild = measures;
    expect(afterBuild).toBeGreaterThan(0);

    for (const px of [100, 420, 460, 830, 900]) {
      await scrollTo(px);
    }

    // The table is a cache invalidated by mutation and reflow, never by a scroll: a
    // position stored in the scroller's own frame does not move when it scrolls.
    expect(measures).toBe(afterBuild);
    expect(current()).toBe("3");
  });

  it("drops the previous chat's cards when re-pointed", async () => {
    const a = await seat("c-a", [1, 2, 3]);
    await scrollTo(830);
    expect(current()).toBe("3");
    expect(a.size).toBe(3);

    pointTurnRail("c-b");
    await seat("c-b", [1, 2]);

    // Never "3": chat B has no turn 3, and a leftover card would name a turn it
    // does not have.
    expect(current()).toBe("1");
  });

  it("drops a departed turn from the table and re-answers without it", async () => {
    const cards = await seat("c-a", [1, 2, 3]);
    await scrollTo(830);
    expect(current()).toBe("3");

    // Turn 3's card leaves the transcript. Nothing else re-derives the mark, so the
    // paint's own invalidation has to re-answer or the marker stays on a turn that
    // is no longer mounted.
    setResidentTurns([at(cards, 1), at(cards, 2)]);
    await frames();

    expect(current()).toBe("2");
  });

  it("re-answers after a reflow that moved a top with no scroll behind it", async () => {
    const cards = await seat("c-a", [1, 2, 3]);
    await scrollTo(830);
    expect(current()).toBe("3");

    // `content-visibility: auto` on `.msg-row` makes a card swapping its estimated
    // height for its real one move every top below it, with no DOM change and no
    // scroll. The reading line then sits in a different turn.
    Object.defineProperty(at(cards, 3), "getClientRects", {
      configurable: true,
      value: () => [fakeRect(2000, 400)],
    });
    Object.defineProperty(at(cards, 3), "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(2000, 400),
    });
    scrollable.contentResize?.();
    await frames();

    expect(current()).toBe("2");
  });

  it("re-answers when a parked view takes the scroller back", async () => {
    const cards = await seat("c-a", [1, 2, 3]);
    await scrollTo(830);
    expect(current()).toBe("3");

    // The reader switches away and back. An unpark restores a saved scrollTop
    // against cards the residency pass re-measured while the view was parked, and it
    // is neither a DOM mutation nor a card resize, so nothing else re-asks.
    Object.defineProperty(at(cards, 3), "getClientRects", {
      configurable: true,
      value: () => [fakeRect(2000, 400)],
    });
    Object.defineProperty(at(cards, 3), "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(2000, 400),
    });
    scrollable.attach?.();
    await frames();

    expect(current()).toBe("2");
  });
});

// ---------------------------------------------------------------------------
// The per-chat record: a switch back to a loaded chat costs zero fetches. Each chat's index is kept
// with the sync epoch and turn count captured BEFORE the request; an activation fetches only when
// the record is missing, pre-gap, pre-count, or overruled by `force`.
// ---------------------------------------------------------------------------

describe("the map's record gates the activation fetch", () => {
  const host = document.createElement("div");

  beforeAll(() => {
    mountRail(host);
  });

  function markers(): string[] {
    const rail = document.querySelector<HTMLElement>(".turn-map");
    return rail === null ? [] : rowLinks(rail).map(turnOf);
  }

  function session(id: string, turnCount: number): Session {
    return {
      id,
      name: id,
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      usage: {
        context_pct: 0,
        context_size: 0,
        credits: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
      ...residentTurns([]),
      turn_count: turnCount,
      has_more: false,
      thinking: false,
      working_label: "Thinking",
    };
  }

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
    setSessions([session("c-a", 2), session("c-b", 0)]);
    // Per-chat answers: c-a is the two-turn session under test, c-b an empty
    // sibling — a blanket answer would paint c-b's first fetch with c-a's turns.
    vi.mocked(apiGet).mockImplementation((path: string) =>
      Promise.resolve(path.includes("c-a") ? { turns: [turn(1), turn(2)] } : { turns: [] }),
    );
  });

  it("paints a recorded chat from memory, with no fetch", async () => {
    await loadTurnRail("c-a");
    expect(markers()).toEqual(["1", "2"]);

    pointTurnRail("c-b");
    expect(markers()).toEqual([]);

    vi.mocked(apiGet).mockClear();
    await loadTurnRail("c-a");
    expect(markers()).toEqual(["1", "2"]);
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("force refetches even when the record is current", async () => {
    // The caller's arm of the gate: an activation that found the TRANSCRIPT
    // stale (eviction, first load) refetches the rail with it, however current
    // the rail's own record looks.
    await loadTurnRail("c-a");
    vi.mocked(apiGet).mockClear();

    await loadTurnRail("c-a", { force: true });
    expect(apiGet).toHaveBeenCalledTimes(1);
  });

  it("a moved turn count invalidates the record", async () => {
    await loadTurnRail("c-a");
    // A background turn lands while the rail points elsewhere: an entry frame moves
    // the chat's turn count, and the record now describes an older session.
    get("c-a")!.turn_count = 3;
    pointTurnRail("c-b");

    vi.mocked(apiGet).mockClear();
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });
    await loadTurnRail("c-a");
    expect(apiGet).toHaveBeenCalledTimes(1);
    expect(markers()).toEqual(["1", "2", "3"]);
  });

  it("a whole reconcile invalidates the record", async () => {
    // The index's GET carries no digest stamp, so the reconcile body drops the records
    // by hand and the next activation refetches.
    await loadTurnRail("c-a");
    invalidateTurnRails();

    vi.mocked(apiGet).mockClear();
    await loadTurnRail("c-a");
    expect(apiGet).toHaveBeenCalledTimes(1);
  });

  it("records a background refresh without painting it, so the next activation is free", async () => {
    // The turn_closed door for a chat the rail points away from: the fetched
    // index is recorded for that chat's next activation but must not paint over
    // the pointed chat's rail.
    await loadTurnRail("c-b");
    expect(markers()).toEqual([]);

    await refreshTurnRail("c-a");
    expect(markers()).toEqual([]);

    vi.mocked(apiGet).mockClear();
    await loadTurnRail("c-a");
    expect(markers()).toEqual(["1", "2"]);
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("keeps the stale record on a failed refetch, so the next activation retries", async () => {
    await loadTurnRail("c-a");
    get("c-a")!.turn_count = 3;
    pointTurnRail("c-b");

    // The refetch the moved count demands fails; the record must not be
    // rewritten into currency by it.
    vi.mocked(apiGet).mockClear();
    vi.mocked(apiGet).mockResolvedValue(null);
    await loadTurnRail("c-a");
    expect(apiGet).toHaveBeenCalledTimes(1);

    pointTurnRail("c-b");
    await loadTurnRail("c-a");
    expect(apiGet).toHaveBeenCalledTimes(2);
  });

  it("renders a purged chat empty rather than from its dead record", async () => {
    await loadTurnRail("c-a");
    expect(markers()).toEqual(["1", "2"]);

    // The chat leaves the store (closed tab, deleted record) while its rail
    // record survives; re-pointing prunes the record rather than painting it.
    setSessions([session("c-b", 0)]);
    pointTurnRail("c-b");
    pointTurnRail("c-a");
    expect(markers()).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// The set is what the transcript HOLDS, extended backwards by the index, which is never refetched
// at turn START (`rail-merge.ts`).
// ---------------------------------------------------------------------------

describe("the newest turn needs no fetch", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  function markers(): string[] {
    return rowLinks(rail).map(turnOf);
  }

  it("paints a resident turn the index has never seen", async () => {
    // Turn 3 is streaming, so the index the rail holds names only turns 1 and 2.
    const live = residentTurns(["m1", "m2"]);
    live.turns.set("m3", residentTurn("m3", 3, { open: true }));
    live.turn_order.push("m3");
    setSessions([
      {
        id: "c-live",
        name: "c-live",
        model: "",
        acp_session_id: "",
        current_mode_id: "",
        usage: {
          context_pct: 0,
          context_size: 0,
          credits: 0,
          last_turn_ms: 0,
          has_real_data: false,
        },
        ...live,
        turn_count: 3,
        has_more: false,
        thinking: false,
        working_label: "Thinking",
      },
    ]);
    setActive("c-live");
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });

    await loadTurnRail("c-live");

    expect(markers()).toEqual(["1", "2", "3"]);
  });
});

// ---------------------------------------------------------------------------
// Which CARD a marker jumps to: `TurnSummary.n` is session-absolute while a card's `turn-{n}` id is
// window-local, so addressing by the marker's number missed by the paged-out count.
// ---------------------------------------------------------------------------

describe("which card a row jumps to", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;
  /** The transcript the rail scopes its lookup to. */
  let view: HTMLElement;
  const mountedBodies: string[] = [];

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
    mountedBodies.length = 0;
    view = document.createElement("div");
    view.className = "transcript-view";
    document.body.appendChild(view);
    initTurnRailCallbacks({
      mountTurnBody: (_chat, turnID) => {
        mountedBodies.push(turnID);
        return Promise.resolve();
      },
      activeView: () => view,
    });
  });

  afterEach(() => {
    view.remove();
  });

  /** A resident card in the transcript: the WINDOW-LOCAL permalink id `messages.ts`
   *  writes, plus the reconcile key the rail joins on. The two disagreeing is the
   *  whole subject of this block. */
  function residentCard(windowN: number, messageID: string): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.id = `turn-${String(windowN)}`;
    e.setAttribute(KEY_ATTR, messageID);
    view.appendChild(e);
    return e;
  }

  function marker(n: number): HTMLAnchorElement {
    const btn = rowLinks(rail).find((b) => turnOf(b) === String(n));
    if (btn === undefined) {
      throw new Error(`no marker for turn ${String(n)}`);
    }
    return btn;
  }

  function paged(id: string, ids: string[], hasMore: boolean): Session {
    return {
      id,
      name: id,
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      usage: {
        context_pct: 0,
        context_size: 0,
        credits: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
      ...residentTurns(ids),
      turn_count: ids.length,
      has_more: hasMore,
      thinking: false,
      working_label: "Thinking",
    };
  }

  /** Wait out the whole operation: the body build, one frame, the scroll and its
   *  settle. The epoch's close is the one observable every exit reaches, including
   *  the exits that scroll nowhere. */
  async function settleJump(): Promise<void> {
    await vi.waitFor(() => {
      expect(scrollable.epochs).toContain("end");
    });
  }

  it("lands on the clicked turn's own card, not the one holding that window ordinal", async () => {
    // The session has 10 turns; the store holds absolute 5..10
    // as window ordinals 1..6. So `#turn-6` exists and is absolute turn TEN.
    vi.mocked(apiGet).mockResolvedValue({
      turns: Array.from({ length: 10 }, (_, i) => turn(i + 1)),
    });
    await loadTurnRail("c-paged");
    const wanted = residentCard(2, "m6");
    for (const [i, key] of ["m5", "m7", "m8", "m9", "m10"].entries()) {
      residentCard(i === 0 ? 1 : i + 2, key);
    }

    marker(6).click();
    await settleJump();

    expect(mountedBodies).toEqual(["m6"]);
    expect(wanted.dataset["railTarget"]).toBe("");
    // And the id it does NOT use, spelled out so a reader sees the two spaces.
    expect(view.querySelector("#turn-6")?.getAttribute(KEY_ATTR)).toBe("m10");
  });

  it("scrolls with the platform's own animation, and instantly under reduced motion", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-paged");
    residentCard(1, "m1");
    residentCard(2, "m2");

    marker(2).click();
    await settleJump();

    // ONE animation, and a correction never starts a second: `auto` is the only
    // behavior a correction may use.
    expect(scrollable.landings[0]?.behavior).toBe("smooth");
    expect(scrollable.landings.slice(1).every((l) => l.behavior === "auto")).toBe(true);
  });

  it("resolves inside the ACTIVE view, not a parked one", async () => {
    // Under the multiplexer a parked chat's cards stay resident, so the same
    // reconcile key exists once per view and a document-wide query answers in
    // document order — which is the parked one when it was mounted first.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-paged");
    const parked = document.createElement("div");
    parked.className = "transcript-view";
    const decoy = document.createElement("div");
    decoy.className = "turn";
    decoy.setAttribute(KEY_ATTR, "m2");
    parked.appendChild(decoy);
    // BEFORE the active view in document order, which is what makes the case bite.
    document.body.insertBefore(parked, view);
    const wanted = residentCard(2, "m2");

    marker(2).click();
    await settleJump();

    expect(wanted.dataset["railTarget"]).toBe("");
    expect(decoy.dataset["railTarget"]).toBeUndefined();
    parked.remove();
  });

  it("pages history in first, and says so while it waits", async () => {
    // The path the wrong-card lookup made unreachable: a resident neighbour always
    // resolved, so the fetch never ran and the pending marker never appeared for
    // the one case it exists for.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(5), turn(6), turn(7), turn(8)] });
    await loadTurnRail("c-page-in");
    setSessions([paged("c-page-in", ["m8"], true)]);
    setActive("c-page-in");
    residentCard(1, "m8");

    let pendingWhileWaiting = false;
    vi.mocked(loadMessages).mockImplementation(async () => {
      pendingWhileWaiting = isPending(marker(6));
      const s = get("c-page-in");
      if (s !== undefined) {
        prependTurns(s, ["m6", "m7"]);
      }
      residentCard(2, "m6");
      await Promise.resolve();
      // The real signature's answer: whether a page landed. The rail does not read
      // it — it re-inspects the store instead — but the mock has to be honest about
      // the shape or the type gate cannot check the call site.
      return true;
    });

    marker(6).click();
    await settleJump();

    expect(pendingWhileWaiting).toBe(true);
    expect(vi.mocked(loadMessages)).toHaveBeenCalledTimes(1);
    // The landing turn's body is built on demand, because a paginated landing is a
    // tier-3 stub — and it is built BEFORE anything scrolls, so the build's own
    // scroller write cannot land mid-animation.
    expect(mountedBodies).toEqual(["m6"]);
    expect(scrollable.landings.length).toBeGreaterThan(0);
    // The pending state is a fetch in flight, so it has to be gone afterwards.
    expect(isPending(marker(6))).toBe(false);
  });

  it("scrolls nowhere when the turn is neither resident nor reachable", async () => {
    // `has_more: false` and the target absent: the store cannot produce it, so the
    // loop stops rather than spinning, and the marker must not be left pending.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-gone");
    setSessions([paged("c-gone", ["m2"], false)]);
    setActive("c-gone");
    residentCard(1, "m2");

    marker(1).click();
    // The click's own render, so the wait below has something real to wait FOR — a
    // `waitFor` on a state that was never entered passes on its first poll and
    // asserts nothing.
    expect(isPending(marker(1))).toBe(true);
    await settleJump();

    expect(scrollable.landings).toEqual([]);
    expect(vi.mocked(loadMessages)).not.toHaveBeenCalled();
    // The pending state means a fetch is in flight, so a dead end has to clear it —
    // a marker left pulsing forever is the same silence the state exists to break.
    expect(isPending(marker(1))).toBe(false);
  });

  it("does not let a superseded jump close the epoch the second one opened", async () => {
    // TWO CLICKS INSIDE ONE PAGING BUDGET: unguarded, the first jump's exit closed the SECOND's epoch
    // and `autoScrollIfAnchored` took the reader to the live edge.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-overlap");
    setSessions([paged("c-overlap", ["m2"], true)]);
    setActive("c-overlap");
    const target = residentCard(2, "m2");

    // The FIRST jump's turn is off the resident window, so it holds inside the paging
    // door until the test releases it — and its page then reports no progress, which
    // is the early return that takes it straight to its own `finally`.
    let releaseFirst: (() => void) | undefined;
    const paging = new Promise<void>((resolve) => {
      releaseFirst = () => {
        resolve();
      };
    });
    vi.mocked(loadMessages).mockImplementation(async () => {
      await paging;
      return false;
    });

    // The second card measures as MOVING, so the first's release at the second measurement lands its
    // exit INSIDE the second's flight; by the third it has provably run.
    const epochsWhenFirstExited: string[] = [];
    let measured = 0;
    Object.defineProperty(target, "getBoundingClientRect", {
      configurable: true,
      value: () => {
        measured += 1;
        if (measured === 2) {
          releaseFirst?.();
        }
        if (measured === 3) {
          epochsWhenFirstExited.push(...scrollable.epochs);
        }
        // Settles from the fourth measurement, so the loop converges rather than
        // spending its whole budget.
        return fakeRect(Math.min(measured, 3) * 1000, 400);
      },
    });
    Object.defineProperty(target, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(0, 400)],
    });

    marker(1).click();
    marker(2).click();
    await vi.waitFor(() => {
      expect(epochsWhenFirstExited.length).toBeGreaterThan(0);
    });

    // The second jump's epoch is still the only one, and still open.
    expect(epochsWhenFirstExited).toEqual(["begin"]);

    await settleJump();
    // One bracket for the two clicks, and the landing the second click asked for.
    expect(scrollable.epochs).toEqual(["begin", "end"]);
    expect(target.dataset["railTarget"]).toBe("");
    expect(currentOf(rail)).toBe("2");
  });

  it("lets a second click on a paging row be the jump already in flight", async () => {
    // A second click on a still-fetching marker must not CLAIM the generation, or the awaited jump is
    // superseded and never scrolls.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-double-click");
    setSessions([paged("c-double-click", ["m2"], true)]);
    setActive("c-double-click");
    residentCard(2, "m2");
    vi.mocked(loadMessages).mockImplementation(async () => {
      const s = get("c-double-click");
      if (s !== undefined) {
        prependTurns(s, ["m1"]);
      }
      const landed = residentCard(1, "m1");
      await Promise.resolve();
      return landed !== null;
    });

    marker(1).click();
    marker(1).click();
    await settleJump();

    expect(vi.mocked(loadMessages)).toHaveBeenCalledTimes(1);
    expect(mountedBodies).toEqual(["m1"]);
    expect(scrollable.landings.length).toBeGreaterThan(0);
    expect(
      view.querySelector<HTMLElement>('[data-reconcile-key="m1"]')?.dataset["railTarget"],
    ).toBe("");
    expect(isPending(marker(1))).toBe(false);
  });

  it("stops a superseded jump's corrections from writing the scroller", async () => {
    // The other half of the same guard. A correction loop that keeps running after a
    // second click re-measures ITS card and writes a landing the reader has already
    // left, so the two jumps fight over the scroller for the rest of the budget.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-overlap-corrections");
    const first = residentCard(1, "m1");
    const second = residentCard(2, "m2");
    let measured = 0;
    Object.defineProperty(first, "getBoundingClientRect", {
      configurable: true,
      value: () => {
        measured += 1;
        // The second click lands INSIDE the first jump's correction loop, which is
        // the only place a stale correction can be observed at all.
        if (measured === 2) {
          marker(2).click();
        }
        return fakeRect(measured * 1000, 400);
      },
    });
    Object.defineProperty(first, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(0, 400)],
    });
    // A landing of its own that nothing else can produce, so the sequence below says
    // which jump wrote what.
    Object.defineProperty(second, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(7000, 400),
    });
    Object.defineProperty(second, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(7000, 400)],
    });

    marker(1).click();
    await vi.waitFor(() => {
      expect(scrollable.landings.some((l) => l.px === 7000)).toBe(true);
    });
    await settleJump();

    // The first jump's aim and the correction it was already inside both stand; its
    // third measurement never happens.
    expect(scrollable.landings.map((l) => l.px)).toEqual([1000, 2000, 7000]);
  });

  it("closes the epoch on every exit, including the one that scrolls nowhere", async () => {
    // An epoch left open suspends pagination and silences every reader gesture, so
    // the close sits in a `finally` rather than after the scroll.
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await loadTurnRail("c-epoch");
    setSessions([paged("c-epoch", ["m2"], false)]);
    setActive("c-epoch");

    marker(1).click();
    await settleJump();

    expect(scrollable.epochs).toEqual(["end"]);
  });
});

// ---------------------------------------------------------------------------
// A click always produces a reaction: with the target already in view `scrollIntoView` is a no-op
// (no scroll, no pick), so the click itself must move `currentN`.
// ---------------------------------------------------------------------------

describe("a click always produces a reaction", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;
  let view: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
    view = document.createElement("div");
    view.className = "transcript-view";
    document.body.appendChild(view);
    initTurnRailCallbacks({
      mountTurnBody: () => Promise.resolve(),
      activeView: () => view,
    });
  });

  afterEach(() => {
    view.remove();
  });

  function residentCard(n: number, top: number): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.id = `turn-${String(n)}`;
    e.setAttribute(KEY_ATTR, `m${String(n)}`);
    Object.defineProperty(e, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(top, 400),
    });
    Object.defineProperty(e, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(top, 400)],
    });
    view.appendChild(e);
    return e;
  }

  function marker(n: number): HTMLAnchorElement {
    const btn = rowLinks(rail).find((b) => turnOf(b) === String(n));
    if (btn === undefined) {
      throw new Error(`no marker for turn ${String(n)}`);
    }
    return btn;
  }

  async function settleJump(): Promise<void> {
    await vi.waitFor(() => {
      expect(scrollable.epochs).toContain("end");
    });
  }

  /** Turns 2 and 3 resident, the reading line at the top — so activation names turn
   *  2 and a click on turn 3 has almost nothing to scroll to. The reported scene. */
  async function bothVisible(): Promise<{ two: HTMLElement; three: HTMLElement }> {
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });
    await loadTurnRail("c-both");
    const two = residentCard(2, 0);
    const three = residentCard(3, 400);
    setResidentTurns([two, three]);
    await frames();
    return { two, three };
  }

  it("marks the clicked row even when the scroll cannot move", async () => {
    await bothVisible();
    expect(currentOf(rail)).toBe("2");

    marker(3).click();
    await settleJump();

    expect(rowOf(marker(3)).dataset["current"]).toBe("");
    expect(marker(3).getAttribute("aria-current")).toBe("location");
    expect(rowOf(marker(2)).dataset["current"]).toBeUndefined();
    expect(marker(2).getAttribute("aria-current")).toBeNull();
  });

  it("claims exactly one position, on exactly one row", async () => {
    await bothVisible();
    expect(rail.querySelectorAll("[data-current]")).toHaveLength(1);

    marker(3).click();
    await settleJump();
    expect(rail.querySelectorAll("[data-current]")).toHaveLength(1);
    expect(currentOf(rail)).toBe("3");

    scrollable.readerGesture?.();
    expect(rail.querySelectorAll("[data-current]")).toHaveLength(1);
  });

  it("keeps exactly one link claiming to be the current location", async () => {
    await bothVisible();
    marker(3).click();
    await settleJump();

    expect(rail.querySelectorAll("[aria-current]")).toHaveLength(1);
    expect(rail.querySelector('[aria-current="location"]')).toBe(marker(3));
  });

  it("marks the pick even when it IS the turn activation already names", async () => {
    await bothVisible();
    marker(2).click();
    await settleJump();

    expect(currentOf(rail)).toBe("2");
    expect(rail.querySelectorAll("[data-current]")).toHaveLength(1);
  });

  it("flashes the landing card, then takes the ring away", async () => {
    await bothVisible();
    const three = view.querySelector<HTMLElement>('[data-reconcile-key="m3"]');

    marker(3).click();
    await settleJump();
    expect(three?.dataset["railTarget"]).toBe("");

    await vi.waitFor(() => {
      expect(three?.dataset["railTarget"]).toBeUndefined();
    });
  });

  it("moves the ring to the newest landing rather than letting the first timer strip it", async () => {
    await bothVisible();
    marker(3).click();
    await settleJump();
    marker(2).click();
    await vi.waitFor(() => {
      expect(
        view.querySelector<HTMLElement>('[data-reconcile-key="m2"]')?.dataset["railTarget"],
      ).toBe("");
    });

    // A shared timer would have fired on the FIRST click's deadline and cleared the
    // card the reader just landed on.
    expect(
      view.querySelector<HTMLElement>('[data-reconcile-key="m3"]')?.dataset["railTarget"],
    ).toBeUndefined();
  });

  it("hands tracking back on a reader gesture", async () => {
    await bothVisible();
    marker(3).click();
    await settleJump();
    expect(currentOf(rail)).toBe("3");

    // Through the real seam: scroll.ts's reader-gesture callback. Which writes publish it is pinned in
    // `scroll.test.ts`.
    scrollable.readerGesture?.();
    scrollable.el.scrollTop = 0;
    scrollable.fire("scroll");
    await frames();

    // The pick would have held turn 3 here; with it revoked the scroll offset decides.
    expect(currentOf(rail), "the scroll-derived turn takes the mark back").toBe("2");
  });

  it("drops a pick the arriving index no longer names", async () => {
    // THE REWIND: a pick held by an opening-message id the reverted index no longer carries must drop,
    // or the map marks no row at all.
    const { two } = await bothVisible();
    marker(3).click();
    await settleJump();
    expect(currentOf(rail)).toBe("3");

    setResidentTurns([two]);
    await frames();
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2)] });
    await refreshTurnRail("c-both");

    expect(currentOf(rail)).toBe("2");
    expect(rail.querySelectorAll("[data-current]")).toHaveLength(1);
    expect(rail.querySelectorAll("[aria-current]")).toHaveLength(1);
  });

  it("keeps a pick the arriving index still names", async () => {
    // The control: a refresh alone must not revoke the pick, since the index refetches every turn end.
    await bothVisible();
    marker(3).click();
    await settleJump();

    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3), turn(4)] });
    await refreshTurnRail("c-both");

    expect(currentOf(rail), "activation alone would say 2").toBe("3");
  });

  it("follows the picked turn through a renumbering rather than the number it wore", async () => {
    // The pick is an id, so an index that renumbers the same turns moves the mark WITH the turn.
    await bothVisible();
    marker(3).click();
    await settleJump();
    expect(currentOf(rail)).toBe("3");

    setSessions([]);
    vi.mocked(apiGet).mockResolvedValue({
      turns: [turn(1, { id: "m2" }), turn(2, { id: "m3" })],
    });
    await refreshTurnRail("c-both");

    expect(currentOf(rail)).toBe("2");
    expect(rail.querySelectorAll("[aria-current]")).toHaveLength(1);
  });

  it("survives a table rebuild that moves activation to a different turn", async () => {
    // Deliberately NOT cleared by activation moving: a streaming turn's own growth
    // moves the reading line's answer with no reader gesture behind it.
    const { two, three } = await bothVisible();
    marker(2).click();
    await settleJump();
    expect(currentOf(rail)).toBe("2");

    scrollable.el.scrollTop = 500;
    setResidentTurns([two, three]);
    await frames();

    expect(currentOf(rail), "the scroll offset alone would say 3").toBe("2");
  });

  it("drops the selection on a chat switch", async () => {
    await bothVisible();
    marker(3).click();
    await settleJump();

    pointTurnRail("c-other");
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(2), turn(3)] });
    await refreshTurnRail("c-other");

    // The other chat carries the same ids, so a kept pick would still mark turn 3.
    expect(rail.querySelector("[data-current]")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// What a row SAYS (composed in `rail-labels.test.ts`): published as the accessible name and the styled
// preview, with no native `title`, which skipped the styled tooltip and `aria-describedby`.
// ---------------------------------------------------------------------------

describe("what a turn-map row says", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  it("names the map Turns", () => {
    expect(rail.tagName).toBe("NAV");
    expect(rail.getAttribute("aria-label")).toBe("Turns");
  });

  it("carries data-tooltip and no native title on every row", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(12) });
    await loadTurnRail("c-say");

    const all = rowLinks(rail);
    expect(all).toHaveLength(12);
    for (const row of all) {
      expect(row.getAttribute("title")).toBeNull();
      expect(row.getAttribute("data-tooltip")).not.toBe("");
      expect(row.getAttribute("aria-label")).not.toBe("");
    }
  });

  it("names an agent-initiated turn and says what it is in the preview", async () => {
    vi.mocked(apiGet).mockResolvedValue({
      turns: [turn(1, { first_line: "do it" }), turn(2, { agent_initiated: true })],
    });
    await loadTurnRail("c-agent");
    const [user, agent] = rowLinks(rail);

    expect(agent === undefined ? undefined : rowOf(agent).dataset["trigger"]).toBe("system");
    expect(agent?.getAttribute("aria-label")).toBe("Turn 2, agent-initiated");
    expect(agent?.getAttribute("data-tooltip")).toBe("#2 \u00b7 Completed\nAgent-initiated turn");
    expect(user?.getAttribute("aria-label")).toBe("Turn 1: do it");
    expect(user?.getAttribute("data-tooltip")).toBe("#1 \u00b7 Completed\ndo it");
  });

  it("shows a duration the index carries, for a turn the store does not hold", async () => {
    vi.mocked(apiGet).mockResolvedValue({
      turns: [turn(1, { first_line: "do it", outcome: "failed", elapsed_ms: 92_000 })],
    });
    await loadTurnRail("c-dur");

    const [row] = rowLinks(rail);
    expect(row?.getAttribute("data-tooltip")).toBe("#1 \u00b7 Failed \u00b7 1m 32s\ndo it");
    expect(row?.getAttribute("aria-label")).toBe("Turn 1, failed: do it");
  });

  it("links every row to its turn, so copy-link and a new tab work", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(3) });
    await loadTurnRail("c-links");

    expect(rowLinks(rail).map((a) => a.getAttribute("href"))).toEqual([
      "/chat/c-links#turn-1",
      "/chat/c-links#turn-2",
      "/chat/c-links#turn-3",
    ]);
  });
});

// ---------------------------------------------------------------------------
// Past density the map BINS consecutive turns into one row, and no turn is dropped.
// ---------------------------------------------------------------------------

describe("a long session bins rather than drops", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  afterEach(async () => {
    stackOf(rail).style.removeProperty("height");
    trackOf(rail).style.removeProperty("height");
    trackOf(rail).style.removeProperty("display");
    await frames();
  });

  it("builds no rows while CSS stops drawing a map it drew before", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(40) });
    await loadTurnRail("c-hidden");
    await frames();
    expect(rowLinks(rail)).toHaveLength(40);

    // What a narrowing chat or a coarse pointer does to the track: no box, so its entry reads 0.
    trackOf(rail).style.display = "none";
    await frames();
    await frames();
    expect(rowLinks(rail)).toHaveLength(0);
    expect(rail.hasAttribute("data-shown")).toBe(true);

    trackOf(rail).style.removeProperty("display");
    await frames();
    await frames();
    expect(rowLinks(rail)).toHaveLength(40);
  });

  it("lays rows of four on a stack that holds a quarter of the turns, at their fractions", async () => {
    // 200 turns on a 100px stack: 50 rows at the 2px floor, four turns a row.
    trackOf(rail).style.height = "100px";
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(200) });
    await loadTurnRail("c-bins");
    await frames();
    await frames();

    const rows = rowLinks(rail);
    expect(rows).toHaveLength(50);
    expect(rows[0]?.getAttribute("aria-label")).toBe("Turns 1 to 4");
    expect(rows[49]?.getAttribute("aria-label")).toBe("Turns 197 to 200");
    expect(rows.map((a) => Number(rowOf(a).style.getPropertyValue("--rail-at")))).toEqual(
      Array.from({ length: 50 }, (_, i) => i / 49),
    );
    expect(stackOf(rail).style.getPropertyValue("--turn-map-slots")).toBe("50");
  });
});

// ---------------------------------------------------------------------------
// The mark is STICKY: it belongs to the row at or below the reading line, and once a turn is placed
// exactly one row is marked; nothing may clear it.
// ---------------------------------------------------------------------------

describe("the mark is sticky", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
  });

  afterEach(() => {
    stackOf(rail).style.removeProperty("height");
    trackOf(rail).style.removeProperty("height");
  });

  function card(key: string, top: number): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.setAttribute(KEY_ATTR, key);
    Object.defineProperty(e, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(top, 400),
    });
    Object.defineProperty(e, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(top, 400)],
    });
    return e;
  }

  function marked(): HTMLElement[] {
    return [...rail.querySelectorAll<HTMLElement>(".turn-pill[data-current]")];
  }

  async function scrollTo(px: number): Promise<void> {
    scrollable.el.scrollTop = px;
    scrollable.fire("scroll");
    await frames();
  }

  async function seatSixty(): Promise<void> {
    // A 30px track holds 15 rows, so the sixty turns bin four to a row.
    trackOf(rail).style.height = "30px";
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(60) });
    await loadTurnRail("c-sticky");
    setResidentTurns(Array.from({ length: 60 }, (_, i) => card(`m${String(i + 1)}`, i * 400)));
    await frames();
    await frames();
  }

  it("marks the bin holding the reading line's turn", async () => {
    await seatSixty();
    expect(rowLinks(rail)).toHaveLength(15);

    for (const k of [0, 3, 4, 17, 59]) {
      await scrollTo(k * 400);
      const m = marked();
      expect(m, `turn ${String(k + 1)}`).toHaveLength(1);
      const first = Math.floor(k / 4) * 4 + 1;
      expect(m[0]?.querySelector("a")?.getAttribute("aria-label"), `turn ${String(k + 1)}`).toBe(
        `Turns ${String(first)} to ${String(first + 3)}`,
      );
    }
  });

  it("keeps the mark when the table empties or the index is re-read", async () => {
    await seatSixty();
    await scrollTo(5 * 400);
    expect(marked()).toHaveLength(1);
    const before = marked()[0]?.querySelector("a")?.getAttribute("aria-label");

    setResidentTurns([]);
    await frames();
    expect(marked()).toHaveLength(1);

    vi.mocked(apiGet).mockResolvedValue({ turns: turns(60) });
    await refreshTurnRail("c-sticky");
    expect(marked()).toHaveLength(1);
    expect(marked()[0]?.querySelector("a")?.getAttribute("aria-label")).toBe(before);
  });

  it("marks the window's first turn when its opening message was paged out", async () => {
    // The window's first card is keyed by an id the index does not name, so only its
    // own `turn_open.n` can place it: the mark has to resolve it to turn 5 anyway.
    setSessions([
      {
        id: "c-frag",
        name: "c-frag",
        model: "",
        acp_session_id: "",
        current_mode_id: "",
        usage: {
          context_pct: 0,
          context_size: 0,
          credits: 0,
          last_turn_ms: 0,
          has_real_data: false,
        },
        turns: new Map([
          ["a5tail", residentTurn("a5tail", 5)],
          ["m6", residentTurn("m6", 6)],
        ]),
        turn_order: ["a5tail", "m6"],
        turn_count: 6,
        has_more: true,
        thinking: false,
        working_label: "Thinking",
      },
    ]);
    setActive("c-frag");
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(6) });
    await loadTurnRail("c-frag");
    setResidentTurns([card("a5tail", 0), card("m6", 400)]);
    await frames();

    await scrollTo(0);

    expect(marked()).toHaveLength(1);
    expect(currentOf(rail)).toBe("5");
  });
});

// ---------------------------------------------------------------------------
// The keyboard, the step buttons and the pointer: one tab stop, and every way in lands through the
// same jump.
// ---------------------------------------------------------------------------

describe("moving through the map", () => {
  const host = document.createElement("div");
  let rail: HTMLElement;
  let view: HTMLElement;

  beforeAll(() => {
    rail = mountRail(host);
  });

  beforeEach(() => {
    scrollable.reset();
    resetTurnRail();
    view = document.createElement("div");
    view.className = "transcript-view";
    document.body.appendChild(view);
    initTurnRailCallbacks({ mountTurnBody: () => Promise.resolve(), activeView: () => view });
  });

  afterEach(() => {
    view.remove();
    stackOf(rail).style.removeProperty("height");
    trackOf(rail).style.removeProperty("height");
  });

  function link(n: number): HTMLAnchorElement {
    const hit = rowLinks(rail).find((a) => turnOf(a) === String(n));
    if (hit === undefined) {
      throw new Error(`no row for turn ${String(n)}`);
    }
    return hit;
  }

  function tabStops(): string[] {
    return rowLinks(rail)
      .filter((a) => a.tabIndex === 0)
      .map(turnOf);
  }

  function residentCard(n: number, top: number): HTMLElement {
    const e = document.createElement("div");
    e.className = "turn";
    e.setAttribute(KEY_ATTR, `m${String(n)}`);
    Object.defineProperty(e, "getBoundingClientRect", {
      configurable: true,
      value: () => fakeRect(top, 400),
    });
    Object.defineProperty(e, "getClientRects", {
      configurable: true,
      value: () => [fakeRect(top, 400)],
    });
    view.appendChild(e);
    return e;
  }

  /** Thirty turns, the first three resident and the reading line in turn 1. */
  async function seat(): Promise<void> {
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(30) });
    await loadTurnRail("c-keys");
    setResidentTurns([residentCard(1, 0), residentCard(2, 400), residentCard(3, 800)]);
    await frames();
  }

  function key(target: HTMLElement, k: string): KeyboardEvent {
    const e = new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true });
    target.dispatchEvent(e);
    return e;
  }

  it("holds one tab stop, on the current row", async () => {
    await seat();
    expect(tabStops()).toEqual(["1"]);
  });

  it("moves the tab stop and focus by one, ten, and to either end", async () => {
    await seat();
    link(1).focus();
    for (const [k, want] of [
      ["ArrowDown", "2"],
      ["PageDown", "12"],
      ["End", "30"],
      ["ArrowDown", "30"],
      ["PageUp", "20"],
      ["ArrowUp", "19"],
      ["Home", "1"],
      ["ArrowUp", "1"],
    ] as const) {
      const active = document.activeElement;
      if (!(active instanceof HTMLElement)) {
        throw new Error("focus left the map");
      }
      const e = key(active, k);
      expect(e.defaultPrevented, k).toBe(true);
      expect(turnOf(document.activeElement), k).toBe(want);
      expect(tabStops(), k).toEqual([want]);
    }
  });

  it("leaves other keys to the browser", async () => {
    await seat();
    link(1).focus();
    expect(key(link(1), "a").defaultPrevented).toBe(false);
    expect(key(link(1), "Tab").defaultPrevented).toBe(false);
    expect(key(link(1), "Enter").defaultPrevented).toBe(false);
  });

  it("returns the tab stop to the current row once focus leaves", async () => {
    await seat();
    link(1).focus();
    key(link(1), "PageDown");
    expect(tabStops()).toEqual(["11"]);
    document.body.focus();
    link(11).blur();
    expect(tabStops()).toEqual(["1"]);
  });

  it("lands the focused row on a pressed Enter", async () => {
    await seat();
    link(1).focus();
    key(link(1), "ArrowDown");
    await userEvent.keyboard("{Enter}");
    await vi.waitFor(() => {
      expect(scrollable.epochs).toContain("end");
    });
    expect(currentOf(rail)).toBe("2");
  });

  it("hands focus back to the transcript on Escape", async () => {
    // The page's own scroller markup, so the focus target is only as focusable as index.html makes it.
    const page = new DOMParser().parseFromString(indexHtml, "text/html");
    const markup = page.getElementById("messages-wrap");
    if (markup === null) {
      throw new Error("index.html has no #messages-wrap");
    }
    const scroller = document.createElement("div");
    for (const a of markup.attributes) {
      scroller.setAttribute(a.name, a.value);
    }
    scroller.id = "messages-wrap-escape-target";
    document.body.appendChild(scroller);
    Object.assign(scrollable.el, {
      focus: (o?: FocusOptions) => {
        scroller.focus(o);
      },
    });
    try {
      await seat();
      link(1).focus();
      const e = key(link(1), "Escape");
      expect(e.defaultPrevented).toBe(true);
      expect(document.activeElement).toBe(scroller);
    } finally {
      scroller.remove();
    }
  });

  it("steps to the next and previous row, disabled at the ends", async () => {
    await seat();
    const prev = rail.querySelector<HTMLButtonElement>('.turn-map-step[data-dir="prev"]');
    const next = rail.querySelector<HTMLButtonElement>('.turn-map-step[data-dir="next"]');
    expect(prev?.disabled).toBe(true);
    expect(next?.disabled).toBe(false);

    next?.click();
    await vi.waitFor(() => {
      expect(scrollable.epochs).toContain("end");
    });
    expect(currentOf(rail)).toBe("2");
    expect(prev?.disabled).toBe(false);

    prev?.click();
    expect(currentOf(rail)).toBe("1");
  });

  it("lands the nearest row for a click in a gap between rows", async () => {
    // Turns 1 and 30 only, on a 300px stack: 30 slots of 10px, so a click 289px down sits in turn
    // 30's slot and one 12px down in turn 1's.
    trackOf(rail).style.height = "300px";
    stackOf(rail).style.height = "300px";
    vi.mocked(apiGet).mockResolvedValue({ turns: [turn(1), turn(30)] });
    await loadTurnRail("c-gap");
    await frames();
    await frames();
    const stack = stackOf(rail);
    const box = stack.getBoundingClientRect();

    stack.dispatchEvent(
      new MouseEvent("click", { bubbles: true, cancelable: true, clientY: box.top + 289 }),
    );
    expect(currentOf(rail)).toBe("30");

    stack.dispatchEvent(
      new MouseEvent("click", { bubbles: true, cancelable: true, clientY: box.top + 12 }),
    );
    expect(currentOf(rail)).toBe("1");
  });

  it("leaves a modified click to the link, so it can open in a new tab", async () => {
    await seat();
    const e = new MouseEvent("click", { bubbles: true, cancelable: true, ctrlKey: true });
    // Kept from navigating the test page, after the map has had its say.
    window.addEventListener("click", (ev) => ev.preventDefault(), { once: true });
    const decided = vi.fn((ev: Event) => ev.defaultPrevented);
    rail.addEventListener("click", decided, { once: true });
    link(3).dispatchEvent(e);
    expect(decided).toHaveReturnedWith(false);
    expect(currentOf(rail)).toBe("1");
  });

  it("says the map is busy while a jump pages history in", async () => {
    vi.mocked(apiGet).mockResolvedValue({ turns: turns(3) });
    await loadTurnRail("c-busy");
    setSessions([
      {
        id: "c-busy",
        name: "c-busy",
        model: "",
        acp_session_id: "",
        current_mode_id: "",
        usage: {
          context_pct: 0,
          context_size: 0,
          credits: 0,
          last_turn_ms: 0,
          has_real_data: false,
        },
        ...residentTurns(["m3"]),
        turn_count: 3,
        has_more: true,
        thinking: false,
        working_label: "Thinking",
      },
    ]);
    setActive("c-busy");
    // Every call's reading: a jump an earlier case left paging can call in after this one settles.
    const busyWhileWaiting: (string | null)[] = [];
    vi.mocked(loadMessages).mockImplementation(async () => {
      busyWhileWaiting.push(rail.getAttribute("aria-busy"));
      await Promise.resolve();
      return false;
    });

    link(1).click();
    await vi.waitFor(() => {
      expect(scrollable.epochs.length + vi.mocked(loadMessages).mock.calls.length).toBeGreaterThan(
        0,
      );
    });
    await vi.waitFor(() => {
      expect(rail.getAttribute("aria-busy")).toBeNull();
    });
    expect(busyWhileWaiting).toContain("true");
  });

  it("reads no layout while it renders", async () => {
    await seat();
    const rect = vi.spyOn(Element.prototype, "getBoundingClientRect");
    const computed = vi.spyOn(window, "getComputedStyle");
    try {
      vi.mocked(apiGet).mockResolvedValue({ turns: turns(31) });
      scrollable.geometry.reads = 0;
      await refreshTurnRail("c-keys");
      expect(rowLinks(rail)).toHaveLength(31);
      expect(scrollable.geometry.reads, "scroller geometry reads").toBe(0);
      expect(computed).not.toHaveBeenCalled();
      // The refresh re-answers activation from a rebuilt table, which measures the resident CARDS
      // (faked); the map's own nodes are never measured.
      for (const call of rect.mock.contexts) {
        expect(rail.contains(call as Node)).toBe(false);
      }
    } finally {
      rect.mockRestore();
      computed.mockRestore();
    }
  });
});
