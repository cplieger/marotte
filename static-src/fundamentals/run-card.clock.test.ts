// A live run card's 1s clock re-reads the foot's elapsed from the last render, never by walking
// the run's nodes again.
import { vi, describe, it, expect } from "vitest";
import type { RunNode, RunState } from "../run-store.js";

// Called through, so a tick that walks the run is countable.
vi.mock("../exec-view/model.js", { spy: true });

const model = await import("../exec-view/model.js");
const { buildRunCard } = await import("./run-card.js");

const STEPS = 1_000;

function bigRun(): RunState {
  const start = "2026-11-10T10:00:00.000Z";
  const children: RunNode[] = Array.from({ length: STEPS }, (_, i) => ({
    nodeId: `s${String(i)}`,
    type: "step",
    status: i === STEPS - 1 ? "running" : "completed",
    startedAt: start,
    ...(i === STEPS - 1 ? {} : { endedAt: "2026-11-10T10:00:10.000Z" }),
  }));
  return {
    workflowId: "wf_1",
    status: "running",
    root: { nodeId: "wf_1", type: "sequence", status: "running", startedAt: start, children },
  };
}

describe("a live 1,000-step run card's clock", () => {
  it("moves the foot's elapsed without walking the run", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    try {
      vi.setSystemTime(new Date("2026-11-10T10:01:00.000Z"));
      const c = buildRunCard("wf_1", "Workflow run", () => {
        /* the footer link is not under test */
      });
      c.render(bigRun());
      const ledger = (): string => c.root.querySelector(".run-ledger")?.textContent ?? "";
      const before = ledger();
      vi.mocked(model.counters).mockClear();
      vi.mocked(model.window).mockClear();

      for (let s = 1; s <= 10; s++) {
        vi.setSystemTime(new Date(Date.parse("2026-11-10T10:01:00.000Z") + s * 1000));
        c.tick();
      }

      expect(ledger()).not.toBe(before);
      expect(ledger()).toMatch(/1m 10s$/);
      expect(vi.mocked(model.counters)).not.toHaveBeenCalled();
      expect(vi.mocked(model.window)).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});
