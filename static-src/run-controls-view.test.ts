import { describe, it, expect, vi, beforeEach } from "vitest";
import type { RunControlsResponse } from "./wire/types.gen.js";
import type * as RunStore from "./run-store.js";
import type * as RunActions from "./actions/runs.js";

const m = vi.hoisted(() => ({
  answer: undefined as RunControlsResponse | undefined,
  dispatched: [] as string[],
}));

vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  runControls: vi.fn(() => m.answer),
}));

vi.mock("./actions/runs.js", async (importOriginal) => {
  const stub = (verb: string) => ({
    name: `runs.${verb}`,
    dispatch: vi.fn((arg: unknown) => {
      m.dispatched.push(`${verb}:${typeof arg === "string" ? arg : JSON.stringify(arg)}`);
      return Promise.resolve();
    }),
  });
  return {
    ...(await importOriginal<typeof RunActions>()),
    pauseRun: stub("pause"),
    resumeRun: stub("resume"),
    cancelRun: stub("cancel"),
    retryRun: stub("retry"),
    extendRunRepeat: stub("extend"),
    finishRunRepeat: stub("finish_loop"),
  };
});

const { buildRunControlsView } = await import("./run-controls-view.js");
const { runControls } = await import("./run-store.js");

beforeEach(() => {
  m.answer = undefined;
  m.dispatched.length = 0;
  vi.mocked(runControls).mockImplementation(() => m.answer);
});

const names = (row: HTMLElement | null): string[] =>
  [...(row?.querySelectorAll("button") ?? [])].map((b) => b.getAttribute("aria-label") ?? "");

describe("the run's controls row", () => {
  it.each([
    [
      ["pause", "cancel"],
      ["Pause", "Cancel"],
    ],
    [
      ["resume", "cancel"],
      ["Resume", "Cancel"],
    ],
    [["retry"], ["Retry failed steps"]],
    [
      ["cancel", "pause"],
      ["Cancel", "Pause"],
    ],
  ])("draws %j in the server's order, each button named", (verbs, labels) => {
    m.answer = { verbs, parent_chat_id: "" };
    const row = buildRunControlsView("wf").current();
    expect(names(row)).toEqual(labels);
    for (const btn of row?.querySelectorAll("button") ?? []) {
      expect(btn.querySelector(".run-control-label")?.textContent).toBe(
        btn.getAttribute("aria-label"),
      );
      expect(btn.getAttribute("data-tooltip")).not.toBe("");
    }
  });

  it("dispatches Pause, Resume and Cancel for the run", () => {
    m.answer = { verbs: ["pause", "resume", "cancel"], parent_chat_id: "" };
    const row = buildRunControlsView("wf").current();
    for (const btn of row?.querySelectorAll("button") ?? []) {
      btn.click();
    }
    expect(m.dispatched).toEqual(["pause:wf", "resume:wf", "cancel:wf"]);
  });

  it("gives a loop at its limit a count field, Add passes and End this loop", () => {
    m.answer = {
      verbs: ["extend", "finish_loop", "cancel"],
      pause_node_id: "loop-1",
      parent_chat_id: "",
    };
    const row = buildRunControlsView("wf").current();
    expect(names(row)).toEqual(["Add passes", "End this loop", "Cancel"]);
    const count = row?.querySelector<HTMLInputElement>(".run-extend-count");
    expect(count?.getAttribute("aria-label")).toBe("Passes to add");
    count!.value = "7";
    for (const btn of row?.querySelectorAll("button") ?? []) {
      btn.click();
    }
    expect(m.dispatched).toEqual([
      'extend:{"workflowID":"wf","nodeID":"loop-1","iterations":7}',
      'finish_loop:{"workflowID":"wf","nodeID":"loop-1"}',
      "cancel:wf",
    ]);
  });

  it("refuses an out-of-range count without sending", () => {
    m.answer = { verbs: ["extend"], pause_node_id: "loop-1", parent_chat_id: "" };
    const row = buildRunControlsView("wf").current();
    document.body.append(row!);
    row!.querySelector<HTMLInputElement>(".run-extend-count")!.value = "1001";
    row!.querySelector("button")?.click();
    expect(m.dispatched).toEqual([]);
    row!.remove();
  });

  it("draws no repeat verb when the server named no paused node", () => {
    m.answer = { verbs: ["extend", "finish_loop", "cancel"], parent_chat_id: "" };
    expect(names(buildRunControlsView("wf").current())).toEqual(["Cancel"]);
  });

  it("hands back the same row while the answer holds, and a new one once it moves", () => {
    const view = buildRunControlsView("wf");
    m.answer = { verbs: ["pause", "cancel"], parent_chat_id: "" };
    const first = view.current();
    m.answer = { verbs: ["pause", "cancel"], parent_chat_id: "c-other" };
    expect(view.current()).toBe(first);
    m.answer = { verbs: ["resume", "cancel"], parent_chat_id: "" };
    expect(view.current()).not.toBe(first);
  });

  it("draws nothing before the answer, and refusals only with no verb", () => {
    const view = buildRunControlsView("wf");
    expect(view.current()).toBeNull();
    m.answer = { verbs: ["cancel"], refused: { pause: "driven by an agent" }, parent_chat_id: "" };
    expect(view.refusals()).toEqual([]);
    m.answer = { verbs: [], refused: { pause: "no live engine" }, parent_chat_id: "" };
    expect(view.current()).toBeNull();
    expect(view.refusals()).toEqual(["no live engine"]);
  });
});
