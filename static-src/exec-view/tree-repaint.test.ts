// A repaint with nothing changed writes nothing: every row and lane write is a changed value.
import { afterEach, describe, expect, it, vi } from "vitest";
import { buildExecTree } from "./tree.js";
import { buildExecTimeline } from "./timeline.js";
import type { ExecNode } from "./model.js";
import type { ExecState } from "./status.js";

const STATES: readonly ExecState[] = ["ok", "fail", "running", "pending", "skipped", "input"];

function rows(): ExecNode[] {
  return Array.from({ length: 10 }, (_, g) => ({
    path: `g${String(g)}`,
    label: `group ${String(g)}`,
    kind: "sequence",
    state: "ok",
    start: "2026-11-10T10:00:00.000Z",
    end: "2026-11-10T10:05:00.000Z",
    children: Array.from({ length: 10 }, (_, s): ExecNode => {
      const state = STATES[(g * 10 + s) % STATES.length] ?? "ok";
      return {
        path: `g${String(g)}:s${String(s)}`,
        label: `step ${String(s)}`,
        kind: "step",
        state,
        subtitle: s % 2 === 0 ? "a subtitle" : undefined,
        ...(state === "pending" ? {} : { start: "2026-11-10T10:00:00.000Z" }),
        ...(state === "running" || state === "pending" ? {} : { end: "2026-11-10T10:01:00.000Z" }),
        children: [],
      } as ExecNode;
    }),
  }));
}

/** The mutation records `act` causes under `root`. */
function recordsOf(root: HTMLElement, act: () => void): MutationRecord[] {
  const observer = new MutationObserver(() => undefined);
  observer.observe(root, { subtree: true, childList: true, attributes: true, characterData: true });
  act();
  const out = observer.takeRecords();
  observer.disconnect();
  return out;
}

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

describe("a repaint of unchanged nodes", () => {
  it("writes nothing into the tree", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-10T10:10:00.000Z"));
    const tree = buildExecTree(vi.fn());
    document.body.append(tree.root);
    tree.render(rows(), "g0:s1");

    expect(recordsOf(tree.root, () => tree.render(rows(), "g0:s1"))).toEqual([]);
  });

  it("writes nothing into the timeline", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-10T10:10:00.000Z"));
    const timeline = buildExecTimeline(vi.fn());
    document.body.append(timeline.root);
    timeline.render(rows(), "g0:s1", true);

    expect(recordsOf(timeline.root, () => timeline.render(rows(), "g0:s1", true))).toEqual([]);
  });
});
