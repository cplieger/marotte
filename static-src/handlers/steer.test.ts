// Tests for handlers/steer.ts, now TWO handlers: `steer_queued`, the server's confirmation of
// a waiting steer, and `agent_notice`, a step's progress line on the same KAS channel.
// A steer's transcript fact is its `steer` ENTRY, so the dock row leaves inside
// `appendEntry` and nothing here writes one.
// The real store is driven so the dock is observable and only the bus is mocked; these cases
// are about the WIRE rather than the store's mechanics: frames arriving twice, out of order,
// or on a background chat.

import { vi, describe, it, expect, beforeEach } from "vitest";
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";

vi.mock("../bus.js", () => createBusMock());

// The notice handler's whole output is a toast at a level derived from the severity, so the
// level IS the assertion. Mocked rather than rendered: the real module is a leaf over
// ui-primitives and its DOM tells us nothing about the mapping under test.
const toastInfo = vi.fn();
const toastSuccess = vi.fn();
const toastError = vi.fn();
vi.mock("../toast.js", () => ({
  info: (m: string) => toastInfo(m),
  success: (m: string) => toastSuccess(m),
  error: (m: string) => toastError(m),
}));

import {
  setSessions,
  setActive,
  get,
  appendEntry,
  defaultUsage,
  openTurn,
  recordSteerSent,
  forgetSteers,
  steerIDFor,
  steerCount,
} from "../store.js";
import type { Session } from "../types.js";
import type { Entry } from "../wire/types.gen.js";

// Import after the mock so the handler registers against it.
await import("./steer.js");

function makeSession(id: string): Session {
  return {
    id,
    name: "test",
    model: "claude",
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

beforeEach(() => {
  setSessions([makeSession("c1"), makeSession("c2")]);
  setActive("c1");
  toastInfo.mockClear();
  toastSuccess.mockClear();
  toastError.mockClear();
});

/** Open a turn on `c1`, so a `steer` entry has somewhere to land. */
function turnOnC1(): void {
  const open: Entry = {
    id: "t1-open",
    turn: "t1",
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1 },
  };
  openTurn("c1", open);
}

/** Append the `steer` ENTRY that says the agent READ this steer, which is the fact the
 *  replay below has to be refused by. */
function steerEntryRead(steerID: string, text: string, seq: number): void {
  appendEntry("c1", {
    id: steerID,
    turn: "t1",
    kind: "steer",
    seq,
    ts: seq + 1,
    payload: { text, origin: "user", state: "read" },
  });
}

describe("steer_queued", () => {
  it("records the steer as waiting", () => {
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "use tabs", origin: "user" });
    expect(get("c1")?.steers).toEqual([{ id: "steer-1", text: "use tabs", origin: "user" }]);
  });

  // The whole point of the optimistic row: the user sees it on the keystroke, and KAS's frame
  // CONFIRMS that row rather than adding a second one beside it. The ids agree because the
  // client derives the one KAS returns.
  it("confirms the row the submit already drew, leaving one row", () => {
    recordSteerSent("c1", "m-1", "use tabs");
    fireSSE("steer_queued", "c1", {
      steer_id: steerIDFor("m-1"),
      text: "use tabs",
      origin: "user",
    });
    expect(get("c1")?.steers).toEqual([{ id: "steer-m-1", text: "use tabs", origin: "user" }]);
  });

  // The safety net for a KAS whose id convention has drifted: an exact TEXT match against the
  // oldest pending row adopts the server's id, so one row still becomes one row.
  it("confirms it through the text fallback when the ids disagree", () => {
    recordSteerSent("c1", "m-1", "use tabs");
    fireSSE("steer_queued", "c1", {
      steer_id: "kas-generated-7",
      text: "use tabs",
      origin: "user",
    });
    expect(get("c1")?.steers).toEqual([
      { id: "kas-generated-7", text: "use tabs", origin: "user" },
    ]);
  });

  // A notice KAS classified as the AGENT's leaves the server as its own event, so it never
  // reaches the chip row: recording one as a steer would put the agent's own words in the
  // composer as though the user had typed them.
  it("does not record an agent notice as a steer", () => {
    fireSSE("agent_notice", "c1", {
      severity: "error",
      text: "[notification/error] step failed",
    });
    expect(steerCount("c1")).toBe(0);
  });

  // The row is per-chat, so a steer raised on a BACKGROUND chat must land on that chat rather
  // than on whichever one happens to be open.
  it("keys by the event's chat, not the active one", () => {
    fireSSE("steer_queued", "c2", { steer_id: "steer-1", text: "elsewhere", origin: "user" });
    expect(steerCount("c1")).toBe(0);
    expect(steerCount("c2")).toBe(1);
  });
});

// The GAP, then the connect burst. `BUS_RECONCILE` forgets every chat's dock without
// promoting anything, because the frames that resolved those steers may be among the lost
// ones (handlers/system.test.ts pins that door; `forgetSteers` is called directly here
// because this file's bus is mocked). What refills it is the connect replay in the same
// burst, so a row still WAITING comes back under its own id while a DELIVERED one stays out —
// and what says "delivered" is the `steer` entry in the log, which `recordSteerQueued`
// checks FIRST.
describe("a gap and then the connect replay", () => {
  it("brings a still-waiting steer back into the dock", () => {
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "use tabs", origin: "user" });
    expect(steerCount("c1")).toBe(1);

    forgetSteers("c1");
    expect(get("c1")?.steers, "the gap empties the dock").toBeUndefined();

    // The connect replay, arriving in the same burst.
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "use tabs", origin: "user" });
    expect(get("c1")?.steers).toEqual([{ id: "steer-1", text: "use tabs", origin: "user" }]);
  });

  it("does not bring back a steer the agent read during the outage", () => {
    // The read arrives as the `steer` entry itself, which is the durable fact rather than
    // a client-side mark.
    turnOnC1();
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "use tabs", origin: "user" });
    steerEntryRead("steer-1", "use tabs", 1);
    forgetSteers("c1");

    // KAS's buffer does not hold it, so the server replays nothing for it — but a frame
    // that predates the read can still be in flight, and the entry is what refuses it.
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "use tabs", origin: "user" });

    expect(get("c1")?.steers, "the delivered message stays out of the dock").toBeUndefined();
  });

  // The two halves at once, which is the state a reconnect mid-turn produces: one steer read
  // before the outage and one still queued behind it.
  it("refills only the waiting half of a mixed set", () => {
    turnOnC1();
    fireSSE("steer_queued", "c1", { steer_id: "steer-read", text: "first", origin: "user" });
    steerEntryRead("steer-read", "first", 1);
    fireSSE("steer_queued", "c1", { steer_id: "steer-waiting", text: "second", origin: "user" });

    forgetSteers("c1");
    for (const [id, text] of [
      ["steer-read", "first"],
      ["steer-waiting", "second"],
    ]) {
      fireSSE("steer_queued", "c1", { steer_id: id, text, origin: "user" });
    }

    expect(get("c1")?.steers?.map((e) => e.id)).toEqual(["steer-waiting"]);
  });
});

// The agent's own notices. They arrive on KAS's steering channel because that buffer is the
// only inbound path into a live turn, and forwarding one as a steer carrying a severity
// renders a line the agent wrote inside the composer's chip row, styled as something the
// user had typed, beside a Discard button that cannot act on it.
describe("agent_notice", () => {
  it("toasts success at the success level", () => {
    fireSSE("agent_notice", "c1", { severity: "success", text: "downloaded the picture" });
    expect(toastSuccess).toHaveBeenCalledWith("downloaded the picture");
    expect(toastInfo).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });

  it("toasts info at the info level", () => {
    fireSSE("agent_notice", "c1", { severity: "info", text: "starting pass two" });
    expect(toastInfo).toHaveBeenCalledWith("starting pass two");
  });

  // The toast vocabulary has three levels and KAS has four, so a warning takes the error face
  // rather than earning a fourth level for a channel that has never been seen to emit one.
  it("gives a warning the error face", () => {
    fireSSE("agent_notice", "c1", { severity: "warning", text: "retrying the fetch" });
    expect(toastError).toHaveBeenCalledWith("retrying the fetch");
  });

  it("toasts error at the error level", () => {
    fireSSE("agent_notice", "c1", { severity: "error", text: "step failed" });
    expect(toastError).toHaveBeenCalledWith("step failed");
  });

  // A severity a later KAS adds is still a notice worth showing, so it falls through to info
  // rather than being dropped.
  it("shows an unrecognised severity rather than dropping it", () => {
    fireSSE("agent_notice", "c1", { severity: "debug", text: "something happened" });
    expect(toastInfo).toHaveBeenCalledWith("something happened");
  });

  it("says nothing for an empty notice", () => {
    fireSSE("agent_notice", "c1", { severity: "info", text: "   " });
    expect(toastInfo).not.toHaveBeenCalled();
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });

  // A notice touches none of the steering state: it has no id, so there is nothing for a
  // later frame to address.
  it("records no steer", () => {
    fireSSE("agent_notice", "c1", { severity: "info", text: "progress" });
    expect(steerCount("c1")).toBe(0);
    expect(get("c1")?.steers).toBeUndefined();
  });
});
