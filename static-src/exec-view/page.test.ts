import { describe, it, expect, beforeEach, afterEach } from "vitest";
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

function selectedPath(): string {
  return shown.at(-1)?.path ?? "";
}

function click(path: string): void {
  dock.querySelector<HTMLElement>(`.ev-row[data-path="${path}"] > .ev-row-main`)?.click();
}

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
      dock: document.createElement("div"),
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
