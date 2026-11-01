// The client half of the wake wording oracle (internal/chat/testdata/wake_line.json; the server half
// is TestWakeLineContract_ServerHalf), plus the projection reads and the waking-row marks.

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { makeEntry, makeTurn } from "./__test-helpers__/model.js";
import { isWakingRow, markWakingRows, turnWakeOf, wakeLine, wakeText } from "./turn-wake.js";
import type { TurnState } from "./types.js";
import type { Entry, EntryTurnWake } from "./wire/types.gen.js";

const FIXTURE_PATH = "../internal/chat/testdata/wake_line.json";

interface WakeCase {
  name: string;
  wake: EntryTurnWake;
  label: string;
  body: string;
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
    });
  }
});

const ASK: EntryTurnWake = {
  reason: "step_message",
  steers: [
    {
      id: "wfmsg-01a14782-572f-722a-b51c-a6ce3e86aaba",
      run: "wf_1729f9b5688ec7bf",
      run_label: "release-impact-leg2",
      step: "ratify",
      severity: "error",
      text: "Handoff blocked: update_workflow is not in my toolset.\nPlease give this step the tool.",
    },
  ],
};

describe("wakeLine's run", () => {
  it("names the run the label opens with, the rest following its name", () => {
    const line = wakeLine(ASK);
    expect(line.run).toEqual({ id: "wf_1729f9b5688ec7bf", name: "release-impact-leg2" });
    expect(line.rest).toBe(" \u00b7 ratify reports an error");
  });

  it("names no run on a re-delivery, whose rows may come from several", () => {
    const line = wakeLine({ reason: "rewake", steers: ASK.steers ?? [] });
    expect(line.run).toBeUndefined();
    expect(line.rest).toBe(line.label);
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
      "release-impact-leg2 \u00b7 ratify reports an error\n" +
        "Handoff blocked: update_workflow is not in my toolset.\nPlease give this step the tool.",
    );
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

  it("leaves a re-delivery's rows their own cards", () => {
    const row = steerRow("wfmsg-q");
    markWakingRows([
      state([row]),
      wakeTurn({ reason: "rewake", steers: [{ id: "wfmsg-q", text: "Which branch?" }] }),
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
