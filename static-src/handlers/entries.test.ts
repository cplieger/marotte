// The entry-log handlers' ROUTING; the store is mocked (position, holes and `seq` rules are
// `store.test.ts`'s). Also: a repo-mutating call SETTLING marks the git badge dirty with its paths
// (empty = full rescan), and the latch runs on the TRANSITION only.
import { vi, describe, it, expect, beforeEach } from "vitest";

vi.mock("../store.js", () => ({
  appendEntry: vi.fn(),
  applyDelta: vi.fn(),
  applyToolProgress: vi.fn(() => ({})),
  get: vi.fn(() => ({ thinking: false })),
  markSteersCompacted: vi.fn(),
  openEntry: vi.fn(),
  openTurn: vi.fn(),
  sealEntry: vi.fn(),
  setCodeReferences: vi.fn(),
  setLiveRefusal: vi.fn(),
  setThinking: vi.fn(),
}));
vi.mock("../store-load.js", () => ({ requestTurnRange: vi.fn() }));
vi.mock("../git.js", () => ({ markGitDirty: vi.fn() }));

import "./entries.js";
import { dispatch } from "../bus.js";
import type { SSEPayloads } from "../bus.js";
import {
  appendEntry,
  applyDelta,
  applyToolProgress,
  get,
  markSteersCompacted,
  openEntry,
  openTurn,
  sealEntry,
  setCodeReferences,
  setLiveRefusal,
  setThinking,
} from "../store.js";
import { requestTurnRange } from "../store-load.js";
import { markGitDirty } from "../git.js";
import type { Entry } from "../types.js";

const opened = vi.mocked(openTurn);
const openEntryOp = vi.mocked(openEntry);
const delta = vi.mocked(applyDelta);
const sealed = vi.mocked(sealEntry);
const appended = vi.mocked(appendEntry);
const progress = vi.mocked(applyToolProgress);
const refs = vi.mocked(setCodeReferences);
const refusal = vi.mocked(setLiveRefusal);
const compacted = vi.mocked(markSteersCompacted);
const thinking = vi.mocked(setThinking);
const row = vi.mocked(get);
const rangeRead = vi.mocked(requestTurnRange);
const gitDirty = vi.mocked(markGitDirty);

const CHAT = "c1";
const TURN = "turn-1";

/** Every store operation the module can reach, so a "nothing happened" assertion names the
 *  whole set rather than whichever one a case happened to think of. */
const EVERY_OP = [
  opened,
  openEntryOp,
  delta,
  sealed,
  appended,
  progress,
  refs,
  refusal,
  compacted,
  thinking,
];

function send<K extends keyof SSEPayloads>(type: K, payload: unknown, chatID = CHAT): void {
  dispatch({ type, chat_id: chatID, payload });
}

function entry(kind: Entry["kind"], payload: unknown, over: Partial<Entry> = {}): Entry {
  const base: Entry = { id: "e1", turn: TURN, kind, seq: 1, ts: 2, payload };
  return Object.assign(base, over);
}

beforeEach(() => {
  vi.clearAllMocks();
  row.mockReturnValue({ thinking: false } as never);
  progress.mockReturnValue({} as never);
});

describe("each event is one call into one store operation", () => {
  it("opens the turn a turn_opened announces", () => {
    const e = entry("turn_open", { source: "prompt", n: 1 }, { seq: 0 });

    send("turn_opened", { entry: e });

    expect(opened).toHaveBeenCalledWith(CHAT, e);
    // No `thinking` latch here: a turn with no `turn_close` IS the liveness, and this frame
    // is what puts one in the store.
    expect(thinking).not.toHaveBeenCalled();
  });

  it("opens the entry an entry_opened announces", () => {
    const open = { turn: TURN, id: "e1", kind: "text", text: "hi", n: 1 };

    send("entry_opened", { open });

    expect(openEntryOp).toHaveBeenCalledWith(CHAT, open);
  });

  it("passes a delta's own five values through, lane included", () => {
    // `lane` rides the FRAME rather than being looked up from the entry id, which is what
    // lets a lane-scoped subscriber route without touching the turn.
    send("entry_delta", { turn: TURN, entry_id: "e1", lane: "sub-7", n: 3, delta: "more" });

    expect(delta).toHaveBeenCalledWith(CHAT, TURN, "e1", "sub-7", 3, "more");
  });

  it("passes a seal's position and count through", () => {
    send("entry_sealed", { turn: TURN, entry_id: "e1", lane: "", seq: 4, ts: 99, n: 6 });

    expect(sealed).toHaveBeenCalledWith(CHAT, TURN, "e1", "", 4, 99, 6);
  });

  it("appends a born-sealed entry", () => {
    const e = entry("tool_call", { id: "tc1", title: "Run Command", kind: "execute" });

    send("entry_appended", { entry: e });

    expect(appended).toHaveBeenCalledWith(CHAT, e);
  });

  it("replaces a turn's code references rather than adding to them", () => {
    const references = [{ license_name: "MIT", repository: "r", url: "u" }];

    send("code_references", { turn: TURN, references });

    expect(refs).toHaveBeenCalledWith(CHAT, TURN, references);
  });
});

describe("a RUN's frame belongs to the run store, not to a chat", () => {
  // A run's log arrives with an EMPTY chat id and one `onSSE` registration fans out to every
  // subscriber, so each run-scoped handler refuses it itself; table-driven so a new handler that
  // forgets fails. `code_references` is excluded: its payload carries no workflow id.
  const frames: [keyof SSEPayloads, unknown][] = [
    ["turn_opened", { entry: entry("turn_open", {}, { seq: 0 }) }],
    ["entry_opened", { open: { turn: TURN, id: "e1", kind: "text", text: "x", n: 1 } }],
    ["entry_delta", { turn: TURN, entry_id: "e1", lane: "", n: 2, delta: "x" }],
    ["entry_sealed", { turn: TURN, entry_id: "e1", lane: "", seq: 1, ts: 1, n: 2 }],
    ["entry_appended", { entry: entry("text", { text: "x" }) }],
    ["tool_progress", { turn: TURN, tool_call_id: "tc1" }],
  ];

  it.each(frames)("drops a %s carrying a workflow id", (type, payload) => {
    send(type, { ...(payload as object), workflow_id: "wf-1" }, "");

    for (const op of EVERY_OP) {
      expect(op).not.toHaveBeenCalled();
    }
    expect(rangeRead).not.toHaveBeenCalled();
  });
});

describe("the thinking latch", () => {
  it("latches on an entry_opened, because a stream is arriving", () => {
    send("entry_opened", { open: { turn: TURN, id: "e1", kind: "text", text: "hi", n: 1 } });

    expect(thinking).toHaveBeenCalledWith(CHAT, true);
  });

  it("runs on the TRANSITION only, so a second frame re-clears nothing", () => {
    // CARRIED from the deleted message suite. `setThinking(true)` clears the previous turn's
    // verdicts and churns the session signal, so a latch per delta would do both per frame.
    row.mockReturnValue({ thinking: true } as never);

    send("entry_delta", { turn: TURN, entry_id: "e1", lane: "", n: 2, delta: "more" });

    expect(delta).toHaveBeenCalled();
    expect(thinking).not.toHaveBeenCalled();
  });

  it("latches for a DELEGATE's lane too, because those entries are the parent's turn's", () => {
    send("entry_delta", { turn: TURN, entry_id: "e1", lane: "sub-7", n: 2, delta: "more" });

    expect(thinking).toHaveBeenCalledWith(CHAT, true);
  });

  it("latches nothing for a chat this client does not hold", () => {
    row.mockReturnValue(undefined);

    send("entry_delta", { turn: TURN, entry_id: "e1", lane: "", n: 2, delta: "more" });

    // The store applies the frame or reports its own hole; what must not happen is a latch
    // written against a row that is not there.
    expect(thinking).not.toHaveBeenCalled();
  });
});

describe("a live refusal", () => {
  // The seal is the carrier: a refusal-tagged chunk opens no entry, it SEALS the lane's
  // open one, so the frame the refusal branch publishes is the seal's own.
  it("is stamped from the seal frame that carried it", () => {
    const info = { category: "policy" };

    send("entry_sealed", {
      turn: TURN,
      entry_id: "e1",
      lane: "",
      seq: 4,
      ts: 99,
      n: 6,
      refusal: info,
    });

    expect(refusal).toHaveBeenCalledWith(CHAT, TURN, info);
  });

  it("is not stamped by an ordinary seal", () => {
    send("entry_sealed", { turn: TURN, entry_id: "e1", lane: "", seq: 4, ts: 99, n: 6 });

    expect(refusal).not.toHaveBeenCalled();
  });

  it("is not stamped by an open or a delta, which carry no refusal at all", () => {
    send("entry_opened", { open: { turn: TURN, id: "e1", kind: "text", text: "x", n: 1 } });
    send("entry_delta", { turn: TURN, entry_id: "e1", lane: "", n: 2, delta: "x" });

    expect(refusal).not.toHaveBeenCalled();
  });
});

describe("tool_progress", () => {
  it("folds a delta into the call the window holds", () => {
    const payload = { turn: TURN, tool_call_id: "tc1", output_delta: "line" };

    send("tool_progress", payload);

    expect(progress).toHaveBeenCalledWith(CHAT, TURN, payload);
    expect(rangeRead).not.toHaveBeenCalled();
  });

  it("asks for the turn's range when the window holds no such call", () => {
    // A card cannot be built from a delta, so the answer is the range read rather than a
    // synthesized create — the same repair every other hole takes.
    progress.mockReturnValue(undefined);

    send("tool_progress", { turn: TURN, tool_call_id: "tc1", output_delta: "line" });

    expect(rangeRead).toHaveBeenCalledWith(CHAT, TURN);
  });
});

describe("what an appended entry means beside its position", () => {
  it("marks the dock's waiting steers when a compaction lands", () => {
    // Arrival order is the whole rule: every row the dock holds NOW was queued before this
    // compaction, so it will be read against a summarized context.
    send("entry_appended", { entry: entry("compaction", { summary: "..." }) });

    expect(compacted).toHaveBeenCalledWith(CHAT);
  });

  it("marks the git badge dirty with the paths a settled write touched", () => {
    // CARRIED from the deleted message suite. Two sources because neither is complete alone:
    // `locations` is what a command reports, `diffs[].path` what a write carries.
    send("entry_appended", {
      entry: entry("tool_result", {
        status: "completed",
        kind: "write",
        locations: [{ path: "a.ts" }],
        diffs: [{ path: "b.ts" }],
      }),
    });

    expect(gitDirty).toHaveBeenCalledWith(["a.ts", "b.ts"]);
  });

  it("marks it with an EMPTY list when the call named nothing, which rescans everything", () => {
    // CARRIED: the honest answer for a mutation whose paths the call did not report, and the
    // half a "was it called" assertion would let a wrong-path list satisfy.
    send("entry_appended", { entry: entry("tool_result", { status: "completed", kind: "edit" }) });

    expect(gitDirty).toHaveBeenCalledWith([]);
  });

  it("skips a path the call reported as empty rather than rescanning on it", () => {
    send("entry_appended", {
      entry: entry("tool_result", {
        status: "completed",
        kind: "write",
        locations: [{ path: "" }, { path: "real.ts" }],
      }),
    });

    expect(gitDirty).toHaveBeenCalledWith(["real.ts"]);
  });

  it("says nothing about the tree for a result that did not complete", () => {
    // A failed or aborted write may have touched nothing, and the badge is a statement about
    // what IS on disk rather than about what was attempted.
    send("entry_appended", {
      entry: entry("tool_result", { status: "failed", kind: "write", diffs: [{ path: "a.ts" }] }),
    });

    expect(gitDirty).not.toHaveBeenCalled();
  });

  it("says nothing for a settled call whose kind does not mutate the repo", () => {
    // `isRepoMutatingKind` is the one vocabulary and is deliberately NOT mocked: a suite that
    // stubbed it would pass for a kind the real set does not name.
    send("entry_appended", {
      entry: entry("tool_result", {
        status: "completed",
        kind: "read",
        locations: [{ path: "a" }],
      }),
    });

    expect(gitDirty).not.toHaveBeenCalled();
  });

  it("says nothing for a settled call whose kind the appender did not copy over", () => {
    // Absent means none, which is what keeps a result with no `kind` off the mutating path
    // rather than reading as one.
    send("entry_appended", { entry: entry("tool_result", { status: "completed" }) });

    expect(gitDirty).not.toHaveBeenCalled();
  });

  it("leaves every other kind to the store alone", () => {
    for (const kind of ["text", "thinking", "tool_call", "steer", "plan", "turn_close"] as const) {
      send("entry_appended", { entry: entry(kind, {}) });
    }

    expect(appended).toHaveBeenCalledTimes(6);
    expect(compacted).not.toHaveBeenCalled();
    expect(gitDirty).not.toHaveBeenCalled();
  });
});
