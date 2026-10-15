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

let wash: HTMLElement;

beforeEach(() => {
  wash = document.createElement("div");
  document.body.append(wash);
});

afterEach(() => {
  wash.remove();
});

function build(live: boolean): ExecSummaryView {
  const view = buildExecSummary({ washHost: wash, live });
  wash.append(view.root);
  return view;
}

const part = (view: ExecSummaryView, cls: string): string =>
  view.root.querySelector(`.${cls}`)?.textContent ?? "";

describe("the run summary", () => {
  it("states the name, the state word and where the run stands", () => {
    const view = build(true);
    view.paint(run([step("a", "ok"), step("b", "running"), step("c", "pending")]));
    expect([
      part(view, "ev-sum-label"),
      part(view, "ev-sum-state"),
      part(view, "ev-sum-progress"),
    ]).toEqual(["nightly", "running", "step 2 of 3"]);
    expect(view.describe()).toBe("nightly, running, step 2 of 3");
  });

  describe("in a loop", () => {
    const loopRun = (): ExecRun =>
      run([
        {
          path: "r/loop",
          label: "loop",
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

    it("states the pass in both forms and in the accessible name", () => {
      const view = build(true);
      view.paint(loopRun());
      expect(view.root.querySelector(".ev-sum-progress")?.hasAttribute("data-loop")).toBe(true);
      expect([part(view, "ev-sum-long"), part(view, "ev-sum-short")]).toEqual([
        "pass 3 of 8 \u00b7 step 2 of 4",
        "3/8 \u00b7 2 of 4",
      ]);
      expect(view.root.querySelector(".ev-sum-short")?.getAttribute("aria-hidden")).toBe("true");
      expect(view.root.querySelector(".ev-sum-short svg")).not.toBeNull();
      expect(view.describe()).toBe("nightly, running, pass 3 of 8, step 2 of 4");
    });

    it("withholds the pass outside a loop", () => {
      const view = build(true);
      view.paint(loopRun());
      view.paint(run([step("a", "ok"), step("b", "running")]));
      expect(view.root.querySelector(".ev-sum-progress")?.hasAttribute("data-loop")).toBe(false);
      expect([part(view, "ev-sum-long"), part(view, "ev-sum-short")]).toEqual(["step 2 of 2", ""]);
      expect(view.describe()).toBe("nightly, running, step 2 of 2");
    });

    it("writes nothing on a repaint that changes nothing", async () => {
      const view = build(true);
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

    for (const [width, narrow] of [
      ["22rem", true],
      ["40rem", false],
    ] as const) {
      it(`shows the ${narrow ? "glyph" : "worded"} form in a ${width} dock`, () => {
        const style = mountAppCSS();
        try {
          wash.className = "ev-dock";
          wash.style.inlineSize = width;
          const view = build(true);
          view.paint(loopRun());
          const short = view.root.querySelector<HTMLElement>(".ev-sum-short")!;
          const long = view.root.querySelector<HTMLElement>(".ev-sum-long")!;
          expect(getComputedStyle(short).display === "none").toBe(!narrow);
          expect(long.getBoundingClientRect().width > 1).toBe(!narrow);
        } finally {
          style.remove();
        }
      });
    }
  });

  it("withholds the progress of a one-step run", () => {
    const view = build(true);
    view.paint(run([step("a", "running")]));
    expect(part(view, "ev-sum-progress")).toBe("");
    expect(view.describe()).toBe("nightly, running");
  });

  it("says needs input while an ask blocks the run", () => {
    const view = build(true);
    view.paint(
      run([step("a", "input")], {
        state: "input",
        alert: { kind: "input", text: "Waiting for answer" },
      }),
    );
    expect(part(view, "ev-sum-state")).toBe("needs input");
  });

  for (const kind of ["input", "paused", "failed"] as const) {
    it(`gives a ${kind} alert the summary's place and its kind to the wash host`, () => {
      const view = build(true);
      view.paint(run([step("a", "waiting")], { alert: { kind, text: `about ${kind}` } }));
      expect(wash.dataset["alert"]).toBe(kind);
      expect(view.root.hasAttribute("data-alerting")).toBe(true);
      const line = view.root.querySelector(".ev-alert-line");
      expect(line?.textContent).toBe(`about ${kind}`);
      expect(line?.parentElement?.getAttribute("role")).toBe(
        kind === "failed" ? "alert" : "status",
      );
      expect(view.describe()).toBe(`nightly: about ${kind}`);
    });
  }

  it("moves the line between the two regions as the kind changes", () => {
    const view = build(true);
    view.paint(run([step("a", "waiting")], { alert: { kind: "paused", text: "Waiting" } }));
    view.paint(run([step("a", "fail")], { alert: { kind: "failed", text: "a failed" } }));
    const [status, alert] = ["status", "alert"].map((r) =>
      view.root.querySelector(`[role="${r}"]`),
    );
    expect(status?.childElementCount).toBe(0);
    expect(alert?.textContent).toBe("a failed");
  });

  it("writes a failure only into the urgent region when a pause turns into it", async () => {
    const view = build(true);
    view.paint(run([step("a", "waiting")], { alert: { kind: "paused", text: "Waiting" } }));
    const status = view.root.querySelector('[role="status"]')!;
    const urgent = view.root.querySelector('[role="alert"]')!;
    const seen: string[] = [];
    const watch = (host: Element, name: string): MutationObserver => {
      const mo = new MutationObserver((records) => {
        for (const r of records) {
          const added = [...r.addedNodes].map((n) => n.textContent).join("");
          const removed = r.removedNodes.length > 0 ? "-" : "";
          seen.push(`${name}:${removed}${added}`);
        }
      });
      mo.observe(host, { childList: true, subtree: true, characterData: true });
      return mo;
    };
    const observers = [watch(status, "status"), watch(urgent, "alert")];
    view.paint(run([step("a", "fail")], { alert: { kind: "failed", text: "a failed" } }));
    await Promise.resolve();
    for (const mo of observers) {
      mo.disconnect();
    }
    expect(seen).toEqual(["status:-", "alert:a failed"]);
  });

  it("keeps both empty regions rendered, so the first line is announced", () => {
    const style = mountAppCSS();
    try {
      const view = build(true);
      view.paint(run([step("a", "running"), step("b", "pending")]));
      for (const role of ["status", "alert"]) {
        const region = view.root.querySelector<HTMLElement>(`[role="${role}"]`)!;
        expect(region.childElementCount).toBe(0);
        expect(getComputedStyle(region).display, role).not.toBe("none");
        expect(region.checkVisibility(), role).toBe(true);
      }
    } finally {
      style.remove();
    }
  });

  it("leaves a stopped alert to the consumer, keeping the summary", () => {
    const view = build(true);
    view.paint(run([step("a", "running")], { alert: { kind: "stopped", text: "Stopped by you" } }));
    expect(wash.dataset["alert"]).toBeUndefined();
    expect(view.root.hasAttribute("data-alerting")).toBe(false);
    expect(view.root.textContent).not.toContain("Stopped by you");
  });

  it("clears the alert once it goes", () => {
    const view = build(true);
    view.paint(run([step("a", "waiting")], { alert: { kind: "paused", text: "Waiting" } }));
    view.paint(run([step("a", "running")]));
    expect(wash.dataset["alert"]).toBeUndefined();
    expect(view.root.querySelector(".ev-alert-line")).toBeNull();
  });

  it("uses no live role on a row that already announces elsewhere", () => {
    const view = build(false);
    view.paint(run([step("a", "fail")], { alert: { kind: "failed", text: "a failed" } }));
    expect(view.root.querySelector("[role]")).toBeNull();
    expect(view.root.querySelector(".ev-alert-line")?.textContent).toBe("a failed");
  });

  it("paints a run with no model as its name alone", () => {
    const view = build(true);
    view.paint(run([step("a", "waiting")], { alert: { kind: "paused", text: "Waiting" } }));
    view.paintPending("Workflow run");
    expect(part(view, "ev-sum-label")).toBe("Workflow run");
    expect([
      part(view, "ev-sum-state"),
      part(view, "ev-sum-progress"),
      part(view, "ev-sum-clock"),
    ]).toEqual(["", "", ""]);
    expect(wash.dataset["alert"]).toBeUndefined();
    expect(view.describe()).toBe("Workflow run");
  });
});
