// The run frames against the real live-runs inventory: whatever order a run's end and an inventory read
// arrive in, the inventory ends where the server's is, and the readers that gate on it (the orphan ask
// sweep) see that.

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { LiveRunsResponse } from "../wire/types.gen.js";

// The inventory read is held open by the test, so a frame can land while it is in flight.
const live = vi.hoisted(() => ({
  pending: [] as ((reply: LiveRunsResponse | null) => void)[],
}));
vi.mock("../api-client.js", () => ({
  apiGetTyped: vi.fn(
    (path: string) =>
      new Promise((resolve) => {
        if (path !== "/api/runs/live") {
          resolve(null);
          return;
        }
        live.pending.push(resolve);
      }),
  ),
  // A run read the server can describe no further, which skips the retry ladder.
  apiGetOrError: vi.fn(async () => ({ ok: false, status: 404, data: null, error: "" })),
}));
vi.mock("../store.js", () => ({ isBusy: vi.fn(() => false) }));
vi.mock("../run-dots.js", () => ({ trackRun: vi.fn() }));
vi.mock("../toast.js", () => ({ info: vi.fn(), jumpToast: vi.fn() }));
vi.mock("../notice-subject.js", () => ({ runEndJump: vi.fn(() => undefined) }));
vi.mock("../decision-dock.js", () => ({
  dropRunRequests: vi.fn(),
  dropTurnDecisions: vi.fn(),
}));
vi.mock("../notify.js", () => ({
  notifyOffScreen: vi.fn(),
  closeRunAsksExcept: vi.fn(() => Promise.resolve()),
}));

import "./run.js";
import { dispatch } from "../bus.js";
import { hasLiveRunForChat, liveRunIDsForChat, rebuildLiveRuns } from "../run-store.js";
import { dropTurnDecisions } from "../decision-dock.js";

const sweepOrphans = vi.mocked(dropTurnDecisions);

// The server's `runs` counter: one value per inventory change, increasing across the whole file because
// the inventory is module state that outlives a case.
let runsVersion = 0;
function nextVersion(): string {
  runsVersion++;
  return String(runsVersion);
}

function send(type: "run_started" | "run_finished", payload: Record<string, unknown>): void {
  dispatch({ type, chat_id: "c1", payload });
}

function start(id: string): void {
  nextVersion();
  send("run_started", { workflow_id: id, name: id });
}

function finish(id: string): void {
  nextVersion();
  send("run_finished", { workflow_id: id, status: "completed", name: id });
}

/** Answer the oldest open inventory read with these runs of chat c1, read at `version`. */
function answerRead(ids: readonly string[], version: string): void {
  const resolve = live.pending.shift();
  if (resolve === undefined) {
    throw new Error("no inventory read is open");
  }
  resolve({
    runs: ids.map((id) => ({ workflow_id: id, chat_id: "c1", executing: true })),
    subject: { kind: "runs", ref: "", version },
  });
}

beforeEach(async () => {
  live.pending.length = 0;
  // The server's inventory is empty between cases.
  const read = rebuildLiveRuns();
  answerRead([], nextVersion());
  await read;
  sweepOrphans.mockClear();
});

describe("a run's end and an inventory read in flight", () => {
  it("a run that finished while the live-runs read was in flight stays settled", async () => {
    start("wf_a");
    expect(hasLiveRunForChat("c1"), "the start put the run in the inventory").toBe(true);

    const read = rebuildLiveRuns();
    // The server answered the read before the run ended, so the answer still lists it.
    const readAt = String(runsVersion);
    finish("wf_a");
    answerRead(["wf_a"], readAt);
    await read;

    expect(
      hasLiveRunForChat("c1"),
      "the server released the run; a read answered before that must not revive it",
    ).toBe(false);
    expect(liveRunIDsForChat("c1")).toEqual([]);
  });

  it("a sibling run's end sweeps the chat's orphan asks after a late read", async () => {
    start("wf_a");
    start("wf_b");
    const read = rebuildLiveRuns();
    const readAt = String(runsVersion);
    finish("wf_a");
    answerRead(["wf_a", "wf_b"], readAt);
    await read;
    expect(sweepOrphans, "wf_b is still running when wf_a ends").not.toHaveBeenCalled();

    finish("wf_b");

    expect(sweepOrphans, "no run of the chat is live once wf_b ends").toHaveBeenCalledWith("c1");
  });
});
