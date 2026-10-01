// Property 12's STORE half: live and reload agree across a revert.
//
// A `turn_revert` frame is an ordinary append into its CARRIER plus two writes the
// envelope cannot state: the drop of every held turn the revert took, and the count
// the surviving high-water leaves behind. Both read `from_n` alone, which is why a
// partial window (turns 6 and 7 resident, the carrier paged out) is the case every
// arm below is shaped around: a rule spelled over `turn_order` POSITIONS answers it
// by dropping nothing, and a count merged through the header's `Math.max` answers it
// by keeping the pre-revert value, and both leave `derivedHasMore` armed forever.
//
// The real store and the real loader, with only the HTTP boundary faked: the abort
// arm asserts THROUGH the two registered hooks, so it exercises the wire `boot.ts`
// builds rather than a mock of the repair map.
import { describe, it, expect, beforeEach, vi } from "vitest";
import * as fc from "fast-check";
import { makeSession, makeTurn } from "./__test-helpers__/model.js";
import {
  appendEntry,
  derivedHasMore,
  get,
  openTurn,
  registerTurnRepair,
  registerRevertReadAbort,
  setSessions,
} from "./store.js";
import { abortReadsForRevert, loadMessages, requestTurnRange } from "./store-load.js";
import type { Entry, EntryTurnRevert } from "./wire/types.gen.js";
// A type-only import of the mocked module, so `importOriginal` can be typed without an
// inline `import()` annotation (the fleet forbids those) and without a runtime edge.
import type * as ApiClient from "./api-client.js";

const { mockApiGetTyped } = vi.hoisted(() => ({ mockApiGetTyped: vi.fn() }));

// Spread `importOriginal`: a whole-module factory drops every other export this
// module's graph reaches, which is the collection failure section 1.2 bans.
vi.mock("./api-client.js", async (importOriginal) => {
  const actual = await importOriginal<typeof ApiClient>();
  return { ...actual, apiGetTyped: mockApiGetTyped };
});

const CHAT = "c-1";

/** One `turn_revert` entry, in its carrier's lane-less position. `seq` is the
 *  carrier's next, which is what makes the hole check apply to it unchanged. */
function revert(carrier: string, seq: number, p: Partial<EntryTurnRevert> = {}): Entry {
  const payload: EntryTurnRevert = {
    from: "t-5",
    from_n: 5,
    through: "t-7",
    kas_message_id: "m-5",
    cause: "rewind",
    ...p,
  };
  return {
    id: `${payload.from}:revert`,
    turn: carrier,
    kind: "turn_revert",
    payload,
    seq,
    ts: 0,
  };
}

/** Turns `lo..hi` resident, each closed, with the session's count at `hi`. */
function seed(lo: number, hi: number): void {
  const turns = [];
  for (let n = lo; n <= hi; n++) {
    turns.push(makeTurn({ id: `t-${String(n)}`, n }));
  }
  setSessions([makeSession({ id: CHAT, turns, turn_count: hi, has_more: lo > 1 })]);
}

function heldOrdinals(): number[] {
  const s = get(CHAT);
  if (s === undefined) {
    return [];
  }
  return s.turn_order.map((id) => {
    const open = s.turns.get(id)?.entries[0];
    const payload = open?.payload as { n?: number } | undefined;
    return payload?.n ?? -1;
  });
}

describe("the revert handler", () => {
  let repaired: { chatID: string; turnID: string; afterSeq?: number }[] = [];

  beforeEach(() => {
    repaired = [];
    mockApiGetTyped.mockReset();
    registerTurnRepair((chatID, turnID, afterSeq) => {
      repaired.push(afterSeq === undefined ? { chatID, turnID } : { chatID, turnID, afterSeq });
    });
    registerRevertReadAbort(abortReadsForRevert);
  });

  it("drops by ORDINAL on a partial window, so turns above the cut go with it", () => {
    // The `?before=` paging case: the store legitimately holds the two newest turns
    // and not the one the revert names, so `from`'s POSITION in `turn_order` is -1.
    seed(6, 7);
    appendEntry(CHAT, revert("t-4", 0));

    expect(heldOrdinals()).toEqual([]);
    const s = get(CHAT);
    expect(s?.turn_count).toBe(4);
    expect(s?.has_more).toBe(derivedHasMore(4, 0));
  });

  it("asks for no range read for `from` NOR for the carrier it does not hold", () => {
    seed(6, 7);
    appendEntry(CHAT, revert("t-4", 0));

    // `appendEntry` classifies an entry naming an absent turn as a hole, and in the
    // partial-window case the envelope names exactly such a turn.
    expect(repaired).toEqual([]);
  });

  it("keeps the carrier and seats the record at its own seq", () => {
    seed(1, 3);
    appendEntry(CHAT, revert("t-2", 1, { from: "t-3", from_n: 3, through: "t-3" }));

    expect(heldOrdinals()).toEqual([1, 2]);
    const carrier = get(CHAT)?.turns.get("t-2");
    expect(carrier?.entries.at(-1)?.kind).toBe("turn_revert");
    expect(carrier?.entries.at(-1)?.seq).toBe(1);
    expect(get(CHAT)?.turn_count).toBe(2);
  });

  it("writes the surviving high-water on the no-survivor frame PAIR, 1 and not 0", () => {
    // The carrier's `turn_opened` arrives first (section 2.2 step 3 mints it at the
    // surviving high-water plus one), then the revert naming turn 1 of 7.
    seed(1, 7);
    openTurn(CHAT, {
      id: "t-c",
      turn: "t-c",
      kind: "turn_open",
      payload: { source: "revert", n: 1 },
      seq: 0,
      ts: 0,
    });
    appendEntry(CHAT, revert("t-c", 1, { from: "t-1", from_n: 1, through: "t-7" }));

    const s = get(CHAT);
    expect([...(s?.turns.keys() ?? [])]).toEqual(["t-c"]);
    expect(heldOrdinals()).toEqual([1]);
    expect(s?.turn_count).toBe(1);
    expect(s?.has_more).toBe(derivedHasMore(1, 1));
  });

  it("changes nothing on a re-delivered revert (the replay-ring case)", () => {
    seed(1, 3);
    appendEntry(CHAT, revert("t-2", 1, { from: "t-3", from_n: 3, through: "t-3" }));
    const after = { order: [...(get(CHAT)?.turn_order ?? [])], count: get(CHAT)?.turn_count };
    appendEntry(CHAT, revert("t-2", 1, { from: "t-3", from_n: 3, through: "t-3" }));

    expect(get(CHAT)?.turn_order).toEqual(after.order);
    expect(get(CHAT)?.turn_count).toBe(after.count);
    expect(repaired).toEqual([]);
  });

  it("aborts an in-flight range repair for a turn it just dropped, through the hook", async () => {
    seed(6, 7);
    let signal: AbortSignal | undefined;
    let release: (() => void) | undefined;
    mockApiGetTyped.mockImplementation(
      (_path: string, _decode: unknown, s: AbortSignal) =>
        new Promise((resolve) => {
          signal = s;
          release = () => {
            resolve({
              entries: [
                {
                  id: "t-6#1",
                  turn: "t-6",
                  kind: "text",
                  payload: { text: "late" },
                  seq: 1,
                  ts: 0,
                },
              ],
              open_entries: [],
              subject: [],
            });
          };
        }),
    );

    requestTurnRange(CHAT, "t-6", 0);
    expect(signal?.aborted).toBe(false);

    appendEntry(CHAT, revert("t-4", 0));
    expect(signal?.aborted).toBe(true);

    release?.();
    await Promise.resolve();
    await Promise.resolve();

    const s = get(CHAT);
    expect(s?.turns.has("t-6")).toBe(false);
    expect(s?.turn_order).toEqual([]);
    expect(s?.turn_count).toBe(4);
    expect(s?.has_more).toBe(derivedHasMore(4, 0));
  });

  it("refuses to re-seat a reverted turn when a late answer reaches the seat", async () => {
    // The abort's belt: a repair issued after the drop cannot put the turn back.
    seed(6, 7);
    appendEntry(CHAT, revert("t-4", 0));
    mockApiGetTyped.mockResolvedValue({
      entries: [
        {
          id: "t-6",
          turn: "t-6",
          kind: "turn_open",
          payload: { source: "prompt", n: 6 },
          seq: 0,
          ts: 0,
        },
      ],
      open_entries: [],
      subject: [],
    });

    requestTurnRange(CHAT, "t-6");
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    const s = get(CHAT);
    expect(s?.turns.has("t-6")).toBe(false);
    expect(s?.turn_order).toEqual([]);
    expect(s?.turn_count).toBe(4);
  });

  /** A window answer whose turns are `ns`, each a bare `turn_open`, under header `count`. */
  function page(ns: readonly number[], count: number): unknown {
    const s = get(CHAT);
    return {
      chat: {
        id: CHAT,
        name: CHAT,
        usage: s?.usage,
        created_at: 0,
        updated_at: 0,
        turn_count: count,
      },
      entries: ns.map((n) => ({
        id: `t-${String(n)}`,
        turn: `t-${String(n)}`,
        kind: "turn_open",
        payload: { source: "prompt", n },
        seq: 0,
        ts: 0,
      })),
      open_entries: [],
      has_more: false,
      subject: [],
      draft: "",
    };
  }

  /** Hold the next page read open; the returned function answers it with `body`. */
  function heldRead(): { signal: () => AbortSignal | undefined; answer: (body: unknown) => void } {
    let signal: AbortSignal | undefined;
    let resolve: ((v: unknown) => void) | undefined;
    mockApiGetTyped.mockImplementationOnce(
      (_path: string, _decode: unknown, s: AbortSignal) =>
        new Promise((r) => {
          signal = s;
          resolve = r;
        }),
    );
    return {
      signal: () => signal,
      answer: (body) => {
        resolve?.(body);
      },
    };
  }

  it("aborts an OLDER page read in flight, so its turns above the cut are never seated", async () => {
    // Device B holds 6..7 and is scrolling up; device A rewinds from 3. Turns 3..5 are
    // reverted and B never held them, so the drop and `reverted` cannot name them.
    seed(6, 7);
    const read = heldRead();
    const load = loadMessages(CHAT, "t-6");
    appendEntry(CHAT, revert("t-2", 0, { from: "t-3", from_n: 3, through: "t-7" }));
    expect(read.signal()?.aborted).toBe(true);

    read.answer(page([3, 4, 5], 7));
    expect(await load).toBe(false);
    const s = get(CHAT);
    expect(s?.turn_order).toEqual([]);
    expect(s?.turn_count).toBe(2);
  });

  it("asks for the NEWEST page again after aborting one, and adopts the post-revert answer", async () => {
    seed(1, 5);
    const stale = heldRead();
    const fresh = heldRead();
    const first = loadMessages(CHAT);
    appendEntry(CHAT, revert("t-2", 1, { from: "t-3", from_n: 3, through: "t-5" }));
    expect(stale.signal()?.aborted).toBe(true);
    expect(mockApiGetTyped).toHaveBeenCalledTimes(2);

    stale.answer(page([1, 2, 3, 4, 5], 5));
    fresh.answer(page([1, 2], 2));
    // The aborted caller answers with the re-issued load's result: an activation awaiting
    // it must see a loaded window, not a failure.
    expect(await first).toBe(true);
    expect(heldOrdinals()).toEqual([1, 2]);
    expect(get(CHAT)?.turn_count).toBe(2);
  });

  it("aborts a NEWEST page read even when the revert dropped nothing this window held", async () => {
    // The window has not caught up with the tail: it holds 1..2 while the pre-revert
    // answer in flight carries 3..4, which the revert took.
    seed(1, 2);
    const stale = heldRead();
    mockApiGetTyped.mockResolvedValueOnce(page([1, 2], 2));
    const first = loadMessages(CHAT);
    appendEntry(CHAT, revert("t-2", 1, { from: "t-3", from_n: 3, through: "t-4" }));
    expect(stale.signal()?.aborted).toBe(true);

    stale.answer(page([1, 2, 3, 4], 4));
    expect(await first).toBe(true);
    expect(get(CHAT)?.residency).toBe("loaded");
    expect(heldOrdinals()).toEqual([1, 2]);
    expect(get(CHAT)?.turn_count).toBe(2);
  });

  it("drops a held turn a NEWEST page no longer serves, for a client that missed the revert", async () => {
    // Device B slept past the replay window: no `turn_revert` reached it, so it still
    // holds 3..5 while the server serves 1..2 under a count of 2.
    seed(1, 5);
    mockApiGetTyped.mockResolvedValueOnce(page([1, 2], 2));
    expect(await loadMessages(CHAT)).toBe(true);

    expect(heldOrdinals()).toEqual([1, 2]);
    expect(get(CHAT)?.turn_count).toBe(2);
  });

  it("keeps a turn that OPENED while the newest page was in flight", async () => {
    seed(1, 2);
    const read = heldRead();
    const load = loadMessages(CHAT);
    openTurn(CHAT, {
      id: "t-3",
      turn: "t-3",
      kind: "turn_open",
      payload: { source: "prompt", n: 3 },
      seq: 0,
      ts: 0,
    });
    read.answer(page([1, 2], 2));
    expect(await load).toBe(true);

    expect(heldOrdinals()).toEqual([1, 2, 3]);
  });

  it("leaves exactly the turns below the cut, plus the carrier when it is held", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 1, max: 8 }),
        fc.integer({ min: 1, max: 8 }),
        fc.boolean(),
        (lowest, cut, carrierHeld) => {
          const hi = 8;
          const lo = Math.min(lowest, hi);
          const fromN = Math.min(cut, hi);
          const carrierN = fromN - 1;
          seed(lo, hi);
          const carrierResident = carrierHeld && carrierN >= lo;
          const carrier = carrierResident ? `t-${String(carrierN)}` : "t-absent";
          // A held carrier's record sits at that turn's next `seq` — the hole check
          // applies to a `turn_revert` unchanged, so a seq that does not fit is a hole
          // and the handler never runs.
          appendEntry(
            CHAT,
            revert(carrier, carrierResident ? 1 : 0, {
              from: `t-${String(fromN)}`,
              from_n: fromN,
              through: `t-${String(hi)}`,
            }),
          );

          const s = get(CHAT);
          const held = heldOrdinals();
          for (const n of held) {
            expect(n < fromN).toBe(true);
          }
          const heldCarrier = s?.turns.has(carrier) === true ? carrierN : 0;
          expect(s?.turn_count).toBe(Math.max(fromN - 1, heldCarrier));
          expect(s?.has_more).toBe(derivedHasMore(s?.turn_count ?? 0, held.length));
        },
      ),
    );
  });
});
