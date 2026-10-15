import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { page as browser } from "vitest/browser";
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
    ...(startedMin === undefined
      ? {}
      : { start: new Date(T0 + startedMin * 60_000).toISOString(), startGen: startedMin + 1 }),
    ...over,
  };
}

function run(nodes: ExecNode[], live = true): ExecRun {
  return { id: "wf_1", label: "release", state: live ? "running" : "ok", nodes, live };
}

let host: HTMLElement;
let page: ExecPageView;
let verbs: HTMLElement | null;
let answer: HTMLButtonElement | null;
let notes: readonly string[];
let shown: (ExecNode | undefined)[];

beforeEach(() => {
  host = document.createElement("div");
  host.className = "ev-dock-host";
  verbs = null;
  answer = null;
  notes = [];
  shown = [];
  page = buildExecPage({
    emptyNote: () => "",
    dock: host,
    controls: () => verbs,
    alertAction: () => answer,
    notes: () => notes,
    onShowNode: (n) => {
      shown.push(n);
    },
    follow: "newest",
  });
  document.body.append(page.root, host);
});

afterEach(() => {
  page.dispose();
  page.root.remove();
  host.remove();
});

const q = <T extends HTMLElement = HTMLElement>(sel: string): T => {
  const found = host.querySelector<T>(sel);
  if (found === null) {
    throw new Error(`no ${sel} in the dock`);
  }
  return found;
};
const toggle = (): HTMLButtonElement => q<HTMLButtonElement>(".ev-box-toggle");
const isOpen = (): boolean => toggle().getAttribute("aria-expanded") === "true";
const rowLabels = (): string[] =>
  [...host.querySelectorAll(".ev-dock-row .ev-dock-label")].map((l) => l.textContent);
const segment = (id: string): HTMLButtonElement => q(`[data-ev-seg="${id}"]`);

describe("the dock", () => {
  it("is the only home of the run's state: the page holds the selected step alone", () => {
    page.render({
      ...run([step("plan", "ok", 0), step("build", "running", 1)]),
      inputs: { a: "b" },
    });
    expect(q(".ev-sum-label").textContent).toBe("release");
    expect(q(".ev-sum-state").textContent).toBe("running");
    expect(q(".ev-sum-progress").textContent).toBe("step 2 of 2");
    for (const gone of [".ev-head", ".ev-inputs", ".ev-alert-line", ".ev-tree", ".ev-tl"]) {
      expect(page.root.querySelector(gone), gone).toBeNull();
    }
    expect(page.root.querySelector(".ev-d-title")?.textContent).toBe("build");
  });

  it("opens closed, and its head toggles the panel from anywhere but its controls", () => {
    verbs = document.createElement("button");
    page.render(run([step("plan", "ok", 0), step("build", "running", 1)]));
    expect(isOpen()).toBe(false);
    verbs.click();
    expect(isOpen()).toBe(false);
    q(".ev-sum").click();
    expect(isOpen()).toBe(true);
    expect(host.querySelector(".ev-dock")?.hasAttribute("data-open")).toBe(true);
    toggle().click();
    expect(isOpen()).toBe(false);
  });

  it("names the toggle and describes it with the summary", () => {
    page.render(run([step("plan", "running", 0)]));
    expect(toggle().getAttribute("aria-label")).toBe("Run details");
    expect(document.getElementById(toggle().getAttribute("aria-describedby") ?? "")).toBe(
      q(".ev-sum"),
    );
  });

  it("holds Steps, Timeline and Inputs, withdrawing Inputs when the run has none", () => {
    page.render({ ...run([step("plan", "running", 0)]), inputs: { repo: "marotte" } });
    expect(segment("steps").getAttribute("aria-selected")).toBe("true");
    expect(q(".ev-dock-body .ev-tree")).not.toBeNull();
    segment("inputs").click();
    expect(q(".ev-in-k").textContent).toBe("repo");
    expect(q(".ev-in-text").textContent).toBe("marotte");
    page.render(run([step("plan", "running", 0)]));
    expect(segment("inputs").classList.contains("hidden")).toBe(true);
    expect(segment("steps").getAttribute("aria-selected")).toBe("true");
    segment("timeline").click();
    expect(q<HTMLElement>(".ev-tl").closest<HTMLElement>(".ev-dock-pane")?.hidden).toBe(false);
  });

  it("keeps the inputs list across repaints while the set holds", () => {
    page.render({ ...run([step("plan", "running", 0)]), inputs: { repo: "marotte" } });
    const before = q(".ev-in-v");
    page.render({ ...run([step("plan", "running", 1)]), inputs: { repo: "marotte" } });
    expect(q(".ev-in-v")).toBe(before);
    page.render({ ...run([step("plan", "running", 1)]), inputs: { repo: "subflux" } });
    expect(q(".ev-in-v")).not.toBe(before);
  });
});

describe("the live rows", () => {
  it("list every step in flight with what it last printed, each a button that shows its step", () => {
    page.render(
      run([
        step("plan", "ok", 0),
        step("seat-a", "running", 1, { tail: "Reading the diff" }),
        step("seat-b", "input", 2),
        step("ship", "pending"),
      ]),
    );
    expect(rowLabels()).toEqual(["seat-a", "seat-b"]);
    const [a, b] = [...host.querySelectorAll<HTMLButtonElement>(".ev-dock-row")];
    expect(a?.querySelector(".ev-dock-tail")?.textContent).toBe("Reading the diff");
    expect(b?.getAttribute("aria-current")).toBe("true");
    expect(a?.getAttribute("aria-label")).toMatch(/^seat-a, running, /);
    a?.click();
    expect(shown.at(-1)?.path).toBe("root/seat-a");
    expect(a?.getAttribute("aria-current")).toBe("true");
    expect(b?.hasAttribute("aria-current")).toBe(false);
  });

  it("keep the shown step named once it has settled and others run", () => {
    page.render(run([step("plan", "ok", 0), step("build", "running", 1)]));
    host.querySelector<HTMLElement>('.ev-row[data-path="root/plan"] > .ev-row-main')?.click();
    expect(rowLabels()).toEqual(["build", "plan"]);
  });

  it("show the newest step once none is in flight, and the first before any started", () => {
    page.render(run([step("plan", "pending"), step("build", "pending")]));
    expect(rowLabels()).toEqual(["plan"]);
    page.render(run([step("plan", "ok", 0), step("build", "fail", 1)], false));
    expect(rowLabels()).toEqual(["build"]);
  });

  it("scroll past five, with half of the sixth in view", () => {
    const style = mountAppCSS();
    const seats = Array.from({ length: 7 }, (_, i) => step(`seat-${String(i)}`, "running", i));
    page.render(run(seats));
    const list = q(".ev-dock-rows");
    const rowH = q(".ev-dock-row").offsetHeight;
    expect(rowH).toBeGreaterThan(0);
    expect(list.clientHeight).toBe(5.5 * rowH);
    expect(getComputedStyle(list).overflowY).toBe("auto");
    style.remove();
  });

  it("hide while the panel is open", () => {
    const style = mountAppCSS();
    page.render(run([step("plan", "running", 0)]));
    toggle().click();
    expect(getComputedStyle(q(".ev-dock-live")).display).toBe("none");
    style.remove();
  });

  it("show one row and a count that opens Steps in a phone-wide dock", () => {
    const style = mountAppCSS();
    host.style.inlineSize = "22rem";
    try {
      page.render(run([step("a", "running", 0), step("b", "running", 1), step("c", "running", 2)]));
      const list = q(".ev-dock-rows");
      expect(list.clientHeight).toBe(q(".ev-dock-row[aria-current]").offsetHeight);
      const more = q<HTMLButtonElement>(".ev-dock-more");
      expect(getComputedStyle(more).display).not.toBe("none");
      expect(more.textContent).toBe("+2");
      expect(more.getAttribute("aria-label")).toBe("Show 2 more steps");
      segment("timeline").click();
      more.click();
      expect(isOpen()).toBe(true);
      expect(segment("steps").getAttribute("aria-selected")).toBe("true");
      expect(getComputedStyle(q(".ev-sum-label")).display).toBe("none");
      expect(getComputedStyle(q(".ev-sum-state")).display).not.toBe("none");
    } finally {
      style.remove();
    }
  });
});

describe("the alert", () => {
  const alerts: [string, NonNullable<ExecRun["alert"]>][] = [
    ["a step waiting on an answer", { kind: "input", text: "Waiting for your answer: Which?" }],
    ["a loop at its limit", { kind: "paused", text: "Waiting: review reached its 3-pass limit" }],
    ["an interrupted run", { kind: "paused", text: "Waiting: Interrupted by agent restart" }],
    ["an unreachable host", { kind: "paused", text: "Waiting: host unreachable (ECONNREFUSED)" }],
    ["a failure", { kind: "failed", text: "build failed: exit status 2" }],
    ["a validation error", { kind: "failed", text: "plan failed: input 'branch' is required" }],
  ];

  for (const [name, alert] of alerts) {
    it(`replaces the summary with ${name}, in its live region, and gives it back`, () => {
      page.render({ ...run([step("plan", "ok", 0), step("loop", "waiting", 1)]), alert });
      const head = q(".ev-dock-head");
      expect(head.dataset["alert"]).toBe(alert.kind);
      const line = q(".ev-alert-line");
      expect(line.querySelector(".ev-alert-text")?.textContent).toBe(alert.text);
      expect(line.parentElement?.getAttribute("role")).toBe(
        alert.kind === "failed" ? "alert" : "status",
      );
      expect(q(".ev-sum").hasAttribute("data-alerting")).toBe(true);
      page.render(run([step("plan", "ok", 0), step("loop", "running", 1)]));
      expect(head.dataset["alert"]).toBeUndefined();
      expect(host.querySelector(".ev-alert-line")).toBeNull();
      expect(q(".ev-sum").hasAttribute("data-alerting")).toBe(false);
    });
  }

  it("keeps both live regions mounted, so the first alert is announced", () => {
    page.render(run([step("loop", "running", 0)]));
    const regions = [...host.querySelectorAll("[role=status], [role=alert]")];
    expect(regions.map((r) => r.getAttribute("role")).sort()).toEqual([
      "alert",
      "status",
      "status",
    ]);
  });

  it("keeps the empty notes region rendered", () => {
    const style = mountAppCSS();
    try {
      page.render(run([step("loop", "running", 0)]));
      const notesHost = q(".ev-dock-notes");
      expect(notesHost.childElementCount).toBe(0);
      expect(getComputedStyle(notesHost).display).not.toBe("none");
      expect(notesHost.getBoundingClientRect().height).toBe(0);
    } finally {
      style.remove();
    }
  });

  it("writes nothing on a repaint that changes nothing", () => {
    const alert = { kind: "paused", text: "Waiting" } as const;
    page.render({ ...run([step("loop", "waiting", 0)]), alert });
    const text = q(".ev-alert-text").firstChild;
    page.render({ ...run([step("loop", "waiting", 0)]), alert });
    expect(q(".ev-alert-text").firstChild).toBe(text);
  });

  it("paints a wash and no leading edge, per kind", () => {
    const style = mountAppCSS();
    for (const kind of ["input", "paused", "failed"] as const) {
      page.render({ ...run([step("loop", "waiting", 0)]), alert: { kind, text: kind } });
      const cs = getComputedStyle(q(".ev-dock-head"));
      expect(cs.borderInlineStartWidth).toBe("0px");
      expect(cs.boxShadow).toBe("none");
      expect(cs.backgroundImage).toContain("linear-gradient");
    }
    style.remove();
  });

  it("seats the consumer's action and verbs, moving neither on a repaint", () => {
    verbs = document.createElement("span");
    answer = document.createElement("button");
    const asking: ExecRun = {
      ...run([step("ask", "input", 0)]),
      alert: { kind: "input", text: "Waiting for your answer" },
    };
    page.render(asking);
    expect(q(".ev-dock-alert-act").firstChild).toBe(answer);
    expect(q(".ev-dock-alert-act").nextElementSibling?.firstChild).toBe(verbs);
    answer.focus();
    page.render(asking);
    expect(document.activeElement).toBe(answer);
    answer = null;
    page.render(run([step("ask", "running", 0)]));
    expect(q(".ev-dock-alert-act").childElementCount).toBe(0);
  });
});

describe("the notes", () => {
  it("state a stop a person made in full, keeping the summary", () => {
    page.render({
      ...run([step("loop", "running", 0)]),
      alert: { kind: "stopped", text: "Resumed by you" },
    });
    expect([...host.querySelectorAll(".ev-dock-note")].map((n) => n.textContent)).toEqual([
      "Resumed by you",
    ]);
    expect(q(".ev-dock-head").dataset["alert"]).toBeUndefined();
    expect(q(".ev-dock-notes").getAttribute("role")).toBe("status");
  });

  it("carry the plan notice, washed only for a rejection, and the consumer's own lines", () => {
    notes = ["This run has no live engine on this server."];
    page.render({
      ...run([step("a", "running", 0)]),
      notice: { text: "Plan revised: 2 steps queued after a", failed: false },
    });
    const all = (): HTMLElement[] => [...host.querySelectorAll<HTMLElement>(".ev-dock-note")];
    expect(all().map((n) => [n.textContent, n.hasAttribute("data-failed")])).toEqual([
      ["Plan revised: 2 steps queued after a", false],
      ["This run has no live engine on this server.", false],
    ]);
    notes = [];
    page.render({ ...run([step("a", "running", 0)]), notice: { text: "rejected", failed: true } });
    expect(all().map((n) => [n.textContent, n.hasAttribute("data-failed")])).toEqual([
      ["rejected", true],
    ]);
  });
});

describe("the timeline segment", () => {
  it("shows every planned step from the start, empty until it runs", () => {
    page.render(
      run([step("plan", "running", 0), step("build", "pending"), step("ship", "pending")]),
    );
    const lanes = [...host.querySelectorAll<HTMLElement>(".ev-tl-lane")];
    expect(lanes.map((l) => l.dataset["path"])).toEqual(["root/plan", "root/build", "root/ship"]);
    expect(lanes.map((l) => l.querySelector<HTMLElement>(".ev-tl-bar")!.hidden)).toEqual([
      false,
      true,
      true,
    ]);
  });

  it("draws one mini segment per step in its state", () => {
    page.render(run([step("plan", "ok", 0), step("build", "running", 1), step("ship", "pending")]));
    const segs = [...host.querySelectorAll<HTMLElement>(".ev-mini-seg")];
    expect(segs.map((s) => s.dataset["state"])).toEqual(["ok", "running", "pending"]);
    expect(segs[1]?.getAttribute("data-tooltip")).toBe("build, running");
  });
});

describe("dispose", () => {
  it("empties the host", () => {
    page.render(run([step("plan", "running", 0)]));
    page.dispose();
    expect(host.childElementCount).toBe(0);
  });
});

// Budgets at 390x844 under a finger: the head and one row are each `--hit-floor` (44px) tall.
describe("the shown step in a one-row dock", () => {
  const onScreen = (): HTMLButtonElement[] =>
    [...host.querySelectorAll<HTMLButtonElement>(".ev-dock-row")].filter((r) =>
      r.checkVisibility(),
    );

  it("is the row left on screen when others are in flight, and the others leave the tab order", () => {
    const style = mountAppCSS();
    host.style.inlineSize = "22rem";
    try {
      page.render(run([step("plan", "ok", 0), step("a", "running", 1), step("b", "running", 2)]));
      host.querySelector<HTMLElement>('.ev-row[data-path="root/plan"] > .ev-row-main')?.click();
      expect(rowLabels()).toEqual(["a", "b", "plan"]);
      const visible = onScreen();
      expect(visible.map((r) => r.querySelector(".ev-dock-label")?.textContent)).toEqual(["plan"]);
      expect(visible[0]?.getAttribute("aria-current")).toBe("true");
      const clipped = host.querySelector<HTMLButtonElement>(".ev-dock-row:not([aria-current])")!;
      clipped.focus();
      expect(document.activeElement).not.toBe(clipped);
    } finally {
      style.remove();
    }
  });

  it("is the first row when the shown step has no row", () => {
    const style = mountAppCSS();
    host.style.inlineSize = "22rem";
    try {
      const group: ExecNode = {
        path: "root/g",
        label: "g",
        kind: "parallel",
        state: "running",
        children: [step("a", "running", 0), step("b", "running", 1)],
      };
      page.dispose();
      // A page that can show a container, which has no live row of its own.
      page = buildExecPage({ emptyNote: () => "", dock: host, follow: "attention" });
      page.render({ ...run([group]), focus: "root/g" });
      expect(host.querySelector(".ev-dock-row[aria-current]")).toBeNull();
      expect(onScreen().map((r) => r.querySelector(".ev-dock-label")?.textContent)).toEqual(["a"]);
    } finally {
      style.remove();
    }
  });

  it("keeps every row reachable in a wide dock", () => {
    const style = mountAppCSS();
    try {
      page.render(run([step("a", "running", 0), step("b", "running", 1)]));
      expect(onScreen()).toHaveLength(2);
    } finally {
      style.remove();
    }
  });
});

describe("the Steps tree on open", () => {
  /** Forty steps, so the tree overflows the capped panel body. */
  const many = (state: (i: number) => ExecState): ExecNode[] =>
    Array.from({ length: 40 }, (_, i) =>
      step(`s${String(i).padStart(2, "0")}`, state(i), i, {
        ...(state(i) === "ok" ? { end: new Date(T0 + (i + 0.5) * 60_000).toISOString() } : {}),
      }),
    );
  const body = (): HTMLElement => q(".ev-dock-body");
  const rowLine = (path: string): HTMLElement => q(`.ev-row[data-path="${path}"] > .ev-row-main`);
  /** Whether the row sits in the middle of the tree's box, within a pixel and a half. */
  const centred = (path: string): boolean => {
    const r = rowLine(path).getBoundingClientRect();
    const b = body().getBoundingClientRect();
    return Math.abs(r.top + r.height / 2 - (b.top + b.height / 2)) <= 1.5;
  };
  const opened = async (path: string): Promise<void> => {
    toggle().click();
    await vi.waitFor(() => {
      expect(centred(path)).toBe(true);
    });
  };
  const settledPanel = async (): Promise<void> => {
    await Promise.allSettled(
      q(".ev-dock-panel")
        .getAnimations()
        .map((a) => a.finished),
    );
  };
  let style: HTMLStyleElement;
  beforeEach(() => {
    style = mountAppCSS();
  });
  afterEach(() => {
    style.remove();
  });

  it("lands on the step waiting on the reader, over the shown one in flight", async () => {
    page.render(run(many((i) => (i === 12 ? "input" : i === 30 ? "running" : "ok"))));
    expect(body().scrollHeight).toBeGreaterThan(body().clientHeight * 2);
    await opened("root/s12");
  });

  it("lands on a failed step", async () => {
    page.render(run(many((i) => (i === 8 ? "fail" : i === 25 ? "running" : "ok"))));
    await opened("root/s08");
  });

  it("lands on the step a paused run has parked", async () => {
    const parked = many((i) => (i === 14 ? "waiting" : i < 14 ? "ok" : "pending"));
    page.render({ ...run(parked), state: "waiting" });
    rowLine("root/s02").click();
    await opened("root/s14");
  });

  it("lands on the shown step while it is in flight, over a newer one", async () => {
    page.render(run(many((i) => (i === 10 || i === 30 ? "running" : "ok"))));
    rowLine("root/s10").click();
    await opened("root/s10");
  });

  it("lands on the newest step in flight when the shown one has settled", async () => {
    page.render(run(many((i) => (i === 15 || i === 20 ? "running" : "ok"))));
    rowLine("root/s05").click();
    await opened("root/s20");
  });

  it("lands on the shown step once nothing is in flight", async () => {
    page.render(
      run(
        many(() => "ok"),
        false,
      ),
    );
    rowLine("root/s18").click();
    await opened("root/s18");
  });

  it("lands on the last step to end when no step is shown", async () => {
    const attention = buildExecPage({
      emptyNote: () => "",
      dock: host,
      follow: "attention",
    });
    try {
      const steps = many(() => "ok");
      const group: ExecNode = {
        path: "root/g",
        label: "g",
        kind: "sequence",
        state: "ok",
        children: steps,
      };
      // The 22nd step ended last, out of plan order.
      steps[21] = { ...steps[21]!, end: new Date(T0 + 90 * 60_000).toISOString() };
      attention.render({ ...run([group], false), focus: "root/g" });
      await opened("root/s21");
    } finally {
      attention.dispose();
    }
  });

  it("unfolds the container the step sits in", async () => {
    const steps = many((i) => (i === 25 ? "running" : "ok"));
    const group: ExecNode = {
      path: "root/g",
      label: "g",
      kind: "sequence",
      state: "running",
      children: steps.map((s) => ({ ...s, path: `root/g/${s.label}` })),
    };
    page.render(run([group]));
    toggle().click();
    await settledPanel();
    q('.ev-row[data-path="root/g"] .ev-twist').click();
    expect(q('.ev-row[data-path="root/g"]').getAttribute("aria-expanded")).toBe("false");
    toggle().click();
    await settledPanel();
    await opened("root/g/s25");
    expect(q('.ev-row[data-path="root/g"]').getAttribute("aria-expanded")).toBe("true");
  });

  it("moves once per open: a repaint leaves the reader's scroll alone", async () => {
    page.render(run(many((i) => (i === 30 ? "running" : "ok"))));
    await opened("root/s30");
    body().scrollTop = 0;
    page.render(run(many((i) => (i === 30 ? "running" : "ok"))));
    expect(body().scrollTop).toBe(0);
  });

  it("is called off when the reader takes the tree while it opens", async () => {
    page.render(run(many((i) => (i === 30 ? "running" : "ok"))));
    toggle().click();
    expect(q(".ev-dock-panel").getAnimations().length).toBeGreaterThan(0);
    body().dispatchEvent(new WheelEvent("wheel"));
    await settledPanel();
    await new Promise((r) => setTimeout(r, 0));
    expect(body().scrollTop).toBe(0);
  });

  it("moves again when Steps is picked over another segment, and not for Steps again", async () => {
    page.render(run(many((i) => (i === 30 ? "running" : "ok"))));
    await opened("root/s30");
    segment("timeline").click();
    body().scrollTop = 0;
    segment("steps").click();
    expect(centred("root/s30")).toBe(true);
    body().scrollTop = 0;
    segment("steps").click();
    expect(body().scrollTop).toBe(0);
  });

  it("never moves the page scroller", async () => {
    const scroller = document.createElement("div");
    scroller.style.cssText = "block-size: 200px; overflow: auto";
    const tall = document.createElement("div");
    tall.style.blockSize = "2000px";
    scroller.append(tall, page.root);
    document.body.prepend(scroller);
    try {
      scroller.scrollTop = 700;
      page.render(run(many((i) => (i === 30 ? "running" : "ok"))));
      await opened("root/s30");
      expect(scroller.scrollTop).toBe(700);
      expect(document.scrollingElement?.scrollTop ?? 0).toBe(0);
    } finally {
      scroller.remove();
    }
  });
});

describe("the phone budget", () => {
  const fan = (): ExecNode[] =>
    ["a", "b", "c", "d"].map((id, i) => step(`review/${id}`, "running", i, { tail: "Reading" }));

  async function phone(): Promise<HTMLStyleElement> {
    await browser.viewport(390, 844);
    document.documentElement.setAttribute("data-pointer", "coarse");
    host.style.inlineSize = "22rem";
    return mountAppCSS();
  }

  async function restore(style: HTMLStyleElement): Promise<void> {
    style.remove();
    document.documentElement.removeAttribute("data-pointer");
    await browser.viewport(1280, 720);
  }

  const dockHeight = (): number => q(".ev-dock").getBoundingClientRect().height;

  it("rests at one row with four steps in flight", async () => {
    const style = await phone();
    try {
      verbs = document.createElement("span");
      verbs.className = "run-controls";
      verbs.append(document.createElement("button"), document.createElement("button"));
      page.render(run(fan()));
      expect(dockHeight()).toBeLessThanOrEqual(96);
    } finally {
      await restore(style);
    }
  });

  it("fits a loop at its limit, its field and three verbs, and a note beside it", async () => {
    const style = await phone();
    try {
      verbs = document.createElement("span");
      verbs.className = "run-controls";
      const field = Object.assign(document.createElement("input"), { type: "number" });
      field.className = "tool-form-input run-extend-count";
      verbs.append(
        field,
        document.createElement("button"),
        document.createElement("button"),
        document.createElement("button"),
      );
      const alert = {
        kind: "paused",
        text: "Waiting: review reached its 3-iteration limit · maxIterations",
      } as const;
      page.render({ ...run(fan()), alert });
      expect(dockHeight()).toBeLessThanOrEqual(150);
      page.render({
        ...run(fan()),
        alert,
        notice: { text: "Plan revised: 2 steps queued after review", failed: false },
      });
      expect(dockHeight()).toBeLessThanOrEqual(195);
    } finally {
      await restore(style);
    }
  });

  it("caps an open panel to the visual viewport", async () => {
    const style = await phone();
    try {
      page.render(run(Array.from({ length: 40 }, (_, i) => step(`s${String(i)}`, "ok", i))));
      toggle().click();
      const body = q(".ev-dock-body");
      expect(body.getBoundingClientRect().height).toBeLessThanOrEqual(0.45 * 844 + 1);
      expect(getComputedStyle(body).overflowY).toBe("auto");
    } finally {
      await restore(style);
    }
  });
});
