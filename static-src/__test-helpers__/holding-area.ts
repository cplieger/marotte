// The chat view's holding area (the run bar, then the steer and queued rows) from the shipped markup, and what
// "the composer stays on screen" means for it.

import { expect } from "vitest";
import indexHtml from "../../static/index.html?raw";
import { buildExecSummary } from "../exec-view/summary.js";
import { chevronEl } from "../chevron.js";
import { mountHoldingArea } from "../holding-area.js";
import { mountSteerStack, type SteerStackSource } from "../pending-steers.js";
import type { ExecRun } from "../exec-view/model.js";
import { LS_UI_STATE_KEY } from "../ls-keys.js";

/** The app's title bar and tab strip above a tab's view. */
export const CHROME_PX = 52;

/** The cap's share of the pane's visible height. */
export const SHARE = 0.4;

/** One transcript line: `--fs-md` (14px) at the message line height of 1.6. */
const LINE_PX = 14 * 1.6;

let frame: HTMLElement | null = null;

/** Removes the view `mountView` placed. */
export function unmountView(): void {
  frame?.remove();
  frame = null;
}

/** One tab view from the shipped markup, under the chrome, in a frame as tall as the viewport. */
export function mountView(height: number, from: string, to: string): HTMLElement {
  const start = indexHtml.indexOf(from);
  const end = indexHtml.indexOf(to, start);
  expect(start, `${from} in index.html`).toBeGreaterThan(-1);
  unmountView();
  frame = document.createElement("main");
  frame.id = "chat-area";
  frame.style.cssText = `display:flex;flex-direction:column;block-size:${String(height)}px`;
  const chrome = document.createElement("div");
  chrome.style.cssText = `flex:none;block-size:${String(CHROME_PX)}px`;
  frame.append(chrome);
  frame.insertAdjacentHTML("beforeend", indexHtml.slice(start, end));
  document.body.prepend(frame);
  return frame;
}

function runOf(i: number): ExecRun {
  return {
    label: `Lanes run ${String(i + 1)}`,
    state: "running",
    live: true,
    nodes: Array.from({ length: 4 }, (_, n) => ({
      path: `r${String(i)}/s${String(n)}`,
      label: `s${String(n)}`,
      kind: "step",
      state: n === 0 ? "ok" : n === 1 ? "running" : "pending",
      children: [],
    })),
  };
}

/** `count` runs as the run bar draws them: the fold row, then a row per run, open or folded. */
function paintRuns(root: HTMLElement, count: number, open: boolean): void {
  if (count === 0) {
    return;
  }
  const fold = root.querySelector<HTMLElement>("#run-bar-fold")!;
  fold.hidden = false;
  const toggle = document.createElement("div");
  toggle.setAttribute("role", "button");
  toggle.tabIndex = 0;
  toggle.className = "run-bar-open run-bar-toggle";
  toggle.setAttribute("aria-expanded", String(open));
  const chev = document.createElement("span");
  chev.className = "run-bar-chev";
  chev.append(chevronEl());
  const text = document.createElement("span");
  text.className = "run-bar-count";
  text.textContent = `${String(count)} workflows running`;
  toggle.append(chev, text);
  fold.append(toggle);
  const bar = root.querySelector<HTMLElement>("#run-bar")!;
  bar.classList.remove("hidden");
  bar.toggleAttribute("data-folded", !open);
  for (let i = 0; i < count; i++) {
    const li = document.createElement("li");
    li.className = "run-bar-row";
    const btn = document.createElement("div");
    btn.setAttribute("role", "button");
    btn.tabIndex = 0;
    btn.className = "run-bar-open";
    const glyph = document.createElement("span");
    glyph.className = "run-bar-glyph";
    glyph.dataset["status"] = "working";
    const summary = buildExecSummary({ alertHost: btn });
    btn.append(glyph, summary.root);
    summary.paint(runOf(i));
    li.append(btn);
    bar.append(li);
  }
}

/** Two frames twice, then every finite animation finished: the area's slides end where they rest. */
export async function settle(): Promise<void> {
  for (let i = 0; i < 2; i++) {
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  }
  for (const a of frame!.getAnimations({ subtree: true })) {
    if (Number.isFinite(a.effect?.getComputedTiming().endTime)) {
      a.finish();
    }
  }
}

export function inside(el: Element, box: DOMRect, what: string, slack = 0.5): void {
  const r = el.getBoundingClientRect();
  expect(r.top, `${what} top`).toBeGreaterThanOrEqual(box.top - slack);
  expect(r.bottom, `${what} bottom`).toBeLessThanOrEqual(box.bottom + slack);
}

export interface Chat {
  pane: HTMLElement;
  area: HTMLElement;
}

/** The chat view under the viewport and styles already in place, over `runs` runs and `src`'s stack. */
export async function mountPane(
  h: number,
  runs: number,
  runsOpen: boolean,
  src: SteerStackSource,
): Promise<Chat> {
  const root = mountView(h, '<div id="chat-view"', '<div id="settings-view"');
  paintRuns(root, runs, runsOpen);
  mountSteerStack(root.querySelector<HTMLUListElement>("#steer-stack")!, src);
  const area = root.querySelector<HTMLElement>("#holding-area")!;
  mountHoldingArea(area);
  await settle();
  return { pane: root.querySelector<HTMLElement>("#chat-view")!, area };
}

/** The reader's stored fold choice for every steer stack mounted after it. */
export function openStack(open: boolean): void {
  localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ stack_open: open }));
}

/** The composer whole inside the pane, three transcript lines visible, the area under its cap, and the
 *  area's end reachable in order by scrolling it. */
export function holds(chat: Chat, visibleH: number): void {
  const pane = chat.pane.getBoundingClientRect();
  const visible = new DOMRect(pane.left, pane.top, pane.width, visibleH);
  inside(document.querySelector("#prompt-box")!, visible, "the composer");
  inside(document.querySelector("#prompt-input")!, visible, "the message box");
  expect(
    document.querySelector("#messages-wrap-outer")!.getBoundingClientRect().height,
    "three transcript lines",
  ).toBeGreaterThanOrEqual(3 * LINE_PX);
  expect(chat.area.getBoundingClientRect().height, "the area's cap").toBeLessThanOrEqual(
    SHARE * visibleH + 0.5,
  );

  chat.area.scrollTop = chat.area.scrollHeight;
  const box = chat.area.getBoundingClientRect();
  const lines = [
    ...chat.area.querySelectorAll<HTMLElement>(".run-bar-fold, .run-bar > li, .steer-stack > li"),
  ].filter((l) => l.checkVisibility({ visibilityProperty: true }));
  const tops = lines.map((l) => l.getBoundingClientRect().top);
  for (let i = 1; i < tops.length; i++) {
    expect(tops[i], `line ${String(i)} below line ${String(i - 1)}`).toBeGreaterThan(tops[i - 1]!);
  }
  // Chromium's scroll range is `scrollHeight - clientHeight`, each rounded to a whole pixel, so a fractional
  // box scrolled to its end can leave up to a pixel of its last line past the edge.
  const last = lines.at(-1);
  if (last !== undefined) {
    inside(last, box, "the area's last line, scrolled to", 1);
  }
}
