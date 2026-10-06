// `steer_queued` (the server's confirmation of a waiting steer) and `agent_notice` (a step's line
// on the same KAS channel). The real store, only the bus mocked; about the WIRE: duplicate,
// out-of-order and background-chat frames. A dock row leaves on its `steer` entry.

import { vi, describe, it, expect, beforeEach } from "vitest";
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";

vi.mock("../bus.js", () => createBusMock());

// The notice handler's whole output is a toast at a level derived from the severity, so the
// message and the level ARE the assertion. Mocked rather than rendered: the real module is a
// leaf over ui-primitives and its DOM tells us nothing about the mapping under test.
const { toastNotice } = vi.hoisted(() => ({ toastNotice: vi.fn() }));
vi.mock("../toast.js", async () => ({
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
  notice: toastNotice,
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
  toastNotice.mockClear();
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

// After a gap `BUS_RECONCILE` forgets every dock (handlers/system.test.ts); the connect replay
// refills a WAITING row under its id, and `recordSteerQueued` checks the log's `steer` entry FIRST,
// so a DELIVERED row stays out.
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

  // A reload brings the rows the server persisted back as frames carrying their state, so
  // a row no turn could read returns Not sent rather than as a plain waiting row.
  it("brings a persisted row back as not sent, from the state its frame carries", () => {
    fireSSE("steer_queued", "c1", {
      steer_id: "steer-1",
      text: "use tabs",
      origin: "user",
      state: "unsent",
    });
    expect(get("c1")?.steers).toEqual([
      { id: "steer-1", text: "use tabs", origin: "user", unsent: true },
    ]);
  });

  it("passes a batch frame's members through, adding no row", () => {
    fireSSE("steer_queued", "c1", { steer_id: "steer-1", text: "one", origin: "user" });
    fireSSE("steer_queued", "c1", { steer_id: "steer-2", text: "two", origin: "user" });
    fireSSE("steer_queued", "c1", {
      steer_id: "steer-b1",
      text: "one\n\ntwo",
      origin: "user",
      replaces: ["steer-1", "steer-2"],
    });
    expect(get("c1")?.steers?.map((e) => [e.id, e.kas])).toEqual([
      ["steer-1", "steer-b1"],
      ["steer-2", "steer-b1"],
    ]);
  });
});

// Agent notices arrive on KAS's steering channel; forwarded as a steer they would render as user
// text in the composer's chip row with a useless Discard.
describe("agent_notice", () => {
  /** The [message, level] of the one notice raised. */
  function raised(): [unknown, unknown] {
    expect(toastNotice).toHaveBeenCalledTimes(1);
    const [message, level] = toastNotice.mock.calls[0] ?? [];
    return [message, level];
  }

  it("toasts success at the success level, named for its chat", () => {
    fireSSE("agent_notice", "c1", { severity: "success", text: "downloaded the picture" });
    expect(raised()).toEqual(["test: downloaded the picture", "success"]);
  });

  it("toasts info at the info level", () => {
    fireSSE("agent_notice", "c1", { severity: "info", text: "starting pass two" });
    expect(raised()).toEqual(["test: starting pass two", "info"]);
  });

  // A warning is amber, its own level, rather than borrowing the error face.
  it("gives a warning the warning level", () => {
    fireSSE("agent_notice", "c1", { severity: "warning", text: "retrying the fetch" });
    expect(raised()).toEqual(["test: retrying the fetch", "warning"]);
  });

  it("toasts error at the error level", () => {
    fireSSE("agent_notice", "c1", { severity: "error", text: "step failed" });
    expect(raised()).toEqual(["test: step failed", "error"]);
  });

  // A severity a later KAS adds is still a notice worth showing, so it falls through to info
  // rather than being dropped.
  it("shows an unrecognised severity rather than dropping it", () => {
    fireSSE("agent_notice", "c1", { severity: "debug", text: "something happened" });
    expect(raised()).toEqual(["test: something happened", "info"]);
  });

  it("says nothing for an empty notice", () => {
    fireSSE("agent_notice", "c1", { severity: "info", text: "   " });
    expect(toastNotice).not.toHaveBeenCalled();
  });

  // A notice touches none of the steering state: it has no id, so there is nothing for a
  // later frame to address.
  it("records no steer", () => {
    fireSSE("agent_notice", "c1", { severity: "info", text: "progress" });
    expect(steerCount("c1")).toBe(0);
    expect(get("c1")?.steers).toBeUndefined();
  });
});
