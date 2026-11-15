// The server pre-pass that makes the DOM search honest: which URL it asks for,
// and what it does with the answer.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Hit, SearchResult } from "./wire/types.gen.js";
import { asked } from "./__test-helpers__/url.js";

const apiGetTyped = vi.fn<(url: string, decode: (v: unknown) => unknown) => Promise<unknown>>();
const openForSearch = vi.fn();
// The chat id is declared because fold-state.ts's clearSearchOpened takes one and
// the wrapper below forwards it; a nullary mock types its own call log as empty.
const clearSearchOpened = vi.fn((_chatID: string): string[] => ["u2"]);
// Both arguments captured: the answer, the reveal and the re-fold declare their render
// cause explicitly, and the assertion needs to see it.
const bumpMessages = vi.fn((_chatID: string, _cause?: string) => undefined);

vi.mock("./api-client.js", () => ({
  apiGetTyped: (url: string, decode: (v: unknown) => unknown) => apiGetTyped(url, decode),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and this name is imported somewhere in it. No case here calls it.
  apiGet: vi.fn(),
}));
vi.mock("./fold-state.js", () => ({
  openForSearch: (chatID: string, id: string) => openForSearch(chatID, id),
  clearSearchOpened: (chatID: string) => clearSearchOpened(chatID),
}));
vi.mock("./store.js", () => ({
  bumpMessages: (id: string, cause?: string) => bumpMessages(id, cause),
}));

const {
  runServerSearch,
  resetServerSearch,
  revealHitTurn,
  refoldAfterReveal,
  searchHitTurns,
  searchHitCount,
  initSearchRevealBuilder,
} = await import("./chat-search.js");

/** A hit, one item keyed by `[turn_id, entry_id, kind]`. `turn` is the turn's own `turn_open.n`, which is
 *  what the rail and the folded rows read. */
function hit(over: Partial<Hit> = {}): Hit {
  return {
    turn_id: "u1",
    entry_id: "e1",
    kind: "message",
    excerpt: "…retry…",
    segments: ["content"],
    turn: 2,
    ...over,
  };
}

/** `matched` defaults to the list's own length, which is the uncut answer; a case staging a cut sets
 *  it higher. */
function reply(hits: Hit[], over: Partial<SearchResult> = {}): SearchResult {
  return {
    matches: hits,
    spread: false,
    scanned: 4,
    matched: hits.length,
    truncated: false,
    ...over,
  };
}

/** Stage what the fetch answers, through the caller's OWN decoder; `null` is the failed fetch. */
function answer(body: SearchResult | null): void {
  apiGetTyped.mockImplementation((_url, decode) =>
    Promise.resolve(body === null ? null : decode(body)),
  );
}

/** The injected build and fold, declared once because they are MODULE state. */
const reveal = vi.fn((_chatID: string, _turnID: string, _entryID?: string) => Promise.resolve());
const fold = vi.fn((_chatID: string, _turnID: string) => undefined);

beforeEach(() => {
  apiGetTyped.mockReset();
  answer(reply([]));
  initSearchRevealBuilder(reveal, fold);
  resetServerSearch();
  openForSearch.mockClear();
  reveal.mockClear();
});

describe("runServerSearch: the request", () => {
  it("asks the chat's own search endpoint with the encoded query", async () => {
    await runServerSearch("c 1", "why retry?");
    expect(asked(String(apiGetTyped.mock.calls[0]?.[0]))).toEqual({
      path: "/api/chats/c%201/search",
      query: { q: "why retry?" },
    });
  });

  it("omits the case parameter by default, so an unset flag keeps the old behaviour", async () => {
    await runServerSearch("c1", "retry");
    expect(apiGetTyped.mock.calls[0]?.[0]).not.toContain("case=");
  });

  it("sends case=1 when the reader asked to match case", async () => {
    await runServerSearch("c1", "Retry", true);
    expect(apiGetTyped).toHaveBeenCalledWith(
      "/api/chats/c1/search?q=Retry&case=1",
      expect.any(Function),
    );
  });

  it("does not call the server for an empty chat id or a blank query", async () => {
    await runServerSearch("", "retry");
    await runServerSearch("c1", "   ");
    expect(apiGetTyped).not.toHaveBeenCalled();
  });
});

describe("runServerSearch: the answer", () => {
  it("opens and builds no hit's turn: only the hit find lands on is revealed", async () => {
    answer(reply([hit({ turn_id: "u1" }), hit({ turn_id: "u3", entry_id: "e2" })]));
    bumpMessages.mockClear();
    await runServerSearch("c1", "retry");
    expect(openForSearch).not.toHaveBeenCalled();
    expect(reveal).not.toHaveBeenCalled();
    // The folded rows' counts and the turn map repaint; nothing opened, so no shape changed.
    expect(bumpMessages).toHaveBeenCalledExactlyOnceWith("c1", "fact");
  });

  it("records the hit turns and their counts for the rail and the folded rows", async () => {
    answer(reply([hit({ turn: 2 }), hit({ turn: 2, entry_id: "e2" }), hit({ turn: 5 })]));
    await runServerSearch("c1", "retry");
    expect([...searchHitTurns()].sort((a, b) => a - b)).toEqual([2, 5]);
    expect(searchHitCount(2)).toBe(2);
    expect(searchHitCount(5)).toBe(1);
    expect(searchHitCount(9)).toBe(0);
  });

  it("leaves the previous reveal in place when the fetch fails", async () => {
    answer(reply([hit({ turn: 2 })]));
    await runServerSearch("c1", "retry");
    answer(null);
    const out = await runServerSearch("c1", "retry");
    // A failed fetch must not collapse turns out from under a reader mid-search:
    // the previous run's reveal and counts stay exactly as they were.
    expect(out).toBeNull();
    expect(searchHitCount(2)).toBe(1);
    expect([...searchHitTurns()]).toEqual([2]);
  });

  it("answers the server's envelope whole, tally included", async () => {
    // The reply travels as one value; a cut is `matched` exceeding the list.
    answer(reply([hit(), hit({ entry_id: "e2" })], { scanned: 24, matched: 347 }));
    expect(await runServerSearch("c1", "retry")).toEqual({
      matches: [hit(), hit({ entry_id: "e2" })],
      spread: false,
      scanned: 24,
      matched: 347,
      truncated: false,
    });
  });

  it("answers null when the fetch failed, so a caller can keep what it had", async () => {
    // An empty envelope means "matched nothing"; a failed request never said that.
    answer(null);
    expect(await runServerSearch("c1", "retry")).toBeNull();
  });

  it("answers an empty envelope for an empty question, because that IS an answer", async () => {
    // No chat or a blank query asked nothing, so the empty answer is the truth.
    const nothing = { matches: [], spread: false, scanned: 0, matched: 0, truncated: false };
    expect(await runServerSearch("", "retry")).toEqual(nothing);
    expect(await runServerSearch("c1", "   ")).toEqual(nothing);
    expect(apiGetTyped).not.toHaveBeenCalled();
  });

  it("drops the reveal and the counts on reset, declaring the re-fold's shape", async () => {
    answer(reply([hit({ turn: 2 })]));
    await runServerSearch("c1", "retry");
    clearSearchOpened.mockReturnValue(["u2", "u5"]);
    bumpMessages.mockClear();
    fold.mockClear();
    resetServerSearch();
    // At once, each turn search opened: a fold pass would wait for the reader to stop reading.
    expect(fold.mock.calls).toEqual([
      ["c1", "u2"],
      ["c1", "u5"],
    ]);
    expect([...searchHitTurns()]).toEqual([]);
    expect(searchHitCount(2)).toBe(0);
    expect(clearSearchOpened).toHaveBeenCalledWith("c1");
    // The re-fold un-mounts what the reveal pinned past the block budget: a
    // shape change, stated at the branch that knows.
    expect(bumpMessages).toHaveBeenCalledWith("c1", "shape");
  });

  it("repaints the counts away on reset when no turn was left to re-fold", async () => {
    answer(reply([hit({ turn: 2 })]));
    await runServerSearch("c1", "retry");
    clearSearchOpened.mockReturnValue([]);
    bumpMessages.mockClear();
    resetServerSearch();
    expect(bumpMessages).toHaveBeenCalledExactlyOnceWith("c1", "fact");
    clearSearchOpened.mockReturnValue(["u2"]);
  });

  it("undoes the reveal in the chat the SEARCH ran in, whatever is active at close", async () => {
    // Both effects name the chat the search ran in, not the active one.
    answer(reply([hit({ turn: 2 })]));
    await runServerSearch("c1", "retry");
    clearSearchOpened.mockClear();
    bumpMessages.mockClear();

    resetServerSearch();

    expect(clearSearchOpened).toHaveBeenCalledExactlyOnceWith("c1");
    expect(bumpMessages).toHaveBeenCalledWith("c1", "shape");
  });
});

describe("revealHitTurn: the per-hit reveal navigation runs before selecting", () => {
  it("opens the hit's turn, builds its body, and then declares the shape change", async () => {
    const order: string[] = [];
    openForSearch.mockImplementation((_chatID: string, id: string) => {
      order.push(`open:${id}`);
    });
    reveal.mockImplementation((_chatID: string, turnID: string) => {
      order.push(`build:${turnID}`);
      return Promise.resolve();
    });
    bumpMessages.mockImplementation(() => {
      order.push("bump");
    });
    await revealHitTurn("c1", hit({ turn_id: "u7" }));
    // The body must exist before the repaint that unfolds it, and the bump is the stated `shape`.
    expect(order).toEqual(["open:u7", "build:u7", "bump"]);
    expect(bumpMessages).toHaveBeenCalledWith("c1", "shape");
  });

  it("names the hit's own ENTRY, so the build can centre on it rather than the head", async () => {
    // The entry's IDENTITY crosses; without it every hit in a long turn builds the head.
    await revealHitTurn("c1", hit({ turn_id: "u7", entry_id: "e311" }));
    expect(reveal).toHaveBeenCalledExactlyOnceWith("c1", "u7", "e311");
  });

  it("names the entry for a header hit too, so the kind cannot move the build", async () => {
    // The kind decides what navigation does, never which element is built.
    await revealHitTurn("c1", hit({ turn_id: "u7", entry_id: "e4", kind: "header" }));
    expect(reveal).toHaveBeenCalledExactlyOnceWith("c1", "u7", "e4");
  });

  it("does nothing for a hit that names no turn", async () => {
    // This test asserts the CALL BELOW adds nothing.
    bumpMessages.mockClear();
    await revealHitTurn("c1", hit({ turn_id: "" }));
    expect(openForSearch).not.toHaveBeenCalled();
    expect(reveal).not.toHaveBeenCalled();
    expect(bumpMessages).not.toHaveBeenCalled();
  });

  it("does nothing without an active chat", async () => {
    bumpMessages.mockClear();
    await revealHitTurn("", hit());
    expect(openForSearch).not.toHaveBeenCalled();
    expect(reveal).not.toHaveBeenCalled();
    expect(bumpMessages).not.toHaveBeenCalled();
  });
});

describe("refoldAfterReveal: the reveal moved off a turn", () => {
  it("folds that turn at once, then declares the shape change", () => {
    const order: string[] = [];
    fold.mockImplementation((_chatID: string, turnID: string) => {
      order.push(`fold:${turnID}`);
    });
    bumpMessages.mockImplementation(() => {
      order.push("bump");
    });
    refoldAfterReveal("c1", "u4");
    expect(order).toEqual(["fold:u4", "bump"]);
    expect(bumpMessages).toHaveBeenCalledWith("c1", "shape");
  });
});
