// The tree pane's 1s clock: a running row's duration advances without a re-render, at any depth.
import { afterEach, describe, expect, it, vi } from "vitest";
import { buildExecTree } from "./tree.js";
import type { ExecNode } from "./model.js";

function node(path: string, over: Partial<ExecNode> = {}): ExecNode {
  return { path, label: path, kind: "step", state: "running", children: [], ...over };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("the tree pane's tick", () => {
  it("advances a nested running row's duration and leaves a settled one alone", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-09-20T06:01:00.000Z"));
    const tree = buildExecTree(vi.fn());
    const running = node("k", { start: "2026-09-20T06:00:00.000Z" });
    const settled = node("d", {
      state: "ok",
      start: "2026-09-20T06:00:00.000Z",
      end: "2026-09-20T06:00:10.000Z",
    });
    tree.render([node("g", { kind: "sequence", children: [running, settled] })], "k");
    const dur = (path: string): string =>
      tree.root.querySelector(`.ev-row[data-path="${path}"] .ev-dur`)?.textContent ?? "";
    const before = dur("k");
    const settledBefore = dur("d");

    vi.setSystemTime(new Date("2026-09-20T06:03:00.000Z"));
    tree.tick();

    expect(dur("k")).not.toBe(before);
    expect(dur("k")).toMatch(/^3m/);
    expect(dur("d")).toBe(settledBefore);
  });
});
