// The run tab's layout as `static/index.html` ships it, under the shipped stylesheet: the run dock
// and its verbs stay on screen at the end of a long run, and the composer stays on a phone with
// the keyboard up, the panel open and an ask showing.

import { describe, it, expect, afterEach } from "vitest";
import { page as browser, userEvent } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import type { ExecNode, ExecRun } from "./exec-view/model.js";
import { buildExecSummary } from "./exec-view/summary.js";
import { ownsItsClick } from "./disclosure-row.js";
import { buildPermissionCard } from "./permission.js";
import type { PermissionNeededPayload } from "./types.js";
import {
  STATE_WORD,
  inFlight,
  neverRan,
  settled as ended,
  type ExecState,
} from "./exec-view/status.js";

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

function verbs(withField: boolean, names?: readonly string[]): HTMLElement {
  const row = document.createElement("span");
  row.className = "run-controls";
  if (withField) {
    const field = Object.assign(document.createElement("input"), { type: "number", value: "1" });
    field.className = "tool-form-input run-extend-count";
    row.append(field);
  }
  for (const name of names ?? ["Add passes", "End this loop", "Cancel"].slice(withField ? 0 : 1)) {
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
  // Every fan step reports an activity, which the dock keeps for the rows it mounts.
  view.paintTails(new Map(fan().map((n) => [n.path, "Reading the diff"])));
  byId("run-body").append(view.root);
  return view;
}

/** Until `region` plays no animation: a slide cancelled for the one replacing it rejects its
 *  `finished` (an AbortError), and the replacement is what the wait follows. */
async function animationsDone(region: Element): Promise<void> {
  for (let round = 0; round < 20; round++) {
    const playing = region.getAnimations();
    if (playing.length === 0) {
      return;
    }
    await Promise.allSettled(playing.map((a) => a.finished));
  }
  throw new Error("the region's animations never settled");
}

/** The panel opens through a height transition; measure the open state, not a frame of it. */
async function settled(region: Element): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  await animationsDone(region);
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

  it("keeps a loop run's head to one line on a phone, the pass in its glyph form", async () => {
    await browser.viewport(390, 844);
    document.documentElement.setAttribute("data-pointer", "coarse");
    style = mountAppCSS();
    const { byId } = mountRunView(844);
    const loop: ExecNode = {
      path: "root/build-loop",
      label: "build-loop",
      kind: "repeat",
      state: "running",
      maxPasses: 8,
      children: [
        {
          path: "root/build-loop/iter-6",
          label: "pass 7",
          kind: "sequence",
          state: "running",
          pass: 7,
          children: fan(),
        },
      ],
    };
    mountPage(byId, verbs(false)).render({
      label: "kiro-cli-2.31.0-contract-fixes-r2",
      state: "running",
      nodes: [loop],
      live: true,
    });
    const head = byId("run-exec-dock").querySelector<HTMLElement>(".ev-dock-head")!;
    expect(head.getBoundingClientRect().height).toBeLessThanOrEqual(80);
    const compact = head.querySelector<HTMLElement>(".ev-sum-compact")!;
    expect(getComputedStyle(compact).display).not.toBe("none");
    expect(compact.textContent).toBe("7/8\u00b70/4");
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
      label: "nightly",
      state: "waiting",
      nodes,
      live: true,
      alert: {
        kind: "paused",
        text: "Waiting: Repeat 'review' reached maxIterations. · 3 of 3 passes",
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
    const withAsk = body.getBoundingClientRect().height;
    ask.replaceChildren();
    ask.classList.add("hidden");
    inside(byId("run-composer"), vv, "composer without the ask");
    const freed = body.getBoundingClientRect().height;
    expect(freed, "the open panel's body grows back").toBeGreaterThan(withAsk);
    // At least one 44px touch row.
    expect(freed, "the open panel's body holds a row").toBeGreaterThanOrEqual(44);
  });
});

/** A run's six steps as a run in `state` holds them, the clock at 22h 31m wherever one runs: in
 *  flight, the 3rd and 4th steps carry the state; settled, every step does; not started, none has
 *  begun. */
function sixSteps(state: ExecState): ExecNode[] {
  const span = (22 * 60 + 31) * 60_000 + 20_000;
  const live = inFlight(state);
  const t0 = live ? Date.now() - span : Date.parse("2026-10-07T00:00:00Z");
  const each: ExecState[] = live
    ? ["ok", "ok", state, state, "pending", "pending"]
    : Array.from({ length: 6 }, () => state);
  return each.map((s, i) => {
    const node: ExecNode = {
      path: `root/build-loop/iter-6/s${String(i)}`,
      label: `s${String(i)}`,
      kind: "step",
      state: s,
      children: [],
    };
    if (!neverRan(s)) {
      node.start = new Date(t0 + i * 60_000).toISOString();
    }
    if (ended(s)) {
      node.end = new Date(live ? t0 + (i + 1) * 60_000 : t0 + span - (5 - i) * 1000).toISOString();
    }
    return node;
  });
}

function inLoop(steps: ExecNode[]): ExecNode[] {
  return [
    {
      path: "root/build-loop",
      label: "build-loop",
      kind: "repeat",
      state: "running",
      maxPasses: 8,
      children: [
        {
          path: "root/build-loop/iter-6",
          label: "pass 7",
          kind: "sequence",
          state: "running",
          pass: 7,
          children: steps,
        },
      ],
    },
  ];
}

/** Every state a summary words at rest: an unanswered ask always arrives with its alert, which
 *  takes the summary's place. */
const REST_STATES = (Object.keys(STATE_WORD) as ExecState[]).filter((s) => s !== "input");

/** The rightmost edge any rendered part of the summary paints to. */
function contentRight(sum: Element): number {
  return Math.max(
    ...[...sum.querySelectorAll<HTMLElement>(":scope > *, .ev-sum-compact > *")]
      .filter(
        (e) => getComputedStyle(e).position !== "absolute" && e.getBoundingClientRect().width > 1,
      )
      .map((e) => e.getBoundingClientRect().right),
  );
}

/** Each text part shows whole: none ellipsed or cut by its box. The run bar's name is the one
 *  part made to ellipse. */
function unclipped(sum: Element, what: string): void {
  for (const e of sum.querySelectorAll<HTMLElement>(
    ".ev-sum-state, .ev-sum-compact, .ev-sum-clock",
  )) {
    if (e.getBoundingClientRect().width > 1) {
      expect(e.scrollWidth, `${what}: ${e.className} clipped`).toBeLessThanOrEqual(e.clientWidth);
    }
  }
}

describe("the run summary on a phone", () => {
  for (const width of [360, 390]) {
    it(`clears the dock's Pause and Cancel at ${String(width)}px, in a loop and out`, async () => {
      await browser.viewport(width, 844);
      document.documentElement.setAttribute("data-pointer", "coarse");
      style = mountAppCSS();
      const { byId } = mountRunView(844);
      const row = verbs(false, ["Pause", "Cancel"]);
      const page = mountPage(byId, row);
      const head = byId("run-exec-dock").querySelector<HTMLElement>(".ev-dock-head")!;
      for (const looped of [true, false]) {
        for (const state of REST_STATES) {
          const what = `${looped ? "loop" : "plain"} ${state} at ${String(width)}`;
          page.render({
            label: "kiro-cli-2.31.0-contract-fixes-r2",
            state,
            nodes: looped ? inLoop(sixSteps(state)) : sixSteps(state),
            live: inFlight(state),
          });
          const sum = head.querySelector(".ev-sum")!;
          expect(sum.querySelector(".ev-sum-clock")?.textContent, what).toBe(
            neverRan(state) ? "" : "22h 31m",
          );
          expect(contentRight(sum), what).toBeLessThanOrEqual(
            row.getBoundingClientRect().left + 0.5,
          );
          unclipped(sum, what);
          const tall = head.getBoundingClientRect().height;
          expect(tall, what).toBeGreaterThanOrEqual(44);
          expect(tall, what).toBeLessThanOrEqual(80);
        }
      }
    });

    it(`fits the chat's run bar row at ${String(width)}px, in a loop and out`, async () => {
      await browser.viewport(width, 844);
      document.documentElement.setAttribute("data-pointer", "coarse");
      style = mountAppCSS();
      const start = indexHtml.indexOf('<div id="chat-view"');
      const end = indexHtml.indexOf("<!-- Settings view", start);
      expect(start, "#chat-view in index.html").toBeGreaterThan(-1);
      frame = document.createElement("div");
      frame.style.cssText = "display:flex;flex-direction:column;block-size:844px";
      frame.innerHTML = indexHtml.slice(start, end);
      document.body.prepend(frame);
      const bar = frame.querySelector<HTMLElement>("#run-bar")!;
      bar.classList.remove("hidden");
      const btn = document.createElement("div");
      btn.setAttribute("role", "button");
      btn.tabIndex = 0;
      btn.className = "run-bar-open";
      const glyph = document.createElement("span");
      glyph.className = "run-bar-glyph";
      glyph.dataset["status"] = "working";
      const summary = buildExecSummary({ alertHost: btn });
      btn.append(glyph, summary.root);
      const li = document.createElement("li");
      li.className = "run-bar-row";
      li.append(btn);
      bar.append(li);
      for (const looped of [true, false]) {
        for (const state of REST_STATES) {
          const what = `${looped ? "loop" : "plain"} ${state} at ${String(width)}`;
          summary.paint({
            label: "kiro-cli-2.31.0-contract-fixes-r2",
            state,
            nodes: looped ? inLoop(sixSteps(state)) : sixSteps(state),
            live: inFlight(state),
          });
          const box = btn.getBoundingClientRect();
          const inner = box.right - parseFloat(getComputedStyle(btn).paddingInlineEnd);
          expect(contentRight(summary.root), what).toBeLessThanOrEqual(inner + 0.5);
          unclipped(summary.root, what);
          expect(box.right, what).toBeLessThanOrEqual(width);
        }
      }
    });
  }

  it("toggles the panel from a tap anywhere on the head row at 360px but its verbs", async () => {
    await browser.viewport(360, 844);
    document.documentElement.setAttribute("data-pointer", "coarse");
    style = mountAppCSS();
    const { byId } = mountRunView(844);
    const row = verbs(false, ["Pause", "Cancel"]);
    mountPage(byId, row).render({
      label: "nightly",
      state: "running",
      nodes: inLoop(sixSteps("running")),
      live: true,
    });
    const dock = byId("run-exec-dock");
    const head = dock.querySelector<HTMLElement>(".ev-dock-head")!;
    const toggle = dock.querySelector<HTMLButtonElement>(".ev-box-toggle")!;
    expect(
      head.getBoundingClientRect().height,
      "the row keeps the floor's height",
    ).toBeGreaterThanOrEqual(44);
    expect(toggle.tabIndex).toBe(0);
    expect(toggle.getAttribute("aria-label")).toBe("Run details");
    const centre = (sel: string): number => {
      const b = head.querySelector(sel)!.getBoundingClientRect();
      return b.left + b.width / 2;
    };
    // Read per tap: the head rises as the panel opens.
    const points: [string, (r: DOMRect) => number][] = [
      ["the chevron", () => centre(".disclosure-chevron")],
      ["the row's start", (r) => r.left + 2],
      ["the state word", () => centre(".ev-sum-state")],
      ["the clock", () => centre(".ev-sum-clock")],
    ];
    const region = dock.querySelector(".ev-dock-panel")!;
    for (const [what, at] of points) {
      for (const want of ["true", "false"]) {
        const r = head.getBoundingClientRect();
        const x = at(r);
        const y = r.top + r.height / 2;
        const hit = document.elementFromPoint(x, y);
        expect(hit !== null && head.contains(hit), `${what} is on the head`).toBe(true);
        expect(hit !== null && row.contains(hit), `${what} is no verb`).toBe(false);
        await userEvent.click(head, { position: { x: x - r.left, y: y - r.top } });
        expect(toggle.getAttribute("aria-expanded"), `a tap on ${what}`).toBe(want);
        await animationsDone(region);
      }
    }
  });
});

describe("the permission card on a phone", () => {
  for (const [width, note] of [
    [360, false],
    [390, true],
  ] as const) {
    it(`keeps the asked command's title and the answer buttons in view at ${String(width)}px${note ? " with the deny note" : ""}, under the dock card's ceiling`, async () => {
      await browser.viewport(width, 844);
      document.documentElement.setAttribute("data-pointer", "coarse");
      style = mountAppCSS();
      const { byId } = mountRunView(844);
      mountPage(byId, verbs(false, ["Pause", "Cancel"])).render({
        label: "nightly",
        state: "waiting",
        nodes: inLoop(sixSteps("running")),
        live: true,
      });
      const ask = byId("run-dock");
      ask.classList.remove("hidden");
      const card = buildPermissionCard(
        "chat-1",
        {
          request_id: 1,
          title: "npm test -- --run",
          kind: "execute",
          tool_call_id: "tooluse_k3Jd9sQ2Rr6vXbT1",
          ...(note ? { rejection_note: "reason" } : {}),
          consent: { capability: "shell", subject: "npm test -- --run" },
          options: [
            { option_id: "a", name: "Allow", kind: "allow_once" },
            { option_id: "aa", name: "Always allow", kind: "allow_always" },
            { option_id: "r", name: "Reject", kind: "reject_once" },
          ],
        } as PermissionNeededPayload,
        () => undefined,
      );
      ask.append(card);
      await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));

      const title = card.querySelector<HTMLElement>(".approval-body > strong")!;
      const body = card.querySelector<HTMLElement>(".approval-body")!;
      const t = title.getBoundingClientRect();
      const b = body.getBoundingClientRect();
      const c = card.getBoundingClientRect();
      expect(t.top, "the title's top inside its region").toBeGreaterThanOrEqual(b.top - 0.5);
      expect(t.bottom, "the title's bottom inside its region").toBeLessThanOrEqual(b.bottom + 0.5);
      expect(b.top, "the region's top inside the card").toBeGreaterThanOrEqual(c.top - 0.5);
      for (const btn of card.querySelectorAll<HTMLElement>(".approval-actions button")) {
        const r = btn.getBoundingClientRect();
        expect(
          r.bottom,
          `the ${String(btn.textContent)} button inside the card`,
        ).toBeLessThanOrEqual(c.bottom + 0.5);
        inside(btn, 844, `the ${String(btn.textContent)} button`);
      }
    });
  }
});

/** A lanes run as the chat's parallel build had it, `steps` long, ten done and the eleventh running
 *  or paused, started 97 hours ago. */
function lanes(paused: boolean, steps = 45): ExecNode[] {
  const t0 = Date.now() - 97 * 3_600_000;
  return Array.from({ length: steps }, (_, i): ExecNode => {
    const state: ExecState =
      i < 10 ? "ok" : i === 10 ? (paused ? "waiting" : "running") : "pending";
    const node: ExecNode = {
      path: `root/lanes/s${String(i)}`,
      label: `s${String(i)}`,
      kind: "step",
      state,
      children: [],
    };
    if (i <= 10) {
      node.start = new Date(t0 + i * 60_000).toISOString();
    }
    if (i < 10) {
      node.end = new Date(t0 + (i + 1) * 60_000).toISOString();
    }
    return node;
  });
}

const LANES_PAUSE = "lane-bolttable reached its pass limit (4/4) \u00b7 Paused by its chat";

describe("the run dock head beside its verbs", () => {
  const box = (el: Element | null | undefined): DOMRect => {
    expect(el, "a rendered part").not.toBeNull();
    return el!.getBoundingClientRect();
  };
  const shows = (el: Element | null): boolean =>
    el !== null && getComputedStyle(el).display !== "none";

  for (const [w, h] of [
    [1280, 800],
    [820, 1180],
    [390, 844],
  ] as const) {
    // A fan-out of 120 steps: more segments than the bar has room for at their old 2px floor.
    it(`never draws the bar, the clock and the verbs over each other at ${String(w)}px`, async () => {
      await browser.viewport(w, h);
      style = mountAppCSS();
      const { byId } = mountRunView(h);
      const row = verbs(false, ["Pause", "Cancel"]);
      mountPage(byId, row).render({
        label: "subflux-build-lanes-relaunch",
        state: "running",
        nodes: lanes(false, 120),
        live: true,
      });
      const head = byId("run-exec-dock").querySelector<HTMLElement>(".ev-dock-head")!;
      const mini = head.querySelector<HTMLElement>(".ev-mini")!;
      const clock = box(head.querySelector(".ev-sum-clock"));
      const verbsBox = box(row);
      const bar = box(mini);
      for (const seg of mini.querySelectorAll(".ev-mini-seg")) {
        const s = seg.getBoundingClientRect();
        expect(s.right, `a segment inside the bar at ${String(w)}`).toBeLessThanOrEqual(
          bar.right + 0.5,
        );
      }
      if (getComputedStyle(mini).position !== "absolute") {
        expect(bar.right, `the bar before the clock at ${String(w)}`).toBeLessThanOrEqual(
          clock.left + 0.5,
        );
      }
      expect(clock.right, `the clock before the verbs at ${String(w)}`).toBeLessThanOrEqual(
        verbsBox.left + 0.5,
      );
      expect(verbsBox.right, `the verbs inside the head at ${String(w)}`).toBeLessThanOrEqual(
        head.getBoundingClientRect().right + 0.5,
      );
    });

    // One verb: between two, the row's tap gap is spacing the touch rule grows on purpose.
    it(`keeps the clock and the end verb where a mouse puts them in touch mode at ${String(w)}px`, async () => {
      await browser.viewport(w, h);
      style = mountAppCSS();
      const { byId } = mountRunView(h);
      const row = verbs(false, ["Cancel"]);
      mountPage(byId, row).render({
        label: "subflux-build-lanes-relaunch",
        state: "running",
        nodes: lanes(false),
        live: true,
      });
      const head = byId("run-exec-dock").querySelector<HTMLElement>(".ev-dock-head")!;
      const xs = (): number[] => {
        const clock = box(head.querySelector(".ev-sum-clock"));
        const verb = box(row.firstElementChild);
        return [clock.left, clock.right, verb.left, verb.right];
      };
      document.documentElement.setAttribute("data-pointer", "fine");
      const fine = xs();
      document.documentElement.setAttribute("data-pointer", "coarse");
      const coarse = xs();
      coarse.forEach((x, i) => {
        expect(x, `edge ${String(i)} at ${String(w)}`).toBeCloseTo(fine[i]!, 1);
      });
    });

    it(`puts a pause in the progress and bar's place, on one line, at ${String(w)}px`, async () => {
      await browser.viewport(w, h);
      style = mountAppCSS();
      const { byId } = mountRunView(h);
      const row = verbs(true);
      const page = mountPage(byId, row);
      page.render({
        label: "subflux-build-lanes-relaunch",
        state: "waiting",
        nodes: lanes(true),
        live: true,
        credits: 257.8,
        alert: { kind: "paused", text: LANES_PAUSE },
      });
      const dock = byId("run-exec-dock");
      const head = dock.querySelector<HTMLElement>(".ev-dock-head")!;
      expect(shows(head.querySelector(".ev-sum-progress"))).toBe(false);
      expect(shows(head.querySelector(".ev-mini"))).toBe(false);
      expect(dock.querySelector(".ev-dock-note"), "no second row").toBeNull();
      const line = box(head.querySelector(".ev-alert-line"));
      const state = box(head.querySelector(".ev-sum-state"));
      const clock = box(head.querySelector(".ev-sum-clock"));
      const text = head.querySelector<HTMLElement>(".ev-alert-text")!;
      if (w > 480) {
        // Between the state word and the clock, level with them, the verbs after the clock.
        expect(line.left).toBeGreaterThanOrEqual(state.right);
        expect(line.right).toBeLessThanOrEqual(clock.left + 0.5);
        expect(line.top + line.height / 2).toBeCloseTo(state.top + state.height / 2, 0);
        expect(clock.right).toBeLessThanOrEqual(box(row).left + 0.5);
      } else {
        // On a phone the pass-limit verbs leave the summary no room beside them: they take the
        // head's second line at its end, under the state, clock and credits on the first, and the
        // pause takes the third, under the state word, most of the head's width.
        const edge = head.getBoundingClientRect();
        const credits = box(head.querySelector(".ev-sum-credits"));
        for (const [name, part] of [
          ["clock", clock],
          ["credits", credits],
        ] as const) {
          expect(part.top + part.height / 2, `the ${name} level with the state`).toBeCloseTo(
            state.top + state.height / 2,
            0,
          );
          expect(box(row).top, `the verbs under the ${name}`).toBeGreaterThanOrEqual(
            part.bottom - 0.5,
          );
        }
        expect(box(row).right, "the verbs at the head's end").toBeCloseTo(
          edge.right - parseFloat(getComputedStyle(head).paddingInlineEnd),
          0,
        );
        expect(line.top).toBeGreaterThanOrEqual(box(row).bottom - 0.5);
        expect(
          line.left +
            parseFloat(getComputedStyle(head.querySelector(".ev-alert-line")!).paddingInlineStart),
        ).toBeCloseTo(state.left, 0);
        expect(line.right).toBeLessThanOrEqual(edge.right + 0.5);
        expect(line.width).toBeGreaterThan(0.6 * edge.width);
      }
      expect(text.scrollHeight, "one line").toBeLessThanOrEqual(text.clientHeight);
      expect(head.querySelector(".ev-alert-line")?.getAttribute("data-tooltip")).toBe(LANES_PAUSE);
      expect(head.querySelector(".ev-sum")?.textContent).toContain(LANES_PAUSE);

      page.render({
        label: "subflux-build-lanes-relaunch",
        state: "running",
        nodes: lanes(false),
        live: true,
      });
      expect(shows(head.querySelector(".ev-sum-progress"))).toBe(true);
      expect(shows(head.querySelector(".ev-mini"))).toBe(true);
      expect(head.querySelector(".ev-alert-line")).toBeNull();
    });
  }

  it("keeps a failure as a note under the head, the progress and bar in place", async () => {
    await browser.viewport(1280, 800);
    style = mountAppCSS();
    const { byId } = mountRunView(800);
    mountPage(byId, verbs(false, ["Retry"])).render({
      label: "subflux-build-lanes-relaunch",
      state: "fail",
      nodes: lanes(false).map((n, i) => (i === 10 ? { ...n, state: "fail" } : n)),
      live: false,
      alert: { kind: "failed", text: "s10 failed: exit status 2" },
    });
    const dock = byId("run-exec-dock");
    const head = dock.querySelector<HTMLElement>(".ev-dock-head")!;
    expect(shows(head.querySelector(".ev-sum-progress"))).toBe(true);
    expect(shows(head.querySelector(".ev-mini"))).toBe(true);
    expect(head.querySelector(".ev-alert-line")).toBeNull();
    const note = dock.querySelector('.ev-dock-notes[role="alert"] .ev-dock-note');
    expect(note?.textContent).toBe("s10 failed: exit status 2");
    expect(box(note).top).toBeGreaterThanOrEqual(head.getBoundingClientRect().bottom - 0.5);
  });
});

describe("the run dock's step rows", () => {
  /** The first step row of the closed dock over `fan()`, whose steps each report an activity. */
  async function firstRow(w: number, h: number, tier: "fine" | "coarse"): Promise<HTMLElement> {
    await browser.viewport(w, h);
    document.documentElement.setAttribute("data-pointer", tier);
    style = mountAppCSS();
    const { byId } = mountRunView(h);
    mountPage(byId, verbs(false)).render({
      label: "nightly",
      state: "running",
      nodes: fan(),
      live: true,
    });
    // A phone shows only the lead row; the others are `display: none`.
    const row = [...byId("run-exec-dock").querySelectorAll<HTMLElement>(".ev-dock-row")].find(
      (r) => r.getBoundingClientRect().height > 0,
    );
    expect(row, "a shown step row").toBeDefined();
    return row!;
  }
  const part = (row: HTMLElement, sel: string): DOMRect =>
    row.querySelector(sel)!.getBoundingClientRect();

  for (const [w, h] of [
    [1024, 768],
    [820, 1180],
  ] as const) {
    it(`puts a step's activity under its name in touch mode, the clock on the first line, at ${String(w)}px`, async () => {
      const row = await firstRow(w, h, "coarse");
      const name = part(row, ".ev-dock-name");
      const tail = part(row, ".ev-dock-tail");
      expect(row.querySelector(".ev-dock-tail")?.textContent).toBe("Reading the diff");
      expect(tail.top).toBeGreaterThanOrEqual(name.bottom - 0.5);
      expect(tail.left).toBeCloseTo(name.left, 0);
      expect(part(row, ".ev-dock-dur").bottom).toBeLessThanOrEqual(tail.top + 0.5);
      expect(row.getBoundingClientRect().height).toBe(44);
    });
  }

  it("keeps a step's activity beside its name with a mouse", async () => {
    const row = await firstRow(1024, 768, "fine");
    const name = part(row, ".ev-dock-name");
    const tail = part(row, ".ev-dock-tail");
    expect(tail.left).toBeGreaterThanOrEqual(name.right);
    expect(tail.top).toBeLessThan(name.bottom);
  });

  for (const tier of ["fine", "coarse"] as const) {
    it(`ends a step's clock on the row's gutter on a phone, ${tier} pointer`, async () => {
      const row = await firstRow(390, 844, tier);
      const gutter =
        row.getBoundingClientRect().right - parseFloat(getComputedStyle(row).paddingRight);
      expect(part(row, ".ev-dock-dur").right).toBeCloseTo(gutter, 0);
    });
  }
});

describe("the run dock head's toggle", () => {
  const WIDTHS = [
    [1000, 800],
    [700, 1000],
    [390, 844],
    [402, 874],
    [360, 780],
  ] as const;

  /** Mount the run tab `h` tall with `names` as its verbs and render `run`; the head and its verbs. */
  function mountHead(
    h: number,
    names: readonly string[] | "pass limit",
    run: ExecRun,
  ): { head: HTMLElement; row: HTMLElement } {
    style = mountAppCSS();
    const { byId } = mountRunView(h);
    const row = names === "pass limit" ? verbs(true) : verbs(false, names);
    mountPage(byId, row).render(run);
    return { head: byId("run-exec-dock").querySelector<HTMLElement>(".ev-dock-head")!, row };
  }

  /** The hit floor: WCAG 2.5.8's 24px on a mouse, Apple HIG's 44px on touch. */
  function hitFloor(): number {
    return document.documentElement.getAttribute("data-pointer") === "coarse" ? 44 : 24;
  }

  interface Box {
    left: number;
    right: number;
    top: number;
    bottom: number;
  }

  function fmt(b: Box): string {
    return `[${b.left.toFixed(1)}..${b.right.toFixed(1)} x ${b.top.toFixed(1)}..${b.bottom.toFixed(1)}]`;
  }

  function meets(a: Box, b: Box): boolean {
    return (
      Math.min(a.right, b.right) - Math.max(a.left, b.left) > 0.5 &&
      Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 0.5
    );
  }

  /** Where `c` answers a tap: a button's expander (61-mcp-tools.css) centres the floor on each
   *  axis its box is smaller on; a field has none, so its box is its target. */
  function targetOf(c: Element, floor: number): Box {
    const r = c.getBoundingClientRect();
    const grow = (size: number): number =>
      c instanceof HTMLButtonElement ? Math.max(0, (floor - size) / 2) : 0;
    return {
      left: r.left - grow(r.width),
      right: r.right + grow(r.width),
      top: r.top - grow(r.height),
      bottom: r.bottom + grow(r.height),
    };
  }

  /** Every control in the head, the toggle included, answers over the floor on each axis inside
   *  the dock's clip, and no other control's target or dock region meets that area. */
  function ownsItsTarget(head: HTMLElement, what: string): void {
    const floor = hitFloor();
    const dock = head.closest<HTMLElement>(".ev-dock")!;
    const d = dock.getBoundingClientRect();
    const clip: Box = {
      left: d.left + dock.clientLeft,
      right: d.left + dock.clientLeft + dock.clientWidth,
      top: d.top + dock.clientTop,
      bottom: d.top + dock.clientTop + dock.clientHeight,
    };
    const shown = (e: Element): boolean => {
      const r = e.getBoundingClientRect();
      return r.width > 0 && r.height > 0;
    };
    const controls = [...head.querySelectorAll("button, input")].filter(shown);
    const regions = [...dock.children].filter((e) => e !== head && shown(e));
    const nameOf = (c: Element): string => c.getAttribute("aria-label") ?? c.tagName.toLowerCase();
    const toggle = head.querySelector(".ev-box-toggle")!;
    for (const c of controls) {
      const name = nameOf(c);
      const t = targetOf(c, floor);
      expect(t.bottom - t.top, `${what}: ${name}'s target's height`).toBeGreaterThanOrEqual(
        floor - 0.5,
      );
      expect(t.right - t.left, `${what}: ${name}'s target's width`).toBeGreaterThanOrEqual(
        floor - 0.5,
      );
      expect(
        t.left >= clip.left - 0.5 &&
          t.right <= clip.right + 0.5 &&
          t.top >= clip.top - 0.5 &&
          t.bottom <= clip.bottom + 0.5,
        `${what}: ${name}'s target ${fmt(t)} inside the dock's clip ${fmt(clip)}`,
      ).toBe(true);
      for (const o of controls) {
        if (o !== c) {
          const u = targetOf(o, floor);
          expect(
            meets(t, u),
            `${what}: ${name}'s target ${fmt(t)} meets ${nameOf(o)}'s ${fmt(u)}`,
          ).toBe(false);
        }
      }
      for (const g of regions) {
        const u = g.getBoundingClientRect();
        expect(
          meets(t, u),
          `${what}: ${name}'s target ${fmt(t)} meets .${g.className.split(" ")[0] ?? ""} ${fmt(u)}`,
        ).toBe(false);
      }
      const x = (t.left + t.right) / 2;
      const y = (t.top + t.bottom) / 2;
      const probes = [
        ["top", x, t.top + 1],
        ["bottom", x, t.bottom - 1],
        ["start", t.left + 1, y],
        ["end", t.right - 1, y],
      ] as const;
      for (const [edge, px, py] of probes) {
        const hit = document.elementFromPoint(px, py);
        // The head forwards a tap on its own ink to the toggle (`wireRowToggle`).
        const answers =
          hit !== null &&
          (c.contains(hit) || (c === toggle && head.contains(hit) && !ownsItsClick(hit, head)));
        expect(
          answers,
          `${what}: ${name} answers at its target's ${edge} edge, not ${hit?.className ?? "nothing"}`,
        ).toBe(true);
      }
    }
  }

  /** The toggle's box holds each of `parts`. */
  function holds(head: HTMLElement, parts: readonly string[], what: string): DOMRect {
    const t = head.querySelector(".ev-box-toggle")!.getBoundingClientRect();
    for (const sel of parts) {
      const p = head.querySelector(sel)!.getBoundingClientRect();
      expect(p.width, `${what}: ${sel} renders`).toBeGreaterThan(0);
      expect(
        t.left <= p.left + 0.5 &&
          t.right >= p.right - 0.5 &&
          t.top <= p.top + 0.5 &&
          t.bottom >= p.bottom - 0.5,
        `${what}: the toggle ${fmt(t)} holds ${sel} ${fmt(p)}`,
      ).toBe(true);
    }
    return t;
  }

  /** The toggle's box holds each of `parts` and is as tall as each button and level with it. */
  function coversAtVerbHeight(
    head: HTMLElement,
    row: HTMLElement,
    parts: readonly string[],
    what: string,
  ): void {
    const t = holds(head, parts, what);
    const chevron = head.querySelector(".disclosure-chevron")!.getBoundingClientRect();
    const credits = head.querySelector(".ev-sum-credits")!.getBoundingClientRect();
    expect(
      t.right - credits.right,
      `${what}: the toggle's end inset past the credits`,
    ).toBeGreaterThanOrEqual(chevron.left - t.left - 0.5);
    for (const v of row.querySelectorAll("button")) {
      const r = v.getBoundingClientRect();
      const name = v.getAttribute("aria-label") ?? "";
      expect(t.height, `${what}: the toggle's height beside ${name}`).toBeCloseTo(r.height, 0);
      expect(t.top, `${what}: the toggle level with ${name}`).toBeCloseTo(r.top, 0);
    }
  }

  /** The toggle's box holds each of `parts` on the head's first line, as tall as each button and
   *  the head's width; the verbs are on a line of their own under it. */
  function coversAboveVerbs(
    head: HTMLElement,
    row: HTMLElement,
    parts: readonly string[],
    what: string,
  ): void {
    const t = holds(head, parts, what);
    const edge = head.getBoundingClientRect();
    const end = edge.right - parseFloat(getComputedStyle(head).paddingInlineEnd);
    expect(t.left, `${what}: the toggle from the head's start`).toBeCloseTo(edge.left, 0);
    expect(t.right, `${what}: the toggle to the head's end`).toBeGreaterThanOrEqual(end - 3);
    for (const v of row.querySelectorAll("button")) {
      const r = v.getBoundingClientRect();
      const name = v.getAttribute("aria-label") ?? "";
      expect(r.top, `${what}: ${name} under the toggle`).toBeGreaterThanOrEqual(t.bottom - 0.5);
      expect(t.height, `${what}: the toggle's height beside ${name}`).toBeCloseTo(r.height, 0);
    }
  }

  const RUNNING: ExecRun = {
    label: "nightly",
    state: "running",
    nodes: inLoop(sixSteps("running")),
    live: true,
    credits: 257.8,
  };
  const PAUSED: ExecRun = {
    label: "subflux-build-lanes-relaunch",
    state: "waiting",
    nodes: lanes(true),
    live: true,
    credits: 257.8,
    alert: { kind: "paused", text: LANES_PAUSE },
  };
  const RUNNING_PARTS = [
    ".disclosure-chevron",
    ".ev-sum-state",
    ".ev-sum-compact",
    ".ev-sum-clock",
    ".ev-sum-credits",
  ];
  const PAUSED_PARTS = [".disclosure-chevron", ".ev-sum-state", ".ev-sum-clock", ".ev-sum-credits"];

  // Beside the verbs while the summary fits, under them once it does not: a running run's
  // summary with its credits and Pause and Stop fit a 390px head with a mouse but overrun it
  // with a finger's wider gap between the verbs, and overrun a 360px head with either; the
  // pass-limit verbs (a field and three buttons) overrun a 390px head with either.
  const CASES = [
    {
      what: "a running run's",
      names: ["Pause", "Stop"],
      run: RUNNING,
      parts: RUNNING_PARTS,
      beside: { fine: [1000, 700, 390], coarse: [1000, 700, 402] },
      under: { fine: [360], coarse: [390, 360] },
    },
    {
      what: "a paused run's",
      names: ["Resume", "Stop"],
      run: PAUSED,
      parts: PAUSED_PARTS,
      beside: { fine: [1000, 700, 390, 360], coarse: [1000, 700, 390, 360] },
      under: { fine: [], coarse: [] },
    },
    {
      what: "a pass-limit pause's",
      names: "pass limit",
      run: PAUSED,
      parts: PAUSED_PARTS,
      beside: { fine: [1000, 700], coarse: [1000, 700] },
      under: { fine: [390, 360], coarse: [390, 360] },
    },
  ] as const;

  for (const pointer of ["fine", "coarse"] as const) {
    it(`keeps a running run's band clear of the verbs under its summary, 360px ${pointer}`, async () => {
      await browser.viewport(360, 780);
      document.documentElement.setAttribute("data-pointer", pointer);
      const { head, row } = mountHead(780, ["Pause", "Stop"], RUNNING);
      const band = head.querySelector(".ev-mini")!.getBoundingClientRect();
      const t = head.querySelector(".ev-box-toggle")!.getBoundingClientRect();
      expect(band.height, "the band renders").toBeGreaterThan(0);
      expect(band.top, "the band under the summary line").toBeGreaterThanOrEqual(t.bottom - 0.5);
      expect(band.bottom, "the band above the verbs").toBeLessThanOrEqual(
        row.getBoundingClientRect().top + 0.5,
      );
    });

    for (const c of CASES) {
      for (const [w, h] of WIDTHS) {
        const at = `${String(w)}px ${pointer}`;
        const beside = (c.beside[pointer] as readonly number[]).includes(w);
        if (beside || (c.under[pointer] as readonly number[]).includes(w)) {
          it(`keeps every target in ${c.what} head its own, ${at}`, async () => {
            await browser.viewport(w, h);
            document.documentElement.setAttribute("data-pointer", pointer);
            const { head } = mountHead(h, c.names, c.run);
            ownsItsTarget(head, at);
          });
        }
        if (beside) {
          it(`covers ${c.what} summary line beside its verbs at their height, ${at}`, async () => {
            await browser.viewport(w, h);
            document.documentElement.setAttribute("data-pointer", pointer);
            const { head, row } = mountHead(h, c.names, c.run);
            coversAtVerbHeight(head, row, c.parts, at);
          });
        }
        if ((c.under[pointer] as readonly number[]).includes(w)) {
          it(`covers ${c.what} summary line above its verbs at their height, ${at}`, async () => {
            await browser.viewport(w, h);
            document.documentElement.setAttribute("data-pointer", pointer);
            const { head, row } = mountHead(h, c.names, c.run);
            coversAboveVerbs(head, row, c.parts, at);
          });
        }
      }
    }
  }
});
