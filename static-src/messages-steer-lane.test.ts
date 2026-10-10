// A steer read by a delegate carries the delegate's lane but draws in the parent's flow at its own `seq`, with a
// marker naming who consumed it; inside the delegate's own view it draws nothing.

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import type { Turn } from "./turns.js";
import type { Entry } from "./wire/types.gen.js";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

const { buildAssistantBody, buildDetachedBody, resetBlockRenders, initBlockRenderer } =
  await import("./messages-blocks.js");
const { steerAckID } = await import("./entry-ids.js");
const { setActive } = await import("./store.js");

const CHAT_ID = "c-steer-lane";
const LANE = "sub-A";
setActive(CHAT_ID);

initBlockRenderer({
  pushEntryEffect: (): void => {
    /* noop */
  },
  disposeEntryEffects: (): void => {
    /* noop */
  },
  makeRow: () => {
    const row = document.createElement("div");
    row.className = "msg-row";
    return row;
  },
  makeEvent: (e: Entry) => {
    const row = document.createElement("div");
    row.className = `event event-${e.kind}`;
    return row;
  },
});

let turnSeq = 0;
let hosts: HTMLElement[] = [];

function sealed(
  turnID: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  opts: { readonly id?: string; readonly lane?: string } = {},
): Entry {
  return {
    id: opts.id ?? `${turnID}-e${String(seq)}`,
    turn: turnID,
    kind,
    seq,
    ts: seq + 1,
    ...(opts.lane !== undefined && { lane: opts.lane }),
    payload,
  };
}

/** The agent's text, the steer, and the delegate's ack; each case varies the steer's lane. */
function turnWithSteer(lane?: string): Turn {
  turnSeq += 1;
  const turnID = `t-${String(turnSeq)}`;
  const steerID = "steer-1";
  const body: Entry[] = [
    sealed(turnID, 1, "text", { text: "on it" }),
    sealed(
      turnID,
      2,
      "steer",
      { id: steerID, text: "use tabs", origin: "user", state: "read" },
      lane === undefined ? { id: steerID } : { id: steerID, lane },
    ),
    sealed(
      turnID,
      3,
      "steer_ack",
      { steer_id: steerID, text: "tabs it is" },
      { id: steerAckID(steerID), ...(lane === undefined ? {} : { lane }) },
    ),
  ];
  return {
    id: turnID,
    n: 1,
    trigger: { id: `${turnID}-p`, text: "go" },
    body,
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
  };
}

function bodyHost(): HTMLElement {
  const host = document.createElement("div");
  host.className = "turn-body";
  document.body.appendChild(host);
  hosts.push(host);
  return host;
}

beforeEach(() => {
  resetBlockRenders();
});

afterEach(() => {
  resetBlockRenders();
  for (const host of hosts) {
    host.remove();
  }
  hosts = [];
});

describe("a steer a delegate read", () => {
  it("renders in the PARENT's flow, marked with the lane that read it", () => {
    const turn = turnWithSteer(LANE);
    const host = bodyHost();
    buildAssistantBody(host, turn, CHAT_ID, false);

    const note = host.querySelector(".steer-note");
    expect(note).not.toBeNull();
    // The lane, for a surface that addresses the delegate; the label carries the fact, since a lane id is a uuid.
    expect((note as HTMLElement).dataset["lane"]).toBe(LANE);
    expect(note?.querySelector(".steer-note-label")?.textContent).toBe(
      "Mid-turn message · acknowledged by a delegate",
    );
    // At its own position: the agent's text above, the ack's words below.
    expect([...host.children].indexOf(note as HTMLElement)).toBe(1);
  });

  it("says READ by a delegate when no acknowledgement has landed yet", () => {
    const turn = turnWithSteer(LANE);
    const host = bodyHost();
    buildAssistantBody(host, { ...turn, body: turn.body.slice(0, 2) }, CHAT_ID, false);

    expect(host.querySelector(".steer-note-label")?.textContent).toBe(
      "Mid-turn message · read by a delegate",
    );
  });

  it("draws NOTHING inside the delegate's own view", () => {
    const turn = turnWithSteer(LANE);
    const host = bodyHost();
    buildDetachedBody(host, turn, CHAT_ID, LANE, false);

    expect(host.querySelector(".steer-note")).toBeNull();
    // The ack is the delegate's own content and stays; only the reader's words move.
    expect(host.textContent).toContain("tabs it is");
  });
});

describe("a steer the turn's own agent read", () => {
  it("carries no lane marker at all", () => {
    const turn = turnWithSteer();
    const host = bodyHost();
    buildAssistantBody(host, turn, CHAT_ID, false);

    const note = host.querySelector(".steer-note") as HTMLElement;
    expect(note.dataset["lane"]).toBeUndefined();
    expect(note.querySelector(".steer-note-label")?.textContent).toBe(
      "Mid-turn message · acknowledged",
    );
  });
});
