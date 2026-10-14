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

function box(key: string): HTMLElement {
  const b = dock.querySelector<HTMLElement>(`.ev-box[data-box="${key}"]`);
  if (b === null) {
    throw new Error(`no ${key} box`);
  }
  return b;
}

function isOpen(key: string): boolean {
  return box(key).querySelector(".ev-box-toggle")?.getAttribute("aria-expanded") === "true";
}

function selectedPath(): string {
  return shown.at(-1)?.path ?? "";
}

function click(path: string): void {
  dock.querySelector<HTMLElement>(`.ev-row[data-path="${path}"] > .ev-row-main`)?.click();
}

function rowLabels(): string[] {
  return [...box("steps").querySelectorAll(".ev-dock-row .ev-dock-label")].map(
    (l) => l.textContent,
  );
}

describe("the dock", () => {
  it("holds the timeline and the steps, closed, and no results while the run is live", () => {
    page.render(run([step("plan", "ok", 0, { output: "done" }), step("build", "running", 1)]));
    expect(
      [...dock.children].map((b) => [
        b.getAttribute("data-box"),
        isOpen(b.getAttribute("data-box")!),
      ]),
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

  it("opens one box at a time, from anywhere on its row but its controls", () => {
    page.render(run([step("plan", "ok", 0), step("build", "running", 1)]));
    box("timeline").querySelector<HTMLElement>(".ev-box-sum")?.click();
    expect(isOpen("timeline")).toBe(true);
    box("steps").querySelector<HTMLElement>(".ev-dock-row")?.click();
    expect([isOpen("timeline"), isOpen("steps")]).toEqual([false, true]);
    box("steps").querySelector<HTMLElement>(".ev-box-toggle")?.click();
    expect(isOpen("steps")).toBe(false);
  });

  it("slides a box with the chat disclosures' height transition", () => {
    const style = mountAppCSS();
    page.render(run([step("plan", "ok", 0), step("build", "running", 1)]));
    const region = box("steps").querySelector<HTMLElement>(".ev-box-region")!;
    expect(region.classList.contains("uip-disclosure-region")).toBe(true);
    expect(getComputedStyle(region).transitionProperty).toContain("height");
    expect(region.inert).toBe(true);
    box("steps").querySelector<HTMLElement>(".ev-box-toggle")?.click();
    expect(region.inert).toBe(false);
    style.remove();
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
});

describe("the folded rows", () => {
  it("draw the plan as one serial bar, a segment per step in its state, and where the run is", () => {
    page.render(run([step("plan", "ok", 0), step("build", "running", 1), step("ship", "pending")]));
    const segs = [...box("timeline").querySelectorAll<HTMLElement>(".ev-mini-seg")];
    expect(segs.map((s) => s.dataset["state"])).toEqual(["ok", "running", "pending"]);
    expect(segs[1]?.getAttribute("data-tooltip")).toBe("build, running");
    expect(box("timeline").querySelector(".ev-box-sum")?.textContent).toBe("step 2 of 3");
    // No title: the bar and the count say which box this is.
    expect(box("timeline").querySelector(".ev-box-title")).toBeNull();
    expect(box("timeline").querySelector(".ev-box-toggle")?.getAttribute("aria-label")).toBe(
      "Timeline",
    );
  });

  it("list every step in flight, with what it last printed", () => {
    page.render(
      run([
        step("plan", "ok", 0),
        step("seat-a", "running", 1, { tail: "Reading the diff" }),
        step("seat-b", "input", 2),
        step("ship", "pending"),
      ]),
    );
    expect(rowLabels()).toEqual(["seat-a", "seat-b"]);
    const first = box("steps").querySelector<HTMLElement>(".ev-dock-row")!;
    expect(first.dataset["state"]).toBe("running");
    expect(first.querySelector(".ev-dock-tail")?.textContent).toBe("Reading the diff");
    expect(box("steps").querySelector(".ev-box-title")).toBeNull();
  });

  it("list every step in flight past five, scrolling with half of the sixth in view", () => {
    const style = mountAppCSS();
    const seats = Array.from({ length: 7 }, (_, i) => step(`seat-${String(i)}`, "running", i));
    page.render(run(seats));
    expect(rowLabels()).toEqual(seats.map((s) => s.label));
    const list = box("steps").querySelector<HTMLElement>(".ev-box-rows")!;
    const rowH = box("steps").querySelector<HTMLElement>(".ev-dock-row")!.offsetHeight;
    expect(rowH).toBeGreaterThan(0);
    expect(list.clientHeight).toBe(5.5 * rowH);
    expect(list.scrollHeight).toBe(7 * rowH);
    expect(getComputedStyle(list).overflowY).toBe("auto");
    style.remove();
  });

  it("show the newest step once none is in flight, and the first before any started", () => {
    page.render(run([step("plan", "pending"), step("build", "pending")]));
    expect(rowLabels()).toEqual(["plan"]);
    page.render(run([step("plan", "ok", 0), step("build", "fail", 1)], false));
    expect(rowLabels()).toEqual(["build"]);
  });

  it("dim an included step's qualifier, the text staying its id", () => {
    page.render(run([step("review/advisor", "running", 0, { qualifier: "review/" })]));
    const label = box("steps").querySelector<HTMLElement>(".ev-dock-label")!;
    expect(label.textContent).toBe("review/advisor");
    expect(label.querySelector(".ev-q")?.textContent).toBe("review/");
  });

  it("mark a stop a person made beside the clock, keeping the summary", () => {
    page.render({
      ...run([step("loop", "running", 0)]),
      alert: { kind: "stopped", text: "Resumed by you" },
    });
    const tl = box("timeline");
    const mark = tl.querySelector<HTMLElement>(".ev-box-alert")!;
    expect(mark.hidden).toBe(false);
    expect(mark.getAttribute("aria-label")).toBe("Resumed by you");
    expect(tl.querySelector<HTMLElement>(".ev-box-summary")!.hidden).toBe(false);
    expect(tl.querySelector<HTMLElement>(".ev-box-alertline")!.hidden).toBe(true);
    expect(tl.classList.contains("ev-box-alerting")).toBe(false);
    page.render(run([step("loop", "running", 0)]));
    expect(mark.hidden).toBe(true);
  });
});

describe("the dock's alert row", () => {
  const alerts: [string, NonNullable<ExecRun["alert"]>, ExecState][] = [
    [
      "a step waiting on an answer",
      { kind: "input", text: "Waiting for your answer: Which branch?" },
      "input",
    ],
    [
      "a loop paused on its limit",
      { kind: "paused", text: "Waiting: review loop reached its 3-pass limit" },
      "waiting",
    ],
    [
      "an unreachable host",
      {
        kind: "paused",
        text: "Waiting: host unreachable · after a transient error (ECONNREFUSED)",
      },
      "waiting",
    ],
    ["a pause on its way", { kind: "paused", text: "Pausing after the current step" }, "running"],
    ["a failure", { kind: "failed", text: "build failed: exit status 2" }, "fail"],
    [
      "a validation error",
      { kind: "failed", text: "plan failed: input 'branch' is required" },
      "fail",
    ],
  ];

  for (const [name, alert, state] of alerts) {
    it(`replaces the run summary with ${name}, and gives it back once it clears`, () => {
      page.render({ ...run([step("plan", "ok", 0), step("loop", state, 1)]), alert });
      const tl = box("timeline");
      const line = tl.querySelector<HTMLElement>(".ev-box-alertline")!;
      expect(tl.querySelector<HTMLElement>(".ev-box-summary")!.hidden).toBe(true);
      expect(line.hidden).toBe(false);
      expect([tl.classList.contains("ev-box-alerting"), tl.dataset["kind"]]).toEqual([
        true,
        alert.kind,
      ]);
      expect(line.querySelector(".ev-alert-text")?.textContent).toBe(alert.text);
      expect(line.getAttribute("data-tooltip")).toBe(alert.text);
      // The page's alert is the same alert: kind, words and glyph.
      const top = page.root.querySelector<HTMLElement>(".ev-alert-line")!;
      expect([top.hidden, top.dataset["kind"], top.textContent]).toEqual([
        false,
        alert.kind,
        alert.text,
      ]);
      expect(line.querySelector(".ev-alert-icon")?.innerHTML).toBe(
        top.querySelector(".ev-alert-icon")?.innerHTML,
      );
      // The box still opens on the timeline it summarises.
      expect(tl.querySelector(".ev-box-toggle")?.getAttribute("aria-label")).toBe("Timeline");

      page.render(run([step("plan", "ok", 0), step("loop", "running", 1)]));
      expect(tl.querySelector<HTMLElement>(".ev-box-summary")!.hidden).toBe(false);
      expect(line.hidden).toBe(true);
      expect([tl.classList.contains("ev-box-alerting"), tl.dataset["kind"]]).toEqual([
        false,
        undefined,
      ]);
      expect(tl.querySelector(".ev-box-sum")?.textContent).toBe("step 2 of 2");
    });
  }

  it("swaps the glyph when the alert's kind moves under the same sentence", () => {
    const text = "Stopped: the loop ended";
    page.render({ ...run([step("loop", "waiting", 0)]), alert: { kind: "paused", text } });
    const icon = box("timeline").querySelector<HTMLElement>(".ev-box-alertline .ev-alert-icon")!;
    const warnGlyph = icon.innerHTML;
    page.render({ ...run([step("loop", "fail", 0)]), alert: { kind: "failed", text } });
    expect(box("timeline").dataset["kind"]).toBe("failed");
    expect(icon.innerHTML).not.toBe(warnGlyph);
  });

  it("paints the page alert's wash, mark and ink on the row, per kind, with no edge", () => {
    const style = mountAppCSS();
    for (const kind of ["input", "paused", "failed"] as const) {
      page.render({ ...run([step("loop", "waiting", 0)]), alert: { kind, text: kind } });
      const top = getComputedStyle(page.root.querySelector<HTMLElement>(".ev-alert-line")!);
      const head = getComputedStyle(box("timeline").querySelector<HTMLElement>(".ev-box-head")!);
      expect([head.borderInlineStartWidth, top.borderInlineStartWidth]).toEqual(["0px", "0px"]);
      expect(head.backgroundImage).toContain(top.backgroundColor);
      const mark = (sel: string): string =>
        getComputedStyle(document.querySelector<HTMLElement>(sel)!).color;
      expect(mark(".ev-box-alertline .ev-alert-icon")).toBe(mark(".ev-alert-line .ev-alert-icon"));
      expect(mark(".ev-box-alertline")).toBe(top.color);
    }
    style.remove();
  });

  it("keeps the row's contents in place as an alert comes and goes", () => {
    const style = mountAppCSS();
    const toggleLeft = (): number =>
      box("timeline").querySelector<HTMLElement>(".ev-box-toggle")!.getBoundingClientRect().left;
    const headLeft = (): number =>
      box("timeline").querySelector<HTMLElement>(".ev-box-head")!.getBoundingClientRect().left;
    page.render(run([step("loop", "running", 0)]));
    const resting = toggleLeft() - headLeft();
    page.render({
      ...run([step("loop", "waiting", 0)]),
      alert: { kind: "paused", text: "Paused" },
    });
    expect(toggleLeft() - headLeft()).toBe(resting);
    expect(resting).toBe(4);
    style.remove();
  });

  it("seats the consumer's alert action beside the verbs, moving nothing on a repaint", () => {
    const own = document.createElement("div");
    const answer = document.createElement("button");
    const verbs = document.createElement("span");
    const view = buildExecPage({
      emptyNote: () => "",
      dock: own,
      follow: "newest",
      dockControls: () => verbs,
      alertAction: (r) => (r.alert?.kind === "input" ? answer : null),
    });
    document.body.append(view.root, own);
    const asking: ExecRun = {
      ...run([step("ask", "input", 0)]),
      alert: { kind: "input", text: "Waiting for your answer" },
    };
    view.render(asking);
    const head = own.querySelector<HTMLElement>('.ev-box[data-box="timeline"] .ev-box-head')!;
    expect(head.querySelector(".ev-box-alert-act")?.firstChild).toBe(answer);
    expect(head.querySelector(".ev-box-alert-act")?.nextElementSibling?.firstChild).toBe(verbs);
    answer.focus();
    view.render(asking);
    expect(document.activeElement).toBe(answer);
    view.render(run([step("ask", "running", 0)]));
    expect(answer.isConnected).toBe(false);
    view.dispose();
    view.root.remove();
    own.remove();
  });
});

describe("the controls on the dock and the shown step", () => {
  it("seat the consumer's compact row on the timeline row and the step's on its name row", () => {
    const own = document.createElement("div");
    const dockRow = document.createElement("span");
    const stepRow = document.createElement("span");
    const view = buildExecPage({
      emptyNote: () => "",
      dock: own,
      follow: "newest",
      dockControls: () => dockRow,
      stepControls: (_run, node) => (node?.state === "running" ? stepRow : null),
    });
    document.body.append(view.root, own);
    view.render(run([step("plan", "ok", 0), step("build", "running", 1)]));
    expect(own.querySelector('.ev-box[data-box="timeline"] .ev-box-actions')?.firstChild).toBe(
      dockRow,
    );
    expect(view.root.querySelector(".ev-d-head .ev-d-actions")?.firstChild).toBe(stepRow);
    view.render(run([step("plan", "ok", 0), step("build", "ok", 1)], false));
    expect(view.root.querySelector(".ev-d-actions")?.childElementCount).toBe(0);
    view.dispose();
    view.root.remove();
    own.remove();
  });
});

describe("the docked timeline", () => {
  it("shows every planned step from the start, empty until it runs", () => {
    page.render(
      run([step("plan", "running", 0), step("build", "pending"), step("ship", "pending")]),
    );
    const lanes = [...box("timeline").querySelectorAll<HTMLElement>(".ev-tl-lane")];
    expect(lanes.map((l) => l.dataset["path"])).toEqual(["root/plan", "root/build", "root/ship"]);
    const bars = lanes.map((l) => l.querySelector<HTMLElement>(".ev-tl-bar")!.hidden);
    expect(bars).toEqual([false, true, true]);
    expect(box("timeline").textContent).not.toContain("two steps");
  });

  it("is not clipped by its own corners inside the box", () => {
    const style = mountAppCSS();
    page.render(run([step("plan", "ok", 0), step("build", "running", 1)]));
    const tl = box("timeline").querySelector<HTMLElement>(".ev-tl")!;
    const cs = getComputedStyle(tl);
    expect(cs.borderTopLeftRadius).toBe("0px");
    expect(cs.borderBottomRightRadius).toBe("0px");
    expect([cs.overflowX, cs.overflowY]).toEqual(["visible", "visible"]);
    style.remove();
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
