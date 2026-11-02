import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { makeEntry, makeTurn } from "./__test-helpers__/model.js";
import { markWakingRows, wakeLine, wakeText } from "./turn-wake.js";
import { projectTurn, projectTurns, type TurnSource } from "./turns.js";
import type { TurnState } from "./types.js";
import { isWakingRow } from "./waking-rows.js";
import type { Entry, EntryTurnWake } from "./wire/types.gen.js";

const FIXTURE_PATH = "../internal/chat/testdata/wake_line.json";

interface WakeCase {
  name: string;
  wake: EntryTurnWake;
  label: string;
  body: string;
  run?: string;
  rest?: string;
  rows?: { run: string; rest: string; text: string }[];
}

function loadCases(): WakeCase[] {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return (JSON.parse(raw) as { cases: WakeCase[] }).cases;
}

describe("the wake wording shared with the Go implementation", () => {
  const cases = loadCases();

  it("reads every reason of the shared fixture", () => {
    expect(new Set(cases.map((c) => c.wake.reason))).toEqual(
      new Set(["step_message", "run_finished", "rewake", "unknown"]),
    );
  });

  for (const c of cases) {
    it(c.name, () => {
      const line = wakeLine(c.wake);
      expect(line.label).toBe(c.label);
      expect(line.body).toBe(c.body);
      expect(line.fallback).toBe(c.label === "Agent-initiated turn");
      if (c.run !== undefined) {
        expect(line.run?.id).toBe(c.run);
      }
      if (c.rest !== undefined) {
        expect(line.rest).toBe(c.rest);
      }
      if (c.rows !== undefined) {
        expect(line.rows?.map((r) => ({ run: r.run.id, rest: r.rest, text: r.text }))).toEqual(
          c.rows,
        );
      }
    });
  }
});

const RUN = "wf_5e1f0c2a9b7d4e36";

const ASK: EntryTurnWake = {
  reason: "step_message",
  steers: [
    {
      id: "wfmsg-3f2a",
      run: RUN,
      step: "build/check-links",
      severity: "error",
      text: "The site build failed.\ndocs/guide/install.md has no front matter.",
    },
  ],
};

function wakeEntry(w: EntryTurnWake, lane?: string): Partial<Entry> {
  return { kind: "turn_wake", payload: w, id: "wake-1", ...(lane === undefined ? {} : { lane }) };
}

describe("wakeLine's run names", () => {
  it("name the label's run from the wake's run_name", () => {
    const named: EntryTurnWake = {
      ...ASK,
      steers: (ASK.steers ?? []).map((s) => ({ ...s, run_name: "publish-docs" })),
    };
    expect(wakeLine(named).run).toEqual({ id: RUN, name: "publish-docs", named: true });
    expect(wakeLine(named).label).toBe("publish-docs \u00b7 build/check-links reports an error");
  });

  it("call a run with no name yet a workflow, never its id", () => {
    expect(wakeLine(ASK).run).toEqual({ id: RUN, name: "A workflow", named: false });
  });

  it("name every re-delivered row's run, and no run on the label", () => {
    const line = wakeLine({
      reason: "rewake",
      steers: [
        { id: "a", run: RUN, run_name: "publish-docs", step: "draft", text: "Which branch?" },
        { id: "b", run: "wf_other", step: "collect", text: "Leg 1 is done." },
      ],
    });
    expect(line.run).toBeUndefined();
    expect(line.rows?.map((r) => r.run.name)).toEqual(["publish-docs", "A workflow"]);
    expect(line.body).toBe(
      "publish-docs \u00b7 draft: Which branch?\nA workflow \u00b7 collect: Leg 1 is done.",
    );
  });
});

function heldTurn(id: string, open: Record<string, unknown>, ...rest: Partial<Entry>[]): TurnState {
  return state([
    makeEntry({ id: `${id}-open`, turn: id, kind: "turn_open", payload: open, seq: 0 }),
    ...rest.map((e, i) => makeEntry({ id: `${id}-e${String(i)}`, turn: id, seq: i + 1, ...e })),
  ]);
}

function source(...turns: [string, TurnState][]): TurnSource {
  return { turn_order: turns.map(([id]) => id), turns: new Map(turns) };
}

describe("a projected turn's wake", () => {
  const say: Partial<Entry> = { kind: "text", payload: { text: "On it." } };

  it("is its turn_wake, named by a run_named a later turn holds", () => {
    const src = source(
      ["t-1", heldTurn("t-1", { n: 1, source: "wire_turn_start" }, wakeEntry(ASK), say)],
      [
        "t-2",
        heldTurn(
          "t-2",
          { n: 2, source: "prompt", prompt: { id: "p", text: "thanks" } },
          { kind: "run_named", payload: { run: RUN, name: "publish-docs" } },
        ),
      ],
    );
    const [woken, prompted] = projectTurns(src);
    expect(woken?.wake?.steers?.[0]?.run_name).toBe("publish-docs");
    expect(prompted?.wake).toBeUndefined();
    expect(projectTurn(src, "t-1")?.wake?.steers?.[0]?.run_name).toBe("publish-docs");
    if (woken !== undefined) {
      expect(wakeText(woken)).toBe(
        "publish-docs \u00b7 build/check-links reports an error\n" +
          "The site build failed.\ndocs/guide/install.md has no front matter.",
      );
    }
  });

  it("keeps the name the wake recorded", () => {
    const recorded: EntryTurnWake = {
      reason: "run_finished",
      run: RUN,
      run_name: "nightly-report",
      run_status: "completed",
    };
    const src = source([
      "t-1",
      heldTurn("t-1", { n: 1, source: "wire_turn_start" }, wakeEntry(recorded), say, {
        kind: "run_named",
        payload: { run: RUN, name: "other" },
      }),
    ]);
    expect(projectTurns(src)[0]?.wake?.run_name).toBe("nightly-report");
  });

  it("is not a delegate lane's turn_wake", () => {
    const src = source([
      "t-1",
      heldTurn("t-1", { n: 1, source: "wire_turn_start" }, wakeEntry(ASK, "sub-a"), say),
    ]);
    expect(projectTurns(src)[0]?.wake).toBeUndefined();
  });

  it("says nothing for a wake that names nothing", () => {
    expect(wakeText(makeTurn({ wake: { reason: "unknown" } }))).toBe("");
    expect(wakeText(makeTurn())).toBe("");
  });
});

function state(entries: Entry[]): TurnState {
  return { entries, openEntries: new Map() };
}

function steerRow(id: string): Entry {
  return makeEntry({
    id,
    turn: "t-1",
    kind: "steer",
    payload: { text: "x", origin: "step" },
    seq: 1,
  });
}

function wakeTurn(w: EntryTurnWake): TurnState {
  return state([
    makeEntry({ id: "t-2-open", turn: "t-2", kind: "turn_open", payload: {}, seq: 0 }),
    makeEntry({ id: "wake-1", turn: "t-2", kind: "turn_wake", payload: w, seq: 1 }),
  ]);
}

describe("markWakingRows", () => {
  it("marks the row a step-message wake names, and only that row", () => {
    const waker = steerRow(ASK.steers?.[0]?.id ?? "");
    const other = steerRow("wfmsg-other");
    markWakingRows([state([waker, other]), wakeTurn(ASK)]);
    expect(isWakingRow(waker)).toBe(true);
    expect(isWakingRow(other)).toBe(false);
  });

  it("marks a re-delivery's rows, which the wake draws once in its header", () => {
    const row = steerRow("wfmsg-q");
    markWakingRows([
      state([row]),
      wakeTurn({ reason: "rewake", steers: [{ id: "wfmsg-q", text: "Which branch?" }] }),
    ]);
    expect(isWakingRow(row)).toBe(true);
  });

  it("leaves a finished run's wake no row to mark", () => {
    const row = steerRow("wfmsg-q");
    markWakingRows([
      state([row]),
      wakeTurn({
        reason: "run_finished",
        run: RUN,
        run_status: "completed",
        steers: [{ id: "wfmsg-q", text: "x" }],
      }),
    ]);
    expect(isWakingRow(row)).toBe(false);
  });

  it("unmarks the row once its wake is no longer held", () => {
    const waker = steerRow(ASK.steers?.[0]?.id ?? "");
    markWakingRows([state([waker]), wakeTurn(ASK)]);
    markWakingRows([state([waker])]);
    expect(isWakingRow(waker)).toBe(false);
  });
});
