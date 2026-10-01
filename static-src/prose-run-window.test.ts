// Adjacent `text` entries are ONE prose run, and a residency window may not cut one.
//
// Two halves, and they are one property rather than two: the run is one `.msg-row` holding
// one markdown stream, so a window edge landing between two of its members would mount it
// as two rows with two parsers — which is why `sliceTurn` snaps `from` down to the run's
// first entry and `to` up past its last. The window case drives both at once: the snapped
// range is what the mount is then asserted over, so the arithmetic and the DOM cannot agree
// separately.
//
// DEVIATION from the property's own wording: it lists `steer_ack` among the kinds that render
// nothing between two `text` entries, and ADDENDUM 5 overrides that — an ack is the model's
// own words, so it renders at its own `seq` and ENDS the run it interrupted. The case below
// pins the addendum.

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import type { Entry, EntryToolCall, EntryToolResult, OpenEntry } from "./types.js";
import type { Turn } from "./turns.js";

// The dispatcher's import graph reaches the shared DOM registry, which throws on a missing
// app root, so these ids exist before the import is evaluated.
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

const { buildAssistantBody, resetBlockRenders, initBlockRenderer, setRunCardOwners } =
  await import("./messages-blocks.js");
const { sliceTurn } = await import("./block-window.js");
const { toolResultID } = await import("./entry-ids.js");
const { clearAllEntrySigs, toolCallSigs } = await import("./store-signals.js");
const { setActive } = await import("./store.js");

/** The chat every render here belongs to. It is part of the per-tool signal key, and it must
 *  be the store's ACTIVE chat: the live-anchor scan only considers the active chat's renders. */
const CHAT_ID = "c-prose-run";
setActive(CHAT_ID);

initBlockRenderer({
  pushStreamingEffect: (): void => {
    /* noop: messages.ts owns the effect registry, and no case here reads it */
  },
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

function nextTurnID(): string {
  turnSeq += 1;
  return `pr-${String(turnSeq)}`;
}

function sealed(
  turnID: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  opts: { readonly id?: string } = {},
): Entry {
  return {
    id: opts.id ?? `${turnID}-e${String(seq)}`,
    turn: turnID,
    kind,
    seq,
    ts: seq + 1,
    payload,
  };
}

function text(turnID: string, seq: number, s: string): Entry {
  return sealed(turnID, seq, "text", { text: s });
}

// `Object.assign` rather than a spread: under `exactOptionalPropertyTypes` a spread of a
// `Partial` widens every required field to include `undefined`.
function toolCall(id: string): EntryToolCall {
  const base: EntryToolCall = {
    id,
    title: "Run Command",
    status: "completed",
    kind: "execute",
    ts: 1,
  };
  return base;
}

function callEntry(turnID: string, seq: number, id: string): Entry {
  return sealed(turnID, seq, "tool_call", toolCall(id), { id: `${turnID}-c${String(seq)}` });
}

/** The `tool_result` settling `callSeq`'s call, its id derived from the call ENTRY's. */
function resultEntry(turnID: string, seq: number, callSeq: number): Entry {
  const payload: EntryToolResult = Object.assign({ status: "completed" } as EntryToolResult, {});
  return sealed(turnID, seq, "tool_result", payload, {
    id: toolResultID(`${turnID}-c${String(callSeq)}`),
  });
}

function planEntry(turnID: string, seq: number, step: string): Entry {
  return sealed(turnID, seq, "plan", {
    entries: [{ content: step, priority: "high", status: "in_progress" }],
  });
}

function turnOf(turnID: string, body: Entry[]): Turn {
  return {
    id: turnID,
    n: 1,
    trigger: { id: `${turnID}-p`, text: "go" },
    body,
    openEntries: new Map<string, OpenEntry>(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
  };
}

let hosts: HTMLElement[] = [];

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

/** Re-key `entries` onto `turnID`, so a fixture reads without threading one through. */
function retarget(entries: Entry[], turnID: string): Entry[] {
  return entries.map((e) => ({
    ...e,
    turn: turnID,
    id: e.id.startsWith("t-") ? turnID + e.id.slice(1) : e.id,
  }));
}

function render(build: (t: string) => Entry[], range?: { from: number; to: number }): Render {
  const turnID = nextTurnID();
  const turn = turnOf(turnID, retarget(build(turnID), turnID));
  const body = bodyHost();
  buildAssistantBody(body, turn, CHAT_ID, false, range);
  return { turnID, body, turn };
}

function rows(body: HTMLElement): HTMLElement[] {
  return [...body.querySelectorAll<HTMLElement>(".msg-row")];
}

/** One prose run's rendered markup, which is what "nothing stripped" is asserted over. */
function bubble(body: HTMLElement): string {
  const el = body.querySelector<HTMLElement>(".message.assistant");
  if (el === null) {
    throw new Error("no prose bubble was mounted");
  }
  return el.innerHTML;
}

function shape(body: HTMLElement): string[] {
  return [...body.children].map((e) => {
    const h = e as HTMLElement;
    if (h.classList.contains("msg-row")) {
      return `text(${String(h.textContent).trim()})`;
    }
    if (h.classList.contains("steer-note")) {
      return "note";
    }
    if (h.classList.contains("plan-message")) {
      return "plan";
    }
    if (h.classList.contains("tool-group")) {
      return "group";
    }
    return h.className;
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

describe("a seal inside a prose run is invisible", () => {
  it("renders a run straddling a tool_result identically to one text entry", () => {
    const split = render((t) => [
      text(t, 1, "one **two** "),
      callEntry(t, 2, "tool-a"),
      resultEntry(t, 3, 2),
      text(t, 4, "three"),
    ]);
    const straddle = render((t) => [
      text(t, 1, "one **two** "),
      resultEntry(t, 2, 1),
      text(t, 3, "three"),
    ]);
    const whole = render((t) => [text(t, 1, "one **two** three")]);

    expect(bubble(straddle.body)).toBe(bubble(whole.body));
    expect(rows(straddle.body)).toHaveLength(1);
    // The CALL renders, so it breaks the run its result did not.
    expect(rows(split.body)).toHaveLength(2);
  });

  it("renders a run straddling a LATER plan identically to one text entry", () => {
    // A turn's first plan renders; every plan after it folds into that card, so it is the
    // second one that leaves a run whole.
    const straddle = render((t) => [
      planEntry(t, 1, "step one"),
      text(t, 2, "one **two** "),
      planEntry(t, 3, "step two"),
      text(t, 4, "three"),
    ]);
    const whole = render((t) => [planEntry(t, 1, "step one"), text(t, 2, "one **two** three")]);

    expect(bubble(straddle.body)).toBe(bubble(whole.body));
    expect(rows(straddle.body)).toHaveLength(1);
    expect(shape(straddle.body)).toEqual(["plan", "text(one two three)"]);
  });

  it("names every member of the run on the row, in stream order", () => {
    const r = render((t) => [text(t, 1, "one "), resultEntry(t, 2, 1), text(t, 3, "two")]);
    const row = rows(r.body)[0];
    expect(row?.dataset["entries"]).toBe(`${r.turnID}-e1 ${r.turnID}-e3`);
  });
});

describe("an entry that RENDERS ends the run it interrupted", () => {
  it("puts a steer note between two runs", () => {
    const r = render((t) => [
      text(t, 1, "one"),
      sealed(t, 2, "steer", { text: "use tabs", origin: "user", state: "read" }, { id: "steer-1" }),
      text(t, 3, "two"),
    ]);
    expect(shape(r.body)).toEqual(["text(one)", "note", "text(two)"]);
    expect(rows(r.body)).toHaveLength(2);
  });

  it("puts a steer_ack's own words in a bubble of their own (ADDENDUM 5)", () => {
    // The ack is the model's content at the position it was stripped from, so it renders as
    // the model's rather than inside the reader's note — which ends the run.
    const r = render((t) => [
      text(t, 1, "one"),
      sealed(t, 2, "steer_ack", { steer_id: "steer-1", text: "tabs it is" }, { id: "steer-1:ack" }),
      text(t, 3, "two"),
    ]);
    expect(shape(r.body)).toEqual(["text(one)", "text(tabs it is)", "text(two)"]);
    expect(rows(r.body)).toHaveLength(3);
  });
});

describe("a residency window may not cut a prose run", () => {
  /** A run of `k` text entries with a rendering entry on each side, so every edge inside the
   *  run is strictly inside it. The run spans `[2, k + 2)`. */
  function withRun(k: number): (t: string) => Entry[] {
    return (t) => [
      callEntry(t, 1, "tool-before"),
      ...Array.from({ length: k }, (_, i) => text(t, 2 + i, `p${String(i)} `)),
      callEntry(t, 2 + k, "tool-after"),
    ];
  }

  /** EVERY window the property is about: both edges inside the turn's span, overlapping the
   *  run. Enumerated rather than sampled — the space is 125 windows over these five k, 95 of
   *  them with an edge STRICTLY inside the run, so a table covers it whole where a sample
   *  reaches part of it and says nothing about the rest. */
  function edgeWindows(k: number): { readonly from: number; readonly to: number }[] {
    const span = k + 3;
    const out: { from: number; to: number }[] = [];
    for (let from = 0; from < span; from += 1) {
      for (let to = from + 1; to <= span; to += 1) {
        if (from < k + 2 && to > 2) {
          out.push({ from, to });
        }
      }
    }
    return out;
  }

  it("snaps every window that overlaps the run out to the run's bounds, and mounts ONE row", () => {
    const runFrom = 2;
    for (let k = 2; k <= 6; k += 1) {
      const runTo = k + 2;
      const members = Array.from({ length: k }, (_, i) => 2 + i);
      for (const range of edgeWindows(k)) {
        const at = `k=${String(k)} from=${String(range.from)} to=${String(range.to)}`;
        const turnID = nextTurnID();
        const turn = turnOf(turnID, retarget(withRun(k)(turnID), turnID));
        const snapped = sliceTurn(turn, range);
        expect(snapped.from, at).toBeLessThanOrEqual(runFrom);
        expect(snapped.to, at).toBeGreaterThanOrEqual(runTo);

        const body = bodyHost();
        buildAssistantBody(body, turn, CHAT_ID, false, snapped);
        const mounted = rows(body);
        expect(mounted, at).toHaveLength(1);
        expect(mounted[0]?.dataset["entries"], at).toBe(
          members.map((seq) => `${turnID}-e${String(seq)}`).join(" "),
        );
        resetBlockRenders();
      }
    }
  });

  it("leaves a window whose edges already fall on a run boundary alone", () => {
    const turnID = nextTurnID();
    const turn = turnOf(turnID, retarget(withRun(3)(turnID), turnID));
    expect(sliceTurn(turn, { from: 2, to: 5 })).toEqual({ from: 2, to: 5 });
    expect(sliceTurn(turn, { from: 1, to: 2 })).toEqual({ from: 1, to: 2 });
  });
});
