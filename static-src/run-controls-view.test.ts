import { describe, it, expect, vi, beforeEach } from "vitest";
import type { RunControlsResponse } from "./wire/types.gen.js";
import type * as Confirm from "./confirm.js";
import type * as RunStore from "./run-store.js";
import type * as RunActions from "./actions/runs.js";
import type { ExecNode } from "./exec-view/model.js";

const m = vi.hoisted(() => ({
  answer: undefined as RunControlsResponse | undefined,
  dispatched: [] as string[],
  confirmed: true,
  asked: [] as string[],
}));

vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Confirm>()),
  confirm: vi.fn((message: string) => {
    m.asked.push(message);
    return Promise.resolve(m.confirmed);
  }),
}));

vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  runControls: vi.fn(() => m.answer),
}));

vi.mock("./actions/runs.js", async (importOriginal) => {
  const stub = (verb: string) => ({
    name: `test.${verb}`,
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
    endRunStep: stub("end_step"),
  };
});

const { buildRunControlsView, buildStepEndView } = await import("./run-controls-view.js");
const { runControls } = await import("./run-store.js");

beforeEach(() => {
  m.answer = undefined;
  m.dispatched.length = 0;
  m.asked.length = 0;
  m.confirmed = true;
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
    m.answer = { verbs };
    const row = buildRunControlsView("wf").current();
    expect(names(row)).toEqual(labels);
    for (const btn of row?.querySelectorAll("button") ?? []) {
      // Icon-only at every width: the name is the label and the tooltip, never a word on screen.
      expect(btn.textContent).toBe("");
      expect(btn.querySelector("svg")).not.toBeNull();
      expect(btn.getAttribute("data-tooltip")).not.toBe("");
    }
  });

  it("dispatches Pause, Resume and Cancel for the run", () => {
    m.answer = { verbs: ["pause", "resume", "cancel"] };
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
    m.answer = { verbs: ["extend"], pause_node_id: "loop-1" };
    const row = buildRunControlsView("wf").current();
    document.body.append(row!);
    row!.querySelector<HTMLInputElement>(".run-extend-count")!.value = "1001";
    row!.querySelector("button")?.click();
    expect(m.dispatched).toEqual([]);
    row!.remove();
  });

  it("draws no repeat verb when the server named no paused node", () => {
    m.answer = { verbs: ["extend", "finish_loop", "cancel"] };
    expect(names(buildRunControlsView("wf").current())).toEqual(["Cancel"]);
  });

  it("hands back the same row while the answer holds, and a new one once it moves", () => {
    const view = buildRunControlsView("wf");
    m.answer = { verbs: ["pause", "cancel"] };
    const first = view.current();
    m.answer = { verbs: ["pause", "cancel"] };
    expect(view.current()).toBe(first);
    m.answer = { verbs: ["resume", "cancel"] };
    expect(view.current()).not.toBe(first);
  });

  it("shows a verb in flight on its button until its row is replaced or disposed", async () => {
    const { defineAction } = await import("@cplieger/ui/actions");
    const inFlight = defineAction<undefined, undefined>({
      name: "test.pause",
      run: (_args, signal) =>
        new Promise((_resolve, reject) => {
          signal.addEventListener("abort", () => {
            reject(signal.reason as Error);
          });
        }),
    });
    const pauseButton = (row: HTMLElement | null): HTMLButtonElement | null =>
      row?.querySelector<HTMLButtonElement>('button[aria-label="Pause"]') ?? null;
    const view = buildRunControlsView("wf");
    m.answer = { verbs: ["pause", "cancel"] };
    const replaced = pauseButton(view.current());
    void inFlight.dispatch(undefined);
    await Promise.resolve();
    expect(replaced?.getAttribute("aria-busy")).toBe("true");
    expect(replaced?.disabled).toBe(true);

    m.answer = { verbs: ["pause"] };
    const disposed = pauseButton(view.current());
    expect(replaced?.getAttribute("aria-busy")).toBeNull();
    expect(replaced?.disabled).toBe(false);
    expect(disposed?.getAttribute("aria-busy")).toBe("true");

    view.dispose();
    expect(disposed?.getAttribute("aria-busy")).toBeNull();
    expect(disposed?.disabled).toBe(false);
    inFlight.cancel();
  });

  it("draws nothing before the answer, and refusals only with no verb", () => {
    const view = buildRunControlsView("wf");
    expect(view.current()).toBeNull();
    m.answer = { verbs: ["cancel"], refused: { pause: "driven by an agent" } };
    expect(view.refusals()).toEqual([]);
    m.answer = { verbs: [], refused: { pause: "no live engine" } };
    expect(view.current()).toBeNull();
    expect(view.refusals()).toEqual(["no live engine"]);
  });
});

describe("a paused step's end buttons", () => {
  const paused = (path = "root:review", endable = true): ExecNode => ({
    path,
    label: "review",
    kind: "step",
    state: "waiting",
    children: [],
    ...(endable ? { endable: true as const } : {}),
  });
  const settle = () => new Promise((r) => setTimeout(r, 0));

  it("draws nothing for a step the server would not end", () => {
    expect(buildStepEndView("wf").current(paused("root:review", false))).toBeNull();
  });

  it("draws Mark step done and Mark step failed, icon-only", () => {
    const row = buildStepEndView("wf").current(paused());
    expect(names(row)).toEqual(["Mark step done", "Mark step failed"]);
    for (const btn of row?.querySelectorAll("button") ?? []) {
      expect(btn.textContent).toBe("");
      expect(btn.querySelector("svg")).not.toBeNull();
    }
  });

  it("ends the step with each status only once the person confirms", async () => {
    const row = buildStepEndView("wf").current(paused());
    const [done, failed] = [...(row?.querySelectorAll("button") ?? [])];
    done?.click();
    failed?.click();
    await settle();
    expect(m.asked).toEqual([
      "Mark review done? Its session closes, and the run goes on as if the step had finished.",
      "Mark review failed? Its session closes, and the run handles it as a failed step.",
    ]);
    expect(m.dispatched).toEqual([
      'end_step:{"workflowID":"wf","nodePath":"root:review","status":"completed"}',
      'end_step:{"workflowID":"wf","nodePath":"root:review","status":"failed"}',
    ]);
  });

  it("sends nothing when the person cancels", async () => {
    m.confirmed = false;
    buildStepEndView("wf").current(paused())?.querySelector("button")?.click();
    await settle();
    expect(m.asked).toHaveLength(1);
    expect(m.dispatched).toEqual([]);
  });

  it("puts the end KAS already recorded first, and redraws when that record moves", () => {
    const view = buildStepEndView("wf");
    const withEnd = (status: "completed" | "failed"): ExecNode => ({
      ...paused(),
      pendingEnd: { status, text: "applies later" },
    });
    expect(names(view.current(withEnd("failed")))).toEqual(["Mark step failed", "Mark step done"]);
    const done = view.current(withEnd("completed"));
    expect(names(done)).toEqual(["Mark step done", "Mark step failed"]);
    expect(view.current(paused())).toBe(done);
  });

  it("hands back the same buttons while the shown step holds, and new ones for another", () => {
    const view = buildStepEndView("wf");
    const first = view.current(paused());
    expect(view.current(paused())).toBe(first);
    expect(view.current(paused("root:other"))).not.toBe(first);
    expect(view.current(paused("root:other", false))).toBeNull();
  });
});
