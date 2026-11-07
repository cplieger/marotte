// The client half of the tool-run boundary oracle (internal/chat/testdata/tool_runs.json; the server half is
// TestToolRunsContract_ServerHalf, through toolCardRuns). Read off the groups the production dispatcher mounts, in
// the transcript, on each delegate's page and in a workflow run's step pane, so a boundary indexGroups draws and find's
// filter does not fails here.

import { describe, it, expect, afterEach } from "vitest";
import fixtureRaw from "../internal/chat/testdata/tool_runs.json?raw";
import type { Turn } from "./turns.js";
import type { TurnState } from "./types.js";
import type { Entry, EntryKind } from "./wire/types.gen.js";

for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  if (document.getElementById(id) === null) {
    const d = document.createElement("div");
    d.id = id;
    document.body.appendChild(d);
  }
}

const { buildAssistantBody, buildDetachedBody, resetBlockRenders, initBlockRenderer } =
  await import("./messages-blocks.js");
const { markWakingRows } = await import("./turn-wake.js");
const { setActive } = await import("./store.js");
const { createRunStepStream } = await import("./run-chat-steps.js");

const CHAT_ID = "c-tool-runs";
setActive(CHAT_ID);

initBlockRenderer({
  pushEntryEffect: (): void => {
    /* noop */
  },
  disposeEntryEffects: (): void => {
    /* noop */
  },
  liveAnchorChanged: () => undefined,
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

interface RunsCase {
  name: string;
  body: { id: string; kind: EntryKind; lane?: string; payload: unknown }[];
  later_wake?: unknown;
  runs: string[][];
  lane_runs?: Record<string, string[][]>;
}

function loadCases(): RunsCase[] {
  return (JSON.parse(fixtureRaw) as { cases: RunsCase[] }).cases;
}

let turnSeq = 0;
let hosts: HTMLElement[] = [];

afterEach(() => {
  resetBlockRenders();
  for (const host of hosts) {
    host.remove();
  }
  hosts = [];
});

/** Each group shell's cards under `host`, by entry id, in document order. */
function groupsIn(host: HTMLElement): string[][] {
  return [...host.querySelectorAll<HTMLElement>(".tool-group")].map((g) =>
    [...g.querySelectorAll<HTMLElement>(":scope > .tool-group-body > .tool-call")].map(
      (card) => card.dataset["entryId"] ?? "",
    ),
  );
}

function mountHost(): HTMLElement {
  const host = document.createElement("div");
  host.className = "turn-body";
  document.body.appendChild(host);
  hosts.push(host);
  return host;
}

/** The case as one held turn under a fresh id, its wakes marked, as the store hands it to a render. */
function caseTurn(c: RunsCase): Turn {
  turnSeq += 1;
  const turnID = `t-runs-${String(turnSeq)}`;
  const open: Entry = {
    id: `${turnID}-open`,
    turn: turnID,
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: {},
  };
  const body: Entry[] = c.body.map((e, i) => ({
    id: e.id,
    turn: turnID,
    lane: e.lane ?? "",
    kind: e.kind,
    seq: i + 1,
    ts: i + 2,
    payload: e.payload,
  }));
  const held: TurnState[] = [{ entries: [open, ...body], openEntries: new Map() }];
  if (c.later_wake !== undefined) {
    const later = `${turnID}-later`;
    held.push({
      entries: [
        { id: `${later}-open`, turn: later, kind: "turn_open", seq: 0, ts: 1, payload: {} },
        {
          id: `${later}-wake`,
          turn: later,
          kind: "turn_wake",
          seq: 1,
          ts: 2,
          payload: c.later_wake,
        },
      ],
      openEntries: new Map(),
    });
  }
  markWakingRows(held);
  return {
    id: turnID,
    n: 1,
    trigger: { id: `${turnID}-p`, text: "go" },
    body,
    openEntries: new Map(),
    ts: 1,
    outcome: "completed",
    rewindTo: undefined,
    branchFrom: undefined,
  };
}

/**
 * The tool-card runs as mounted, keyed by lane: the chat lane's from the transcript (`""`), and each delegate lane
 * holding a tool call from that delegate's own page.
 */
function renderedRuns(c: RunsCase): Record<string, string[][]> {
  const turn = caseTurn(c);
  const body = turn.body;
  const chat = mountHost();
  buildAssistantBody(chat, turn, CHAT_ID, false);
  const out: Record<string, string[][]> = { "": groupsIn(chat) };
  const delegates = new Set(
    body.filter((e) => e.kind === "tool_call" && e.lane !== "").map((e) => e.lane ?? ""),
  );
  for (const lane of delegates) {
    const page = mountHost();
    buildDetachedBody(page, turn, CHAT_ID, lane, false);
    out[lane] = groupsIn(page);
  }
  return out;
}

/** The tool-card runs a workflow run's tab mounts for the case as one step turn, through the pane's own stream. */
function runPaneRuns(c: RunsCase): string[][] {
  const turn = caseTurn(c);
  const pane = mountHost();
  const stream = createRunStepStream(() => pane);
  stream.apply(new Map([["step", [{ turn, live: false }]]]));
  const runs = groupsIn(pane);
  stream.dispose();
  return runs;
}

describe("tool-card run boundaries shared with the Go implementation", () => {
  const cases = loadCases();

  it("reads every case of the shared fixture", () => {
    expect(cases.length).toBeGreaterThanOrEqual(15);
  });

  for (const c of cases) {
    it(c.name, () => {
      expect(renderedRuns(c)).toEqual({ "": c.runs, ...c.lane_runs });
    });
  }
});

describe("a workflow run's tab draws the same tool-card runs as the chat", () => {
  for (const c of loadCases()) {
    it(c.name, () => {
      expect(runPaneRuns(c)).toEqual(c.runs);
    });
  }
});
