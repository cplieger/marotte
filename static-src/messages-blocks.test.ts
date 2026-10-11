// Two grouping rules are deliberately opposite: a tool group is contiguous, keyed on the run's start `seq`; a
// delegate's card is minted only by its invocation `tool_call`, since its own entries carry its uuid as `lane`.
// Position is `seq`: an entry that renders nothing does not break a prose run, one that renders ends it.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type { Turn } from "./turns.js";
import type { Entry, EntryToolCall, EntryToolResult, OpenEntry } from "./wire/types.gen.js";

// The import graph reaches the DOM registry, which throws on a missing app root.
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

const {
  buildAssistantBody,
  updateAssistantBody,
  buildDetachedBody,
  updateDetachedBody,
  disposeAssistantBody,
  resetBlockRenders,
  initBlockRenderer,
  blockElement,
  getLiveAnchor,
  mountedWindow,
  mountHeadRange,
  dropHead,
  dropTail,
  setRunCardOwners,
} = await import("./messages-blocks.js");
const { runCardOwners, sliceTurn } = await import("./block-window.js");
const { toolResultID } = await import("./entry-ids.js");
const { clearAllEntrySigs, ensureToolCallSig, toolCallSigs } = await import("./store-signals.js");
const { setActive } = await import("./store.js");

/** Part of the per-tool signal key, and the store's active chat: the live-anchor fallback scans only that chat. */
const CHAT_ID = "c-blocks";
setActive(CHAT_ID);

initBlockRenderer({
  // messages.ts owns the effect registry; no case here asserts on it.
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

// --- Fixtures -------------------------------------------------------------------------

let turnSeq = 0;

function nextTurnID(): string {
  turnSeq += 1;
  return `t-${String(turnSeq)}`;
}

/** A sealed entry of any kind at `seq`. `lane` absent is `""`, the transcript's own lane. */
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

function text(turnID: string, seq: number, s: string, lane?: string): Entry {
  return sealed(turnID, seq, "text", { text: s }, lane === undefined ? {} : { lane });
}

function thinking(turnID: string, seq: number, s: string, lane?: string): Entry {
  return sealed(turnID, seq, "thinking", { text: s }, lane === undefined ? {} : { lane });
}

// `Object.assign`, not a spread: under `exactOptionalPropertyTypes` a spread of a `Partial` admits `undefined`.
function toolCall(id: string, over: Partial<EntryToolCall> = {}): EntryToolCall {
  const base: EntryToolCall = {
    id,
    title: "Run Command",
    status: "completed",
    kind: "execute",
    ts: 1,
  };
  return Object.assign(base, over);
}

function callEntry(
  turnID: string,
  seq: number,
  call: EntryToolCall,
  opts: { readonly lane?: string } = {},
): Entry {
  return sealed(turnID, seq, "tool_call", call, { id: `${turnID}-c${String(seq)}`, ...opts });
}

/** The `tool_result` settling `callSeq`'s call; its id derives from the call entry's, so pairing needs no join. */
function resultEntry(
  turnID: string,
  seq: number,
  callSeq: number,
  over: Partial<EntryToolResult> = {},
): Entry {
  const payload: EntryToolResult = Object.assign({ status: "completed" } as EntryToolResult, over);
  return sealed(turnID, seq, "tool_result", payload, {
    id: toolResultID(`${turnID}-c${String(callSeq)}`),
  });
}

function invocation(turnID: string, seq: number, subtask: string): Entry {
  return callEntry(
    turnID,
    seq,
    toolCall(`tool-${subtask}`, {
      title: `Sub-agent: ${subtask}`,
      agent_subtask_id: subtask,
    }),
  );
}

/** A workflow launch plus the `tool_result` carrying its run id: a `tool_call` never has a `workflow_id`. */
function launch(turnID: string, seq: number, runID: string, toolID = `tool-${runID}`): Entry[] {
  return [
    callEntry(turnID, seq, toolCall(toolID, { title: "Run Workflow" })),
    resultEntry(turnID, seq + 1, seq, { workflow_id: runID }),
  ];
}

function turnOf(
  turnID: string,
  body: Entry[],
  opts: { readonly open?: OpenEntry[]; readonly n?: number } = {},
): Turn {
  return {
    id: turnID,
    n: opts.n ?? 1,
    trigger: { id: `${turnID}-p`, text: "go" },
    body,
    openEntries: new Map((opts.open ?? []).map((o) => [o.lane ?? "", o])),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
  };
}

let hosts: HTMLElement[] = [];

/** A `.turn-body` attached to the document, so geometry reads and focus behave as in a card. */
function bodyHost(): HTMLElement {
  const host = document.createElement("div");
  host.className = "turn-body";
  document.body.appendChild(host);
  hosts.push(host);
  return host;
}

interface Render {
  readonly turnID: string;
  readonly body: HTMLElement;
  readonly turn: Turn;
}

/** Re-key `entries` onto `turnID`; the builders spell ids against the placeholder `t`. */
function retarget(entries: Entry[], turnID: string): Entry[] {
  return entries.map((e) => ({
    ...e,
    turn: turnID,
    id: e.id.startsWith("t-") ? turnID + e.id.slice(1) : e.id,
  }));
}

/** Build one turn's body from `entries`; a fresh turn id per call, so renders never share a slot. */
function render(entries: Entry[], opts: { readonly live?: boolean } = {}): Render {
  const turnID = nextTurnID();
  const turn = turnOf(turnID, retarget(entries, turnID));
  const body = bodyHost();
  buildAssistantBody(body, turn, CHAT_ID, opts.live ?? false);
  return { turnID, body, turn };
}

function renderWindow(entries: Entry[], range: { from: number; to: number }): Render {
  const turnID = nextTurnID();
  const turn = turnOf(turnID, retarget(entries, turnID));
  const body = bodyHost();
  buildAssistantBody(body, turn, CHAT_ID, false, range);
  return { turnID, body, turn };
}

function update(r: Render, entries: Entry[], live = false): void {
  const turn = turnOf(r.turnID, retarget(entries, r.turnID));
  updateAssistantBody(r.body, turn, CHAT_ID, live);
}

/** The body's direct children, as a readable shape. */
function shape(body: HTMLElement): string[] {
  return [...body.children].map((e) => {
    const h = e as HTMLElement;
    if (h.classList.contains("subagent-container")) {
      return `pipeline(${String(h.dataset["pipeline"])})`;
    }
    if (h.classList.contains("subagent-block")) {
      return `card(${String(h.dataset["subtask"])})`;
    }
    if (h.classList.contains("run-card")) {
      return `run(${String(h.dataset["run"])})`;
    }
    if (h.classList.contains("tool-group")) {
      return `group(${String(h.querySelectorAll(".tool-call").length)})`;
    }
    if (h.classList.contains("steer-note")) {
      return "note";
    }
    if (h.classList.contains("plan-message")) {
      return "plan";
    }
    if (h.classList.contains("reasoning-block")) {
      return "thinking";
    }
    if (h.classList.contains("event")) {
      return `event(${h.className.replace("event event-", "")})`;
    }
    return `text(${String(h.textContent).trim().slice(0, 16)})`;
  });
}

beforeEach(() => {
  resetBlockRenders();
  setRunCardOwners(new Map());
  clearAllEntrySigs();
  toolCallSigs.clearAll();
});

afterEach(() => {
  resetBlockRenders();
  for (const host of hosts) {
    host.remove();
  }
  hosts = [];
});

// ---------------------------------------------------------------------------

describe("a delegate's card is its invocation, and its own entries are dropped", () => {
  it("draws ONE card for a delegate whose own entries are in its own lane", () => {
    const r = render([
      text("t", 1, "parent one"),
      invocation("t", 2, "sub-A"),
      text("t", 3, "delegate says", "sub-A"),
      thinking("t", 4, "delegate thinks", "sub-A"),
      text("t", 5, "parent two"),
    ]);
    const cards = r.body.querySelectorAll(".subagent-block");
    expect(cards).toHaveLength(1);
    expect(r.body.textContent).not.toContain("delegate says");
    expect(r.body.textContent).not.toContain("delegate thinks");
  });

  it("keeps two DIFFERENT delegates in two cards", () => {
    const r = render([
      invocation("t", 1, "sub-A"),
      text("t", 2, "parent between"),
      invocation("t", 3, "sub-B"),
    ]);
    expect(shape(r.body)).toEqual(["card(sub-A)", "text(parent between)", "card(sub-B)"]);
  });

  it("renders the parent's continuation BELOW the card, where it arrived", () => {
    // The invocation renders at its own `seq`, so prose that arrived after it sits below the card.
    const r = render([text("t", 1, "before"), invocation("t", 2, "sub-A"), text("t", 3, "after")]);
    expect(shape(r.body)).toEqual(["text(before)", "card(sub-A)", "text(after)"]);
  });

  it("does not break a prose run: a dropped lane's entry renders nothing", () => {
    const r = render([
      text("t", 1, "one "),
      text("t", 2, "delegate", "sub-A"),
      text("t", 3, "two"),
    ]);
    const rows = r.body.querySelectorAll(".msg-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain("one two");
  });

  it("gives a delegate-hosted entry no element of its own", () => {
    const r = render([invocation("t", 1, "sub-A"), text("t", 2, "delegate", "sub-A")]);
    expect(blockElement(r.turnID, 1)).toBeInstanceOf(HTMLElement);
    expect(blockElement(r.turnID, 2)).toBeUndefined();
  });
});

describe("a workflow run's card is its launch", () => {
  it("makes the launch entry the run card, not a tool row", () => {
    const r = render(launch("t", 1, "wf-1"));
    expect(shape(r.body)).toEqual(["run(wf-1)"]);
    expect(r.body.querySelectorAll(".tool-call")).toHaveLength(0);
  });

  it("leaves an ordinary tool call alone: no run id means no card", () => {
    const r = render([callEntry("t", 1, toolCall("tool-plain")), resultEntry("t", 2, 1)]);
    expect(shape(r.body)).toEqual(["group(1)"]);
  });

  it("gives two runs their own cards, and neither any rows", () => {
    const r = render([...launch("t", 1, "wf-1"), ...launch("t", 3, "wf-2")]);
    expect(shape(r.body)).toEqual(["run(wf-1)", "run(wf-2)"]);
  });

  it("seats the card in ENTRY order, between the tool cards either side", () => {
    const r = render([
      callEntry("t", 1, toolCall("tool-before")),
      ...launch("t", 2, "wf-1"),
      callEntry("t", 4, toolCall("tool-after")),
    ]);
    expect(shape(r.body)).toEqual(["group(1)", "run(wf-1)", "group(1)"]);
  });

  it("makes the launch entry's element the card it opened", () => {
    const r = render(launch("t", 1, "wf-1"));
    const el = blockElement(r.turnID, 1);
    expect(el?.classList.contains("run-card")).toBe(true);
    expect(el?.dataset["run"]).toBe("wf-1");
  });

  it("re-dispatches the launch in place when the RESULT supplies the id later", () => {
    // Live, the call is a tool row until the entry settling it brings the run id; the card replaces it in place.
    const call = callEntry("t", 1, toolCall("tool-late", { title: "Run Workflow" }));
    const r = render([call]);
    expect(shape(r.body)).toEqual(["group(1)"]);
    update(r, [call, resultEntry("t", 2, 1, { workflow_id: "wf-late" })]);
    expect(shape(r.body)).toEqual(["run(wf-late)"]);
    expect(blockElement(r.turnID, 1)?.classList.contains("run-card")).toBe(true);
  });

  it("RE-HOMES the card into a later turn's render, keeping the element", () => {
    // One card per run per transcript: ownership is derived over the resident window, so the card moves rather than
    // being rebuilt, keeping its effect and clock.
    const a = render(launch("t", 1, "wf-1"));
    const card = blockElement(a.turnID, 1);
    expect(shape(a.body)).toEqual(["run(wf-1)"]);
    const b = render(launch("t", 1, "wf-1"));
    expect(shape(b.body)).toEqual(["run(wf-1)"]);
    expect(shape(a.body)).toEqual([]);
    expect(blockElement(b.turnID, 1)).toBe(card);
  });

  it("keeps the card alive through a sibling render's dispose", () => {
    const a = render(launch("t", 1, "wf-1"));
    const b = render(launch("t", 1, "wf-2"));
    disposeAssistantBody(b.turnID);
    expect(shape(a.body)).toEqual(["run(wf-1)"]);
    expect(mountedWindow(a.turnID)).toEqual({ from: 0, to: 3 });
  });

  it("drops the card on unmount", () => {
    const r = render(launch("t", 1, "wf-1"));
    disposeAssistantBody(r.turnID);
    expect(mountedWindow(r.turnID)).toBeUndefined();
    expect(blockElement(r.turnID, 1)).toBeUndefined();
  });
});

describe("only the entry that started a run hosts its card", () => {
  it("gives a later mention of the run a tool card, not the run's box", () => {
    const turnID = nextTurnID();
    const body = [
      ...launch(turnID, 1, "wf-1", "tool-launch"),
      callEntry(turnID, 3, toolCall("tool-inspect", { title: "Inspect Workflow" })),
      resultEntry(turnID, 4, 3, { workflow_id: "wf-1" }),
    ].map((e) => ({ ...e, turn: turnID }));
    const turn = turnOf(turnID, body);
    setRunCardOwners(runCardOwners([turn]));
    const host = bodyHost();
    buildAssistantBody(host, turn, CHAT_ID, false);
    expect(shape(host)).toEqual(["run(wf-1)", "group(1)"]);
  });

  it("falls toward the pre-owner behaviour when NO owner is named", () => {
    // With no owner map every mention takes the run branch, so the later one seats on the card.
    const r = render([
      ...launch("t", 1, "wf-1", "tool-launch"),
      callEntry("t", 3, toolCall("tool-inspect", { title: "Inspect Workflow" })),
      resultEntry("t", 4, 3, { workflow_id: "wf-1" }),
    ]);
    expect(shape(r.body)).toEqual(["run(wf-1)"]);
    expect(r.body.querySelectorAll(".tool-call")).toHaveLength(0);
    expect(blockElement(r.turnID, 3)).toBe(blockElement(r.turnID, 1));
  });

  it("leaves a mention a tool card when the owner is in ANOTHER turn", () => {
    const first = turnOf(nextTurnID(), []);
    const secondID = nextTurnID();
    const body = [
      callEntry(secondID, 1, toolCall("tool-inspect", { title: "Inspect Workflow" })),
      resultEntry(secondID, 2, 1, { workflow_id: "wf-1" }),
    ].map((e) => ({ ...e, turn: secondID }));
    const second = turnOf(secondID, body);
    // The owner names an entry of a turn this body does not hold.
    setRunCardOwners(new Map([["wf-1", `${first.id}-c1`]]));
    const host = bodyHost();
    buildAssistantBody(host, second, CHAT_ID, false);
    expect(shape(host)).toEqual(["group(1)"]);
  });

  it("exempts a DETACHED render, whose lane the transcript's owner does not describe", () => {
    const turnID = nextTurnID();
    const body = [
      callEntry(turnID, 1, toolCall("tool-inv", { title: "Sub-agent: sub-A" })),
      ...launch(turnID, 2, "wf-1", "tool-launch"),
    ].map((e) => ({ ...e, turn: turnID }));
    body[0] = {
      ...body[0]!,
      payload: toolCall("tool-inv", { title: "Sub-agent: sub-A", agent_subtask_id: "sub-A" }),
    };
    // The launch entry moves into the delegate's lane, which is the detached render's root.
    body[1] = { ...body[1]!, lane: "sub-A" };
    body[2] = { ...body[2]!, lane: "sub-A" };
    const turn = turnOf(turnID, body);
    setRunCardOwners(new Map([["wf-1", "some-other-entry"]]));
    const host = bodyHost();
    buildDetachedBody(host, turn, CHAT_ID, "sub-A", false);
    expect(shape(host)).toEqual(["run(wf-1)"]);
  });
});

describe("tool grouping IS contiguous, which is the contrast", () => {
  it("splits a run of tool calls broken by prose into two groups", () => {
    const r = render([
      callEntry("t", 1, toolCall("tool-a")),
      callEntry("t", 2, toolCall("tool-b")),
      text("t", 3, "prose"),
      callEntry("t", 4, toolCall("tool-c")),
    ]);
    expect(shape(r.body)).toEqual(["group(2)", "text(prose)", "group(1)"]);
  });

  it("keeps an unbroken run in one group", () => {
    const r = render([
      callEntry("t", 1, toolCall("tool-a")),
      callEntry("t", 2, toolCall("tool-b")),
      callEntry("t", 3, toolCall("tool-c")),
    ]);
    expect(shape(r.body)).toEqual(["group(3)"]);
  });

  it("does not break a group at an entry that renders nothing", () => {
    const r = render([
      callEntry("t", 1, toolCall("tool-a")),
      resultEntry("t", 2, 1),
      callEntry("t", 3, toolCall("tool-b")),
    ]);
    expect(shape(r.body)).toEqual(["group(2)"]);
  });
});

describe("sealing is invisible: the prose run", () => {
  it("mounts consecutive text entries as ONE row carrying the concatenation", () => {
    const r = render([text("t", 1, "one "), text("t", 2, "two "), text("t", 3, "three")]);
    const rows = r.body.querySelectorAll(".msg-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]?.textContent).toContain("one two three");
  });

  it("names its members on the row, in stream order", () => {
    const r = render([text("t", 1, "a"), text("t", 2, "b")]);
    const row = r.body.querySelector(".msg-row") as HTMLElement;
    expect(row.dataset["entries"]).toBe(`${r.turnID}-e1 ${r.turnID}-e2`);
  });

  it("ends the run at an entry that RENDERS, and opens a new one after it", () => {
    const r = render([
      text("t", 1, "before"),
      callEntry("t", 2, toolCall("tool-a")),
      text("t", 3, "after"),
    ]);
    expect(shape(r.body)).toEqual(["text(before)", "group(1)", "text(after)"]);
  });

  it("renders a run broken by a FOLD kind identically to one text entry", () => {
    // A seal inside a run is invisible: a non-rendering entry between members leaves the concatenated markup.
    const split = render([
      text("t", 1, "one **two** "),
      callEntry("t", 2, toolCall("tool-a")),
      resultEntry("t", 3, 2),
      text("t", 4, "three"),
    ]);
    // The tool call breaks the run; its result does not.
    const whole = render([text("t", 1, "one **two** three")]);
    const straddle = render([
      text("t", 1, "one **two** "),
      resultEntry("t", 2, 1),
      text("t", 3, "three"),
    ]);
    const bubble = (r: Render): string =>
      (r.body.querySelector(".message.assistant") as HTMLElement).innerHTML;
    expect(bubble(straddle)).toBe(bubble(whole));
    expect(split.body.querySelectorAll(".msg-row")).toHaveLength(2);
  });

  it("answers for every member with the run's ONE element", () => {
    const r = render([text("t", 1, "a"), text("t", 2, "b")]);
    const row = r.body.querySelector(".msg-row");
    expect(blockElement(r.turnID, 1)).toBe(row);
    expect(blockElement(r.turnID, 2)).toBe(row);
  });
});

describe("four kinds fold into an entry already on screen", () => {
  it("folds a tool_result onto its call's card and renders nothing of its own", () => {
    const r = render([
      callEntry("t", 1, toolCall("tool-a", { status: "in_progress" })),
      resultEntry("t", 2, 1, { status: "failed" }),
    ]);
    expect(shape(r.body)).toEqual(["group(1)"]);
    expect(r.body.querySelector(".tool-call")?.getAttribute("data-outcome")).toBe("fail");
    expect(blockElement(r.turnID, 2)).toBeUndefined();
  });

  it("renders NOTHING for a turn_bind", () => {
    const r = render([sealed("t", 1, "turn_bind", { kas_message_id: "kas-1", session: "s-1" })]);
    expect(shape(r.body)).toEqual([]);
    expect(blockElement(r.turnID, 1)).toBeUndefined();
  });

  it("replaces the first plan card's entries with a later plan's", () => {
    const first = sealed("t", 1, "plan", {
      entries: [{ content: "step one", priority: "high", status: "in_progress" }],
    });
    const r = render([first]);
    expect(shape(r.body)).toEqual(["plan"]);
    expect(r.body.textContent).toContain("step one");
    update(r, [
      first,
      sealed("t", 2, "plan", {
        entries: [{ content: "step two", priority: "high", status: "completed" }],
      }),
    ]);
    expect(r.body.querySelectorAll(".plan-message")).toHaveLength(1);
    expect(r.body.textContent).toContain("step two");
    expect(r.body.textContent).not.toContain("step one");
    expect(blockElement(r.turnID, 2)).toBeUndefined();
  });

  it("marks the steer note acknowledged AND renders the ack's own words as agent content", () => {
    // The acknowledgement's words are the model's, in their own bubble, since a rendering entry ends the run.
    const steer = sealed(
      "t",
      1,
      "steer",
      { text: "use tabs", origin: "user", state: "read" },
      {
        id: "steer-1",
      },
    );
    const r = render([
      steer,
      sealed(
        "t",
        2,
        "steer_ack",
        { steer_id: "steer-1", text: "tabs it is" },
        {
          id: "steer-1:ack",
        },
      ),
    ]);
    const note = r.body.querySelector(".steer-note") as HTMLElement;
    expect(note.getAttribute("data-acknowledged")).toBe("true");
    expect(shape(r.body)).toEqual(["note", "text(tabs it is)"]);
  });
});

describe("the lane's OPEN entry is what streams, at the tail of its view", () => {
  const openText = (turnID: string, id: string, s: string, lane = ""): OpenEntry => ({
    turn: turnID,
    id,
    lane,
    kind: "text",
    text: s,
    n: 1,
  });

  /** Build a live body holding `open` as the lane's open entry. */
  function renderLive(entries: Entry[], open: OpenEntry[]): Render {
    const turnID = nextTurnID();
    const turn = turnOf(turnID, retarget(entries, turnID), {
      open: open.map((o) => ({ ...o, turn: turnID })),
    });
    const body = bodyHost();
    buildAssistantBody(body, turn, CHAT_ID, true);
    return { turnID, body, turn };
  }

  // A live bubble's text arrives over frames, so these cases assert run membership, not painted words.
  it("joins the run its lane's tail sits in", () => {
    const r = renderLive([text("t", 1, "sealed ")], [openText("t", "open-1", "growing")]);
    const rows = r.body.querySelectorAll(".msg-row");
    expect(rows).toHaveLength(1);
    expect((rows[0] as HTMLElement).dataset["entries"]).toBe(`${r.turnID}-e1 open-1`);
  });

  it("mounts a run of its OWN when the lane's tail is not prose", () => {
    const r = renderLive(
      [callEntry("t", 1, toolCall("tool-a"))],
      [openText("t", "open-1", "after the card")],
    );
    const rows = r.body.querySelectorAll(".msg-row");
    expect(rows).toHaveLength(1);
    expect((rows[0] as HTMLElement).dataset["entries"]).toBe("open-1");
    expect(r.body.lastElementChild).toBe(rows[0]);
  });

  it("carries the caret on the open run alone", () => {
    const r = renderLive(
      [text("t", 1, "one"), callEntry("t", 2, toolCall("tool-a")), text("t", 3, "two")],
      [openText("t", "open-1", "three")],
    );
    const carets = r.body.querySelectorAll(".message.assistant.streaming");
    expect(carets).toHaveLength(1);
    const row = carets[0]?.closest(".msg-row") as HTMLElement;
    expect(row.dataset["entries"]).toBe(`${r.turnID}-e3 open-1`);
  });

  it("opens no caret on a REPLAY", () => {
    const r = render([text("t", 1, "history")]);
    expect(r.body.querySelectorAll(".message.assistant.streaming")).toHaveLength(0);
  });

  it("hands the surface to the SEAL rather than mounting a second bubble", () => {
    const r = renderLive([], [openText("t", "open-1", "growing")]);
    const row = r.body.querySelector(".msg-row");
    // The same words now carry a position: the open-only run is re-keyed under it.
    update(r, [sealed("t", 1, "text", { text: "growing" }, { id: "open-1" })], true);
    expect(r.body.querySelectorAll(".msg-row")).toHaveLength(1);
    expect(blockElement(r.turnID, 1)).toBe(row);
  });

  it("drops the open surface when the store retires that entry unsealed", () => {
    const r = renderLive([], [openText("t", "open-1", "abandoned")]);
    expect(r.body.querySelectorAll(".msg-row")).toHaveLength(1);
    const turn = turnOf(r.turnID, [], { open: [] });
    updateAssistantBody(r.body, turn, CHAT_ID, true);
    expect(shape(r.body)).toEqual([]);
  });

  it("ignores an open entry of another LANE", () => {
    const r = renderLive([text("t", 1, "root")], [openText("t", "open-A", "delegate", "sub-A")]);
    const rows = r.body.querySelectorAll(".msg-row");
    expect(rows).toHaveLength(1);
    // Membership, not painted words: a text assertion would pass whether or not the tail had mounted.
    expect((rows[0] as HTMLElement).dataset["entries"]).toBe(`${r.turnID}-e1`);
  });
});

describe("an event kind renders a row at its own seq", () => {
  it("draws every event kind in seq order, between prose", () => {
    const r = render([
      text("t", 1, "before"),
      sealed("t", 2, "compaction", { summary: "s", id: "compaction-1" }),
      sealed("t", 3, "compaction_failed", { reason: "too big" }),
      sealed("t", 4, "safety_blocked", { reason: "blocked" }),
      sealed("t", 5, "model_switched", { from: "a", to: "b" }),
      sealed("t", 6, "model_routed", { message: "Switched to it." }),
      text("t", 7, "after"),
    ]);
    expect(shape(r.body)).toEqual([
      "text(before)",
      "event(compaction)",
      "event(compaction_failed)",
      "event(safety_blocked)",
      "event(model_switched)",
      "event(model_routed)",
      "text(after)",
    ]);
  });

  it("breaks a prose run, because it renders at its own position", () => {
    const r = render([
      text("t", 1, "one"),
      sealed("t", 2, "compaction", { summary: "s", id: "compaction-1" }),
      text("t", 3, "two"),
    ]);
    expect(r.body.querySelectorAll(".msg-row")).toHaveLength(2);
  });
});

describe("a thinking trace is per LANE, and its disclosure is positional", () => {
  const trace = (body: HTMLElement): HTMLDetailsElement =>
    body.querySelector(".reasoning-block") as HTMLDetailsElement;

  it("renders EXPANDED while it is the newest element of its lane", () => {
    const r = render([text("t", 1, "before"), thinking("t", 2, "weighing it up")]);
    expect(shape(r.body)).toEqual(["text(before)", "thinking"]);
    expect(trace(r.body).open).toBe(true);
  });

  it("renders collapsed once the store holds a successor", () => {
    // Sealed from the store, so a trace reaches this range finished however the range got here.
    const r = render([thinking("t", 1, "weighing it up"), text("t", 2, "answer")]);
    expect(shape(r.body)).toEqual(["thinking", "text(answer)"]);
    expect(trace(r.body).open).toBe(false);
    expect(r.body.querySelector(".reasoning-label")?.textContent).toContain("completed");
  });

  it("renders NOTHING for an empty settled trace", () => {
    const r = render([thinking("t", 1, "")]);
    expect(shape(r.body)).toEqual([]);
    expect(blockElement(r.turnID, 1)).toBeUndefined();
  });

  it("drops a trace of another lane", () => {
    const r = render([thinking("t", 1, "delegate thinks", "sub-A"), text("t", 2, "root")]);
    expect(shape(r.body)).toEqual(["text(root)"]);
  });

  it("ends the prose run it interrupts", () => {
    const r = render([text("t", 1, "one"), thinking("t", 2, "hmm"), text("t", 3, "two")]);
    expect(shape(r.body)).toEqual(["text(one)", "thinking", "text(two)"]);
  });

  it("adopts the OPEN trace's element when it seals rather than drawing it twice", () => {
    const turnID = nextTurnID();
    const open: OpenEntry = {
      turn: turnID,
      id: "open-th",
      lane: "",
      kind: "thinking",
      text: "still weighing",
      n: 1,
    };
    const body = bodyHost();
    buildAssistantBody(body, turnOf(turnID, [], { open: [open] }), CHAT_ID, true);
    const live = trace(body);
    expect(live).not.toBeNull();
    expect(body.querySelector(".reasoning-label")?.textContent).toBe("Thinking…");
    updateAssistantBody(
      body,
      turnOf(turnID, [
        {
          id: "open-th",
          turn: turnID,
          kind: "thinking",
          seq: 1,
          ts: 1,
          payload: { text: "still weighing" },
        },
      ]),
      CHAT_ID,
      true,
    );
    expect(body.querySelectorAll(".reasoning-block")).toHaveLength(1);
    expect(blockElement(turnID, 1)).toBe(live);
  });
});

describe("getLiveAnchor: the registry, not the tree", () => {
  /** A live body, which is what registers a top-level bubble as the follow anchor. */
  function renderLiveBody(entries: Entry[], chatID = CHAT_ID): Render {
    const turnID = nextTurnID();
    const turn = turnOf(turnID, retarget(entries, turnID));
    const body = bodyHost();
    buildAssistantBody(body, turn, chatID, true);
    return { turnID, body, turn };
  }

  it("has no anchor when nothing is streaming", () => {
    render([text("t", 1, "history")]);
    expect(getLiveAnchor()).toBeNull();
  });

  it("takes the NEWEST live bubble, not the first in document order", () => {
    const first = renderLiveBody([text("t", 1, "older")]);
    const second = renderLiveBody([text("t", 1, "newer")]);
    expect(second.body.contains(getLiveAnchor())).toBe(true);
    expect(first.body.contains(getLiveAnchor())).toBe(false);
  });

  it("falls back to the older survivor when the newest render goes", () => {
    const first = renderLiveBody([text("t", 1, "older")]);
    const second = renderLiveBody([text("t", 1, "newer")]);
    disposeAssistantBody(second.turnID);
    expect(first.body.contains(getLiveAnchor())).toBe(true);
  });

  it("clears for good once every live render is disposed", () => {
    const r = renderLiveBody([text("t", 1, "live")]);
    expect(getLiveAnchor()).not.toBeNull();
    disposeAssistantBody(r.turnID);
    expect(getLiveAnchor()).toBeNull();
  });

  it("never anchors a DETACHED render, whose box the reader cannot see", () => {
    const turnID = nextTurnID();
    const turn = turnOf(turnID, retarget([text("t", 1, "delegate", "sub-A")], turnID));
    buildDetachedBody(bodyHost(), turn, CHAT_ID, "sub-A", true);
    expect(getLiveAnchor()).toBeNull();
  });

  it("never anchors into a PARKED chat on the fallback scan", () => {
    const parked = renderLiveBody([text("t", 1, "other chat")], "c-other");
    const active = renderLiveBody([text("t", 1, "this chat")]);
    disposeAssistantBody(active.turnID);
    expect(getLiveAnchor()).toBeNull();
    expect(parked.body.querySelectorAll(".msg-row")).toHaveLength(1);
  });
});

describe("a body mounts an entry RANGE, and the grouping is derived", () => {
  const sixCalls = (): Entry[] => [
    text("t", 1, "one"),
    callEntry("t", 2, toolCall("tool-a")),
    callEntry("t", 3, toolCall("tool-b")),
    callEntry("t", 4, toolCall("tool-c")),
    text("t", 5, "two"),
    text("t", 6, "three"),
  ];

  it("mounts the range and nothing outside it", () => {
    const r = renderWindow(sixCalls(), { from: 3, to: 5 });
    expect(mountedWindow(r.turnID)).toEqual({ from: 3, to: 5 });
    expect(shape(r.body)).toEqual(["group(2)"]);
    expect(blockElement(r.turnID, 1)).toBeUndefined();
    expect(blockElement(r.turnID, 5)).toBeUndefined();
  });

  it("INSERTS a head extension above what is mounted", () => {
    const r = renderWindow(sixCalls(), { from: 4, to: 7 });
    expect(shape(r.body)).toEqual(["group(1)", "text(twothree)"]);
    mountHeadRange(r.turn, { from: 1, to: 7 }, false);
    expect(mountedWindow(r.turnID)).toEqual({ from: 1, to: 7 });
    expect(shape(r.body)).toEqual(["text(one)", "group(3)", "text(twothree)"]);
  });

  it("derives the group from the STORE run, so a head extension joins it", () => {
    // The key is the `seq` the run of calls started at, not mount order.
    const r = renderWindow(sixCalls(), { from: 4, to: 5 });
    expect(shape(r.body)).toEqual(["group(1)"]);
    mountHeadRange(r.turn, { from: 2, to: 5 }, false);
    expect(shape(r.body)).toEqual(["group(3)"]);
  });

  it("retracts at the head, and at the tail", () => {
    const r = render(sixCalls());
    expect(mountedWindow(r.turnID)).toEqual({ from: 0, to: 7 });
    dropHead(r.turn, { from: 2, to: 7 });
    expect(mountedWindow(r.turnID)).toEqual({ from: 2, to: 7 });
    expect(blockElement(r.turnID, 1)).toBeUndefined();
    dropTail(r.turn, { from: 2, to: 5 });
    expect(mountedWindow(r.turnID)).toEqual({ from: 2, to: 5 });
    expect(shape(r.body)).toEqual(["group(3)"]);
  });

  it("snaps an edge that would CUT a prose run", () => {
    // A run cut at a window edge would mount as two rows with two markdown streams, so `from` snaps down to the run's
    // first entry and `to` past its last; the budget overspends by at most one run per side.
    const turnID = nextTurnID();
    const turn = turnOf(turnID, retarget(sixCalls(), turnID));
    expect(sliceTurn(turn, { from: 6, to: 7 })).toEqual({ from: 5, to: 7 });
    expect(sliceTurn(turn, { from: 4, to: 6 })).toEqual({ from: 4, to: 7 });
    // A range whose edges already fall on a boundary is left alone.
    expect(sliceTurn(turn, { from: 2, to: 5 })).toEqual({ from: 2, to: 5 });
  });

  it("mounts a snapped range's run as ONE row", () => {
    const r = renderWindow(sixCalls(), { from: 5, to: 7 });
    const rows = r.body.querySelectorAll(".msg-row");
    expect(rows).toHaveLength(1);
    expect((rows[0] as HTMLElement).dataset["entries"]).toBe(`${r.turnID}-e5 ${r.turnID}-e6`);
  });

  it("leaves a run's row to its HEAD's own drop", () => {
    // Only the head releases the shared element; a later member leaves the row standing.
    const r = render(sixCalls());
    const row = blockElement(r.turnID, 5);
    dropTail(r.turn, { from: 0, to: 6 });
    expect(blockElement(r.turnID, 6)).toBeUndefined();
    expect(blockElement(r.turnID, 5)).toBe(row);
    expect(r.body.contains(row ?? null)).toBe(true);
  });

  it("removes a run card when the drop takes its launch", () => {
    const r = render([text("t", 1, "one"), ...launch("t", 2, "wf-1")]);
    expect(shape(r.body)).toEqual(["text(one)", "run(wf-1)"]);
    dropTail(r.turn, { from: 0, to: 2 });
    expect(shape(r.body)).toEqual(["text(one)"]);
    expect(blockElement(r.turnID, 2)).toBeUndefined();
  });
});

describe("a pipeline's stages render inside the orchestrate call that started them", () => {
  /** An `orchestrate_subagent` driver. `declared` is the `input.stages` length, a floor on the stage count. */
  function driver(seq: number, id: string, declared: number, status = "completed"): Entry {
    return callEntry(
      "t",
      seq,
      toolCall(id, {
        title: "Orchestrate Sub-agent",
        status: status as EntryToolCall["status"],
        input: { stages: Array.from({ length: declared }, (_, i) => ({ name: `s${String(i)}` })) },
      }),
    );
  }

  /** One stage of `driverID`; its tool-call id embeds the driver's, which is the whole join. */
  function stage(seq: number, driverID: string, name: string, subtask: string): Entry {
    return callEntry(
      "t",
      seq,
      toolCall(`invoke_subagent_${driverID}_stage_${name}`, {
        title: `Sub-agent: ${name}`,
        agent_subtask_id: subtask,
      }),
    );
  }

  const box = (r: Render, pipelineID: string): HTMLElement =>
    r.body.querySelector(`.subagent-container[data-pipeline="${pipelineID}"]`) as HTMLElement;

  const inside = (r: Render, pipelineID: string): string[] =>
    shape(box(r, pipelineID).querySelector(".subagent-body") as HTMLElement);

  it("nests every stage's card inside the driver's box", () => {
    const r = render([
      driver(1, "orc-1", 2),
      stage(2, "orc-1", "review", "sub-A"),
      stage(3, "orc-1", "apply", "sub-B"),
    ]);
    expect(shape(r.body)).toEqual(["pipeline(orc-1)"]);
    expect(inside(r, "orc-1")).toEqual(["card(sub-A)", "card(sub-B)"]);
  });

  it("labels the box by its DECLARED count, which knows a stage still on its way", () => {
    const r = render([driver(1, "orc-1", 3), stage(2, "orc-1", "review", "sub-A")]);
    expect(box(r, "orc-1").querySelector(".subagent-name")?.textContent).toBe(
      "Subagent pipeline · 3 stages",
    );
  });

  it("places a stage whose invocation arrives BEFORE its driver", () => {
    // `indexPipelines` reads the whole tool-call array, so neither arrival order strands a stage.
    const r = render([
      stage(1, "orc-1", "review", "sub-A"),
      driver(2, "orc-1", 2),
      stage(3, "orc-1", "apply", "sub-B"),
    ]);
    expect(shape(r.body)).toEqual(["pipeline(orc-1)"]);
    expect(inside(r, "orc-1")).toEqual(["card(sub-A)", "card(sub-B)"]);
  });

  it("PROMOTES a lone stage: no container over one card", () => {
    const r = render([driver(1, "orc-1", 1), stage(2, "orc-1", "only", "sub-A")]);
    expect(shape(r.body)).toEqual(["card(sub-A)"]);
    expect(r.body.querySelectorAll(".subagent-container")).toHaveLength(0);
  });

  it("upgrades a promoted stage into a container when a sibling arrives, KEEPING the card", () => {
    // A re-parent, not a rebuild: the node carries its disclosure, observers and effects.
    const first = [driver(1, "orc-1", 1), stage(2, "orc-1", "one", "sub-A")];
    const r = render(first);
    const card = blockElement(r.turnID, 2);
    expect(shape(r.body)).toEqual(["card(sub-A)"]);
    update(r, [...first, stage(3, "orc-1", "two", "sub-B")]);
    expect(shape(r.body)).toEqual(["pipeline(orc-1)"]);
    expect(inside(r, "orc-1")).toEqual(["card(sub-A)", "card(sub-B)"]);
    expect(blockElement(r.turnID, 2)).toBe(card);
  });

  it("keeps the box for a driver that SETTLED having dispatched no stage", () => {
    const r = render([driver(1, "orc-1", 1)]);
    expect(shape(r.body)).toEqual(["pipeline(orc-1)"]);
    expect(inside(r, "orc-1")).toEqual([]);
  });

  it("withholds that box while the driver is still RUNNING", () => {
    // Deferring to the settle stops the fallback box displacing a live stage about to be promoted.
    const r = render([driver(1, "orc-1", 1, "in_progress")]);
    expect(shape(r.body)).toEqual([]);
  });

  it("resolves a stage whose NAME carries the separator to its own driver", () => {
    // `indexOf`, not `lastIndexOf`: the driver half is machine-minted, the stage name author-supplied.
    const r = render([driver(1, "orc-1", 2), stage(2, "orc-1", "run_stage_two", "sub-A")]);
    expect(inside(r, "orc-1")).toEqual(["card(sub-A)"]);
  });

  it("leaves a delegate whose id is not stage-shaped OUT of the pipeline", () => {
    const r = render([driver(1, "orc-1", 2), invocation("t", 2, "sub-A")]);
    expect(shape(r.body)).toEqual(["pipeline(orc-1)", "card(sub-A)"]);
    expect(inside(r, "orc-1")).toEqual([]);
  });
});

describe("the newest top-level box renders expanded", () => {
  function driver(seq: number, id: string, declared: number): Entry {
    return callEntry(
      "t",
      seq,
      toolCall(id, {
        title: "Orchestrate Sub-agent",
        input: { stages: Array.from({ length: declared }, (_, i) => ({ name: `s${String(i)}` })) },
      }),
    );
  }

  function stage(seq: number, driverID: string, name: string, subtask: string): Entry {
    return callEntry(
      "t",
      seq,
      toolCall(`invoke_subagent_${driverID}_stage_${name}`, {
        title: `Sub-agent: ${name}`,
        agent_subtask_id: subtask,
      }),
    );
  }

  const pipeline = (r: Render): HTMLElement =>
    r.body.querySelector(".subagent-container") as HTMLElement;

  /** The disclosure settles on a microtask and its own MutationObserver, so the verdict is readable a task later. */
  const settled = (): Promise<void> => new Promise((r) => setTimeout(r, 0));

  it("leaves a pipeline box open while it is the newest top-level element", async () => {
    const r = render([driver(1, "orc-1", 2), stage(2, "orc-1", "a", "sub-A")]);
    await settled();
    expect(pipeline(r).classList.contains("collapsed")).toBe(false);
  });

  it("is BORN folded when the range already holds the entry after it", async () => {
    // The verdict precedes the box, so a superseded box is built closed rather than animated shut.
    const r = render([
      driver(1, "orc-1", 2),
      stage(2, "orc-1", "a", "sub-A"),
      text("t", 3, "and so"),
    ]);
    await settled();
    expect(pipeline(r).classList.contains("collapsed")).toBe(true);
  });

  it("folds when the entry after it ARRIVES, on the next pass", async () => {
    const before = [driver(1, "orc-1", 2), stage(2, "orc-1", "a", "sub-A")];
    const r = render(before);
    await settled();
    expect(pipeline(r).classList.contains("collapsed")).toBe(false);
    update(r, [...before, text("t", 3, "and so")]);
    await settled();
    expect(pipeline(r).classList.contains("collapsed")).toBe(true);
  });

  it("refuses that fold while the driver is still RUNNING", async () => {
    // A refusal to collapse, not a reason to expand: an open box with work in flight stays open.
    const running = callEntry(
      "t",
      1,
      toolCall("orc-1", {
        title: "Orchestrate Sub-agent",
        status: "in_progress",
        input: { stages: [{ name: "a" }, { name: "b" }] },
      }),
    );
    const before = [running, stage(2, "orc-1", "a", "sub-A")];
    const r = render(before);
    await settled();
    expect(pipeline(r).classList.contains("collapsed")).toBe(false);
    update(r, [...before, text("t", 3, "and so")]);
    await settled();
    expect(pipeline(r).classList.contains("collapsed")).toBe(false);
  });

  it("gives a box whose HOST is another box no expanded verdict of its own", () => {
    // Only the view's root lane earns the newest-element verdict, so a delegate page's trace never does.
    const turnID = nextTurnID();
    const turn = turnOf(turnID, retarget([thinking("t", 1, "on its own page", "sub-A")], turnID));
    const host = bodyHost();
    buildDetachedBody(host, turn, CHAT_ID, "sub-A", false);
    const trace = host.querySelector(".reasoning-block") as HTMLDetailsElement;
    expect(trace).not.toBeNull();
    expect(trace.open).toBe(false);
  });
});

describe("blockElement resolves an entry's own element", () => {
  it("answers the trace's own details element", () => {
    const r = render([thinking("t", 1, "weighing")]);
    expect(blockElement(r.turnID, 1)).toBe(r.body.querySelector(".reasoning-block"));
  });

  it("answers the tool card, not the group that holds it", () => {
    const r = render([
      callEntry("t", 1, toolCall("tool-a")),
      callEntry("t", 2, toolCall("tool-b")),
    ]);
    const cards = r.body.querySelectorAll(".tool-call");
    expect(blockElement(r.turnID, 1)).toBe(cards[0]);
    expect(blockElement(r.turnID, 2)).toBe(cards[1]);
  });

  it("answers the pipeline box for the driver that opened it", () => {
    const r = render([
      callEntry(
        "t",
        1,
        toolCall("orc-1", { title: "Orchestrate Sub-agent", input: { stages: [] } }),
      ),
    ]);
    expect(blockElement(r.turnID, 1)).toBe(r.body.querySelector(".subagent-container"));
  });

  it("answers undefined for a seq the turn does not hold", () => {
    const r = render([text("t", 1, "one")]);
    expect(blockElement(r.turnID, 99)).toBeUndefined();
    expect(blockElement("t-nosuch", 1)).toBeUndefined();
  });
});

describe("a delegate card's status follows its invocation CALL", () => {
  const cardOf = (r: Render): HTMLElement => r.body.querySelector(".subagent-block") as HTMLElement;

  it("says CANCELLED for a persisted aborted invocation", () => {
    // marotte's word for a call its turn ended under, matching the footer; the wire value is `aborted`.
    const r = render([
      callEntry(
        "t",
        1,
        toolCall("tool-sub-A", {
          title: "Sub-agent: sub-A",
          agent_subtask_id: "sub-A",
          status: "aborted",
        }),
      ),
    ]);
    const head = cardOf(r).querySelector(".subagent-header") as HTMLElement;
    expect(head.getAttribute("aria-label")).toContain("cancelled");
    expect(cardOf(r).querySelector(".subagent-icon")?.classList.contains("is-warn")).toBe(true);
  });

  it("follows a later status arriving on the CALL's signal", () => {
    const call = toolCall("tool-sub-A", {
      title: "Sub-agent: sub-A",
      agent_subtask_id: "sub-A",
      status: "in_progress",
    });
    const r = render([callEntry("t", 1, call)]);
    const head = cardOf(r).querySelector(".subagent-header") as HTMLElement;
    expect(head.getAttribute("aria-label")).toContain("running");
    const sig = ensureToolCallSig(CHAT_ID, "tool-sub-A", call);
    sig.value = { ...call, status: "failed" };
    expect(head.getAttribute("aria-label")).toContain("failed");
  });

  it("shows an inline helper's model and effort on its card", () => {
    const r = render([
      callEntry(
        "t",
        1,
        toolCall("tool-sub-A", {
          title: "Sub-agent: x",
          agent_subtask_id: "sub-A",
          input: { name: "x", inlineAgent: { systemPrompt: "p", model: "m", effort: "high" } },
        }),
      ),
    ]);
    expect(cardOf(r).querySelector(".subagent-name")?.textContent).toBe("x (inline agent)");
    expect(cardOf(r).querySelector(".subagent-detail")?.textContent).toBe("m · high");
  });

  it("shows the category a saved agent was asked to run on as its card's detail", () => {
    const r = render([
      callEntry(
        "t",
        1,
        toolCall("tool-sub-A", {
          title: "Sub-agent: context-gatherer",
          agent_subtask_id: "sub-A",
          input: { name: "context-gatherer", modelCategory: "auto-balanced" },
        }),
      ),
    ]);
    expect(cardOf(r).querySelector(".subagent-detail")?.textContent).toBe(
      "Category: auto-balanced",
    );
  });

  it("re-binds a pipeline box whose driver entry the window dropped", () => {
    // The box outlives the entry that opened it, so without the rebind its header freezes.
    const driverCall = toolCall("orc-1", {
      title: "Orchestrate Sub-agent",
      status: "in_progress",
      input: { stages: [{ name: "a" }, { name: "b" }] },
    });
    const r = render([
      callEntry("t", 1, driverCall),
      callEntry(
        "t",
        2,
        toolCall("invoke_subagent_orc-1_stage_a", {
          title: "Sub-agent: a",
          agent_subtask_id: "sub-A",
        }),
      ),
    ]);
    dropHead(r.turn, { from: 2, to: 3 });
    const box = r.body.querySelector(".subagent-container") as HTMLElement;
    expect(box).not.toBeNull();
    const sig = ensureToolCallSig(CHAT_ID, "orc-1", driverCall);
    sig.value = { ...driverCall, status: "completed", duration_ms: 1234 };
    expect(box.querySelector(".subagent-icon")?.classList.contains("is-ok")).toBe(true);
  });
});

describe("an empty bubble's row is marked is-empty", () => {
  it("marks a settled entry that carries no text", () => {
    const r = render([text("t", 1, "")]);
    const row = r.body.querySelector(".msg-row") as HTMLElement;
    expect(row.classList.contains("is-empty")).toBe(true);
  });

  it("clears the mark when the entry's text lands", async () => {
    const r = render([text("t", 1, "")]);
    const row = r.body.querySelector(".msg-row") as HTMLElement;
    expect(row.classList.contains("is-empty")).toBe(true);
    update(r, [text("t", 1, "the answer")]);
    await vi.waitFor(() => {
      expect(row.classList.contains("is-empty")).toBe(false);
    });
  });

  it("never marks a LIVE bubble's row, which is reserved rather than empty", () => {
    const turnID = nextTurnID();
    const turn = turnOf(turnID, [], {
      open: [{ turn: turnID, id: "open-1", lane: "", kind: "text", text: "", n: 1 }],
    });
    const body = bodyHost();
    buildAssistantBody(body, turn, CHAT_ID, true);
    const row = body.querySelector(".msg-row") as HTMLElement;
    expect(row).not.toBeNull();
    expect(row.classList.contains("is-empty")).toBe(false);
  });
});

describe("a mounted entry picks up text that arrived after it mounted", () => {
  it("brings a mounted run up to the store's text for its member", async () => {
    const r = render([text("t", 1, "one "), text("t", 2, "two")]);
    const row = r.body.querySelector(".msg-row") as HTMLElement;
    // The markdown stream holds a trailing partial token, so the growth ends on a space.
    update(r, [text("t", 1, "one "), text("t", 2, "two THREE ")]);
    await vi.waitFor(() => {
      expect(row.textContent).toContain("one two THREE");
    });
    expect(r.body.querySelectorAll(".msg-row")).toHaveLength(1);
  });

  it("adds nothing when the store's text did not grow", async () => {
    const r = render([text("t", 1, "settled")]);
    const row = r.body.querySelector(".msg-row") as HTMLElement;
    await vi.waitFor(() => {
      expect(row.textContent).toContain("settled");
    });
    const before = row.textContent;
    update(r, [text("t", 1, "settled")]);
    expect(row.textContent).toBe(before);
  });
});

describe("the auto-collapse registry: which arrivals close a tool group", () => {
  it("folds a group of two once a later entry closes its run", () => {
    const r = render([
      callEntry("t", 1, toolCall("tool-a")),
      callEntry("t", 2, toolCall("tool-b")),
      text("t", 3, "and so"),
    ]);
    const group = r.body.querySelector(".tool-group") as HTMLElement;
    expect(group.classList.contains("tool-group-auto-collapsed")).toBe(true);
    expect(group.querySelector(".tool-group-header")?.getAttribute("aria-expanded")).toBe("false");
  });

  it("leaves the run open while nothing has closed it", () => {
    const r = render([
      callEntry("t", 1, toolCall("tool-a")),
      callEntry("t", 2, toolCall("tool-b")),
    ]);
    const group = r.body.querySelector(".tool-group") as HTMLElement;
    expect(group.classList.contains("tool-group-auto-collapsed")).toBe(false);
  });

  it("leaves a BARE group open, because its body region IS the lone card", () => {
    const r = render([callEntry("t", 1, toolCall("tool-a")), text("t", 2, "and so")]);
    const group = r.body.querySelector(".tool-group") as HTMLElement;
    expect(group.classList.contains("tool-group-bare")).toBe(true);
    expect(group.classList.contains("tool-group-auto-collapsed")).toBe(false);
  });
});

describe("rendering a superseded run animates nothing", () => {
  it("mounts an already-folded group with zero animations and a committed 0px body", () => {
    // The verdict precedes the disclosure, so the region is born closed.
    const r = render([
      callEntry("t", 1, toolCall("tool-a")),
      callEntry("t", 2, toolCall("tool-b")),
      text("t", 3, "and so"),
    ]);
    const group = r.body.querySelector(".tool-group") as HTMLElement;
    const region = group.querySelector(".tool-group-body") as HTMLElement;
    expect(group.getAnimations({ subtree: true })).toHaveLength(0);
    expect(getComputedStyle(region).height).toBe("0px");
  });
});

describe("legacy internal tool calls are dropped at the dispatcher", () => {
  it("renders no card for a persisted cloud-config fetch", () => {
    const r = render([
      callEntry("t", 1, toolCall("tool-cc", { title: "Fetching your cloud config" })),
    ]);
    expect(shape(r.body)).toEqual([]);
    expect(r.body.textContent).not.toContain("cloud config");
  });

  it("drops it as if it never existed, so real neighbours still group", () => {
    const r = render([
      callEntry("t", 1, toolCall("tool-a")),
      callEntry("t", 2, toolCall("tool-cc", { title: "Fetching your cloud config" })),
      callEntry("t", 3, toolCall("tool-b")),
    ]);
    expect(shape(r.body)).toEqual(["group(2)"]);
  });
});

describe("one entry that fails to render does not take its turn with it", () => {
  const baseCbs = {
    pushEntryEffect: (): void => {
      /* noop */
    },
    disposeEntryEffects: (): void => {
      /* noop */
    },
    makeRow: (): HTMLDivElement => {
      const row = document.createElement("div");
      row.className = "msg-row";
      return row;
    },
    makeEvent: (e: Entry): HTMLElement => {
      const row = document.createElement("div");
      row.className = `event event-${e.kind}`;
      return row;
    },
  };
  let consoleError: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    consoleError = vi.spyOn(console, "error").mockImplementation(() => {
      /* noop */
    });
  });

  afterEach(() => {
    initBlockRenderer(baseCbs);
    consoleError.mockRestore();
  });

  function throwOnCompaction(): void {
    initBlockRenderer({
      ...baseCbs,
      makeEvent: (e) => {
        if (e.kind === "compaction") {
          throw new Error("boom");
        }
        return baseCbs.makeEvent(e);
      },
    });
  }

  function compaction(turnID: string, seq: number): Entry {
    return sealed(turnID, seq, "compaction", { summary: "s" });
  }

  it("shows a fallback in place of the failed entry and still mounts every later one", () => {
    throwOnCompaction();
    const r = render([text("t", 1, "before"), compaction("t", 2), text("t", 3, "after")]);
    const kids = [...r.body.children] as HTMLElement[];
    expect(kids.map((k) => k.className)).toEqual(["msg-row", "entry-fallback", "msg-row"]);
    expect(kids[0]?.textContent).toContain("before");
    expect(kids[2]?.textContent).toContain("after");
    expect(consoleError).toHaveBeenCalledTimes(1);
  });

  it("keeps every seq addressable, so a later update mounts only what is new", () => {
    throwOnCompaction();
    const r = render([compaction("t", 1), text("t", 2, "two")]);
    update(r, [compaction("t", 1), text("t", 2, "two"), callEntry("t", 3, toolCall("tool-a"))]);
    expect(blockElement(r.turnID, 1)?.classList.contains("entry-fallback")).toBe(true);
    expect(blockElement(r.turnID, 2)?.textContent).toContain("two");
    expect(blockElement(r.turnID, 3)?.classList.contains("tool-call")).toBe(true);
    expect(r.body.querySelectorAll(".entry-fallback")).toHaveLength(1);
  });

  it("carries the entry's text as TEXT, with only the label marked as chrome", () => {
    let calls = 0;
    initBlockRenderer({
      ...baseCbs,
      makeRow: () => {
        calls += 1;
        throw new Error("boom");
      },
    });
    const raw = "see <img src=x onerror=alert(1)> here";
    const r = render([text("t", 1, raw)]);
    expect(calls).toBe(1);
    const fallback = r.body.querySelector(".entry-fallback") as HTMLElement;
    const label = fallback.querySelector(".entry-fallback-label") as HTMLElement;
    const body = fallback.querySelector(".entry-fallback-text") as HTMLElement;
    expect(label.hasAttribute("data-vk-chrome")).toBe(true);
    expect(body.hasAttribute("data-vk-chrome")).toBe(false);
    expect(body.textContent).toBe(raw);
    expect(body.children).toHaveLength(0);
    expect(r.body.querySelector("img")).toBeNull();
  });

  it("contains a throw outside every entry, and logs a repeating one once", () => {
    const turnID = nextTurnID();
    // Iterating the body throws, so the pass fails before any entry is placed.
    const body: Entry[] = [];
    Object.defineProperty(body, Symbol.iterator, {
      value: (): never => {
        throw new Error("boom");
      },
    });
    const bad = turnOf(turnID, body);
    const host = bodyHost();
    expect(() => {
      buildAssistantBody(host, bad, CHAT_ID, false);
    }).not.toThrow();
    expect(() => {
      updateAssistantBody(host, bad, CHAT_ID, false);
    }).not.toThrow();
    expect(consoleError).toHaveBeenCalledTimes(1);
    const next = render([text("t", 1, "fine")]);
    expect(next.body.textContent).toContain("fine");
  });
});

describe("a render no chat owns settles its own cards", () => {
  // A run step's transcript passes "" for the chat, so no store publishes its `tool_result`s.
  const running = (): Entry => callEntry("t", 1, toolCall("tool-a", { status: "in_progress" }));

  it("settles a mounted card when its tool_result arrives", () => {
    const turnID = nextTurnID();
    const host = bodyHost();
    buildDetachedBody(host, turnOf(turnID, retarget([running()], turnID)), "", "", true);
    expect(host.querySelector(".tool-call")?.getAttribute("data-outcome")).toBe("running");
    const settled = retarget([running(), resultEntry("t", 2, 1, { status: "failed" })], turnID);
    updateDetachedBody(host, turnOf(turnID, settled), "", "", false);
    expect(host.querySelector(".tool-call")?.getAttribute("data-outcome")).toBe("fail");
  });
});
