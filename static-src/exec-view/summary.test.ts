import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";
import { buildExecSummary, type ExecSummaryView } from "./summary.js";
import type { ExecNode, ExecRun } from "./model.js";
import type { ExecState } from "./status.js";

function step(id: string, state: ExecState): ExecNode {
  return { path: `r/${id}`, label: id, kind: "step", state, children: [] };
}

function run(nodes: ExecNode[], over: Partial<ExecRun> = {}): ExecRun {
  return { label: "nightly", state: "running", nodes, live: true, ...over };
}

let host: HTMLElement;

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
});

afterEach(() => {
  host.remove();
});

/** A dock head's summary (no alert host), or a run-bar row's (the host takes the alert). */
function build(row: boolean): ExecSummaryView {
  const view = buildExecSummary(row ? { alertHost: host } : {});
  host.append(view.root);
  return view;
}

const part = (view: ExecSummaryView, cls: string): string =>
  view.root.querySelector(`.${cls}`)?.textContent ?? "";

const compact = (view: ExecSummaryView): string =>
  view.root.querySelector(".ev-sum-compact")?.textContent ?? "";

const progress = (view: ExecSummaryView): HTMLElement =>
  view.root.querySelector<HTMLElement>(".ev-sum-progress")!;

describe("the run summary", () => {
  it("states the name, the state word and where the run stands, compactly", () => {
    const view = build(false);
    view.paint(run([step("a", "ok"), step("b", "running"), step("c", "pending")]));
    expect([part(view, "ev-sum-label"), part(view, "ev-sum-state"), compact(view)]).toEqual([
      "nightly",
      "running",
      "2/3",
    ]);
    expect(view.describe()).toBe("nightly, running, step 2 of 3");
  });

  it("keeps the compact form out of the accessibility tree and the words in it", () => {
    const view = build(false);
    view.paint(run([step("a", "ok"), step("b", "running"), step("c", "pending")]));
    expect(view.root.querySelector(".ev-sum-compact")?.getAttribute("aria-hidden")).toBe("true");
    expect(progress(view).querySelector(".sr-only")?.textContent).toBe("step 2 of 3");
    expect(progress(view).getAttribute("data-tooltip")).toBe("step 2 of 3");
  });

  describe("in a loop", () => {
    const loopRun = (): ExecRun =>
      run([
        {
          path: "r/loop",
          label: "fix-loop",
          kind: "repeat",
          state: "running",
          maxPasses: 8,
          children: [
            {
              path: "r/loop/iter-2",
              label: "pass 3",
              kind: "sequence",
              state: "running",
              pass: 3,
              children: [step("code", "ok"), step("review", "running"), step("fix", "pending")],
            },
          ],
        },
        step("land", "pending"),
      ]);

    it("draws the repeat glyph for the pass and the words in the name and tooltip", () => {
      const view = build(false);
      view.paint(loopRun());
      expect(compact(view)).toBe("3/8\u00b72/4");
      expect(view.root.querySelector(".ev-sum-pass svg")).not.toBeNull();
      expect(progress(view).getAttribute("data-tooltip")).toBe("fix-loop pass 3 of 8, step 2 of 4");
      expect(view.describe()).toBe("nightly, running, fix-loop pass 3 of 8, step 2 of 4");
    });

    it("withholds the pass outside a loop", () => {
      const view = build(false);
      view.paint(loopRun());
      view.paint(run([step("a", "ok"), step("b", "running")]));
      expect(view.root.querySelector<HTMLElement>(".ev-sum-pass")?.hidden).toBe(true);
      expect(view.root.querySelector<HTMLElement>(".ev-sum-dot")?.hidden).toBe(true);
      expect(view.describe()).toBe("nightly, running, step 2 of 2");
    });

    it("writes nothing on a repaint that changes nothing", async () => {
      const view = build(false);
      view.paint(loopRun());
      const seen: MutationRecord[] = [];
      const mo = new MutationObserver((records) => seen.push(...records));
      mo.observe(view.root, {
        childList: true,
        subtree: true,
        characterData: true,
        attributes: true,
      });
      view.paint(loopRun());
      await Promise.resolve();
      mo.disconnect();
      expect(seen.map((r) => `${r.type}:${(r.target as Element).className}`)).toEqual([]);
    });

    for (const width of ["22rem", "40rem"]) {
      it(`shows the one compact form in a ${width} dock`, () => {
        const style = mountAppCSS();
        try {
          host.className = "ev-dock";
          host.style.inlineSize = width;
          const view = build(false);
          view.paint(loopRun());
          const shown = view.root.querySelector<HTMLElement>(".ev-sum-compact")!;
          expect(shown.getBoundingClientRect().width).toBeGreaterThan(1);
          const words = progress(view).querySelector<HTMLElement>(".sr-only")!;
          expect(words.getBoundingClientRect().width).toBeLessThanOrEqual(1);
        } finally {
          style.remove();
        }
      });
    }
  });

  it("withholds the progress of a one-step run", () => {
    const view = build(false);
    view.paint(run([step("a", "running")]));
    expect(progress(view).hidden).toBe(true);
    expect(view.describe()).toBe("nightly, running");
  });

  it("says needs input while an ask blocks the run", () => {
    const view = build(false);
    view.paint(
      run([step("a", "input")], {
        state: "input",
        alert: { kind: "input", text: "Waiting for answer" },
      }),
    );
    expect(part(view, "ev-sum-state")).toBe("needs input");
  });

  it("never draws an alert in the dock head, which shows it in its own region", () => {
    const view = build(false);
    view.paint(run([step("a", "fail")], { alert: { kind: "failed", text: "a failed" } }));
    expect(view.root.querySelector(".ev-alert-line")).toBeNull();
    expect(view.describe()).toBe("nightly, running");
  });

  for (const kind of ["paused", "failed"] as const) {
    it(`gives a run-bar row's ${kind} alert a second line and its kind to the row`, () => {
      const view = build(true);
      view.paint(run([step("a", "waiting")], { alert: { kind, text: `about ${kind}` } }));
      expect(host.dataset["alert"]).toBe(kind);
      expect(view.root.querySelector(".ev-alert-line")?.textContent).toBe(`about ${kind}`);
      // The columns stay: the alert is beside them, never in their place.
      expect(part(view, "ev-sum-label")).toBe("nightly");
      expect(view.describe()).toBe(`nightly: about ${kind}`);
    });
  }

  it("gives a run-bar row's open question its line and kind, the CSS placing it in the progress's place", () => {
    const view = build(true);
    view.paint(run([step("a", "waiting")], { alert: { kind: "input", text: "about input" } }));
    expect(host.dataset["alert"]).toBe("input");
    expect(view.root.querySelector(".ev-alert-line")?.textContent).toBe("about input");
    expect(part(view, "ev-sum-label")).toBe("nightly");
    expect(view.describe()).toBe("nightly: about input");
  });

  it("puts an open question whole in the tooltip of its line, and no other alert", () => {
    const view = build(true);
    const tip = (): string | null =>
      view.root.querySelector(".ev-alert-line")?.getAttribute("data-tooltip") ?? null;
    view.paint(
      run([step("a", "waiting")], { alert: { kind: "input", text: "Waiting for answer: A?" } }),
    );
    expect(tip()).toBe("Waiting for answer: A?");
    view.paint(
      run([step("a", "waiting")], { alert: { kind: "input", text: "Waiting for answer: B?" } }),
    );
    expect(tip()).toBe("Waiting for answer: B?");
    view.paint(run([step("a", "waiting")], { alert: { kind: "paused", text: "Waiting: host" } }));
    expect(tip()).toBeNull();
  });

  it("uses no live role on a row that already announces elsewhere", () => {
    const view = build(true);
    view.paint(run([step("a", "fail")], { alert: { kind: "failed", text: "a failed" } }));
    expect(view.root.querySelector("[role]")).toBeNull();
  });

  it("leaves a stopped alert to the consumer, keeping the summary", () => {
    const view = build(true);
    view.paint(run([step("a", "running")], { alert: { kind: "stopped", text: "Stopped by you" } }));
    expect(host.dataset["alert"]).toBeUndefined();
    expect(view.root.textContent).not.toContain("Stopped by you");
  });

  it("clears the alert once it goes", () => {
    const view = build(true);
    view.paint(run([step("a", "waiting")], { alert: { kind: "paused", text: "Waiting" } }));
    view.paint(run([step("a", "running")]));
    expect(host.dataset["alert"]).toBeUndefined();
    expect(view.root.querySelector(".ev-alert-line")).toBeNull();
  });

  it("marks the first segment of a sibling parallel branch", () => {
    const view = build(false);
    const fan: ExecNode = {
      path: "r/fan",
      label: "fan",
      kind: "parallel",
      state: "running",
      children: [step("a", "running"), step("b", "running")],
    };
    view.paint(run([fan]));
    const segs = [...view.root.querySelectorAll<HTMLElement>(".ev-mini-seg")];
    expect(segs.map((s) => s.hasAttribute("data-branch"))).toEqual([false, true]);
  });

  it("draws a long loop's bar as the current pass's steps, gapped like any run's", () => {
    const view = build(false);
    const pass = (n: number, states: ExecState[]): ExecNode => ({
      path: `r/loop/${String(n)}`,
      label: `pass ${String(n)}`,
      kind: "sequence",
      state: states.includes("running") ? "running" : "ok",
      pass: n,
      children: states.map((s, i) => step(`p${String(n)}-${String(i)}`, s)),
    });
    const loop: ExecNode = {
      path: "r/loop",
      label: "tasks",
      kind: "repeat",
      state: "running",
      maxPasses: 20,
      children: [pass(1, ["ok", "ok"]), pass(2, ["ok", "ok", "ok", "running", "pending"])],
    };
    const style = mountAppCSS();
    try {
      host.className = "ev-dock";
      host.style.inlineSize = "40rem";
      view.paint(run([loop]));
      expect(compact(view)).toBe("2/20\u00b74/5");
      const mini = view.root.querySelector<HTMLElement>(".ev-mini")!;
      const segs = [...mini.querySelectorAll<HTMLElement>(".ev-mini-seg")];
      expect(segs.map((s) => s.dataset["state"])).toEqual(["ok", "ok", "ok", "running", "pending"]);
      // A plain run's look: equal segments with a gap between.
      const boxes = segs.map((s) => s.getBoundingClientRect());
      expect(new Set(boxes.map((b) => Math.round(b.width))).size).toBe(1);
      expect(boxes.slice(1).every((b, i) => b.left > boxes[i]!.right)).toBe(true);
    } finally {
      style.remove();
    }
  });

  it("paints a run with no model as its name alone", () => {
    const view = build(true);
    view.paint(run([step("a", "waiting")], { alert: { kind: "paused", text: "Waiting" } }));
    view.paintPending("Workflow run");
    expect(part(view, "ev-sum-label")).toBe("Workflow run");
    expect([part(view, "ev-sum-state"), part(view, "ev-sum-clock")]).toEqual(["", ""]);
    expect(progress(view).hidden).toBe(true);
    expect(host.dataset["alert"]).toBeUndefined();
    expect(view.describe()).toBe("Workflow run");
  });

  it("puts the whole name in the title's tooltip, which a long one needs once it ellipsizes", () => {
    const name = "a-workflow-title-that-is-far-longer-than-any-column-should-ever-hold";
    const view = build(true);
    const title = (): string | null =>
      view.root.querySelector(".ev-sum-label")?.getAttribute("data-tooltip") ?? null;
    view.paintPending(name);
    expect(title()).toBe(name);
    view.paint(run([step("a", "running")], { label: "nightly" }));
    expect(title()).toBe("nightly");
  });
});
