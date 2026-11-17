// The hover wash on a run summary box's header row and on its icon buttons, in the run tab's dock and in the
// chat's run bar, read off a real mouse's hover under the shipped stylesheet. The box is 12px with a 1px border
// and a 4px inset, so a line of it rounds at 7px (`--stack-in-r`).

import { describe, it, expect, afterEach } from "vitest";
import { page as browser, userEvent } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import type { ExecNode } from "./exec-view/model.js";
import { CONTROL_LABEL, type RunVerb } from "./run-controls.js";

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
  await browser.viewport(1280, 720);
});

/** The four corner radii in px, top-left first. */
function corners(el: Element): number[] {
  const cs = getComputedStyle(el);
  return [
    cs.borderTopLeftRadius,
    cs.borderTopRightRadius,
    cs.borderBottomRightRadius,
    cs.borderBottomLeftRadius,
  ].map((v) => Number.parseFloat(v));
}

/** Hover `target` with the real mouse and answer `painter` once its wash has settled. */
async function hovered(target: Element, painter: Element): Promise<Element> {
  await userEvent.hover(target);
  await Promise.all(painter.getAnimations().map((a) => a.finished));
  expect(target.matches(":hover"), "under the mouse").toBe(true);
  expect(getComputedStyle(painter).backgroundColor, "the hover paints its wash").not.toBe(
    "rgba(0, 0, 0, 0)",
  );
  return painter;
}

const VERBS: readonly RunVerb[] = ["pause", "resume", "retry", "extend", "finish_loop", "cancel"];

/** Every verb the dock head can draw, as `run-controls-view.ts` builds them. */
function verbs(): HTMLElement {
  const row = document.createElement("span");
  row.className = "run-controls";
  for (const verb of VERBS) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = verb === "cancel" ? "icon-btn run-icon-btn is-danger" : "icon-btn run-icon-btn";
    b.setAttribute("aria-label", CONTROL_LABEL[verb]);
    b.textContent = "·";
    if (verb === "extend") {
      const extend = document.createElement("span");
      extend.className = "run-extend";
      const field = Object.assign(document.createElement("input"), { type: "number", value: "1" });
      field.className = "tool-form-input run-extend-count";
      extend.append(field, b);
      row.append(extend);
    } else {
      row.append(b);
    }
  }
  return row;
}

/** The run tab from the shipped markup, its dock painted for a running run with every verb. */
function mountDock(): HTMLElement {
  const start = indexHtml.indexOf('<div id="run-view"');
  const end = indexHtml.indexOf("<!-- One Kiro spec", start);
  expect(start, "#run-view in index.html").toBeGreaterThan(-1);
  frame = document.createElement("div");
  frame.style.cssText = "display:flex;flex-direction:column;block-size:900px";
  frame.innerHTML = indexHtml.slice(start, end);
  document.body.prepend(frame);
  const runView = frame.querySelector<HTMLElement>("#run-view")!;
  runView.classList.remove("hidden");
  const controls = verbs();
  view = buildExecPage({
    emptyNote: () => "",
    dock: runView.querySelector<HTMLElement>("#run-exec-dock")!,
    controls: () => controls,
    follow: "newest",
  });
  runView.querySelector("#run-body")!.append(view.root);
  const step: ExecNode = {
    path: "root/build",
    label: "build",
    kind: "step",
    state: "running",
    children: [],
    start: "2026-10-07T10:00:00Z",
  };
  view.render({ label: "nightly", state: "running", nodes: [step], live: true });
  return runView.querySelector<HTMLElement>(".ev-dock-head")!;
}

describe("the run tab dock's header row", () => {
  // The verbs' corner is the line's 7px less their 2px block inset in a 36px head on a mouse, and less 6px in
  // a 44px head under a finger.
  it.each([
    ["fine", 7, 5],
    ["coarse", 7, 1],
  ] as const)("rounds the row's and every verb's hover on the %s tier", async (tier, row, verb) => {
    await browser.viewport(1440, 900);
    document.documentElement.setAttribute("data-pointer", tier);
    style = mountAppCSS();
    const head = mountDock();
    const toggle = head.querySelector(".ev-box-toggle")!;
    expect(
      corners(await hovered(head.querySelector(".ev-sum-label")!, toggle)),
      "header row",
    ).toEqual([row, row, row, row]);
    const buttons = [...head.querySelectorAll<HTMLButtonElement>(".run-icon-btn")];
    expect(buttons.map((b) => b.getAttribute("aria-label"))).toEqual(
      VERBS.map((v) => CONTROL_LABEL[v]),
    );
    for (const b of buttons) {
      expect(corners(await hovered(b, b)), b.getAttribute("aria-label") ?? "").toEqual([
        verb,
        verb,
        verb,
        verb,
      ]);
    }
  });
});

/** The chat's run bar as `run-bar.ts` paints it for `runs` runs: the fold row from two runs, else the one run's
 *  row ending with the batch button. */
function mountRunBar(runs: number): HTMLElement {
  frame = document.createElement("div");
  frame.style.cssText = "inline-size:1000px";
  const host = document.createElement("div");
  host.className = "run-bar-host";
  const fold = document.createElement("div");
  fold.className = "run-bar-fold";
  const batch = document.createElement("button");
  batch.type = "button";
  batch.className = "icon-btn run-bar-all";
  batch.setAttribute("aria-label", "Pause all");
  batch.textContent = "·";
  const bar = document.createElement("ul");
  bar.className = "run-bar";
  for (let i = 0; i < runs; i++) {
    const li = document.createElement("li");
    li.className = "run-bar-row";
    const open = document.createElement("div");
    open.className = "run-bar-open";
    open.setAttribute("role", "button");
    const sum = document.createElement("span");
    sum.className = "ev-sum";
    const label = document.createElement("span");
    label.className = "ev-sum-label";
    label.textContent = `nightly sweep ${String(i + 1)}`;
    sum.append(label);
    open.append(sum);
    li.append(open);
    bar.append(li);
  }
  if (runs >= 2) {
    const toggle = document.createElement("div");
    toggle.className = "run-bar-open run-bar-toggle";
    toggle.setAttribute("role", "button");
    toggle.setAttribute("aria-expanded", "true");
    const count = document.createElement("span");
    count.className = "run-bar-count";
    count.textContent = `${String(runs)} workflows, 1 needs you`;
    toggle.append(count);
    fold.append(toggle, batch);
  } else {
    fold.hidden = true;
    bar.querySelector("li")!.append(batch);
  }
  host.append(fold, bar);
  frame.append(host);
  document.body.prepend(frame);
  return host;
}

describe("the chat run bar's header row", () => {
  // The batch button keeps the icon button's own 6px (`--r`).
  it.each([
    ["the fold row", 3, ".run-bar-fold > .run-bar-toggle", ".run-bar-count"],
    ["the one run's row", 1, ".run-bar > li > .run-bar-open", ".ev-sum-label"],
  ] as const)("rounds %s's hover and its batch button's", async (_, runs, rowSel, inkSel) => {
    await browser.viewport(1440, 900);
    style = mountAppCSS();
    const host = mountRunBar(runs);
    const row = host.querySelector(rowSel)!;
    expect(corners(await hovered(row.querySelector(inkSel)!, row)), "header row").toEqual([
      7, 7, 7, 7,
    ]);
    const batch = host.querySelector(".run-bar-all")!;
    expect(corners(await hovered(batch, batch)), "Pause all").toEqual([6, 6, 6, 6]);
  });
});
