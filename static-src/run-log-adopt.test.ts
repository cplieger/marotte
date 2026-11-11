// A read's answer lands in the run's log as ONE write: whatever the answer holds, a reader of the
// log is told once, and every per-entry effect of the log still runs.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as ApiClientModule from "./api-client.js";
import { effect } from "@cplieger/ui";
import type { Entry } from "./types.js";
import type { RunStepTranscript } from "./wire/types.gen.js";

const apiGetTypedOrError = vi.fn<(url: string, decode: unknown) => Promise<unknown>>();
const apiGetTyped =
  vi.fn<(url: string, decode: unknown, signal?: AbortSignal) => Promise<unknown>>();

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClientModule>()),
  apiGetTypedOrError: (url: string, decode: unknown) => apiGetTypedOrError(url, decode),
  apiGetTyped: (url: string, decode: unknown, signal?: AbortSignal) =>
    apiGetTyped(url, decode, signal),
  apiGet: vi.fn(),
  apiGetOrError: vi.fn(),
  apiPost: vi.fn(),
}));

const { clearStepTranscripts, requestStepTranscript } = await import("./run-step-transcript.js");
const { requestRunTurnRange } = await import("./run-turn-range.js");
const { registerRunLogObserver, runStepTurns } = await import("./run-store.js");
const { beginStepMessage, forgetRunStepSteers, retireStepSteers, stepSteers } =
  await import("./run-step-steers.js");

registerRunLogObserver({ steer: retireStepSteers, forget: forgetRunStepSteers });

const PATH = "lanes:lane-a:iter-0:build";

function turnOpen(turn: string): Entry {
  return {
    id: `${turn}-open`,
    turn,
    kind: "turn_open",
    seq: 0,
    ts: 0,
    payload: { source: "workflow_step", node_path: PATH, n: 1 },
  };
}

function text(turn: string, seq: number): Entry {
  return { id: `${turn}-${String(seq)}`, turn, kind: "text", seq, ts: 0, payload: { text: "x" } };
}

/** A whole turn of `count` entries: its `turn_open` then text. */
function wholeTurn(turn: string, count: number): Entry[] {
  return [turnOpen(turn), ...Array.from({ length: count - 1 }, (_, i) => text(turn, i + 1))];
}

function stepAnswer(entries: Entry[]): unknown {
  const data: RunStepTranscript = {
    workflow_id: "wf",
    node_path: PATH,
    state: "ready",
    source: "log",
    entries,
    open_entries: [],
    subject: [],
  };
  return { ok: true, status: 200, data, error: "" };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
  }
}

/** How many times a reader of the step's log has been told it moved since `watch` began. */
function watch(workflowID: string): { readonly passes: () => number; readonly stop: () => void } {
  let runs = 0;
  const stop = effect(() => {
    runStepTurns(workflowID, PATH);
    runs++;
  });
  return { passes: () => runs - 1, stop };
}

const stops: (() => void)[] = [];

beforeEach(() => {
  clearStepTranscripts();
  apiGetTypedOrError.mockReset();
  apiGetTyped.mockReset();
});

afterEach(() => {
  for (const s of stops.splice(0)) {
    s();
  }
});

describe("adopting a step read's answer", () => {
  it("tells the log's reader once for a 2,000-entry answer", async () => {
    const wf = "wf-step";
    const reader = watch(wf);
    stops.push(reader.stop);
    apiGetTypedOrError.mockResolvedValueOnce(stepAnswer(wholeTurn("t1", 2000)));

    requestStepTranscript(wf, PATH);
    await settle();

    expect(runStepTurns(wf, PATH)[0]?.[1].entries).toHaveLength(2000);
    expect(reader.passes()).toBeLessThanOrEqual(1);
  });

  it("still retires the pending steer and settles the message the answer delivers", async () => {
    const wf = "wf-steer";
    beginStepMessage(wf, PATH, "m1", "go on", true);
    expect(stepSteers(wf, PATH).map((r) => r.id)).toEqual(["steer-m1"]);
    const reader = watch(wf);
    stops.push(reader.stop);
    const steer: Entry = {
      id: "steer-m1",
      turn: "t1",
      kind: "steer",
      seq: 2,
      ts: 0,
      payload: { text: "go on", origin: "user" },
    };
    const delivered: Entry = {
      id: "steer-m1:delivered",
      turn: "t1",
      kind: "steer_delivered",
      seq: 3,
      ts: 0,
      payload: { steer_id: "steer-m1", read_ts: 5 },
    };
    apiGetTypedOrError.mockResolvedValueOnce(
      stepAnswer([turnOpen("t1"), text("t1", 1), steer, delivered, text("t1", 4)]),
    );

    requestStepTranscript(wf, PATH);
    await settle();

    expect(stepSteers(wf, PATH)).toEqual([]);
    const held = runStepTurns(wf, PATH)[0]?.[1].entries.find((e) => e.id === "steer-m1");
    expect(held?.payload).toMatchObject({ state: "read", read_ts: 5 });
    expect(reader.passes()).toBeLessThanOrEqual(1);
  });
});

describe("adopting a range read's answer", () => {
  it("tells the log's reader once for a 500-entry answer", async () => {
    const wf = "wf-range";
    const reader = watch(wf);
    stops.push(reader.stop);
    apiGetTyped.mockResolvedValueOnce({ entries: wholeTurn("t1", 500), open_entries: [] });

    requestRunTurnRange(wf, "t1");
    await settle();

    expect(runStepTurns(wf, PATH)[0]?.[1].entries).toHaveLength(500);
    expect(reader.passes()).toBeLessThanOrEqual(1);
  });
});
