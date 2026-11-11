// A live run page's 1s clock moves the clocks it shows from the last paint's model, never by
// walking the run again: the walk is O(nodes), the clock O(what is on screen).
import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach, vi } from "vitest";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";
import { openDockPanel } from "../__test-helpers__/exec-dock.js";
import type { ExecNode, ExecRun } from "./model.js";

// Called through, so a tick that walks the run is countable.
vi.mock("./model.js", { spy: true });

const model = await import("./model.js");
const { buildExecPage } = await import("./page.js");

const STEPS = 10_000;

function bigRun(): ExecRun {
  const nodes = Array.from({ length: STEPS }, (_, i): ExecNode => {
    const live = i === STEPS - 1;
    return {
      path: `root/s${String(i)}`,
      label: `step ${String(i)}`,
      kind: "step",
      state: live ? "running" : "ok",
      start: "2026-11-10T10:00:00.000Z",
      ...(live ? {} : { end: "2026-11-10T10:00:10.000Z" }),
      startGen: i + 1,
      children: [],
    };
  });
  return { label: "big", state: "running", nodes, live: true };
}

/** Every model walk a paint makes over the whole run. */
const walks = [
  model.currentWork,
  model.miniBar,
  model.summaryProgress,
  model.window,
  model.flatten,
  model.workNodes,
] as const;

let css: HTMLStyleElement;
let host: HTMLElement;
let page: ReturnType<typeof buildExecPage>;

beforeAll(() => {
  css = mountAppCSS();
});

afterAll(() => {
  css.remove();
});

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
  vi.setSystemTime(new Date("2026-11-10T10:00:30.000Z"));
  host = document.createElement("div");
  host.className = "ev-dock-host";
  page = buildExecPage({ emptyNote: () => "", dock: host, follow: "newest" });
  document.body.append(page.root, host);
});

afterEach(() => {
  vi.useRealTimers();
  page.dispose();
  page.root.remove();
  host.remove();
});

const clock = (): string => host.querySelector(".ev-sum-clock")?.textContent ?? "";

describe("a live 10,000-step run page's clock", () => {
  it("moves the dock's clocks without walking the run", () => {
    page.render(bigRun());
    const before = clock();
    for (const f of walks) {
      vi.mocked(f).mockClear();
    }

    vi.advanceTimersByTime(10_000);

    expect(clock()).not.toBe(before);
    expect(walks.map((f) => vi.mocked(f).mock.calls.length)).toEqual(walks.map(() => 0));
  });

  it("moves the open timeline's bars without walking the run", () => {
    page.render(bigRun());
    openDockPanel(host, "timeline");
    page.render(bigRun());
    for (const f of walks) {
      vi.mocked(f).mockClear();
    }

    vi.advanceTimersByTime(10_000);

    expect(walks.map((f) => vi.mocked(f).mock.calls.length)).toEqual(walks.map(() => 0));
  });
});
