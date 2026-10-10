import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";
import { buildExecPage, type ExecPageView } from "./page.js";
import type { ExecNode, ExecRun } from "./model.js";
import type { ExecState } from "./status.js";

const T0 = Date.parse("2026-10-07T10:00:00Z");

function step(
  id: string,
  state: ExecState,
  startedMin?: number,
  over: Partial<ExecNode> = {},
): ExecNode {
  return {
    path: `root/${id}`,
    label: id,
    kind: "step",
    state,
    children: [],
    // The source saw these starts in time order.
    ...(startedMin === undefined
      ? {}
      : { start: new Date(T0 + startedMin * 60_000).toISOString(), startGen: startedMin + 1 }),
    ...over,
  };
}

function run(nodes: ExecNode[], live = true): ExecRun {
  return { id: "wf_1", label: "release", state: live ? "running" : "ok", nodes, live };
}

let dock: HTMLElement;
let page: ExecPageView;
let shown: (ExecNode | undefined)[];

beforeEach(() => {
  dock = document.createElement("div");
  shown = [];
  page = buildExecPage({
    emptyNote: () => "",
    onShowNode: (n) => {
      shown.push(n);
    },
    dock,
    follow: "newest",
  });
  document.body.append(page.root, dock);
});

afterEach(() => {
  page.dispose();
  page.root.remove();
  dock.remove();
});

function box(key: string): HTMLDetailsElement {
  const b = dock.querySelector<HTMLDetailsElement>(`details[data-box="${key}"]`);
  if (b === null) {
    throw new Error(`no ${key} box`);
  }
  return b;
}

function selectedPath(): string {
  return shown.at(-1)?.path ?? "";
}

function click(path: string): void {
  dock.querySelector<HTMLElement>(`.ev-row[data-path="${path}"] > .ev-row-main`)?.click();
}

describe("the dock", () => {
  it("holds the timeline and the steps, closed, and no results while the run is live", () => {
    page.render(run([step("plan", "ok", 0, { output: "done" }), step("build", "running", 1)]));
    expect(
      [...dock.children].map((b) => [b.getAttribute("data-box"), (b as HTMLDetailsElement).open]),
    ).toEqual([
      ["timeline", false],
      ["steps", false],
      ["results", false],
    ]);
    // Even on a step that produced results: they join only once the run is done.
    click("root/plan");
    expect(selectedPath()).toBe("root/plan");
    expect(box("results").hidden).toBe(true);
    expect(box("steps").querySelector('.ev-row[data-path="root/build"]')).not.toBeNull();
    expect(page.root.querySelector(".ev-row")).toBeNull();
  });

  it("opens one box at a time", async () => {
    page.render(run([step("plan", "ok", 0), step("build", "running", 1)]));
    box("timeline").querySelector("summary")?.click();
    box("steps").querySelector("summary")?.click();
    await new Promise((r) => setTimeout(r, 0));
    expect([box("timeline").open, box("steps").open]).toEqual([false, true]);
  });

  it("adds the results box once the run is done, its row naming the latest step and its time", () => {
    const end = new Date(T0 + 3 * 60_000).toISOString();
    page.render(run([step("plan", "ok", 0), step("build", "ok", 1, { end })], false));
    const row = box("results");
    expect(row.hidden).toBe(false);
    expect(row.querySelector(".ev-box-latest")?.textContent).toBe("build");
    expect(row.querySelector(".ev-box-dur")?.textContent).toBe("2m 0s");
  });

  it("says so in the results box when the selected step captured nothing", () => {
    page.render(run([step("plan", "ok", 0, { output: "planned" }), step("build", "ok", 1)], false));
    const body = box("results").querySelector<HTMLElement>(".ev-box-body")!;
    const empty = body.querySelector<HTMLElement>(".ev-box-empty")!;
    expect([empty.hidden, empty.textContent]).toEqual([false, "No results from build."]);
    click("root/plan");
    expect(empty.hidden).toBe(true);
    expect(body.querySelector(".ev-r-text")?.textContent).toContain("planned");
  });

  it("gives the detail the page's width and keeps its name row, since the Steps box navigates", () => {
    const style = mountAppCSS();
    page.render(run([step("plan", "ok", 0), step("build", "running", 1)]));
    const panes = page.root.querySelector<HTMLElement>(".ev-panes")!;
    const head = page.root.querySelector<HTMLElement>(".ev-d-head")!;
    expect(getComputedStyle(head).display).not.toBe("none");
    expect(head.querySelector(".ev-d-title")?.textContent).toBe("build");
    expect(getComputedStyle(panes).gridTemplateColumns.split(" ")).toHaveLength(1);
    style.remove();
  });

  it("names the newest started step on each box's row", () => {
    page.render(run([step("plan", "ok", 0), step("build", "running", 1), step("ship", "pending")]));
    expect(box("steps").querySelector(".ev-box-latest")?.textContent).toBe("build");
  });
});

describe("following the newest step", () => {
  it("shows the newest started step and switches when a newer one starts", () => {
    page.render(run([step("plan", "running", 0), step("build", "pending")]));
    expect(selectedPath()).toBe("root/plan");
    page.render(run([step("plan", "ok", 0), step("build", "running", 1)]));
    expect(selectedPath()).toBe("root/build");
  });

  it("holds a pick until a newer step starts", () => {
    page.render(run([step("plan", "ok", 0), step("build", "running", 1), step("ship", "pending")]));
    click("root/plan");
    expect(selectedPath()).toBe("root/plan");
    page.render(run([step("plan", "ok", 0), step("build", "running", 1), step("ship", "pending")]));
    expect(selectedPath()).toBe("root/plan");
    page.render(run([step("plan", "ok", 0), step("build", "ok", 1), step("ship", "running", 2)]));
    expect(selectedPath()).toBe("root/ship");
  });

  it("ends a pick when an already-seen step starts again", () => {
    page.render(run([step("plan", "ok", 0), step("build", "running", 1)]));
    click("root/plan");
    expect(selectedPath()).toBe("root/plan");
    page.render(run([step("plan", "ok", 0), step("build", "running", 5)]));
    expect(selectedPath()).toBe("root/build");
  });

  it("orders starts by generation, never by time", () => {
    const min = (m: number): string => new Date(T0 + m * 60_000).toISOString();
    page.render(
      run([
        step("plan", "ok", 0),
        step("build", "running", 1, { start: min(5) }),
        step("ship", "pending"),
      ]),
    );
    click("root/plan");
    // The same start, its time re-read earlier.
    page.render(
      run([
        step("plan", "ok", 0),
        step("build", "running", 1, { start: min(1) }),
        step("ship", "pending"),
      ]),
    );
    expect(selectedPath()).toBe("root/plan");
    // A later start, timed before every other.
    page.render(
      run([
        step("plan", "ok", 0),
        step("build", "running", 1, { start: min(1) }),
        step("ship", "running", 2, { start: min(-1) }),
      ]),
    );
    expect(selectedPath()).toBe("root/ship");
  });

  it("maps a container pick to its newest step, so a step is always selected", () => {
    const loop: ExecNode = {
      path: "root/loop",
      label: "loop",
      kind: "repeat",
      state: "running",
      children: [
        { ...step("a", "ok", 0), path: "root/loop/a" },
        { ...step("b", "running", 2), path: "root/loop/b" },
      ],
    };
    page.render(run([step("plan", "ok", 1), loop]));
    click("root/plan");
    click("root/loop");
    expect(selectedPath()).toBe("root/loop/b");
  });

  it("selects the first step before any has started", () => {
    page.render(run([step("plan", "pending"), step("build", "pending")]));
    expect(selectedPath()).toBe("root/plan");
  });
});

describe("the selection a render publishes", () => {
  it("is the node that render built, a field the attention guard ignores included", () => {
    const published: (ExecNode | undefined)[] = [];
    const own = buildExecPage({
      emptyNote: () => "",
      onSelect: (n) => {
        published.push(n);
      },
      follow: "newest",
    });
    own.render(run([step("review", "input", 0)]));
    own.render(run([step("review", "input", 0, { verb: "answer" })]));
    own.dispose();
    expect(published.map((n) => [n?.path, n?.verb])).toEqual([
      ["root/review", undefined],
      ["root/review", "answer"],
    ]);
  });
});

describe("the plan notice", () => {
  it("draws the run's plan line beside the alert, failed only for a rejection", () => {
    page.render({
      ...run([step("a", "running", 0)]),
      notice: { text: "Plan revised: 2 steps queued after a", failed: false },
    });
    const notice = page.root.querySelector<HTMLElement>(".ev-notice");
    expect(notice?.hidden).toBe(false);
    expect(notice?.textContent).toBe("Plan revised: 2 steps queued after a");
    expect(notice?.dataset["kind"]).toBe("stopped");

    page.render({ ...run([step("a", "running", 0)]), notice: { text: "rejected", failed: true } });
    expect(notice?.dataset["kind"]).toBe("failed");

    page.render(run([step("a", "running", 0)]));
    expect(notice?.hidden).toBe(true);
  });
});
