// Tests for store-load.ts, the ENTRY-LOG loaders: the chat list, the window GET of section 6.3 and
// the range read of 6.4.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { Session, TurnState } from "./types.js";
import { _resetForTest as resetVersions, hasSubject, versionMap } from "./subject-versions.js";
// The module's own shape, for the fresh-instance loader at the foot of this file: `chatListLoaded`,
// `listReach` and the outcome counters are module state, so their cases re-evaluate the module and
// need a type for what the dynamic import hands back.
import type * as StoreLoad from "./store-load.js";
// The store's shape, for the `importOriginal` call in its mock factory below. A type-only import,
// so it adds no runtime edge the `vi.mock` would have to reach around.
import type * as Store from "./store.js";
import type { ChatHeader, Entry, OpenEntry, SubjectStamp, Usage } from "./wire/types.gen.js";

const {
  sessions,
  mockApiGetTyped,
  mockApiGetTypedOrError,
  mockSetSessions,
  mockUpsertHeader,
  mockBumpMessages,
  mockRepublishToolCalls,
  mockMarkWindowStale,
  mockSetTurnOpen,
  mockClearTurnState,
} = vi.hoisted(() => ({
  sessions: new Map<string, Session>(),
  mockApiGetTyped: vi.fn(),
  // The status-bearing GET. `confirmChatExists` reads the STATUS rather than a collapsed null, so
  // its fixture is the whole `ApiResult` envelope.
  mockApiGetTypedOrError: vi.fn(),
  mockSetSessions: vi.fn(),
  // A spy so the confirm cases can assert that a chat the server DOES know lands in the store.
  mockUpsertHeader: vi.fn(),
  mockBumpMessages: vi.fn(),
  // The channel a MOUNTED tool card refreshes through. A spy, because what this file owns is that a
  // fetched window is put on it at all and with which turns; what the channel then does to a card's
  // signal is store.test.ts's, against the real one.
  mockRepublishToolCalls: vi.fn(),
  // A spy: this file owns that a hole MARKS the window, and what the mark does to `residency` and
  // to `transcriptStale` is store.test.ts's.
  mockMarkWindowStale: vi.fn(),
  mockSetTurnOpen: vi.fn(),
  // The shared turn teardown, mocked at the boundary rather than run for real: the real one reaches
  // the tab strip and the decision dock, and none of that is what a fetch-lifecycle file owns.
  // turn-teardown.test.ts drives the real thing against the real store.
  mockClearTurnState: vi.fn(),
}));

vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));
vi.mock("./api-client.js", () => ({
  apiGetTyped: mockApiGetTyped,
  apiGetTypedOrError: mockApiGetTypedOrError,
}));
vi.mock("./turn-teardown.js", () => ({
  clearTurnState: mockClearTurnState,
  // Present-but-inert so real-ESM linking succeeds; nothing here reaches them.
  healSettledChat: vi.fn(),
  retractStaleThinking: vi.fn(),
}));
vi.mock("./store.js", async (importOriginal) => {
  // `derivedHasMore` is the REAL one, and that is deliberate: it is the rule the `has_more` cases
  // below are ABOUT, so a hand-written copy here would assert the mock rather than the production
  // rule and would go stale silently the first time the rule moved.
  const { derivedHasMore } = await importOriginal<typeof Store>();
  return {
    derivedHasMore,
    get: (id: string) => sessions.get(id),
    getSessions: () => [...sessions.values()],
    setSessions: mockSetSessions,
    upsertHeader: mockUpsertHeader,
    bumpMessages: mockBumpMessages,
    republishWindowToolCalls: mockRepublishToolCalls,
    markWindowStale: mockMarkWindowStale,
    setTurnOpen: mockSetTurnOpen,
    // Present-but-inert so real-ESM linking succeeds: this graph reaches these names from
    // somewhere. No case here calls them.
    getActive: vi.fn(() => undefined),
    getActiveId: vi.fn(() => ""),
    tabStatusFor: vi.fn(() => ""),
    registerTurnRepair: vi.fn(),
  };
});

import {
  loadList,
  loadMessages,
  confirmChatExists,
  scheduleListRetry,
  requestTurnRange,
} from "./store-load.js";

function usage(): Usage {
  return {
    context_pct: 0,
    context_size: 0,
    credits: 0,
    last_turn_ms: 0,
    has_real_data: false,
  };
}

// `Object.assign` rather than a spread: under `exactOptionalPropertyTypes` a spread of a `Partial`
// widens every required field to include `undefined`, which the target refuses.
function header(id: string, over: Partial<ChatHeader> = {}): ChatHeader {
  const base: ChatHeader = {
    id,
    name: id,
    usage: usage(),
    created_at: 0,
    updated_at: 0,
    turn_count: 0,
  };
  return Object.assign(base, over);
}

function turnOpen(turnID: string, n: number): Entry {
  return {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n, prompt: { id: `m-${turnID}`, text: "hi" } },
  };
}

/** A sealed entry of any kind at `seq`. `lane` is `""` unless named, which is the transcript's
 *  own lane. */
function entry(
  turnID: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown = {},
  opts: { readonly id?: string; readonly lane?: string } = {},
): Entry {
  const base: Entry = {
    id: opts.id ?? `${turnID}-e${String(seq)}`,
    turn: turnID,
    kind,
    seq,
    ts: seq + 1,
    payload,
  };
  return opts.lane === undefined ? base : Object.assign(base, { lane: opts.lane });
}

function textEntry(turnID: string, seq: number, text = "hi"): Entry {
  return entry(turnID, seq, "text", { text });
}

/** An in-memory tail, one per `(turn, lane)`. */
function openTail(
  turnID: string,
  id: string,
  n: number,
  opts: { readonly lane?: string; readonly text?: string } = {},
): OpenEntry {
  const base: OpenEntry = { turn: turnID, id, kind: "text", text: opts.text ?? "so far", n };
  return opts.lane === undefined ? base : Object.assign(base, { lane: opts.lane });
}

function stamp(kind: string, ref: string, version: string, epoch?: string): SubjectStamp {
  const base: SubjectStamp = { kind, ref, version };
  return epoch === undefined ? base : Object.assign(base, { epoch });
}

function seedSession(id: string, over: Partial<Session> = {}): Session {
  const base: Session = {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    usage: usage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
  const s = Object.assign(base, over);
  sessions.set(id, s);
  return s;
}

function seedTurn(session: Session, turnID: string, entries: Entry[], open: OpenEntry[] = []) {
  const state: TurnState = {
    entries,
    openEntries: new Map(open.map((o) => [o.lane ?? "", o])),
  };
  const close = entries.find((e) => e.kind === "turn_close");
  if (close !== undefined) {
    state.closeAt = close.seq;
  }
  session.turns.set(turnID, state);
  session.turn_order.push(turnID);
  return state;
}

interface PageAnswer {
  readonly entries?: Entry[];
  readonly open?: OpenEntry[];
  readonly has_more?: boolean;
  readonly live?: boolean;
  readonly subject?: SubjectStamp[];
  readonly draft?: string;
  readonly header?: Partial<ChatHeader>;
}

/** The DECODED window answer. `apiGetTyped` is mocked, so the module's own decoder does not run
 *  on this path — the raw-wire door below is what exercises it. */
function answerPage(chatID: string, o: PageAnswer = {}): void {
  mockApiGetTyped.mockResolvedValue({
    chat: header(chatID, o.header ?? {}),
    entries: o.entries ?? [],
    open_entries: o.open ?? [],
    has_more: o.has_more ?? false,
    live: o.live,
    subject: o.subject ?? [],
    draft: o.draft ?? "",
  });
}

/** Answer with RAW WIRE bytes, put through the module's OWN decoder — the same fold
 *  `apiGetTyped` performs, throw collapsed to null. This is the only door that exercises
 *  `decodeTolerant` and `decodeStamps`, which is what the drop-a-member cases are about. */
function answerWire(raw: unknown): void {
  mockApiGetTyped.mockImplementation((_path: string, decode: (v: unknown) => unknown) => {
    try {
      return Promise.resolve(decode(raw));
    } catch {
      return Promise.resolve(null);
    }
  });
}

/** The version held for one subject, read off the live map's snapshot. */
function heldVersion(kind: string, ref: string): string | undefined {
  return versionMap()
    .snapshot()
    .held.find((h) => h.kind === kind && h.ref === ref)?.version;
}

function heldCount(): number {
  return versionMap().snapshot().held.length;
}

/** Every line one console spy recorded, joined, so a case can match one regex. */
function logLines(spy: { readonly mock: { readonly calls: readonly unknown[][] } }): string {
  return spy.mock.calls.map((c) => String(c[0])).join("\n");
}

function turnOf(chatID: string, turnID: string): TurnState {
  const state = sessions.get(chatID)?.turns.get(turnID);
  if (state === undefined) {
    throw new Error(`no resident turn ${turnID}`);
  }
  return state;
}

function seqsOf(chatID: string, turnID: string): number[] {
  return turnOf(chatID, turnID).entries.map((e) => e.seq);
}

/** The range reads this module issued, in order, read off the request path rather than off a
 *  spy: `requestTurnRange` is this module's own export, so the observable is the GET. */
function rangeReads(): { turnID: string; after: string | null }[] {
  return mockApiGetTyped.mock.calls
    .map((c) => String(c[0]))
    .filter((p) => p.includes("/turns/"))
    .map((p) => {
      const u = new URL(p, "https://x");
      const parts = u.pathname.split("/");
      return {
        turnID: decodeURIComponent(parts[parts.length - 1] ?? ""),
        after: u.searchParams.get("after"),
      };
    });
}

let warn: ReturnType<typeof vi.spyOn>;
let debug: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  vi.clearAllMocks();
  sessions.clear();
  resetVersions();
  // Every loader reports its outcome on the console; a case that is about the LINE spies on it
  // itself, and the rest would otherwise print one per assertion.
  warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
  debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
});

afterEach(() => {
  warn.mockRestore();
  debug.mockRestore();
});

describe("loadList pruning", () => {
  // Server-minted ids mean a chat with a store row is a chat the server has, so absence from the
  // listing means DELETED and no id is exempt from the prune.
  it("prunes a chat the server does not list", async () => {
    seedSession("real");
    seedSession("c-untracked");
    mockApiGetTyped.mockResolvedValue({ chats: [header("real")] });

    expect(await loadList()).toBe(true);
    const passed = (mockSetSessions.mock.calls.at(-1)?.[0] ?? []) as Session[];
    expect(passed.map((s) => s.id)).toEqual(["real"]);
  });

  it("keeps a chat that arrived while the request was in flight", async () => {
    // `upsertHeader` builds that row from an SSE frame, so the answer being applied predates it and
    // is not entitled to drop it.
    mockApiGetTyped.mockImplementation(() => {
      seedSession("c-sse");
      return Promise.resolve({ chats: [header("kept")] });
    });

    await loadList();
    const passed = (mockSetSessions.mock.calls.at(-1)?.[0] ?? []) as Session[];
    expect(passed.map((s) => s.id)).toEqual(["kept", "c-sse"]);
  });

  it("drops a provisional row the server did not name", async () => {
    // Identical on every axis the rule above tests — unknown before the request, unnamed by the
    // answer — and the opposite meaning: the boot snapshot painted it, so the chat may have been
    // deleted since that capture and there is nothing to preserve.
    mockApiGetTyped.mockImplementation(() => {
      seedSession("c-hint", { provisional: true });
      return Promise.resolve({ chats: [header("kept")] });
    });

    await loadList();
    const passed = (mockSetSessions.mock.calls.at(-1)?.[0] ?? []) as Session[];
    expect(passed.map((s) => s.id)).toEqual(["kept"]);
  });

  it("commits nothing when the list does not decode", async () => {
    seedSession("real");
    mockApiGetTyped.mockResolvedValue(null);

    expect(await loadList()).toBe(false);
    expect(mockSetSessions).not.toHaveBeenCalled();
  });
});

describe("loadList rebuilds each row from the header", () => {
  async function rebuild(h: ChatHeader): Promise<Session> {
    mockApiGetTyped.mockResolvedValue({ chats: [h] });
    await loadList();
    const passed = (mockSetSessions.mock.calls.at(-1)?.[0] ?? []) as Session[];
    const row = passed[0];
    if (row === undefined) {
      throw new Error("no row committed");
    }
    return row;
  }

  it("takes the server's turn_count", async () => {
    const row = await rebuild(header("c1", { turn_count: 7 }));
    expect(row.turn_count).toBe(7);
  });

  it("carries the resident window over as ONE value", async () => {
    // The map and the order describe the same turns, so carrying one without the other is a window
    // that renders nothing or renders it twice.
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);

    const row = await rebuild(header("c1", { turn_count: 1 }));
    expect([...row.turns.keys()]).toEqual(["t1"]);
    expect(row.turn_order).toEqual(["t1"]);
  });

  it("DERIVES has_more over the header's count against the carried window", async () => {
    // A header carries no window, so this is the derivation and never an answer. Never OR'd with
    // the previous value: a sticky true is a Load-older button with nothing behind it.
    const s = seedSession("c1", { has_more: true });
    seedTurn(s, "t1", [turnOpen("t1", 1)]);

    expect((await rebuild(header("c1", { turn_count: 1 }))).has_more).toBe(false);
    expect((await rebuild(header("c1", { turn_count: 4 }))).has_more).toBe(true);
  });

  it("answers has_more false for an empty chat", async () => {
    seedSession("c1", { has_more: true });
    expect((await rebuild(header("c1", { turn_count: 0 }))).has_more).toBe(false);
  });

  it("answers has_more true for a chat with turns and no resident window", async () => {
    seedSession("c1");
    expect((await rebuild(header("c1", { turn_count: 3 }))).has_more).toBe(true);
  });

  it("replaces last_turn_outcome, and an ABSENT one is a CLEAR", async () => {
    // The server's own statement rather than client memory, unlike `model` and `effort_levels` one
    // bullet over, which carry forward.
    seedSession("c1", { last_turn_outcome: "failed" });
    expect(
      (await rebuild(header("c1", { last_turn_outcome: "completed" }))).last_turn_outcome,
    ).toBe("completed");

    seedSession("c1", { last_turn_outcome: "failed" });
    expect((await rebuild(header("c1"))).last_turn_outcome).toBeUndefined();
  });

  it("replaces pending_model in both directions", async () => {
    // The badge's ONE input: an absent `pending_model` is a CLEAR, which is how a pick applied at a
    // turn's close reaches every device.
    seedSession("c1", { pending_model: "old" });
    expect((await rebuild(header("c1", { pending_model: "new" }))).pending_model).toBe("new");

    seedSession("c1", { pending_model: "old" });
    expect((await rebuild(header("c1"))).pending_model).toBeUndefined();
  });

  it("replaces the interrupt mode and the queued rows, an absent one reading as none", async () => {
    seedSession("c1", { interrupt_mode: "queue", queued: [{ id: "m-old", text: "gone" }] });
    const rebuilt = await rebuild(header("c1"));
    expect(rebuilt.interrupt_mode).toBe("steer");
    expect(rebuilt.queued).toEqual([]);

    const fresh = await rebuild(
      header("c1", { interrupt_mode: "queue", queued_prompts: [{ id: "m-q1", text: "next" }] }),
    );
    expect(fresh.interrupt_mode).toBe("queue");
    expect(fresh.queued?.map((q) => q.id)).toEqual(["m-q1"]);
  });

  it("replaces updated_at, which is required on the wire", async () => {
    seedSession("c1", { updated_at: 1 });
    expect((await rebuild(header("c1", { updated_at: 999 }))).updated_at).toBe(999);
  });

  it("carries every client-only projection across the rebuild", async () => {
    // The server sends none of these, so rebuilding from a header alone silently resets them — and
    // `loadList` runs on every `connected`, so an ordinary network recovery dropped the agent's
    // declared status and read a loaded chat as never-loaded.
    const s = seedSession("c1", {
      thinking: true,
      working_label: "Working",
      agent_status: "waiting_on_user",
      residency: "loaded",
      effort_levels: [{ id: "max", name: "Max" }],
      effort_active: "max",
      steers: [{ id: "s1", text: "wait", origin: "user" }],
    });
    seedTurn(s, "t1", [turnOpen("t1", 1)]);

    const row = await rebuild(header("c1", { turn_count: 1 }));
    expect(row.thinking).toBe(true);
    expect(row.working_label).toBe("Working");
    expect(row.agent_status).toBe("waiting_on_user");
    expect(row.residency).toBe("loaded");
    expect(row.effort_levels).toEqual([{ id: "max", name: "Max" }]);
    expect(row.effort_active).toBe("max");
    expect(row.steers).toEqual([{ id: "s1", text: "wait", origin: "user" }]);
  });

  it("keeps the client's effort catalog when the header carries none", async () => {
    seedSession("c1", { effort_levels: [{ id: "high", name: "High" }] });
    const row = await rebuild(header("c1"));
    expect(row.effort_levels).toEqual([{ id: "high", name: "High" }]);
  });
});

describe("loadList observes the chats stamp", () => {
  it("observes it once the list is committed", async () => {
    mockApiGetTyped.mockResolvedValue({
      chats: [header("c1")],
      subject: stamp("chats", "", "9"),
    });

    await loadList();
    expect(hasSubject("chats", "")).toBe(true);
    expect(heldVersion("chats", "")).toBe("9");
  });

  it("observes nothing when the list does not decode", async () => {
    mockApiGetTyped.mockResolvedValue(null);
    await loadList();
    expect(heldCount()).toBe(0);
  });
});

describe("the retry ladder behind a failed list load", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("climbs three rungs at a doubling delay and then gives up", async () => {
    // Bounded because the next SSE `connected` refetches the list anyway: what this covers is the
    // window in between, which no other trigger revisits.
    mockApiGetTyped.mockResolvedValue(null);
    await loadList();
    expect(mockApiGetTyped).toHaveBeenCalledTimes(1);

    scheduleListRetry();
    for (const delay of [1000, 2000, 4000]) {
      await vi.advanceTimersByTimeAsync(delay);
    }
    expect(mockApiGetTyped).toHaveBeenCalledTimes(4);

    await vi.advanceTimersByTimeAsync(60_000);
    expect(mockApiGetTyped).toHaveBeenCalledTimes(4);
  });

  it("arms nothing when the failure was an ABORT", async () => {
    // An abort is a fact about a REQUEST, so it records no verdict about the server and a ladder
    // armed on `!ok` would chase a load a newer one superseded.
    const loader = await freshLoader();
    const controller = new AbortController();
    mockApiGetTyped.mockImplementation(() => {
      controller.abort();
      return Promise.resolve(null);
    });
    expect(await loader.loadList(controller.signal)).toBe(false);

    loader.scheduleListRetry();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(mockApiGetTyped).toHaveBeenCalledTimes(1);
  });

  it("arms nothing when the last attempt reached the server", async () => {
    mockApiGetTyped.mockResolvedValue({ chats: [] });
    await loadList();

    scheduleListRetry();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(mockApiGetTyped).toHaveBeenCalledTimes(1);
  });

  it("is answered by any list that lands", async () => {
    mockApiGetTyped.mockResolvedValue(null);
    await loadList();
    scheduleListRetry();

    mockApiGetTyped.mockResolvedValue({ chats: [] });
    await vi.advanceTimersByTimeAsync(1000);
    expect(mockApiGetTyped).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(60_000);
    expect(mockApiGetTyped).toHaveBeenCalledTimes(2);
  });

  it("REPLACES a ladder in flight rather than stacking beside it", async () => {
    // Without this the orphaned timer also fires and the ladder fetches twice per rung.
    mockApiGetTyped.mockResolvedValue(null);
    await loadList();
    scheduleListRetry();
    scheduleListRetry();

    await vi.advanceTimersByTimeAsync(1000);
    expect(mockApiGetTyped).toHaveBeenCalledTimes(2);
  });
});

describe("loadMessages walks a page's entries into whole turns", () => {
  it("opens a TurnState per turn_open and appends every other entry at its seq", async () => {
    seedSession("c1");
    answerPage("c1", {
      entries: [turnOpen("t1", 1), textEntry("t1", 1), turnOpen("t2", 2), textEntry("t2", 1)],
      header: { turn_count: 2 },
    });

    expect(await loadMessages("c1")).toBe(true);
    expect(sessions.get("c1")?.turn_order).toEqual(["t1", "t2"]);
    expect(seqsOf("c1", "t1")).toEqual([0, 1]);
    expect(seqsOf("c1", "t2")).toEqual([0, 1]);
  });

  it("keeps FILE order when two turns interleave", async () => {
    // Two of a chat's turns are open together in one state, so a few lines of two turns alternate
    // and nothing may assume a turn is a contiguous byte range.
    seedSession("c1");
    answerPage("c1", {
      entries: [
        turnOpen("t1", 1),
        turnOpen("t2", 2),
        textEntry("t1", 1),
        textEntry("t2", 1),
        textEntry("t1", 2),
      ],
      header: { turn_count: 2 },
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.turn_order).toEqual(["t1", "t2"]);
    expect(seqsOf("c1", "t1")).toEqual([0, 1, 2]);
    expect(seqsOf("c1", "t2")).toEqual([0, 1]);
  });

  it("caches a turn_close's seq as closeAt", async () => {
    seedSession("c1");
    answerPage("c1", {
      entries: [turnOpen("t1", 1), entry("t1", 1, "turn_close", { outcome: "completed" })],
      header: { turn_count: 1 },
    });

    await loadMessages("c1");
    expect(turnOf("c1", "t1").closeAt).toBe(1);
  });

  it("is idempotent by turn id", async () => {
    seedSession("c1");
    answerPage("c1", {
      entries: [turnOpen("t1", 1), turnOpen("t1", 1), textEntry("t1", 1)],
      header: { turn_count: 1 },
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.turn_order).toEqual(["t1"]);
    expect(seqsOf("c1", "t1")).toEqual([0, 1]);
  });

  it("seats each open tail under its own (turn, lane)", async () => {
    seedSession("c1");
    answerPage("c1", {
      entries: [turnOpen("t1", 1)],
      open: [openTail("t1", "o-agent", 3), openTail("t1", "o-sub", 2, { lane: "sub-1" })],
      header: { turn_count: 1 },
    });

    await loadMessages("c1");
    const open = turnOf("c1", "t1").openEntries;
    expect([...open.keys()].sort()).toEqual(["", "sub-1"]);
    expect(open.get("")?.id).toBe("o-agent");
    // A lane-less tail is normalised to `""`, so no reader has to fold an absent lane.
    expect(open.get("")?.lane).toBe("");
    expect(open.get("sub-1")?.n).toBe(2);
  });
});

describe("a gap the walk cannot fold in is a HOLE, and it asks for one range read", () => {
  it("records the FIRST gap's watermark and asks past it", async () => {
    // The first gap's watermark, kept: a later entry of the same turn sits past it, so its index
    // would ask the repair for less than the turn is actually missing.
    seedSession("c1");
    answerPage("c1", {
      entries: [turnOpen("t1", 1), textEntry("t1", 2), textEntry("t1", 4)],
      header: { turn_count: 1 },
    });

    await loadMessages("c1");
    expect(seqsOf("c1", "t1")).toEqual([0]);
    expect(mockMarkWindowStale).toHaveBeenCalledWith("c1");
    expect(rangeReads()).toEqual([{ turnID: "t1", after: "0" }]);
  });

  it("asks for the WHOLE turn when the page did not open it", async () => {
    // A turn whose `turn_open` the tolerant decode dropped: the server serves whole turns, so an
    // entry naming a turn this page did not open can only be that.
    seedSession("c1");
    answerPage("c1", {
      entries: [turnOpen("t1", 1), textEntry("t2", 1)],
      header: { turn_count: 2 },
    });

    await loadMessages("c1");
    expect(rangeReads()).toEqual([{ turnID: "t2", after: null }]);
  });

  it("asks for the whole turn when a TAIL names a turn the walk did not open", async () => {
    seedSession("c1");
    answerPage("c1", {
      entries: [turnOpen("t1", 1)],
      open: [openTail("t9", "o-9", 1)],
      header: { turn_count: 1 },
    });

    await loadMessages("c1");
    expect(rangeReads()).toEqual([{ turnID: "t9", after: null }]);
  });

  it("marks the window BEFORE it repaints, and repaints what did arrive", async () => {
    // The store's own order: the window is stale in the same pass that renders the rest.
    seedSession("c1");
    answerPage("c1", {
      entries: [turnOpen("t1", 1), textEntry("t1", 3)],
      header: { turn_count: 1 },
    });

    await loadMessages("c1");
    const staleAt = mockMarkWindowStale.mock.invocationCallOrder[0] ?? 0;
    const bumpAt = mockBumpMessages.mock.invocationCallOrder[0] ?? 0;
    expect(staleAt).toBeLessThan(bumpAt);
    expect(mockBumpMessages).toHaveBeenCalledWith("c1", "load");
  });

  it("marks nothing when the page is contiguous", async () => {
    seedSession("c1");
    answerPage("c1", {
      entries: [turnOpen("t1", 1), textEntry("t1", 1)],
      header: { turn_count: 1 },
    });

    await loadMessages("c1");
    expect(mockMarkWindowStale).not.toHaveBeenCalled();
    expect(rangeReads()).toEqual([]);
  });
});

describe("an undecodable entry is DROPPED, never the window", () => {
  it("keeps the rest of the page and leaves the gap to the range read", async () => {
    // One unknown field may not cost the whole window: the member is dropped with a warn naming
    // what it was, and the `seq` gap it leaves reaches the same hole check every other gap does.
    seedSession("c1");
    answerWire({
      chat: header("c1", { turn_count: 1 }),
      entries: [
        turnOpen("c1-t1", 1),
        { id: "bad", turn: "c1-t1", kind: "text", ts: 2 },
        textEntry("c1-t1", 2),
      ],
      open_entries: [],
      has_more: false,
      subject: [],
      draft: "",
    });

    expect(await loadMessages("c1")).toBe(true);
    expect(seqsOf("c1", "c1-t1")).toEqual([0]);
    expect(rangeReads()).toEqual([{ turnID: "c1-t1", after: "0" }]);
    // The warn IS the signal, so it names the member rather than dropping it silently.
    expect(logLines(warn)).toMatch(/dropped text bad of turn c1-t1/);
  });

  it("drops an undecodable open tail the same way", async () => {
    seedSession("c1");
    answerWire({
      chat: header("c1", { turn_count: 1 }),
      entries: [turnOpen("t1", 1)],
      open_entries: [{ turn: "t1", id: "o", kind: "text" }],
      has_more: false,
      subject: [],
      draft: "",
    });

    expect(await loadMessages("c1")).toBe(true);
    expect(turnOf("c1", "t1").openEntries.size).toBe(0);
  });

  it("refuses the WHOLE answer when the container is not an array", async () => {
    // A reply whose `entries` is not an array is not a page with one bad line in it.
    seedSession("c1", { residency: "loaded" });
    answerWire({
      chat: header("c1"),
      entries: "nope",
      open_entries: [],
      has_more: false,
      subject: [],
      draft: "",
    });

    expect(await loadMessages("c1")).toBe(false);
    expect(sessions.get("c1")?.residency).toBe("load_failed");
  });

  it("tolerates an answer that carries no subject list at all", async () => {
    // Optional for the reason the `chats` stamp is: a server from before the stamp still answers a
    // usable page.
    seedSession("c1");
    answerWire({
      chat: header("c1", { turn_count: 1 }),
      entries: [turnOpen("t1", 1)],
      open_entries: [],
      has_more: false,
      draft: "",
    });

    expect(await loadMessages("c1")).toBe(true);
    expect(heldCount()).toBe(0);
  });
});

describe("mergeTurn keeps what landed while the request was in flight", () => {
  it("puts held entries past the page's end back on, contiguously", async () => {
    // The page is a point-in-time read, so an entry that landed during the flight is NEWER than the
    // answer and the answer is not entitled to drop it.
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1), textEntry("t1", 1), textEntry("t1", 2)]);
    answerPage("c1", {
      entries: [turnOpen("t1", 1), textEntry("t1", 1)],
      header: { turn_count: 1 },
    });

    await loadMessages("c1");
    expect(seqsOf("c1", "t1")).toEqual([0, 1, 2]);
  });

  it("stops at the first held entry whose seq is not next", async () => {
    const s = seedSession("c1");
    const held = seedTurn(s, "t1", [turnOpen("t1", 1)]);
    // A held array with a hole in it: index 1 carries seq 2, so nothing past the page's end may be
    // re-seated.
    held.entries.push(textEntry("t1", 2));
    answerPage("c1", { entries: [turnOpen("t1", 1)], header: { turn_count: 1 } });

    await loadMessages("c1");
    expect(seqsOf("c1", "t1")).toEqual([0]);
  });

  it("re-caches closeAt for a held turn_close it carried back", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1), entry("t1", 1, "turn_close", { outcome: "completed" })]);
    answerPage("c1", { entries: [turnOpen("t1", 1)], header: { turn_count: 1 } });

    await loadMessages("c1");
    expect(turnOf("c1", "t1").closeAt).toBe(1);
  });

  it("keeps a held tail the page carries at a LOWER n", async () => {
    // A delta that landed during the flight leaves the tail at a higher `n`.
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)], [openTail("t1", "o1", 5)]);
    answerPage("c1", {
      entries: [turnOpen("t1", 1)],
      open: [openTail("t1", "o1", 3)],
      header: { turn_count: 1 },
    });

    await loadMessages("c1");
    expect(turnOf("c1", "t1").openEntries.get("")?.n).toBe(5);
  });

  it("drops a held tail the page carries SEALED", async () => {
    // The page carrying that entry as a sealed line IS the answer that the tail is history.
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)], [openTail("t1", "o1", 5)]);
    answerPage("c1", {
      entries: [turnOpen("t1", 1), entry("t1", 1, "text", { text: "done" }, { id: "o1" })],
      header: { turn_count: 1 },
    });

    await loadMessages("c1");
    expect(turnOf("c1", "t1").openEntries.size).toBe(0);
  });

  it("keeps a held tail in a lane the page says nothing about", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)], [openTail("t1", "o-sub", 2, { lane: "sub-1" })]);
    answerPage("c1", { entries: [turnOpen("t1", 1)], header: { turn_count: 1 } });

    await loadMessages("c1");
    expect(turnOf("c1", "t1").openEntries.get("sub-1")?.id).toBe("o-sub");
  });
});

describe("applyPage decides whose has_more is the answer", () => {
  it("PREPENDS an older page and takes the server's answer", async () => {
    const s = seedSession("c1", { has_more: true });
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", {
      entries: [turnOpen("t1", 1)],
      has_more: false,
      header: { turn_count: 2 },
    });

    await loadMessages("c1", "t2");
    expect(sessions.get("c1")?.turn_order).toEqual(["t1", "t2"]);
    expect(sessions.get("c1")?.has_more).toBe(false);
  });

  it("prepends only the turns it does not already hold", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", {
      entries: [turnOpen("t1", 1), turnOpen("t2", 2)],
      header: { turn_count: 2 },
    });

    await loadMessages("c1", "t2");
    expect(sessions.get("c1")?.turn_order).toEqual(["t1", "t2"]);
  });

  it("takes the server's answer when a newest page STARTS the window", async () => {
    seedSession("c1", { has_more: false });
    answerPage("c1", {
      entries: [turnOpen("t1", 1)],
      has_more: true,
      header: { turn_count: 9 },
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.has_more).toBe(true);
  });

  it("keeps the resident turns OLDER than the page's oldest, in front of it", async () => {
    // Those are pages this client already fetched that the answer says nothing about, so dropping
    // them would throw a paged-up reader's history away on every no-cursor reload.
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    seedTurn(s, "t3", [turnOpen("t3", 3)]);
    answerPage("c1", {
      entries: [turnOpen("t2", 2), turnOpen("t3", 3)],
      has_more: false,
      header: { turn_count: 3 },
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.turn_order).toEqual(["t1", "t2", "t3"]);
  });

  it("DERIVES has_more when older turns sit in front of the page", async () => {
    // The page said nothing about this window's left edge, so `has_more` falls back to the
    // derivation rather than preserving the previous value: preserving is only right when that
    // value was an ANSWER.
    const s = seedSession("c1", { has_more: true });
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", {
      entries: [turnOpen("t2", 2)],
      has_more: true,
      header: { turn_count: 2 },
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.has_more).toBe(false);
  });

  it("keeps a turn that OPENED while the request was in flight, behind the page's newest", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    answerPage("c1", { entries: [turnOpen("t1", 1)], header: { turn_count: 1 } });
    const answer = mockApiGetTyped.getMockImplementation();
    // t2 opens while the read is out: seated by the frame, absent from the answer.
    mockApiGetTyped.mockImplementationOnce((...args: unknown[]) => {
      seedTurn(s, "t2", [turnOpen("t2", 2)]);
      return answer?.(...args);
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.turn_order).toEqual(["t1", "t2"]);
  });

  it("REPLACES the window when the page overlaps nothing it holds", async () => {
    // No overlap means the window moved out from under what is held, and then the page replaces,
    // which is the honest answer.
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    answerPage("c1", {
      entries: [turnOpen("t8", 8), turnOpen("t9", 9)],
      has_more: true,
      header: { turn_count: 9 },
    });

    await loadMessages("c1");
    expect(sessions.get("c1")?.turn_order).toEqual(["t8", "t9"]);
    expect(sessions.get("c1")?.has_more).toBe(true);
  });

  it("takes the server's turn_count before it reads has_more", async () => {
    const s = seedSession("c1", { turn_count: 0 });
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", { entries: [turnOpen("t2", 2)], header: { turn_count: 5 } });

    await loadMessages("c1");
    expect(sessions.get("c1")?.turn_count).toBe(5);
    expect(sessions.get("c1")?.has_more).toBe(true);
  });
});

describe("residency", () => {
  it("marks the chat loaded on a successful newest-page load", async () => {
    seedSession("c1");
    answerPage("c1");
    await loadMessages("c1");
    expect(sessions.get("c1")?.residency).toBe("loaded");
  });

  it("asserts nothing on an older-page prepend", async () => {
    const s = seedSession("c1", { residency: "partial" });
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", { entries: [turnOpen("t1", 1)], header: { turn_count: 2 } });

    await loadMessages("c1", "t2");
    expect(sessions.get("c1")?.residency).toBe("partial");
  });

  it("marks a failed newest-page load load_failed", async () => {
    seedSession("c1", { residency: "loaded" });
    mockApiGetTyped.mockResolvedValue(null);

    expect(await loadMessages("c1")).toBe(false);
    expect(sessions.get("c1")?.residency).toBe("load_failed");
  });

  it("leaves residency alone when an older-page load fails", async () => {
    seedSession("c1", { residency: "loaded" });
    mockApiGetTyped.mockResolvedValue(null);

    expect(await loadMessages("c1", "t2")).toBe(false);
    expect(sessions.get("c1")?.residency).toBe("loaded");
  });
});

describe("the digest stamps a window certifies", () => {
  it("observes the chat stamp and one live_turn per open turn, epoch included", async () => {
    seedSession("c1");
    answerPage("c1", {
      subject: [stamp("chat", "c1", "12", "e1"), stamp("live_turn", "c1/t1", "3", "e1")],
    });

    await loadMessages("c1");
    expect(heldVersion("chat", "c1")).toBe("12");
    expect(heldVersion("live_turn", "c1/t1")).toBe("3");
  });

  it("observes them AFTER the commit", async () => {
    seedSession("c1");
    answerPage("c1", { subject: [stamp("chat", "c1", "1")] });

    await loadMessages("c1");
    // A stamp certifies exactly the entries this page served, so it may not be recorded before they
    // are in the store.
    expect(sessions.get("c1")?.residency).toBe("loaded");
    expect(hasSubject("chat", "c1")).toBe(true);
  });

  it("observes nothing on an older-page prepend", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", { entries: [turnOpen("t1", 1)], subject: [stamp("chat", "c1", "1")] });

    await loadMessages("c1", "t2");
    expect(heldCount()).toBe(0);
  });

  it("observes nothing when the load fails", async () => {
    seedSession("c1");
    mockApiGetTyped.mockResolvedValue(null);

    await loadMessages("c1");
    expect(heldCount()).toBe(0);
  });
});

describe("loadMessages turn_open", () => {
  it("stores the server's statement from a newest page, in both directions", async () => {
    seedSession("c1");
    answerPage("c1", { live: true });
    await loadMessages("c1");
    expect(mockSetTurnOpen).toHaveBeenCalledWith("c1", true);

    answerPage("c1", { live: false });
    await loadMessages("c1");
    expect(mockSetTurnOpen).toHaveBeenCalledWith("c1", false);
  });

  it("FORGETS it when the answer carries no statement", async () => {
    // An ABSENT field is not a statement, and a `true` left standing would keep `turnLive`
    // answering live off an answer nothing restates.
    seedSession("c1", { turn_open: true });
    answerPage("c1");

    await loadMessages("c1");
    expect(sessions.get("c1")?.turn_open).toBeUndefined();
    expect(mockSetTurnOpen).not.toHaveBeenCalled();
  });

  it("does not write it on an older-page fetch", async () => {
    const s = seedSession("c1", { turn_open: true });
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", { entries: [turnOpen("t1", 1)], live: false });

    await loadMessages("c1", "t2");
    expect(mockSetTurnOpen).not.toHaveBeenCalled();
    expect(sessions.get("c1")?.turn_open).toBe(true);
  });

  it("runs the teardown on a stated live === false, after the repaint", async () => {
    // A stated `false` covers the chat's WHOLE liveness, so it retracts a `thinking` this client is
    // holding for a turn that is over — and the repaint and the dot must read one settled window.
    seedSession("c1");
    answerPage("c1", { live: false });

    await loadMessages("c1");
    expect(mockClearTurnState).toHaveBeenCalledWith("c1");
    const bumpAt = mockBumpMessages.mock.invocationCallOrder[0] ?? 0;
    const tearAt = mockClearTurnState.mock.invocationCallOrder[0] ?? 0;
    expect(bumpAt).toBeLessThan(tearAt);
  });

  it("runs no teardown on a live turn or on a silent answer", async () => {
    seedSession("c1");
    answerPage("c1", { live: true });
    await loadMessages("c1");
    answerPage("c1");
    await loadMessages("c1");
    expect(mockClearTurnState).not.toHaveBeenCalled();
  });
});

describe("loadMessages publishes a fetched window's tool calls", () => {
  it("hands the newest page's own turns to the card channel", async () => {
    // A card already on screen has ONE refresh channel, the per-call signal, and the repaint writes
    // none — so a card built from a truncated copy would keep its hint for the life of the
    // document.
    seedSession("c1");
    answerPage("c1", {
      entries: [turnOpen("t1", 1), turnOpen("t2", 2)],
      header: { turn_count: 2 },
    });

    await loadMessages("c1");
    expect(mockRepublishToolCalls).toHaveBeenCalledExactlyOnceWith("c1", ["t1", "t2"]);
  });

  it("publishes the fetched turns only, never the local tail it kept", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", { entries: [turnOpen("t2", 2)], header: { turn_count: 2 } });

    await loadMessages("c1");
    expect(mockRepublishToolCalls).toHaveBeenCalledExactlyOnceWith("c1", ["t2"]);
  });

  it("publishes nothing on an older-page prepend", async () => {
    // A prepended page mounts its cards fresh.
    const s = seedSession("c1");
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", { entries: [turnOpen("t1", 1)] });

    await loadMessages("c1", "t2");
    expect(mockRepublishToolCalls).not.toHaveBeenCalled();
  });

  it("publishes nothing on a failed load", async () => {
    seedSession("c1");
    mockApiGetTyped.mockResolvedValue(null);

    await loadMessages("c1");
    expect(mockRepublishToolCalls).not.toHaveBeenCalled();
  });
});

describe("loadMessages parks the server's draft", () => {
  it("parks it on a newest page", async () => {
    seedSession("c1");
    answerPage("c1", { draft: "half a sentence" });
    await loadMessages("c1");
    expect(sessions.get("c1")?.draft).toBe("half a sentence");
  });

  it("does not park it on an older-page fetch, which is a scroll-up rather than an open", async () => {
    const s = seedSession("c1", { draft: "kept" });
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", { entries: [turnOpen("t1", 1)], draft: "from the page" });

    await loadMessages("c1", "t2");
    expect(sessions.get("c1")?.draft).toBe("kept");
  });
});

describe("loadMessages announces a fetched window as a replay", () => {
  it("bumps the newest page with the load cause", async () => {
    seedSession("c1");
    answerPage("c1", { entries: [turnOpen("t1", 1)], header: { turn_count: 1 } });

    await loadMessages("c1");
    expect(mockBumpMessages).toHaveBeenCalledExactlyOnceWith("c1", "load");
  });

  it("bumps an older-page prepend with it too", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", { entries: [turnOpen("t1", 1)] });

    await loadMessages("c1", "t2");
    expect(mockBumpMessages).toHaveBeenCalledExactlyOnceWith("c1", "load");
  });

  it("writes nothing at all when the read was aborted", async () => {
    const s = seedSession("c1", { residency: "loaded" });
    const controller = new AbortController();
    mockApiGetTyped.mockImplementation(() => {
      controller.abort();
      return Promise.resolve(null);
    });

    expect(await loadMessages("c1", undefined, controller.signal)).toBe(false);
    expect(s.residency).toBe("loaded");
    expect(mockBumpMessages).not.toHaveBeenCalled();
  });

  it("writes nothing when the chat left the store during the flight", async () => {
    seedSession("c1");
    mockApiGetTyped.mockImplementation(() => {
      sessions.delete("c1");
      return Promise.resolve({
        chat: header("c1"),
        entries: [],
        open_entries: [],
        has_more: false,
        live: undefined,
        subject: [],
        draft: "",
      });
    });

    expect(await loadMessages("c1")).toBe(false);
    expect(mockBumpMessages).not.toHaveBeenCalled();
  });
});

describe("confirmChatExists asks the SERVER about one chat id", () => {
  function answer(over: { ok?: boolean; data?: unknown; status?: number; error?: string }): void {
    mockApiGetTypedOrError.mockResolvedValue({
      ok: over.ok ?? false,
      data: over.data ?? null,
      status: over.status ?? 0,
      error: over.error ?? "",
    });
  }

  it("adopts the header through the ONE door and answers exists", async () => {
    // The same door the missed `chat_created` frame would have used, so no second
    // Session-construction rule appears and the deep link opens.
    answer({ ok: true, status: 200, data: { chat: header("c-abc") } });

    expect(await confirmChatExists("c-abc")).toBe("exists");
    expect(mockUpsertHeader).toHaveBeenCalledExactlyOnceWith(header("c-abc"));
  });

  it("asks for the cheapest page the endpoint will serve", async () => {
    // The ONE read that names a limit: the transcript is not what is being asked about.
    answer({ ok: true, status: 200, data: { chat: header("c-abc") } });

    await confirmChatExists("c-abc");
    expect(String(mockApiGetTypedOrError.mock.calls[0]?.[0])).toBe("/api/chats/c-abc?limit=1");
  });

  it("percent-encodes the id it asks about", async () => {
    answer({ status: 404 });
    await confirmChatExists("a/b");
    expect(String(mockApiGetTypedOrError.mock.calls[0]?.[0])).toBe("/api/chats/a%2Fb?limit=1");
  });

  it("reads a 404 as the server reading its own store", async () => {
    answer({ status: 404 });
    expect(await confirmChatExists("c-abc")).toBe("gone");
  });

  it("reads a 400 as gone only for an id this client can SEE is not a chat id", async () => {
    answer({ status: 400 });
    expect(await confirmChatExists("not a chat id")).toBe("gone");
  });

  it("refuses to read a request-level 400 as no such chat", async () => {
    // A stale CSRF header, a host check or a body limit is not evidence about a conversation, and
    // reading one as "no such chat" is the false-terminal claim this whole path exists to remove.
    answer({ status: 400 });
    expect(await confirmChatExists("c-abc")).toBe("unresolved");
  });

  const shaped: [string, boolean][] = [
    ["c-abc", true],
    ["Abc_012-xyz", true],
    ["", false],
    ["a".repeat(128), true],
    ["a".repeat(129), false],
    ["c abc", false],
    ["c.abc", false],
    ["c/abc", false],
    ["café", false],
  ];
  it.each(shaped)(
    "shapes %s as %s, at least as permissively as the server's gate",
    async (id, ok) => {
      // KEEP IT AT LEAST AS PERMISSIVE as `ids.ValidChatID`: accepting an id the server would
      // refuse costs one non-terminal `unresolved`, while refusing one it would ACCEPT reads a
      // request-level 400 as "no such chat".
      answer({ status: 400 });
      expect(await confirmChatExists(id)).toBe(ok ? "unresolved" : "gone");
    },
  );

  it("answers unresolved for every failure that is not the server answering", async () => {
    for (const status of [0, 401, 403, 409, 500, 502, 503]) {
      answer({ status });
      expect(await confirmChatExists("c-abc")).toBe("unresolved");
    }
  });

  it("answers unresolved for a 2xx whose body did not decode", async () => {
    answer({ ok: true, status: 200, data: null });
    expect(await confirmChatExists("c-abc")).toBe("unresolved");
  });

  it("adopts nothing on any answer that is not a decoded 2xx", async () => {
    answer({ status: 404 });
    await confirmChatExists("c-abc");
    expect(mockUpsertHeader).not.toHaveBeenCalled();
  });
});

describe("the range read repairs one turn", () => {
  function answerRange(o: {
    entries?: Entry[];
    open?: OpenEntry[];
    subject?: SubjectStamp[];
  }): void {
    mockApiGetTyped.mockResolvedValue({
      entries: o.entries ?? [],
      open_entries: o.open ?? [],
      subject: o.subject ?? [],
    });
  }

  it("asks past `after` when the store holds part of the turn", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    answerRange({ entries: [textEntry("t1", 1)] });

    requestTurnRange("c1", "t1", 0);
    await vi.waitFor(() => expect(mockApiGetTyped).toHaveBeenCalled());
    expect(String(mockApiGetTyped.mock.calls[0]?.[0])).toBe("/api/chats/c1/turns/t1?after=0");
  });

  it("omits `after` for a turn the store does not hold at all", async () => {
    // Which is what a lost `turn_opened` asks for.
    seedSession("c1");
    answerRange({ entries: [turnOpen("t1", 1)] });

    requestTurnRange("c1", "t1");
    await vi.waitFor(() => expect(mockApiGetTyped).toHaveBeenCalled());
    expect(String(mockApiGetTyped.mock.calls[0]?.[0])).toBe("/api/chats/c1/turns/t1");
  });

  it("seats the answer's entries from the held turn's own end", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    answerRange({ entries: [textEntry("t1", 1), textEntry("t1", 2)] });

    requestTurnRange("c1", "t1", 0);
    await vi.waitFor(() => expect(seqsOf("c1", "t1")).toEqual([0, 1, 2]));
    expect(mockBumpMessages).toHaveBeenCalledWith("c1", "load");
  });

  it("REPLACES the turn's open tails with the answer's, lane set included", async () => {
    const s = seedSession("c1");
    seedTurn(
      s,
      "t1",
      [turnOpen("t1", 1)],
      [openTail("t1", "stale", 1), openTail("t1", "stale-sub", 1, { lane: "sub-1" })],
    );
    answerRange({ entries: [textEntry("t1", 1)], open: [openTail("t1", "fresh", 4)] });

    requestTurnRange("c1", "t1", 0);
    await vi.waitFor(() => expect(turnOf("c1", "t1").entries).toHaveLength(2));
    expect([...turnOf("c1", "t1").openEntries.values()].map((o) => o.id)).toEqual(["fresh"]);
  });

  it("stops at a SECOND gap rather than folding an entry in at the wrong index", async () => {
    // The answer's entries are seated contiguously from the held turn's own end and the walk stops
    // at the first `seq` that is not next, which is a second gap.
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    answerRange({ entries: [textEntry("t1", 1), textEntry("t1", 3)] });

    requestTurnRange("c1", "t1", 0);
    await vi.waitFor(() => expect(seqsOf("c1", "t1")).toEqual([0, 1]));
    expect(logLines(warn)).toMatch(/turn range: c1 t1 left a gap at seq 2/);
  });

  it("seats a whole-turn answer by its turn_open.n among the resident turns", async () => {
    // The ordinal is session-absolute in every window, so it orders a repaired turn against the
    // ones already held without a second projection.
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    seedTurn(s, "t3", [turnOpen("t3", 3)]);
    answerRange({ entries: [turnOpen("t2", 2)] });

    requestTurnRange("c1", "t2");
    await vi.waitFor(() => expect(sessions.get("c1")?.turn_order).toEqual(["t1", "t2", "t3"]));
  });

  it("seats a turn whose ordinal cannot be read at the END, where a newer turn belongs", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    answerRange({ entries: [entry("t9", 0, "turn_open", { source: "prompt" })] });

    requestTurnRange("c1", "t9");
    await vi.waitFor(() => expect(sessions.get("c1")?.turn_order).toEqual(["t1", "t9"]));
  });

  it("seats nothing and asks nothing again when the answer carried no turn_open", async () => {
    // There is no turn to render and asking again would ask the same question.
    seedSession("c1");
    answerRange({ entries: [textEntry("t1", 1)] });

    requestTurnRange("c1", "t1");
    await vi.waitFor(() => expect(logLines(warn)).toMatch(/answered no turn_open/));
    expect(sessions.get("c1")?.turns.size).toBe(0);
    expect(rangeReads()).toHaveLength(1);
  });

  it("asks ONCE per (chat, turn) while a read is in flight", async () => {
    // A burst of holes on one turn asks once, because the answer covers every gap that arrived
    // while it was out.
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    let release = (): void => undefined;
    mockApiGetTyped.mockImplementation(
      () =>
        new Promise((resolve) => {
          release = () => resolve({ entries: [], open_entries: [], subject: [] });
        }),
    );

    requestTurnRange("c1", "t1", 0);
    await vi.waitFor(() => expect(mockApiGetTyped).toHaveBeenCalledTimes(1));
    requestTurnRange("c1", "t1", 0);
    expect(mockApiGetTyped).toHaveBeenCalledTimes(1);

    release();
    await vi.waitFor(() => expect(mockBumpMessages).toHaveBeenCalled());
    requestTurnRange("c1", "t1", 0);
    expect(mockApiGetTyped).toHaveBeenCalledTimes(2);
    // Release the last one too: the in-flight table is module state, so a repair left out here
    // would make every later case's `requestTurnRange("c1", "t1", …)` a no-op.
    release();
    await vi.waitFor(() => expect(mockBumpMessages).toHaveBeenCalledTimes(2));
  });

  it("leaves the window PARTIAL while another repair is still out", async () => {
    // The restore is read after one lands, so `residency` may go back to `loaded` only when nothing
    // is missing rather than after whichever answer arrives last. The restore ITSELF is not
    // observable.
    const s = seedSession("c1", { residency: "partial" });
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    seedTurn(s, "t2", [turnOpen("t2", 1)]);
    let release = (): void => undefined;
    mockApiGetTyped.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          release = () => resolve({ entries: [], open_entries: [], subject: [] });
        }),
    );
    mockApiGetTyped.mockResolvedValue({
      entries: [textEntry("t1", 1)],
      open_entries: [],
      subject: [],
    });

    requestTurnRange("c1", "t2", 0);
    requestTurnRange("c1", "t1", 0);
    await vi.waitFor(() => expect(seqsOf("c1", "t1")).toEqual([0, 1]));
    expect(sessions.get("c1")?.residency).toBe("partial");

    release();
    await vi.waitFor(() => expect(mockApiGetTyped).toHaveBeenCalledTimes(2));
  });

  it("leaves the window stale when the repair gets no answer", async () => {
    // A repair that gets no answer must not hand the store a window it will trust.
    const s = seedSession("c1", { residency: "partial" });
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    mockApiGetTyped.mockResolvedValue(null);

    requestTurnRange("c1", "t1", 0);
    await vi.waitFor(() => expect(logLines(warn)).toMatch(/turn range: no answer for c1 t1/));
    expect(sessions.get("c1")?.residency).toBe("partial");
    expect(mockBumpMessages).not.toHaveBeenCalled();
  });

  it("observes the stamps the answer certifies", async () => {
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)]);
    answerRange({
      entries: [textEntry("t1", 1)],
      subject: [stamp("live_turn", "c1/t1", "7")],
    });

    requestTurnRange("c1", "t1", 0);
    await vi.waitFor(() => expect(heldVersion("live_turn", "c1/t1")).toBe("7"));
  });

  it("writes nothing for a chat the store no longer holds", async () => {
    answerRange({ entries: [turnOpen("t1", 1)] });
    requestTurnRange("c-gone", "t1");
    await vi.waitFor(() => expect(mockApiGetTyped).toHaveBeenCalled());
    expect(mockBumpMessages).not.toHaveBeenCalled();
  });
});

// Module state: `chatListLoaded`, `serverMayAnswer` and the outcome counters.

let bootSeq = 0;

/** A fresh `store-load` instance, so the latch starts where a page load starts. */
async function freshLoader(): Promise<typeof StoreLoad> {
  vi.resetModules();
  bootSeq++;
  return (await import(/* @vite-ignore */ `./store-load.ts?boot=${bootSeq}`)) as typeof StoreLoad;
}

describe("chatListLoaded", () => {
  it("is false before any list has been read", async () => {
    const loader = await freshLoader();
    expect(loader.chatListLoaded()).toBe(false);
  });

  it("is true for a list that landed EMPTY, which is a real answer", async () => {
    // The distinction the predicate exists for, from the side that is easy to get wrong: a server
    // with no chats HAS answered, so a deep link naming one is genuinely dead.
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue({ chats: [] });

    expect(await loader.loadList()).toBe(true);
    expect(loader.chatListLoaded()).toBe(true);
  });

  it("stays false when the fetch failed", async () => {
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue(null);

    expect(await loader.loadList()).toBe(false);
    expect(loader.chatListLoaded()).toBe(false);
  });

  it("stays true after a LATER failed refetch", async () => {
    // Latched rather than a snapshot of the last attempt: once a list has landed the store holds a
    // row per chat, and a failed refetch does not un-know them.
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue({ chats: [header("c1")] });
    await loader.loadList();

    mockApiGetTyped.mockResolvedValue(null);
    expect(await loader.loadList()).toBe(false);
    expect(loader.chatListLoaded()).toBe(true);
  });
});

describe("serverMayAnswer", () => {
  it("is true before anything has been tried", async () => {
    const loader = await freshLoader();
    expect(loader.serverMayAnswer()).toBe(true);
  });

  it("is false for a load that reached the network and failed", async () => {
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue(null);
    await loader.loadList();
    expect(loader.serverMayAnswer()).toBe(false);
  });

  it("is true for a load that was ABORTED, which is routine", async () => {
    const loader = await freshLoader();
    const controller = new AbortController();
    mockApiGetTyped.mockImplementation(() => {
      controller.abort();
      return Promise.resolve(null);
    });
    await loader.loadList(controller.signal);
    expect(loader.serverMayAnswer()).toBe(true);
  });

  it("stays true once a list has landed, even after the server goes down", async () => {
    // `listLoaded` is LATCHED where the reach is not, so rows in the store outlive a server that
    // has since gone away.
    const loader = await freshLoader();
    mockApiGetTyped.mockResolvedValue({ chats: [header("c1")] });
    await loader.loadList();

    mockApiGetTyped.mockResolvedValue(null);
    await loader.loadList();
    expect(loader.serverMayAnswer()).toBe(true);
  });
});

describe("the refetch-outcome line", () => {
  // The counters are module state, so each case takes a fresh instance and its counts start at
  // zero. The mocked GET performs no fetch, so every line ends in `proto=?`; the regexes stop at
  // `proto=` so they hold for a real request too.
  it("warns with the chat id and the counts when a newest-page load fails", async () => {
    const loader = await freshLoader();
    seedSession("c1");
    mockApiGetTyped.mockResolvedValue(null);

    await loader.loadMessages("c1");
    expect(logLines(warn)).toMatch(
      /^chat_get: c1 load_failed changed=0 unchanged=0 load_failed=1 proto=/,
    );
  });

  it("reports changed when the page moved the window", async () => {
    const loader = await freshLoader();
    seedSession("c1");
    answerPage("c1", { entries: [turnOpen("t1", 1)], header: { turn_count: 1 } });

    await loader.loadMessages("c1");
    expect(logLines(debug)).toMatch(
      /^chat_get: c1 changed changed=1 unchanged=0 load_failed=0 proto=/,
    );
  });

  it("reports unchanged when the page carried the window it already held", async () => {
    // The measurement is over turn ids plus each turn's entry ids and open tails: enough to tell a
    // page that changed nothing from one that did.
    const loader = await freshLoader();
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1), textEntry("t1", 1)]);
    answerPage("c1", {
      entries: [turnOpen("t1", 1), textEntry("t1", 1)],
      header: { turn_count: 1 },
    });

    await loader.loadMessages("c1");
    expect(logLines(debug)).toMatch(
      /^chat_get: c1 unchanged changed=0 unchanged=1 load_failed=0 proto=/,
    );
  });

  it("reads a moved open TAIL as a change", async () => {
    const loader = await freshLoader();
    const s = seedSession("c1");
    seedTurn(s, "t1", [turnOpen("t1", 1)], [openTail("t1", "o1", 1)]);
    answerPage("c1", {
      entries: [turnOpen("t1", 1)],
      open: [openTail("t1", "o1", 4)],
      header: { turn_count: 1 },
    });

    await loader.loadMessages("c1");
    expect(logLines(debug)).toMatch(/c1 changed changed=1/);
  });

  it("reports nothing at all on an older-page prepend", async () => {
    const loader = await freshLoader();
    const s = seedSession("c1");
    seedTurn(s, "t2", [turnOpen("t2", 2)]);
    answerPage("c1", { entries: [turnOpen("t1", 1)] });

    await loader.loadMessages("c1", "t2");
    expect(debug).not.toHaveBeenCalled();
    expect(warn).not.toHaveBeenCalled();
  });
});
