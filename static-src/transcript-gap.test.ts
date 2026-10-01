// A chat's transcript survives ONE lost SSE frame: the fold detects the gap, asks for one
// range read, and the answer restores the state an undropped stream would have produced.
//
// Every drop position is enumerated rather than sampled, because the stream is a fixed list
// and its positions are the whole input space. The last frame is the one position no `seq`
// can see, so it is a case of its own and the `live_turn` digest stamp is what catches it.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import type * as ApiClient from "./api-client.js";
import type { Entry, OpenEntry } from "./wire/types.gen.js";
import type { Session } from "./types.js";

const api = vi.hoisted(() => ({ getTyped: vi.fn() }));

vi.mock("./api-client.js", async (importOriginal) => {
  const orig = await importOriginal<typeof ApiClient>();
  return { ...orig, apiGetTyped: api.getTyped };
});

import {
  setSessions,
  setActive,
  get,
  defaultUsage,
  transcriptStale,
  registerTurnRepair,
  openTurn,
  appendEntry,
  openEntry,
  applyDelta,
  sealEntry,
} from "./store.js";
import { requestTurnRange } from "./store-load.js";
import { observeStamp, _resetForTest as resetFreshness } from "./subject-versions.js";
import { clearAllEntrySigs } from "./store-signals.js";

const TURN = "t1";

/** One SSE frame, as the operation the entry handler folds it into. Total over the six
 *  entry events, so a seventh fails the type check rather than being dropped silently. */
type Frame =
  | { readonly kind: "turn_opened"; readonly entry: Entry }
  | { readonly kind: "entry_appended"; readonly entry: Entry }
  | { readonly kind: "entry_opened"; readonly open: OpenEntry }
  | {
      readonly kind: "entry_delta";
      readonly id: string;
      readonly n: number;
      readonly delta: string;
    }
  | { readonly kind: "entry_sealed"; readonly id: string; readonly seq: number; readonly n: number }
  | { readonly kind: "turn_closed"; readonly entry: Entry };

function sealedEntry(seq: number, kind: Entry["kind"], payload: unknown, id?: string): Entry {
  return { id: id ?? `${TURN}-e${String(seq)}`, turn: TURN, kind, seq, ts: seq + 1, payload };
}

const STREAM: readonly Frame[] = [
  { kind: "turn_opened", entry: sealedEntry(0, "turn_open", { source: "prompt", n: 1 }) },
  { kind: "entry_appended", entry: sealedEntry(1, "text", { text: "one" }) },
  { kind: "entry_appended", entry: sealedEntry(2, "thinking", { text: "hmm" }) },
  {
    kind: "entry_opened",
    open: { turn: TURN, id: `${TURN}-open3`, kind: "text", text: "he", n: 1 },
  },
  { kind: "entry_delta", id: `${TURN}-open3`, n: 2, delta: "llo" },
  { kind: "entry_sealed", id: `${TURN}-open3`, seq: 3, n: 2 },
  { kind: "entry_appended", entry: sealedEntry(4, "text", { text: "tail" }) },
  { kind: "turn_closed", entry: sealedEntry(5, "turn_close", { outcome: "completed" }) },
];

function apply(chatID: string, f: Frame): void {
  switch (f.kind) {
    case "turn_opened":
      openTurn(chatID, f.entry);
      return;
    case "entry_appended":
    case "turn_closed":
      appendEntry(chatID, f.entry);
      return;
    case "entry_opened":
      openEntry(chatID, f.open);
      return;
    case "entry_delta":
      applyDelta(chatID, TURN, f.id, "", f.n, f.delta);
      return;
    case "entry_sealed":
      sealEntry(chatID, TURN, f.id, "", f.seq, f.seq + 1, f.n);
      return;
  }
}

function session(chatID: string): Session {
  return {
    id: chatID,
    name: chatID,
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
    residency: "loaded",
  };
}

/** What the two folds are compared on: the entries at the positions their `seq` claims, the
 *  open tails per lane, and where the closer landed. */
function foldOf(chatID: string): unknown {
  const s = get(chatID);
  const state = s?.turns.get(TURN);
  return {
    order: s?.turn_order,
    entries: state?.entries,
    open: [...(state?.openEntries.entries() ?? [])],
    closeAt: state?.closeAt,
  };
}

function refEntries(): Entry[] {
  const s = get("ref");
  return [...(s?.turns.get(TURN)?.entries ?? [])];
}

/** The server serving the log: the entries above `after`, with `after` absent asking for the
 *  whole turn. The turn is closed by the stream's end, so it has no open tail to answer. */
function serveRange(path: string): { entries: Entry[]; open_entries: OpenEntry[]; subject: [] } {
  const cut = path.indexOf("?after=");
  const after = cut === -1 ? -1 : Number(path.slice(cut + "?after=".length));
  return { entries: refEntries().filter((e) => e.seq > after), open_entries: [], subject: [] };
}

beforeEach(() => {
  resetFreshness();
  clearAllEntrySigs();
  api.getTyped.mockReset();
  api.getTyped.mockImplementation((path: string) => Promise.resolve(serveRange(path)));
  registerTurnRepair(requestTurnRange);
  setSessions([session("ref"), session("c")]);
  setActive("c");
  observeStamp({ kind: "chat", ref: "c", version: "1" });
  for (const f of STREAM) {
    apply("ref", f);
  }
});

afterEach(() => {
  registerTurnRepair(() => undefined);
  setSessions([]);
});

describe("one lost frame in a chat's entry stream", () => {
  it("folds the undropped stream into a closed turn (the oracle the cases compare against)", () => {
    expect(refEntries().map((e) => e.seq)).toEqual([0, 1, 2, 3, 4, 5]);
    expect(get("ref")?.turns.get(TURN)?.closeAt).toBe(5);
    expect(get("ref")?.turns.get(TURN)?.openEntries.size).toBe(0);
  });

  // `after` is the highest `seq` the fold still held when it noticed, and it is absent for a
  // frame naming a turn the store never opened, which asks for the whole turn.
  it.each([
    [0, ""],
    [1, "?after=0"],
    [2, "?after=1"],
    [3, "?after=2"],
    [4, "?after=2"],
    [5, "?after=2"],
    [6, "?after=3"],
  ])("frame %i lost: one read %s restores the undropped fold", async (drop, query) => {
    expect(transcriptStale(get("c") as Session)).toBe(false);

    for (const [i, f] of STREAM.entries()) {
      if (i !== drop) {
        apply("c", f);
      }
    }

    expect(transcriptStale(get("c") as Session)).toBe(true);
    expect(api.getTyped).toHaveBeenCalledTimes(1);
    expect(api.getTyped.mock.calls[0]?.[0]).toBe(`/api/chats/c/turns/${TURN}${query}`);

    await vi.waitFor(() => {
      expect(foldOf("c")).toEqual(foldOf("ref"));
    });
    // AFTER the answer landed as well: the count before it leaves a read the repair's own
    // answer triggers unasserted, which is the one way a second read hides here.
    expect(api.getTyped).toHaveBeenCalledTimes(1);
    // The window is the server's answer again, so the next activation refetches nothing the
    // range read has already restored.
    expect(get("c")?.residency).toBe("loaded");
    expect(transcriptStale(get("c") as Session)).toBe(false);
  });

  it("cannot see a lost TRAILING frame, which is what the live_turn stamp is for", () => {
    for (const f of STREAM.slice(0, -1)) {
      apply("c", f);
    }

    expect(api.getTyped).not.toHaveBeenCalled();
    expect(get("c")?.residency).toBe("loaded");
    expect(transcriptStale(get("c") as Session)).toBe(false);
    expect(get("c")?.turns.get(TURN)?.closeAt).toBeUndefined();
    expect(foldOf("c")).not.toEqual(foldOf("ref"));
  });

  it("asks once for a turn it never opened, however many of its frames arrive", async () => {
    for (const f of STREAM.slice(1)) {
      apply("c", f);
    }

    expect(api.getTyped).toHaveBeenCalledTimes(1);
    await vi.waitFor(() => {
      expect(foldOf("c")).toEqual(foldOf("ref"));
    });
    expect(api.getTyped).toHaveBeenCalledTimes(1);
  });
});
