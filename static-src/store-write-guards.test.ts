// A WRITE THAT CHANGES NOTHING MUST CHURN NOTHING, and a write that carries a fact must
// not drop it.
//
// Four guards whose whole job is to be invisible, so the only way to pin one is to assert
// that a repaint did NOT happen — or, for the header's watermark, that a field the server
// sent survived the re-sync.
//
// A separate file from `store.test.ts` deliberately: `store.ts` is under concurrent edit,
// and its own suite is the file most likely to be touched alongside it.
import { describe, it, expect, beforeEach } from "vitest";
import {
  setSessions,
  setActive,
  get,
  defaultUsage,
  parseContextSize,
  setWorkingLabel,
  messagesVersionOf,
  upsertHeader,
  recordSteerSent,
  restoreSteers,
  steerCount,
} from "./store.js";
import type { ChatHeader, Session } from "./types.js";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function headerFor(chatID: string): ChatHeader {
  return {
    id: chatID,
    name: chatID,
    usage: defaultUsage(),
    turn_count: 0,
    created_at: 0,
    updated_at: 0,
  };
}

/** Run `mutate` and report whether the chat's transcript version moved. The version is the
 *  renderer's one input, so it is the observable a no-op guard has to hold still. */
async function bumped(chatID: string, mutate: () => void): Promise<boolean> {
  const before = messagesVersionOf(chatID).peek();
  mutate();
  await Promise.resolve();
  return messagesVersionOf(chatID).peek() !== before;
}

beforeEach(() => {
  setSessions([makeSession("a")]);
  setActive("a");
});

// --- The working label ----------------------------------------------------------------

describe("setWorkingLabel", () => {
  it("repaints when the label actually changes", async () => {
    expect(await bumped("a", () => setWorkingLabel("a", "Compacting"))).toBe(true);
    expect(get("a")?.working_label).toBe("Compacting");
  });

  it("repaints nothing when the same label is written again", async () => {
    setWorkingLabel("a", "Compacting");
    await Promise.resolve();
    // The label is re-derived on a cadence the DATA does not follow, so the same value
    // arrives repeatedly; each one reaching the renderer is a transcript pass per frame.
    expect(await bumped("a", () => setWorkingLabel("a", "Compacting"))).toBe(false);
    expect(get("a")?.working_label).toBe("Compacting");
  });
});

// --- The steer rollback ---------------------------------------------------------------

describe("restoreSteers", () => {
  it("puts a captured snapshot back", async () => {
    recordSteerSent("a", "m1", "first");
    await Promise.resolve();
    const snapshot = [...(get("a")?.steers ?? [])];
    expect(snapshot).toHaveLength(1);

    expect(
      await bumped("a", () => {
        restoreSteers("a", snapshot);
      }),
    ).toBe(true);
    expect(steerCount("a")).toBe(1);
  });

  it("leaves the dock alone when the snapshot it is rolling back is EMPTY", async () => {
    // `dropConfirmedSteers` answers `[]` when nothing was confirmed, and `withSteers`
    // DELETES the field for an empty list — so an unguarded rollback of that answer would
    // clear the rows the drop deliberately kept.
    recordSteerSent("a", "m1", "still waiting");
    recordSteerSent("a", "m2", "also waiting");
    await Promise.resolve();
    expect(steerCount("a")).toBe(2);

    expect(
      await bumped("a", () => {
        restoreSteers("a", []);
      }),
    ).toBe(false);
    expect(steerCount("a")).toBe(2);
  });
});

// --- The header re-sync ---------------------------------------------------------------

describe("upsertHeader and the compaction watermark", () => {
  it("adopts a watermark the header carries onto a row that already exists", () => {
    const h: ChatHeader = { ...headerFor("a"), compaction_watermark: "7" };
    upsertHeader(h);
    expect(get("a")?.compaction_watermark).toBe("7");
  });

  it("clears a watermark a later header does not carry, because the header is authority", () => {
    upsertHeader({ ...headerFor("a"), compaction_watermark: "7" });
    expect(get("a")?.compaction_watermark).toBe("7");
    upsertHeader(headerFor("a"));
    expect(get("a")?.compaction_watermark).toBeUndefined();
  });
});

// --- The window size a model's own description states --------------------------------

describe("parseContextSize reads a bare 1M as a window", () => {
  // The `<N>M context` spellings are a table in `store.test.ts`. This is the arm BELOW it:
  // the catalog ships descriptions that name the window without the word "context" at all
  // (`Claude Sonnet 4.5 (1M)`), and the client's own `MODEL_CONTEXT_SIZES` is empty, so
  // this regex is the only thing standing between such a model and a context ring that
  // reports no window at all.
  const cases: [string, number | undefined][] = [
    ["Claude Sonnet 4.5 (1M)", 1_000_000],
    ["1M", 1_000_000],
    ["Sonnet 1M beta", 1_000_000],
    // Not a bare `1M`: no word boundary on either side, so neither arm may claim it.
    ["11M tokens", undefined],
    ["1Mb", undefined],
  ];
  for (const [input, expected] of cases) {
    it(`reads ${JSON.stringify(input)} as ${String(expected)}`, () => {
      expect(parseContextSize(input)).toBe(expected);
    });
  }
});
