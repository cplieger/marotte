// The client half of the wake wording oracle (internal/chat/testdata/wake_line.json; the server half
// is TestWakeLineContract_ServerHalf), plus the projection reads and the waking-row marks.

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { makeEntry, makeTurn } from "./__test-helpers__/model.js";
import { markWakingRows, turnWakeOf, wakeLine, wakeText } from "./turn-wake.js";
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

describe("wakeLine's run names", () => {
  const live = (id: string, recorded: string): string =>
    id === RUN ? "publish-docs" : recorded === "" ? "unnamed" : recorded;

  it("name the label's run as the caller's namer does", () => {
    const line = wakeLine(ASK, live);
    expect(line.run).toEqual({ id: RUN, name: "publish-docs", recorded: "" });
    expect(line.label).toBe("publish-docs \u00b7 build/check-links reports an error");
  });

  it("hand a finished run's recorded name to the namer", () => {
    const line = wakeLine(
      {
        reason: "run_finished",
        run: "wf_other",
        run_label: "nightly-report",
        run_status: "failed",
      },
      live,
    );
    expect(line.run).toEqual({
      id: "wf_other",
      name: "nightly-report",
      recorded: "nightly-report",
    });
  });

  it("name every re-delivered row's run, and no run on the label", () => {
    const line = wakeLine(
      {
        reason: "rewake",
        steers: [
          { id: "a", run: RUN, step: "draft", text: "Which branch?" },
          { id: "b", run: "wf_other", step: "collect", text: "Leg 1 is done." },
        ],
      },
      live,
    );
    expect(line.run).toBeUndefined();
    expect(line.rows?.map((r) => r.run.name)).toEqual(["publish-docs", "unnamed"]);
    expect(line.body).toBe(
      "publish-docs \u00b7 draft: Which branch?\nunnamed \u00b7 collect: Leg 1 is done.",
    );
  });
});

function wakeEntry(w: EntryTurnWake, lane?: string): Partial<Entry> {
  return { kind: "turn_wake", payload: w, id: "wake-1", ...(lane === undefined ? {} : { lane }) };
}

describe("turnWakeOf and wakeText", () => {
  it("read the turn's own wake: label, then the message on the next line", () => {
    const t = makeTurn({ body: [wakeEntry(ASK), { kind: "text", payload: { text: "On it." } }] });
    expect(turnWakeOf(t)).toEqual(ASK);
    expect(wakeText(t)).toBe(
      "wf_5e1f0c2a \u00b7 build/check-links reports an error\n" +
        "The site build failed.\ndocs/guide/install.md has no front matter.",
    );
    expect(wakeText(t, () => "publish-docs")).toMatch(/^publish-docs \u00b7 /);
  });

  it("ignore a delegate lane's entry and a prompt turn", () => {
    expect(turnWakeOf(makeTurn({ body: [wakeEntry(ASK, "sub-a")] }))).toBeUndefined();
    const prompted = makeTurn({ trigger: { id: "p", text: "go" }, body: [wakeEntry(ASK)] });
    expect(turnWakeOf(prompted)).toBeUndefined();
  });

  it("say nothing for a wake that names nothing", () => {
    expect(wakeText(makeTurn({ body: [wakeEntry({ reason: "unknown" })] }))).toBe("");
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
