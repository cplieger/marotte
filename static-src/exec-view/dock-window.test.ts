// The dock's Steps and Timeline panes hold what their box shows: nothing while the panel is
// closed, and a window of rows around the visible range while it is open.
import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach, vi } from "vitest";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";
import { settledOpen } from "../__test-helpers__/exec-dock.js";
import { buildExecPage, type ExecPageView } from "./page.js";
import type { ExecNode, ExecRun } from "./model.js";

const STEPS = 10_000;
/** The steps in flight: ten at the top of the tree, inside the window, and two far below it, about
 *  the parallelism of an eleven-lane run. */
const RUNNING = new Set([0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 5_000, 9_999]);
/** The pane's box, about 20 rows tall; the window may add an overscan of the same order. */
const BOX_PX = 20 * 28;
const WINDOW_LIMIT = 60;

function bigRun(): ExecRun {
  const nodes = Array.from({ length: STEPS }, (_, i): ExecNode => ({
    path: `root/s${String(i)}`,
    label: `step ${String(i)}`,
    kind: "step",
    state: RUNNING.has(i) ? "running" : "ok",
    start: "2026-11-10T10:00:00.000Z",
    ...(RUNNING.has(i) ? {} : { end: "2026-11-10T10:00:10.000Z" }),
    startGen: i + 1,
    children: [],
  }));
  return { label: "big", state: "running", nodes, live: true };
}

/** The same number of steps, nested: a hundred top-level boxes of a hundred steps. */
function nestedRun(): ExecRun {
  const groups = Array.from({ length: 100 }, (_, g): ExecNode => ({
    path: `root/g${String(g)}`,
    label: `group ${String(g)}`,
    kind: "sequence",
    state: "ok",
    children: Array.from({ length: 100 }, (_, i): ExecNode => ({
      path: `root/g${String(g)}/s${String(i)}`,
      label: `step ${String(i)}`,
      kind: "step",
      state: "ok",
      start: "2026-11-10T10:00:00.000Z",
      end: "2026-11-10T10:00:10.000Z",
      children: [],
    })),
  }));
  return { label: "nested", state: "ok", nodes: groups, live: false };
}

let css: HTMLStyleElement;
let host: HTMLElement;
let page: ExecPageView;

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

const treeRows = (): number => document.querySelectorAll(".ev-row[data-path]").length;
const lanes = (): number => document.querySelectorAll(".ev-tl-lane").length;

function openPanel(): void {
  host.querySelector<HTMLButtonElement>(".ev-box-toggle")?.click();
  const body = host.querySelector<HTMLElement>(".ev-dock-body");
  if (body === null) {
    throw new Error("no dock body");
  }
  body.style.height = `${String(BOX_PX)}px`;
  body.style.maxHeight = `${String(BOX_PX)}px`;
}

describe("a 10,000-step run in the dock", () => {
  it("renders no tree row and no timeline lane while the panel is closed", () => {
    page.render(bigRun());

    expect(treeRows()).toBe(0);
    expect(lanes()).toBe(0);
  });

  it("seats a window of tree rows when the panel opens on Steps", () => {
    page.render(bigRun());
    openPanel();
    page.render(bigRun());

    expect(treeRows()).toBeGreaterThan(0);
    expect(treeRows()).toBeLessThanOrEqual(WINDOW_LIMIT);
  });

  it("seats a window of timeline lanes when the panel shows the Timeline", () => {
    page.render(bigRun());
    openPanel();
    host.querySelector<HTMLButtonElement>('[data-ev-seg="timeline"]')?.click();
    page.render(bigRun());

    expect(lanes()).toBeGreaterThan(0);
    expect(lanes()).toBeLessThanOrEqual(WINDOW_LIMIT);
  });

  it("ticks only the rows the window holds", async () => {
    page.render(bigRun());
    openPanel();
    page.render(bigRun());

    expect(await tickRecords()).toBeLessThanOrEqual(WINDOW_LIMIT * 4);
  });

  it("ticks only the lanes the window holds", async () => {
    page.render(bigRun());
    openPanel();
    host.querySelector<HTMLButtonElement>('[data-ev-seg="timeline"]')?.click();
    page.render(bigRun());

    expect(await tickRecords()).toBeLessThanOrEqual(WINDOW_LIMIT * 4);
  });
});

describe("scrolling the open panel", () => {
  const body = (): HTMLElement => host.querySelector<HTMLElement>(".ev-dock-body")!;
  const frames = async (n: number): Promise<void> => {
    for (let i = 0; i < n; i++) {
      await new Promise<void>((resolve) => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    }
  };
  /** The paths of the rows whose line the box shows. */
  const inBox = (sel: string): string[] => {
    const box = body().getBoundingClientRect();
    return [...host.querySelectorAll<HTMLElement>(sel)]
      .filter((r) => {
        const line = (r.firstElementChild ?? r).getBoundingClientRect();
        return line.bottom > box.top && line.top < box.bottom;
      })
      .map((r) => r.dataset["path"] ?? "");
  };

  it("seats the rows of a nested tree it scrolls to, and then holds still", async () => {
    page.render(nestedRun());
    openPanel();
    await settledOpen(host);
    body().scrollTop = body().scrollHeight / 2;
    await frames(2);

    // Half way down a hundred equal boxes is the end of the 50th or the start of the 51st.
    const shown = inBox('.ev-kids > .ev-row[data-kind="step"]');
    expect(shown.length).toBeGreaterThan(0);
    expect(
      shown.every((p) => /^root\/g(49|50)\//.test(p)),
      shown.join(),
    ).toBe(true);
    expect(treeRows()).toBeLessThanOrEqual(WINDOW_LIMIT);
    const records: MutationRecord[] = [];
    const observer = new MutationObserver((r) => records.push(...r));
    observer.observe(host, {
      subtree: true,
      childList: true,
      characterData: true,
      attributes: true,
    });
    await frames(5);
    records.push(...observer.takeRecords());
    observer.disconnect();
    expect(records).toHaveLength(0);
  });

  it("seats the last lanes when the timeline scrolls to its end", async () => {
    page.render(bigRun());
    openPanel();
    host.querySelector<HTMLButtonElement>('[data-ev-seg="timeline"]')?.click();
    await settledOpen(host);
    body().scrollTop = body().scrollHeight;
    await frames(2);

    expect(inBox(".ev-tl-lane")).toContain(`root/s${String(STEPS - 1)}`);
    expect(lanes()).toBeLessThanOrEqual(WINDOW_LIMIT);
  });
});

/** The mutation records one second of the page's clock writes into the dock. */
async function tickRecords(): Promise<number> {
  const records: MutationRecord[] = [];
  const observer = new MutationObserver((r) => records.push(...r));
  observer.observe(host, { subtree: true, childList: true, characterData: true, attributes: true });

  await vi.advanceTimersByTimeAsync(1000);
  records.push(...observer.takeRecords());
  observer.disconnect();
  return records.length;
}
