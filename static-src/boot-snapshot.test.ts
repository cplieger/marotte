// The boot snapshot: a bounded projection of what this screen was showing, held in
// IndexedDB so a resume paints before the network answers. Two properties carry the
// design and both are asserted against REAL IndexedDB and the REAL store: a record that
// does not decode is rejected without throwing, and what it paints is superseded by the
// server's answer rather than competing with it. `tabs.js` is the one mocked collaborator,
// because its import graph reaches the DOM strip and what matters here is which subjects
// the snapshot hands it.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { Session, TabSubject } from "./types.js";
import type { Entry, EntryToolCall, EntryToolResult, TextSpan } from "./wire/types.gen.js";

const m = vi.hoisted(() => ({
  openTabSubjects: vi.fn(),
  paintProvisionalTabs: vi.fn(),
  tabSetVersion: vi.fn(() => 0),
}));

vi.mock("./tabs.js", () => ({
  openTabSubjects: m.openTabSubjects,
  paintProvisionalTabs: m.paintProvisionalTabs,
  tabSetVersion: m.tabSetVersion,
}));

import {
  _resetForTest,
  captureBootSnapshot,
  clearBootSnapshot,
  paintBootSnapshot,
  readBootSnapshot,
  startBootSnapshot,
  type BootSnapshot,
} from "./boot-snapshot.js";
import {
  appendEntry,
  defaultUsage,
  get,
  openTurn,
  setActive,
  setSessions,
  tabStatusFor,
  transcriptStale,
} from "./store.js";

const DB_NAME = "marotte-boot";
const STORE_NAME = "snapshot";
const RECORD_KEY = "current";

/** The record's own bounds, restated so a fixture can be sized to fall on a known side
 *  of one and so a case can say which bound refused a turn. */
const MAX_BYTES = 96 * 1024;
const MAX_ENTRIES = 80;
const MAX_TOOL_OUTPUT = 256;

function chatTab(id: string, ref: string): TabSubject {
  return { id, kind: "chat", ref, parent: "", pinned: false, owns: true };
}

/** A store row with an EMPTY window: the shape `loadList` builds from a header, before any
 *  page lands. Every fixture below fills the window through the store's own operations. */
function session(id: string, name: string): Session {
  return {
    id,
    name,
    model: "claude",
    acp_session_id: "acp-1",
    current_mode_id: "default",
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** The `turn_open` that opens `turnID` at session-absolute ordinal `n`. `n` is the card's
 *  ordinal, so it rides on the entry rather than on any base the record carries. */
function turnOpen(turnID: string, n: number): Entry {
  return {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n, prompt: { id: `p-${turnID}`, text: "ask" } },
  };
}

function sealed(turnID: string, seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(seq)}`, turn: turnID, kind, seq, ts: seq + 1, payload };
}

function textEntry(turnID: string, seq: number, text = "answer"): Entry {
  return sealed(turnID, seq, "text", { text });
}

function closeEntry(turnID: string, seq: number): Entry {
  return sealed(turnID, seq, "turn_close", { outcome: "completed" });
}

// `Object.assign` rather than a spread: under `exactOptionalPropertyTypes` a spread of a
// `Partial` widens every required field to include `undefined`, which the target refuses.
function toolCallEntry(turnID: string, seq: number, over: Partial<EntryToolCall> = {}): Entry {
  const base: EntryToolCall = {
    id: `tc-${turnID}-${String(seq)}`,
    title: "Run Command",
    kind: "execute",
    status: "completed",
    ts: 100 + seq,
  };
  return sealed(turnID, seq, "tool_call", Object.assign(base, over));
}

function toolResultEntry(turnID: string, seq: number, over: Partial<EntryToolResult> = {}): Entry {
  const base: EntryToolResult = { status: "completed" };
  return sealed(turnID, seq, "tool_result", Object.assign(base, over));
}

/** A tool payload read back off a carried entry. */
function toolPayload(e: Entry | undefined): EntryToolCall {
  if (e === undefined) {
    throw new Error("the record carried no entry there");
  }
  return e.payload as EntryToolCall;
}

/** Ingest one turn through the store's own operations, so a fixture is the shape the SSE
 *  path produces rather than a hand-built turn map. */
function seedTurn(chatID: string, turnID: string, n: number, body: readonly Entry[]): void {
  openTurn(chatID, turnOpen(turnID, n));
  for (const e of body) {
    appendEntry(chatID, e);
  }
}

/** One settled turn: its header, one reply and its close. Three entries, which is the unit
 *  the record's turn bound is expressed in. */
function settledTurn(chatID: string, turnID: string, n: number): void {
  seedTurn(chatID, turnID, n, [textEntry(turnID, 1), closeEntry(turnID, 2)]);
}

/** `count` prose entries at seq 1..count. */
function textBody(turnID: string, count: number, text = "answer"): Entry[] {
  return Array.from({ length: count }, (_unused, i) => textEntry(turnID, i + 1, text));
}

/** A chat with one open tab, active, ready for a capture. */
function oneOpenChat(chatID = "c1"): void {
  m.openTabSubjects.mockReturnValue([chatTab("t1", chatID)]);
  setSessions([session(chatID, "One")]);
  setActive(chatID);
}

/** The captured window, asserted PRESENT. A `?? []` fallback would let a null window
 *  satisfy an "these ids are gone" assertion for the wrong reason. */
function capturedWindow(): NonNullable<BootSnapshot["window"]> {
  const win = captureBootSnapshot().window;
  if (win === null) {
    throw new Error("captureBootSnapshot carried no window");
  }
  return win;
}

function carriedIDs(win: NonNullable<BootSnapshot["window"]>): string[] {
  return win.entries.map((e) => e.id);
}

/** The store row for a chat, asserted present. */
function row(id: string): Session {
  const s = get(id);
  if (s === undefined) {
    throw new Error(`no store row for ${id}`);
  }
  return s;
}

function snapWindow(chatID: string, entries: readonly Entry[]): BootSnapshot["window"] {
  return { chat_id: chatID, entries };
}

function snapChat(
  id: string,
  over: Partial<BootSnapshot["chats"][number]> = {},
): BootSnapshot["chats"][number] {
  const base: BootSnapshot["chats"][number] = {
    id,
    name: id,
    model: "",
    current_mode_id: "",
    turn_count: 1,
    usage: defaultUsage(),
  };
  return Object.assign(base, over);
}

/** A chat row for a PLANTED record, typed as the decoder sees it: `unknown`. The builder
 *  above cannot serve these cases, because the shape they exist to test is one the type
 *  refuses — and a fixture asserted into the shape it is about to be checked against is
 *  what makes a boundary test vacuous. */
function rawChat(id: string, over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id,
    name: id,
    model: "",
    current_mode_id: "",
    turn_count: 1,
    usage: defaultUsage(),
    ...over,
  };
}

/** One styled span over a tool call's output, in the shape the wire carries. */
function span(i: number): TextSpan {
  return { start: i, end: i + 1, fg: 1, bg: -1, attrs: 0 };
}

/** The module's own object store, opened separately so a test can plant a record the
 *  module would never write. Two connections are safe: the version never changes, so
 *  neither blocks the other. */
async function withStore<T>(
  mode: IDBTransactionMode,
  fn: (store: IDBObjectStore) => IDBRequest,
): Promise<T> {
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => {
      req.result.createObjectStore(STORE_NAME);
    };
    req.onsuccess = () => {
      resolve(req.result);
    };
    req.onerror = () => {
      reject(new Error("open failed"));
    };
  });
  try {
    return await new Promise<T>((resolve, reject) => {
      const req = fn(db.transaction(STORE_NAME, mode).objectStore(STORE_NAME));
      req.onsuccess = () => {
        resolve(req.result as T);
      };
      req.onerror = () => {
        reject(new Error("request failed"));
      };
    });
  } finally {
    db.close();
  }
}

async function plantRecord(value: unknown): Promise<void> {
  await withStore("readwrite", (s) => s.put(value, RECORD_KEY));
}

beforeEach(async () => {
  vi.useFakeTimers();
  _resetForTest();
  setSessions([]);
  setActive("");
  m.openTabSubjects.mockReturnValue([]);
  await withStore("readwrite", (s) => s.clear());
});

afterEach(() => {
  vi.useRealTimers();
  _resetForTest();
});

describe("readBootSnapshot", () => {
  it("resolves null when this screen has never been captured", async () => {
    expect(await readBootSnapshot()).toBeNull();
  });

  it("rejects a corrupt record without throwing", async () => {
    await plantRecord("not an object at all");

    expect(await readBootSnapshot()).toBeNull();
  });

  it("rejects a record whose tab set holds a malformed subject", async () => {
    // The container is the right shape and the array is an array; the one subject is
    // missing `pinned`. A container-only check would hand it to the paint.
    await plantRecord({
      tabs: [{ id: "t1", kind: "chat", ref: "c1", parent: "", owns: true }],
      chats: [snapChat("c1")],
      window: null,
    });

    expect(await readBootSnapshot()).toBeNull();
  });

  it("rejects the WHOLE record for one undecodable entry", async () => {
    // STRICT, unlike the window decoder on the network path: one undecodable entry there
    // costs that entry and the range read fills the gap, while a dropped entry here would
    // leave a `seq` hole in a hint nothing repairs.
    await plantRecord({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1")],
      window: {
        chat_id: "c1",
        entries: [
          turnOpen("turn-1", 1),
          { id: "e1", turn: "turn-1", kind: "narrator", seq: 1, ts: 2, payload: {} },
        ],
      },
    });

    expect(await readBootSnapshot()).toBeNull();
  });

  it("refuses a record carrying no window field at all", async () => {
    // `null` is the value for "no chat was active"; ABSENT is a foreign record, and it
    // rejects like any other missing required field — which is what makes the first boot
    // after this shape ships paint nothing rather than paint half a record.
    await plantRecord({ tabs: [chatTab("t1", "c1")], chats: [snapChat("c1")] });

    expect(await readBootSnapshot()).toBeNull();
  });

  it("reads an outcome the vocabulary does not name as ABSENT, not as a rejection", async () => {
    // A member the generated union gains later must cost this record nothing: every other
    // field is intact, so refusing the whole thing would leave a build older than the
    // server's painting no first frame at all.
    await plantRecord({
      tabs: [chatTab("t1", "c1")],
      chats: [rawChat("c1", { last_turn_outcome: "reticulated", updated_at: 5 })],
      window: null,
    });

    const snap = await readBootSnapshot();

    expect(snap?.chats).toHaveLength(1);
    expect(snap?.chats[0]?.last_turn_outcome).toBeUndefined();
    expect(snap?.chats[0]?.updated_at).toBe(5);
  });

  it("reads a non-numeric updated_at as ABSENT rather than carrying it", async () => {
    // The tolerant reader's ONE rule covers a wrong TYPE as well as a value the vocabulary
    // does not name, and the field it feeds is spent as epoch millis by `relativeTime` — so
    // a string reaching the row renders an age of NaN on the dot's tooltip.
    await plantRecord({
      tabs: [chatTab("t1", "c1")],
      chats: [rawChat("c1", { last_turn_outcome: "completed", updated_at: "yesterday" })],
      window: null,
    });

    const snap = await readBootSnapshot();

    expect(snap?.chats).toHaveLength(1);
    expect(snap?.chats[0]?.updated_at, "the wrong type reads as absent").toBeUndefined();
    // The sibling field survives, which is what makes this a per-field tolerance rather
    // than a rejection of the record.
    expect(snap?.chats[0]?.last_turn_outcome, "the sibling field").toBe("completed");
  });
});

describe("the capture", () => {
  it("persists the tab set, the open chats and the active transcript", async () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([session("c1", "Refactor the boot")]);
    setActive("c1");
    settledTurn("c1", "turn-1", 1);

    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(1_000);

    const snap = await readBootSnapshot();
    expect(snap?.tabs).toEqual([chatTab("t1", "c1")]);
    expect(snap?.chats.map((c) => c.name)).toEqual(["Refactor the boot"]);
    // The count the strip and `has_more` are read against, from the row rather than from
    // the entries the record happened to carry.
    expect(snap?.chats[0]?.turn_count).toBe(1);
    expect(snap?.window?.chat_id).toBe("c1");
    expect(snap?.window?.entries.map((e) => e.kind)).toEqual(["turn_open", "text", "turn_close"]);
  });

  it("writes nothing until the projection has stood still", async () => {
    oneOpenChat();

    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(999);

    // A streaming turn moves the transcript version every frame; a write per frame is a
    // whole-record replace per frame.
    expect(await readBootSnapshot()).toBeNull();
  });

  it("carries the newest three turns and no more", () => {
    oneOpenChat();
    for (const n of [1, 2, 3, 4, 5]) {
      settledTurn("c1", `turn-${String(n)}`, n);
    }

    // FILE order, newest three turns: the walk is newest-first and unshifts, so what the
    // record holds reads the way the transcript renders.
    expect(capturedWindow().entries.map((e) => e.turn)).toEqual([
      "turn-3",
      "turn-3",
      "turn-3",
      "turn-4",
      "turn-4",
      "turn-4",
      "turn-5",
      "turn-5",
      "turn-5",
    ]);
  });

  it("trims the newest turn to a contiguous PREFIX when it alone is over the entry cap", () => {
    oneOpenChat();
    // One turn of 100 entries against the 80-entry cap, which is the case the cap exists
    // for. Dropping it would resume with no transcript at all.
    seedTurn("c1", "turn-1", 1, textBody("turn-1", 99));

    const win = capturedWindow();

    expect(win.entries).toHaveLength(MAX_ENTRIES);
    // A PREFIX from `turn_open`, not a tail: `TurnState`'s invariant is
    // `entries[i].seq === i`, so a tail cannot be seated at all, where a prefix is a card
    // with its own header and a short body — and its header is what carries the turn's
    // own ordinal, so the card is numbered without the record stating a base.
    expect(win.entries.map((e) => e.seq)).toEqual([...Array(MAX_ENTRIES).keys()]);
    expect(win.entries[0]?.kind).toBe("turn_open");
    expect(carriedIDs(win)).not.toContain("turn-1-e99");
  });

  it("stops a prefix at the first entry the byte budget cannot afford", () => {
    oneOpenChat();
    // 71 entries of ordinary prose: inside the entry cap and past the byte budget, so the
    // cut is the BYTE bound's rather than the count's.
    const body = textBody("turn-1", 70, "x".repeat(1_500));
    seedTurn("c1", "turn-1", 1, body);

    const win = capturedWindow();

    expect(JSON.stringify(win).length).toBeLessThanOrEqual(MAX_BYTES);
    expect(win.entries.length).toBeLessThan(body.length + 1);
    // Under the cap, so a passing assertion above cannot be the count bound wearing this
    // case's name.
    expect(win.entries.length).toBeLessThan(MAX_ENTRIES);
    expect(win.entries[0]?.kind).toBe("turn_open");
  });

  it("drops WHOLE turns when the newest three do not fit", () => {
    oneOpenChat();
    // 50 + 20 + 15 entries against the 80-entry cap. The two newest fit (35); adding the
    // oldest would not, so all 50 of it go. The sizes are uneven on purpose: with three
    // equal turns a tail slice of the flattened list lands on a turn boundary and both
    // rules agree.
    for (const [n, size] of [
      [1, 50],
      [2, 20],
      [3, 15],
    ] as const) {
      seedTurn("c1", `turn-${String(n)}`, n, textBody(`turn-${String(n)}`, size - 1));
    }

    const win = capturedWindow();

    // Two whole turns, not an 80-entry tail: a tail would have kept 80, the oldest 45 of
    // them a headerless fragment of turn 1.
    expect(win.entries).toHaveLength(35);
    expect(win.entries[0]?.turn).toBe("turn-2");
    expect(win.entries.map((e) => e.turn)).not.toContain("turn-1");
  });

  it("carries only the chats a tab names", () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    // c2 is a closed chat whose row the store still holds. It is not what this screen was
    // showing.
    setSessions([session("c1", "Open"), session("c2", "Closed")]);

    expect(captureBootSnapshot().chats.map((c) => c.id)).toEqual(["c1"]);
  });

  it("carries the chat's last outcome and the age of it", async () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([{ ...session("c1", "One"), last_turn_outcome: "failed", updated_at: 1_700_000 }]);
    setActive("c1");
    settledTurn("c1", "turn-1", 1);

    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(1_000);

    const snap = await readBootSnapshot();
    expect(snap?.chats[0]?.last_turn_outcome).toBe("failed");
    expect(snap?.chats[0]?.updated_at).toBe(1_700_000);
  });

  it("drops the record and stops capturing on a sign-out", async () => {
    oneOpenChat();
    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(await readBootSnapshot()).not.toBeNull();

    await clearBootSnapshot();
    expect(await readBootSnapshot()).toBeNull();

    // And nothing writes it back: a login screen must not re-capture the workspace it is
    // covering. `setActive` is one of the three reads the capture watches, so a live effect
    // would schedule a write here.
    setActive("c1");
    await vi.advanceTimersByTimeAsync(1_000);
    expect(await readBootSnapshot()).toBeNull();
  });

  it("does not resurrect the record when the page hides after a sign-out", async () => {
    oneOpenChat();
    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(1_000);

    await clearBootSnapshot();
    // The last event a backgrounded PWA gets. It flushes the projection, which is exactly
    // what must NOT happen once the user has signed out: the rows are still in the store,
    // so a live listener would write the record straight back.
    dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(0);

    expect(await readBootSnapshot()).toBeNull();
  });

  it("round-trips a snapshot with no active chat, and paints rows with no transcript", async () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([session("c1", "One")]);
    setActive("c1");
    settledTurn("c1", "turn-1", 1);
    setActive("");

    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(1_000);

    const snap = await readBootSnapshot();
    // `null` decodes as the VALUE it is, so the rows and the strip still paint.
    expect(snap?.window).toBeNull();
    expect(paintBootSnapshot(snap)).toBe(true);
    expect(row("c1").turns.size).toBe(0);
    expect(row("c1").turn_order).toEqual([]);
  });
});

// The BYTE bound, which is the one the count bound cannot stand in for: this record was
// measured at 1,778,339 bytes over SEVEN messages on the live instance, 207 tool calls in
// one of them. It is written on every quiet gap and read plus parsed before the first frame
// of every boot, and on WebKit that storage is owned by the process that also owns the
// page's sockets — so the size is a reload, not a slow write.
describe("the record's byte budget", () => {
  it("keeps a megabyte turn's record inside the budget", () => {
    oneOpenChat();
    const body = Array.from({ length: 60 }, (_unused, i) =>
      toolCallEntry("turn-1", i + 1, { output: "x".repeat(40_000) }),
    );
    seedTurn("c1", "turn-1", 1, body);

    const snap = captureBootSnapshot();

    // The unbounded projection of this fixture is over a megabyte, so a passing assertion
    // below cannot be an accident of a small fixture.
    expect(JSON.stringify(body).length).toBeGreaterThan(1_000_000);
    expect(JSON.stringify(snap).length).toBeLessThanOrEqual(MAX_BYTES);
    expect(snap.window?.entries.length).toBeGreaterThan(0);
  });

  it("refuses an older turn the running total cannot afford", () => {
    oneOpenChat();
    // TWO turns of ordinary prose, each of which fits on its own, so only a budget carried
    // ACROSS turns can refuse the older one. 80 entries in total, which is exactly the
    // entry cap — so the count bound admits both and the bytes are what refuse.
    for (const [n, id] of [
      [1, "turn-1"],
      [2, "turn-2"],
    ] as const) {
      seedTurn("c1", id, n, textBody(id, 39, "x".repeat(1_500)));
    }

    const win = capturedWindow();

    expect(JSON.stringify(win).length).toBeLessThanOrEqual(MAX_BYTES);
    // The NEWEST turn survives whole, which is the half a plain "it is small" assertion
    // would let a record of nothing satisfy.
    expect(win.entries).toHaveLength(40);
    expect(new Set(win.entries.map((e) => e.turn))).toEqual(new Set(["turn-2"]));
  });

  it("truncates a tool call's output and drops the spans that style it", () => {
    oneOpenChat();
    seedTurn("c1", "turn-1", 1, [
      toolCallEntry("turn-1", 1, {
        output: "x".repeat(40_000),
        output_spans: Array.from({ length: 200 }, (_unused, j) => span(j)),
      }),
    ]);

    const call = toolPayload(capturedWindow().entries.at(-1));

    // Truncated rather than DROPPED: `tool-card.ts` reads a non-blank output as "there is
    // something to reveal", so an empty one withdraws the disclosure and pops the chevron
    // in when the server's answer lands.
    expect(call.output).not.toBe("");
    expect((call.output ?? "").length).toBeLessThanOrEqual(MAX_TOOL_OUTPUT);
    expect(call.output_spans).toBeUndefined();
  });

  it("trims a tool RESULT the same way it trims the call", () => {
    oneOpenChat();
    seedTurn("c1", "turn-1", 1, [
      toolCallEntry("turn-1", 1),
      toolResultEntry("turn-1", 2, {
        output: "x".repeat(40_000),
        output_spans: [span(0)],
      }),
    ]);

    const result = toolPayload(capturedWindow().entries.at(-1));

    // ONE trim for both payloads, because the three fields it touches are the same three
    // on each — a `tool_result` carrying no `input` simply has nothing there to trim.
    expect((result.output ?? "").length).toBeLessThanOrEqual(MAX_TOOL_OUTPUT);
    expect(result.output_spans).toBeUndefined();
  });

  it("keeps the input key the visible claim line renders", () => {
    oneOpenChat();
    seedTurn("c1", "turn-1", 1, [
      toolCallEntry("turn-1", 1, { input: { command: "go test ".repeat(200) } }),
    ]);

    const input = toolPayload(capturedWindow().entries.at(-1)).input as
      { command?: string } | undefined;

    // `.tool-subtitle` renders `input.command` on the visible claim line, so the KEY
    // survives the trim and only its value is cut.
    expect(typeof input?.command).toBe("string");
    expect((input?.command ?? "").length).toBeLessThanOrEqual(MAX_TOOL_OUTPUT);
  });

  it("carries an entry that holds no tool payload exactly as it arrived", () => {
    oneOpenChat();
    const reply = textEntry("turn-1", 1);
    seedTurn("c1", "turn-1", 1, [reply, closeEntry("turn-1", 2)]);

    // The record is smaller than the transcript only in the fields the tool trim touches,
    // so every other entry rides through by identity — which is what makes a card's own
    // ordinal, its lane and its `seq` the record's rather than something re-derived.
    expect(capturedWindow().entries[1]).toBe(reply);
  });
});

describe("paintBootSnapshot", () => {
  it("paints nothing when there is no snapshot", () => {
    expect(paintBootSnapshot(null)).toBe(false);
    expect(m.paintProvisionalTabs).not.toHaveBeenCalled();
  });

  it("paints nothing when the snapshot holds no tabs", () => {
    expect(paintBootSnapshot({ tabs: [], chats: [], window: null })).toBe(false);
    expect(m.paintProvisionalTabs).not.toHaveBeenCalled();
  });

  it("paints the chat rows, then the strip, then the transcript", () => {
    // The ORDER is what this case is about, so it is observed from inside the strip's own
    // call: both writes happen whichever way round they run, so asserting them afterwards
    // pins nothing.
    let nameWhenStripPainted = "";
    m.paintProvisionalTabs.mockImplementation(() => {
      nameWhenStripPainted = get("c1")?.name ?? "";
    });

    const painted = paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1", { name: "Refactor the boot", model: "claude" })],
      window: snapWindow("c1", [turnOpen("turn-1", 1), textEntry("turn-1", 1)]),
    });

    expect(painted).toBe(true);
    // The rows go in BEFORE the strip: a chat tab's label is read from the store while its
    // row is built.
    expect(nameWhenStripPainted).toBe("Refactor the boot");
    expect(m.paintProvisionalTabs).toHaveBeenCalledWith([chatTab("t1", "c1")]);
    expect(row("c1").turn_order).toEqual(["turn-1"]);
    expect(row("c1").turns.get("turn-1")?.entries).toHaveLength(2);
  });

  it("claims no transcript residency, so the activation refetches the window", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1")],
      window: snapWindow("c1", [turnOpen("turn-1", 1)]),
    });

    // The mechanism that makes the hint self-superseding: `transcriptStale` is what
    // `activateChatView` keys its fetch on, and a painted window must not pass for one the
    // server answered.
    expect(transcriptStale(row("c1"))).toBe(true);
  });

  it("records where each carried turn closed, so liveness stays the log's", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1")],
      window: snapWindow("c1", [
        turnOpen("turn-1", 1),
        textEntry("turn-1", 1),
        closeEntry("turn-1", 2),
      ]),
    });

    // `closeAt` is what `hasOpenTurn` reads, so a carried `turn_close` has to seat itself
    // at the paint rather than being re-derived by a scan.
    expect(row("c1").turns.get("turn-1")?.closeAt).toBe(2);
  });

  it("drops a turn whose window does not start at its turn_open", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1")],
      window: snapWindow("c1", [textEntry("turn-1", 1)]),
    });

    // `TurnState`'s invariant is `entries[i].seq === i`, and this record is a hint nothing
    // repairs — the store's own hole path is for a page that has a turn to re-read.
    expect(row("c1").turn_order).toEqual([]);
    expect(row("c1").turns.size).toBe(0);
  });

  it("skips an entry that would leave a seq hole", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1")],
      window: snapWindow("c1", [turnOpen("turn-1", 1), textEntry("turn-1", 2)]),
    });

    // The turn is seated on its header alone: a card with a short body is a card, where a
    // body seated at the wrong index is a transcript claiming something that never happened.
    expect(
      row("c1")
        .turns.get("turn-1")
        ?.entries.map((e) => e.seq),
    ).toEqual([0]);
  });

  it("claims no more turns for a chat carried WHOLE", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1", { turn_count: 1 })],
      window: snapWindow("c1", [turnOpen("turn-1", 1), closeEntry("turn-1", 1)]),
    });

    // `has_more` is the record's own count against what it carried, so a chat holding
    // every turn it has must not claim older ones exist.
    expect(row("c1").has_more).toBe(false);
  });

  it("claims older turns for a window that is a PAGED tail", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1", { turn_count: 40 })],
      window: snapWindow("c1", [turnOpen("turn-40", 40), closeEntry("turn-40", 1)]),
    });

    const painted = row("c1");
    expect(painted.turn_count).toBe(40);
    expect(painted.has_more).toBe(true);
  });

  it("gives the window to the transcript chat and to no other row", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1"), chatTab("t2", "c2")],
      chats: [
        snapChat("c1", { name: "Active", turn_count: 40 }),
        snapChat("c2", { name: "Open, not showing", turn_count: 12 }),
      ],
      window: snapWindow("c1", [turnOpen("turn-40", 40)]),
    });

    expect(row("c1").turn_order).toEqual(["turn-40"]);
    // Every other row is built with no window at all, and `has_more` is derived against
    // that emptiness rather than guessed.
    expect(row("c2").turns.size).toBe(0);
    expect(row("c2").has_more).toBe(true);
  });

  it("carries the last outcome onto the row it paints", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1", { last_turn_outcome: "failed", updated_at: 1_700_000 })],
      window: snapWindow("c1", [turnOpen("turn-1", 1), closeEntry("turn-1", 1)]),
    });

    const painted = row("c1");
    // The header's own statement, verbatim: it is what the strip grades once the chat list
    // lands and drops the provisional mark.
    expect(painted.last_turn_outcome).toBe("failed");
    expect(painted.updated_at).toBe(1_700_000);
  });

  it("paints the working dot for a hinted row, whatever outcome the record carried", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1", { last_turn_outcome: "failed" })],
      window: snapWindow("c1", [turnOpen("turn-1", 1), closeEntry("turn-1", 1)]),
    });

    const painted = row("c1");
    expect(painted.name, "the row was painted").toBe("c1");
    // A hinted row STATES NO LIVENESS (`Session.provisional`), so `turnLive` reads it as
    // live and both terminal arms of the dot are gated off: guessing the other way derives
    // a settled verdict over a turn the server may still be streaming.
    expect(tabStatusFor(painted), "the dot").toBe("working");
  });

  it("is replaced whole by the server's own chat list", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [snapChat("c1", { name: "Stale name" })],
      window: snapWindow("c1", [turnOpen("turn-1", 1)]),
    });

    // What `loadList` does when it lands.
    setSessions([session("c1", "The name the server holds")]);

    expect(get("c1")?.name).toBe("The name the server holds");
  });
});
