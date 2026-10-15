// The run tab's layout as `static/index.html` ships it, under the shipped stylesheet: the run dock
// and its verbs stay on screen at the end of a long run, and the composer stays on a phone with
// the keyboard up, the panel open and an ask showing.

import { describe, it, expect, afterEach } from "vitest";
import { page as browser } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import type { ExecNode, ExecRun } from "./exec-view/model.js";

/** The app's chrome above a tab's view on a phone: the toolbar and the tab strip. */
const CHROME_PX = 88;

let style: HTMLStyleElement | null = null;
let view: ExecPageView | null = null;
let frame: HTMLElement | null = null;

afterEach(async () => {
  view?.dispose();
  view = null;
  frame?.remove();
  frame = null;
  style?.remove();
  style = null;
  document.documentElement.removeAttribute("data-pointer");
  document.documentElement.style.removeProperty("--dock-viewport-h");
  await browser.viewport(1280, 720);
});

function fan(): ExecNode[] {
  return ["a", "b", "c", "d"].map((id, i) => ({
    path: `root/review/${id}`,
    label: `review/${id}`,
    kind: "step",
    state: "running",
    children: [],
    tail: "Reading the diff",
    start: new Date(Date.parse("2026-10-07T10:00:00Z") + i * 60_000).toISOString(),
    startGen: i + 1,
  }));
}

/** The run view from the shipped markup in a frame as tall as the visible viewport. */
function mountRunView(height: number): { runView: HTMLElement; byId: (id: string) => HTMLElement } {
  const start = indexHtml.indexOf('<div id="run-view"');
  const end = indexHtml.indexOf("<!-- One Kiro spec", start);
  expect(start, "#run-view in index.html").toBeGreaterThan(-1);
  frame = document.createElement("div");
  frame.style.cssText = `display:flex;flex-direction:column;block-size:${String(height)}px`;
  const chrome = document.createElement("div");
  chrome.style.cssText = `flex:none;block-size:${String(CHROME_PX)}px`;
  const slot = document.createElement("div");
  slot.style.cssText = "flex:1;min-block-size:0;display:flex;flex-direction:column";
  slot.innerHTML = indexHtml.slice(start, end);
  frame.append(chrome, slot);
  document.body.prepend(frame);
  const runView = slot.querySelector<HTMLElement>("#run-view")!;
  runView.classList.remove("hidden");
  const byId = (id: string): HTMLElement => {
    const found = runView.querySelector<HTMLElement>(`#${id}`);
    if (found === null) {
      throw new Error(`no #${id} in the run view`);
    }
    return found;
  };
  return { runView, byId };
}

function verbs(withField: boolean): HTMLElement {
  const row = document.createElement("span");
  row.className = "run-controls";
  if (withField) {
    const field = Object.assign(document.createElement("input"), { type: "number", value: "1" });
    field.className = "tool-form-input run-extend-count";
    row.append(field);
  }
  for (const name of ["Add iterations", "End this loop", "Cancel"].slice(withField ? 0 : 1)) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "icon-btn run-icon-btn";
    b.setAttribute("aria-label", name);
    b.textContent = "·";
    row.append(b);
  }
  return row;
}

function mountPage(byId: (id: string) => HTMLElement, controls: HTMLElement): ExecPageView {
  view = buildExecPage({
    emptyNote: () => "",
    dock: byId("run-exec-dock"),
    controls: () => controls,
    follow: "newest",
  });
  byId("run-body").append(view.root);
  return view;
}

/** The panel opens through a height transition; measure the open state, not a frame of it. */
async function settled(region: Element): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  await Promise.all(region.getAnimations().map((a) => a.finished));
  expect(region.getAttribute("aria-hidden")).toBe("false");
}

function inside(el: Element, height: number, what: string): void {
  const r = el.getBoundingClientRect();
  expect(r.top, `${what} top`).toBeGreaterThanOrEqual(CHROME_PX - 0.5);
  expect(r.bottom, `${what} bottom`).toBeLessThanOrEqual(height + 0.5);
}

describe("the run tab's layout", () => {
  it("keeps the dock and every verb on screen at the end of a long run", async () => {
    for (const [w, h] of [
      [390, 844],
      [820, 1180],
      [1280, 800],
    ] as const) {
      await browser.viewport(w, h);
      style ??= mountAppCSS();
      const { byId, runView } = mountRunView(h);
      const row = verbs(false);
      const run: ExecRun = {
        id: "wf",
        label: "nightly",
        state: "running",
        nodes: fan(),
        live: true,
      };
      mountPage(byId, row).render(run);
      const filler = document.createElement("div");
      filler.style.blockSize = "6000px";
      byId("run-body").append(filler);
      const scroller = runView.querySelector<HTMLElement>(".page-content")!;
      scroller.scrollTop = scroller.scrollHeight;
      expect(scroller.scrollTop, `scrolled at ${String(w)}`).toBeGreaterThan(1000);
      inside(byId("run-exec-dock").querySelector(".ev-dock-head")!, h, `head at ${String(w)}`);
      for (const b of row.querySelectorAll("button")) {
        inside(b, h, `${b.getAttribute("aria-label") ?? ""} at ${String(w)}`);
      }
      inside(byId("run-composer"), h, `composer at ${String(w)}`);
      view?.dispose();
      view = null;
      frame?.remove();
      frame = null;
    }
  });

  it("keeps the composer on a phone with the keyboard up, the panel open and an ask showing", async () => {
    const vv = 464;
    await browser.viewport(390, 844);
    document.documentElement.setAttribute("data-pointer", "coarse");
    style = mountAppCSS();
    const { byId } = mountRunView(vv);
    const nodes = [
      ...fan(),
      ...Array.from({ length: 30 }, (_, i) => ({
        ...fan()[0]!,
        path: `root/s${String(i)}`,
        label: `s${String(i)}`,
        state: "ok" as const,
      })),
    ];
    mountPage(byId, verbs(true)).render({
      id: "wf",
      label: "nightly",
      state: "waiting",
      nodes,
      live: true,
      alert: {
        kind: "paused",
        text: "Waiting: review reached its 3-iteration limit · maxIterations",
      },
      notice: { text: "Plan revised: 2 steps queued after review · applied", failed: false },
    });
    byId("run-exec-dock").querySelector<HTMLButtonElement>(".ev-box-toggle")!.click();
    await settled(byId("run-exec-dock").querySelector(".ev-dock-panel")!);
    // What `dock-ask.ts` publishes from `visualViewport` once a raised keyboard shrinks it;
    // written after the frames that publish the unshrunk one.
    document.documentElement.style.setProperty("--dock-viewport-h", `${String(vv)}px`);

    const ask = byId("run-dock");
    ask.classList.remove("hidden");
    const card = document.createElement("div");
    card.className = "dock-card dock-permission";
    const question = document.createElement("div");
    question.className = "approval-body";
    question.textContent = "Allow the step to run `npm test` in the repository?";
    const actions = document.createElement("div");
    actions.className = "approval-actions";
    for (const name of ["Allow", "Deny"]) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "btn-small";
      b.textContent = name;
      actions.append(b);
    }
    card.append(question, actions);
    ask.append(card);
    byId("run-composer-input").focus();

    inside(byId("run-composer"), vv, "composer");
    for (const b of actions.querySelectorAll("button")) {
      inside(b, vv, `ask ${b.textContent}`);
    }
    inside(byId("run-exec-dock").querySelector(".ev-dock-head")!, vv, "dock head");
    // The open panel is what gives way to the ask; once the ask goes, it has room for rows again.
    const body = byId("run-exec-dock").querySelector(".ev-dock-body")!;
    expect(getComputedStyle(body).overflowY).toBe("auto");
    ask.replaceChildren();
    ask.classList.add("hidden");
    inside(byId("run-composer"), vv, "composer without the ask");
    expect(body.getBoundingClientRect().height, "the open panel's body").toBeGreaterThan(80);
  });
});
