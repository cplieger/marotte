// The holding area above the composer (the run bar, then the steer and queued rows) under one cap: however
// many runs and messages wait, the composer stays inside the pane and a few transcript lines stay visible.

import { describe, it, expect, afterEach, vi } from "vitest";
import { page as browser } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { rowEntry, runAt, turnEntry } from "./__test-helpers__/run-order.js";
import { buildExecSummary } from "./exec-view/summary.js";
import { chevronEl } from "./chevron.js";
import { mountHoldingArea } from "./holding-area.js";
import { mountSteerStack, type SteerStackSource } from "./pending-steers.js";
import type { ExecRun } from "./exec-view/model.js";
import type { PendingSteer, QueuedPrompt, RunEntryView } from "./types.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

/** The app's title bar and tab strip above a tab's view. */
const CHROME_PX = 52;

/** The cap's share of the pane's visible height. */
const SHARE = 0.4;

/** One transcript line: `--fs-md` (14px) at the message line height of 1.6. */
const LINE_PX = 14 * 1.6;

const ATTACHED = "pasted-2026-11-16T10-00-41.png";

const VIEWPORTS = [
  [1440, 900],
  [390, 844],
] as const;

let style: HTMLStyleElement | null = null;
let frame: HTMLElement | null = null;

afterEach(async () => {
  frame?.remove();
  frame = null;
  style?.remove();
  style = null;
  vi.unstubAllGlobals();
  localStorage.removeItem(LS_UI_STATE_KEY);
  document.documentElement.removeAttribute("data-pointer");
  document.documentElement.style.removeProperty("--dock-viewport-h");
  await browser.viewport(1280, 720);
});

/** One tab view from the shipped markup, under the chrome, in a frame as tall as the viewport. */
function mountView(height: number, from: string, to: string): HTMLElement {
  const start = indexHtml.indexOf(from);
  const end = indexHtml.indexOf(to, start);
  expect(start, `${from} in index.html`).toBeGreaterThan(-1);
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

/** `queued` follow-ups behind a running turn (the last one attachment-only) and `steers` unread steers. */
function stackSource(queued: number, steers: number): SteerStackSource {
  const prompts = Array.from({ length: queued }, (_, i): QueuedPrompt =>
    i === queued - 1 && queued > 1
      ? {
          id: `m-q${String(i)}`,
          text: "",
          attachments: [{ path: `/uploads/${ATTACHED}`, name: ATTACHED }],
        }
      : { id: `m-q${String(i)}`, text: `follow-up number ${String(i + 1)}, check the logs again` },
  );
  return sourceOf(prompts, steers);
}

/** `prompts` queued behind a running turn and `steers` unread steers. */
function sourceOf(prompts: readonly QueuedPrompt[], steers: number): SteerStackSource {
  const entries: RunEntryView[] = prompts.map((row, i) =>
    rowEntry(
      row.id,
      i === 0 ? { after: "turns", turns: 1, this: true } : { after: "turns", turns: i + 1 },
      { row, send_now: "stop" },
    ),
  );
  const run = runAt(1, [turnEntry("t1", { origin: "prompt", kind: "row" }), ...entries], {
    cancel: "t1",
    send: "steer",
  });
  const pending = Array.from({ length: steers }, (_, i): PendingSteer => ({
    id: `steer-${String(i)}`,
    text: i === 0 ? "fsd" : "and check the logs",
    origin: "user",
  }));
  return {
    read: () => ({ id: "c1", steers: pending, run }),
    edit: () => Promise.resolve(),
    editByClear: () => Promise.resolve(),
    remove: () => Promise.resolve(),
    discard: () => Promise.resolve(),
    waitingFor: "the agent",
  };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 2; i++) {
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  }
  for (const a of frame!.getAnimations({ subtree: true })) {
    if (Number.isFinite(a.effect?.getComputedTiming().endTime)) {
      a.finish();
    }
  }
}

function inside(el: Element, box: DOMRect, what: string): void {
  const r = el.getBoundingClientRect();
  expect(r.top, `${what} top`).toBeGreaterThanOrEqual(box.top - 0.5);
  expect(r.bottom, `${what} bottom`).toBeLessThanOrEqual(box.bottom + 0.5);
}

interface Chat {
  pane: HTMLElement;
  area: HTMLElement;
}

async function mountChat(
  w: number,
  h: number,
  runs: number,
  runsOpen: boolean,
  queued: number,
  steers: number,
): Promise<Chat> {
  await browser.viewport(w, h);
  style = mountAppCSS();
  const root = mountView(h, '<div id="chat-view"', '<div id="settings-view"');
  paintRuns(root, runs, runsOpen);
  mountSteerStack(
    root.querySelector<HTMLUListElement>("#steer-stack")!,
    stackSource(queued, steers),
  );
  const area = root.querySelector<HTMLElement>("#holding-area")!;
  mountHoldingArea(area);
  await settle();
  return { pane: root.querySelector<HTMLElement>("#chat-view")!, area };
}

/** The reader's stored fold choice for every steer stack mounted after it. */
function openStack(open: boolean): void {
  localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ stack_open: open }));
}

/** The composer whole inside the pane, three transcript lines visible, the area under its cap, and the
 *  area's end reachable in order by scrolling it. */
function holds(chat: Chat, visibleH: number): void {
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
  const last = lines[lines.length - 1]!;
  inside(last, box, "the area's last line, scrolled to");
  const tops = lines.map((l) => l.getBoundingClientRect().top);
  for (let i = 1; i < tops.length; i++) {
    expect(tops[i], `line ${String(i)} below line ${String(i - 1)}`).toBeGreaterThan(tops[i - 1]!);
  }
}

describe("the holding area", () => {
  for (const [w, h] of VIEWPORTS) {
    describe(`at ${String(w)}x${String(h)}`, () => {
      for (const [what, runs, queued, steers] of [
        ["5 runs and 17 queued messages", 5, 17, 0],
        ["5 runs", 5, 0, 0],
        ["15 steers and 2 queued messages", 0, 2, 15],
        ["17 queued messages", 0, 17, 0],
      ] as const) {
        for (const runsOpen of runs === 0 ? [false] : [true, false]) {
          const bar = runs === 0 ? "" : runsOpen ? ", the run bar open" : ", the run bar folded";
          for (const stackOpen of [true, false]) {
            const stack = stackOpen ? "every message shown" : "the stack folded";
            it(`keeps the composer on screen beside ${what}${bar}, ${stack}`, async () => {
              openStack(stackOpen);
              const chat = await mountChat(w, h, runs, runsOpen, queued, steers);
              holds(chat, h - CHROME_PX);
            });
          }
        }
      }
    });
  }

  it("ellipsises a long file name inside its row at phone width, the actions column in the row", async () => {
    await browser.viewport(390, 844);
    style = mountAppCSS();
    const root = mountView(844, '<div id="chat-view"', '<div id="settings-view"');
    const name = `${"a".repeat(116)}.png`;
    mountSteerStack(
      root.querySelector<HTMLUListElement>("#steer-stack")!,
      sourceOf([{ id: "m-q0", text: "", attachments: [{ path: `/uploads/${name}`, name }] }], 0),
    );
    mountHoldingArea(root.querySelector<HTMLElement>("#holding-area")!);
    await settle();
    const row = root.querySelector<HTMLElement>("#steer-stack > .steer-row")!;
    const shown = row.querySelector<HTMLElement>(".steer-attach-name")!;
    const box = row.getBoundingClientRect();

    expect(shown.textContent).toBe(name);
    expect(getComputedStyle(shown).textOverflow).toBe("ellipsis");
    expect(shown.scrollWidth, "the name is cut").toBeGreaterThan(shown.clientWidth);
    const text = row.querySelector(".steer-text")!.getBoundingClientRect();
    expect(
      shown.getBoundingClientRect().right,
      "the name ends inside its column",
    ).toBeLessThanOrEqual(text.right + 0.5);
    const actions = row.querySelector(".steer-actions")!.getBoundingClientRect();
    expect(actions.left, "the actions start inside the row").toBeGreaterThanOrEqual(box.left);
    expect(actions.right, "the actions end inside the row").toBeLessThanOrEqual(box.right + 0.5);
    expect(actions.left, "the actions follow the message").toBeGreaterThanOrEqual(text.right);
  });

  it("caps the area against the visual viewport when a keyboard is up", async () => {
    const vv = 464;
    vi.stubGlobal("visualViewport", {
      height: vv,
      width: 390,
      offsetTop: 0,
      offsetLeft: 0,
      scale: 1,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    });
    openStack(true);
    const chat = await mountChat(390, 844, 5, true, 17, 0);
    expect(chat.area.getBoundingClientRect().height).toBeLessThanOrEqual(
      SHARE * (vv - CHROME_PX) + 0.5,
    );
  });

  it("keeps the run tab's composer on screen beside 17 messages to a step", async () => {
    await browser.viewport(390, 844);
    openStack(true);
    style = mountAppCSS();
    const root = mountView(844, '<div id="run-view"', "<!-- One Kiro spec");
    const runView = root.querySelector<HTMLElement>("#run-view")!;
    runView.classList.remove("hidden");
    const steers = Array.from({ length: 17 }, (_, i): PendingSteer => ({
      id: `steer-${String(i)}`,
      text: "check the logs",
      origin: "user",
    }));
    mountSteerStack(root.querySelector<HTMLUListElement>("#run-steer-stack")!, {
      read: () => ({ id: "run-1/step", steers }),
      edit: () => Promise.resolve(),
      editByClear: () => Promise.resolve(),
      remove: () => Promise.resolve(),
      discard: () => Promise.resolve(),
      waitingFor: "the step",
    });
    mountHoldingArea(root.querySelector<HTMLElement>("#run-holding-area")!);
    await settle();
    inside(
      root.querySelector("#run-composer")!,
      runView.getBoundingClientRect(),
      "the run composer",
    );
    inside(root.querySelector("#run-composer")!, new DOMRect(0, 0, 390, 844), "the run composer");
  });
});
