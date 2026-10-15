import { describe, it, expect, afterEach } from "vitest";
import extendedLoopRaw from "../testdata/run-extended-loop.json?raw";
import { runToExec } from "../run-exec-source.js";
import type { RunState } from "../run-store.js";
import { buildExecTimeline, type ExecTimelineView } from "./timeline.js";
import type { ExecNode } from "./model.js";

const NO_ASKS = { count: 0, asked: [], label: "" };

/** `testdata/run-extended-loop.json`: `wait`, seven passes of `build-loop` (build, two reviews,
 *  aggregate), then `land`. A resume restamped pass 7's container to 21:19-21:24, while its build
 *  ran from 16:54. */
function fixture(): ExecNode[] {
  const raw = JSON.parse(extendedLoopRaw) as { state: RunState; nodePlan: unknown };
  return runToExec(raw.state, raw.nodePlan, NO_ASKS).nodes;
}

let view: ExecTimelineView;

function mount(): ExecTimelineView {
  view = buildExecTimeline(() => undefined);
  document.body.append(view.root);
  return view;
}

afterEach(() => {
  view.root.remove();
});

/** Every row in order: `head:<name> <span>` for a pass header, `lane:<name>` for a lane. */
function rows(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>(".ev-tl-lanes > *")].map((r) =>
    r.classList.contains("ev-tl-pass")
      ? `head:${r.querySelector(".ev-tl-pass-name")?.textContent ?? ""} ${r.querySelector(".ev-tl-dur")?.textContent ?? ""}`
      : `lane:${r.querySelector(".ev-tl-name")?.textContent ?? ""}`,
  );
}

describe("the timeline's loop passes", () => {
  it("heads each pass's lanes with its number and its steps' span, in pass order", () => {
    const t = mount();
    t.render(fixture(), "", false);
    const all = rows(t.root);
    expect(all.filter((r) => r.startsWith("head:"))).toEqual([
      "head:pass 1 1h 46m",
      "head:pass 2 47m 49s",
      "head:pass 3 1h 25m",
      "head:pass 4 1h 13m",
      "head:pass 5 1h 1m",
      "head:pass 6 25m 28s",
      "head:pass 7 4h 30m",
    ]);
    expect(all.slice(0, 6)).toEqual([
      "lane:wait",
      "head:pass 1 1h 46m",
      "lane:build",
      "lane:review-claude",
      "lane:review-gpt",
      "lane:aggregate",
    ]);
    expect(all.at(-1)).toBe("lane:land");
  });

  it("names each lane's pass, and none for a step outside the loop", () => {
    const t = mount();
    t.render(fixture(), "", false);
    const name = (suffix: string): string | null =>
      t.root.querySelector(`.ev-tl-lane[data-path$="${suffix}"]`)?.getAttribute("aria-label") ??
      null;
    expect(name(":iter-0:build")).toMatch(/^build, pass 1, succeeded, /);
    expect(name(":iter-6:build")).toMatch(/^build, pass 7, succeeded, /);
    expect(name(":land")).toMatch(/^land, succeeded, /);
  });

  it("names the innermost pass of a lane inside nested loops", () => {
    const node = (path: string, kind: ExecNode["kind"], children: ExecNode[] = []): ExecNode => ({
      path,
      label: path.split(":").at(-1) ?? path,
      kind,
      state: "running",
      children,
    });
    const inner = node("o:i", "repeat", [
      { ...node("o:i:p3", "sequence", [node("o:i:p3:try", "step")]), pass: 3 },
    ]);
    const outer = node("o", "repeat", [{ ...node("o:p2", "sequence", [inner]), pass: 2 }]);
    const t = mount();
    t.render([outer], "", true);
    expect(
      t.root.querySelector('.ev-tl-lane[data-path="o:i:p3:try"]')?.getAttribute("aria-label"),
    ).toMatch(/^try, pass 3, running/);
  });

  it("leaves a header out of the tab order and the button roles", () => {
    const t = mount();
    t.render(fixture(), "", false);
    const head = t.root.querySelector(".ev-tl-pass")!;
    expect([head.getAttribute("role"), head.getAttribute("tabindex")]).toEqual([null, null]);
  });

  it("keeps a focused lane, and every header, across a repaint", () => {
    const t = mount();
    t.render(fixture(), "", false);
    const heads = [...t.root.querySelectorAll(".ev-tl-pass")];
    const lane = t.root.querySelector<HTMLElement>('.ev-tl-lane[data-path$=":iter-6:build"]')!;
    lane.focus();
    t.render(fixture(), lane.dataset["path"] ?? "", false);
    expect(document.activeElement).toBe(lane);
    const again = [...t.root.querySelectorAll(".ev-tl-pass")];
    expect(again.length).toBe(heads.length);
    again.forEach((h, i) => {
      expect(h).toBe(heads[i]);
    });
  });
});
