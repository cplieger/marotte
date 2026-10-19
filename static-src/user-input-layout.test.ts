import { describe, it, expect, afterEach } from "vitest";
import { page as browser, userEvent } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { makeSession } from "./__test-helpers__/model.js";
import {
  _resetForTest as resetDocks,
  collapseSettledDecision,
  dropRunRequests,
  dropTurnDecisions,
  mountDecisionDock,
  mountRunDecisionDock,
  pushDecision,
  type AnswerOutcome,
} from "./decision-dock.js";
import { paintViewportHeight } from "./dock-ask.js";
import { mountSteerStack } from "./pending-steers.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import { setActive, setSessions } from "./store.js";
import { buildUserInputCard } from "./user-input.js";
import type { PendingSteer, UserInputOption } from "./types.js";

const LOOP_QUESTION =
  "I've been trying to use \"execute_bash\" but it's failed 7 times in a row. I'm clearly stuck in a loop.\n\nWhat would you like me to do?";
const LOOP_OPTIONS: UserInputOption[] = [{ title: "Cancel" }, { title: "Keep Iterating" }];
/** The wire's optional `description`, rendered inside the option's button, wrapping on a phone. */
const DESCRIBED_OPTIONS: UserInputOption[] = [
  { title: "Cancel", description: "Stop retrying and hand the step back to you." },
  { title: "Keep Iterating", description: "Retry the same step after checking the logs again." },
];

/** `long`: clamped to four lines behind Show more. */
const FIXTURES: Record<
  string,
  { readonly question: string; readonly options: UserInputOption[]; readonly long: boolean }
> = {
  "the loop prompt": { question: LOOP_QUESTION, options: LOOP_OPTIONS, long: false },
  "the loop prompt, free-form": { question: LOOP_QUESTION, options: [], long: false },
  "the loop prompt, described options": {
    question: LOOP_QUESTION,
    options: DESCRIBED_OPTIONS,
    long: false,
  },
  "a 600-character free-form question": {
    question:
      "I finished the first pass over the dock styles and found two places where the question card's height is decided: the shared card ceiling, which follows the visual viewport, and the run tab's bottom bar, which caps the dock, the ask and the composer together at three quarters of the screen. Changing either one moves every card kind that shares it, so before I go further I need to know which of these you want: should I keep the change to the question card only, or apply the same ceiling to the permission, approval and elicitation cards as well?",
    options: [],
    long: true,
  },
  "a long question with options": {
    question: `${Array.from({ length: 12 }, (_, i) => `Attempt ${String(i + 1)} failed on the same build step with the same error.`).join(" ")}\n\nWhat would you like me to do?`,
    options: LOOP_OPTIONS,
    long: true,
  },
};

/** The app's chrome above a tab's view on a phone: the toolbar and the tab strip. */
const CHROME_PX = 88;

/** Width, layout height, and the visual viewport's height (below the layout height: keyboard up). */
const VIEWPORTS: readonly (readonly [number, number, number])[] = [
  [320, 568, 568],
  [320, 568, 261],
  [390, 844, 844],
  [390, 844, 388],
  [430, 932, 932],
  [430, 932, 429],
  [768, 1024, 1024],
  [768, 1024, 600],
  [1280, 800, 800],
  [844, 390, 390],
  [568, 320, 320],
];

/** `height <= 30rem`: a phone on its side, where a waiting question takes the whole bar. */
const SIDEWAYS_MAX_H = 480;

/** A visual viewport under 40rem, where, under a soft keyboard, the chat's run strip steps aside for a question, and
 *  a composer while the card holds the typing. */
const SHORT_MAX_VV = 639;

/** A visual viewport under 20rem, where the steer stack steps aside too, and a composer unless it holds the typing. */
const TIGHT_MAX_VV = 319;

type Host = "chat" | "run";

let style: HTMLStyleElement | null = null;
let frame: HTMLElement | null = null;
let view: ExecPageView | null = null;

afterEach(async () => {
  resetDocks();
  setSessions([]);
  view?.dispose();
  view = null;
  frame?.remove();
  frame = null;
  style?.remove();
  style = null;
  for (const attr of [
    "data-pointer",
    "data-theme",
    "data-short-band",
    "data-tight-band",
    "data-keyboard",
  ]) {
    document.documentElement.removeAttribute(attr);
  }
  document.documentElement.style.removeProperty("--dock-viewport-h");
  await browser.viewport(1280, 720);
});

/** One tab view from the shipped markup, under the chrome, in a frame at the layout height. */
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

/** The chat's bar as the screenshot shows it: the card in the dock, the folded run bar below. */
function mountChat(height: number): { dock: HTMLElement; bar: HTMLElement; composer: HTMLElement } {
  const root = mountView(height, '<div id="chat-view"', '<div id="settings-view"');
  const fold = root.querySelector<HTMLElement>("#run-bar-fold")!;
  fold.hidden = false;
  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "run-bar-open run-bar-toggle";
  toggle.setAttribute("aria-expanded", "false");
  const count = document.createElement("span");
  count.className = "run-bar-count";
  count.textContent = "7 workflows, 1 needs you";
  toggle.append(count);
  fold.append(toggle);
  const runBar = root.querySelector<HTMLElement>("#run-bar")!;
  runBar.classList.remove("hidden");
  for (let i = 0; i < 7; i++) {
    const row = document.createElement("li");
    row.className = "run-bar-row";
    row.hidden = true;
    runBar.append(row);
  }
  return {
    dock: root.querySelector<HTMLElement>("#decision-dock")!,
    bar: root.querySelector<HTMLElement>("#prompt-form")!,
    composer: root.querySelector<HTMLElement>("#prompt-box")!,
  };
}

/** A run tab waiting on a step's question: the run dock above the card, the run composer below. */
function mountRun(height: number): { dock: HTMLElement; bar: HTMLElement; composer: HTMLElement } {
  const root = mountView(height, '<div id="run-view"', "<!-- One Kiro spec");
  const runView = root.querySelector<HTMLElement>("#run-view")!;
  runView.classList.remove("hidden");
  view = buildExecPage({
    emptyNote: () => "",
    dock: runView.querySelector<HTMLElement>("#run-exec-dock")!,
    controls: () => document.createElement("span"),
    follow: "newest",
  });
  runView.querySelector("#run-body")!.append(view.root);
  view.render({
    label: "nightly",
    state: "input",
    nodes: ["a", "b"].map((id) => ({
      path: `root/${id}`,
      label: id,
      kind: "step",
      state: "input",
      children: [],
    })),
    live: true,
    alert: { kind: "input", text: "Waiting for your answer" },
  });
  return {
    dock: runView.querySelector<HTMLElement>("#run-dock")!,
    bar: runView.querySelector<HTMLElement>(".run-bottom-bar")!,
    composer: runView.querySelector<HTMLElement>("#run-composer")!,
  };
}

async function settle(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
}

/** `el` between `top` and `bottom` on the block axis, as a band bounds it. */
function within(el: Element, top: number, bottom: number, what: string): void {
  const r = el.getBoundingClientRect();
  expect(r.top, `${what} top`).toBeGreaterThanOrEqual(top - 0.5);
  expect(r.bottom, `${what} bottom`).toBeLessThanOrEqual(bottom + 0.5);
}

/** `el` wholly inside `box`, both axes. */
function inside(el: Element, box: DOMRect, what: string): void {
  within(el, box.top, box.bottom, what);
  const r = el.getBoundingClientRect();
  expect(r.left, `${what} left`).toBeGreaterThanOrEqual(box.left - 0.5);
  expect(r.right, `${what} right`).toBeLessThanOrEqual(box.right + 0.5);
}

/** `count` queued steers in the host's stack, built by the stack's own rows. */
function queueSteers(host: Host, count: number): HTMLUListElement {
  const stack = frame!.querySelector<HTMLUListElement>(
    host === "chat" ? "#steer-stack" : "#run-steer-stack",
  )!;
  const steers = Array.from({ length: count }, (_, i): PendingSteer => ({
    id: `steer-${String(i)}`,
    text: "check the logs",
    origin: "user",
  }));
  mountSteerStack(stack, {
    read: () => ({ id: "c1", steers, queued: [] }),
    edit: () => Promise.resolve(),
    editByClear: () => Promise.resolve(),
    remove: () => Promise.resolve(),
    discard: () => Promise.resolve(),
    waitingFor: "the agent",
  });
  return stack;
}

/** The text box holding the typing, if one holds it. */
function typingBox(): HTMLTextAreaElement | null {
  const active = document.activeElement;
  return active instanceof HTMLTextAreaElement && frame!.contains(active) ? active : null;
}

/** The visual viewport, `vv` tall over the frame, modelled from the element holding the typing alone:
 *  beside a composer's draft it ends at the draft's bottom (iOS pans to show the focused field and no
 *  further, the most the composer's pill row allows), and otherwise, the card's own answer box
 *  holding it included, at the frame's bottom, where the bar ends. Physical iOS panning is a device check. */
function visibleBand(vv: number): { top: number; bottom: number } {
  const f = frame!.getBoundingClientRect();
  const field = typingBox();
  const end =
    field === null || field.closest(".dock-card") !== null
      ? f.bottom
      : field.getBoundingClientRect().bottom;
  return { top: Math.max(f.top, end - vv), bottom: end };
}

function unscrolled(box: HTMLElement, what: string): void {
  expect(box.scrollHeight, `${what} holds its content without scrolling`).toBeLessThanOrEqual(
    box.clientHeight + 1,
  );
}

/** A button's target on the pointer tier in force: 44px under a finger, 24px under a mouse. */
function hitFloor(): number {
  return document.documentElement.dataset["pointer"] === "coarse" ? 44 : 24;
}

/** The card's controls (every option, the answer box, Send and Skip) whole inside the card and inside
 *  one visual band with the box holding the typing, each button at the hit floor, every option inside
 *  its region's box (a described one once the region scrolls to it) with its content inside its own
 *  border, and the question region's top in the band. */
function inOneBand(card: HTMLElement, vv: number): void {
  const options = [...card.querySelectorAll<HTMLElement>(".user-input-option")];
  const actions = [...card.querySelectorAll<HTMLElement>(".dock-ask-actions button")];
  expect(options.length + actions.length, "the options, Send and Skip").toBeGreaterThanOrEqual(2);
  const band = visibleBand(vv);
  const field = typingBox();
  if (field !== null) {
    within(field, band.top, band.bottom, "the box holding the typing inside the visible band");
  }
  const atFloor = (el: HTMLElement, label: string): void => {
    expect(el.getBoundingClientRect().height, `${label} at the hit floor`).toBeGreaterThanOrEqual(
      hitFloor() - 0.5,
    );
  };
  const c = card.getBoundingClientRect();
  for (const el of [card.querySelector<HTMLElement>(".dock-ask-text")!, ...actions]) {
    const label = el.tagName === "TEXTAREA" ? "the answer box" : String(el.textContent);
    inside(el, c, `${label} inside the card`);
    within(el, band.top, band.bottom, `${label} inside the visible band`);
    if (el.tagName === "BUTTON") {
      atFloor(el, label);
    }
  }
  const region = card.querySelector<HTMLElement>(".user-input-options")!;
  const described = card.querySelector(".user-input-option-desc") !== null;
  for (const el of options) {
    const label = String(el.querySelector(".user-input-option-title")!.textContent);
    if (described) {
      region.scrollTop += el.getBoundingClientRect().top - region.getBoundingClientRect().top;
    }
    inside(el, region.getBoundingClientRect(), `${label} inside the options' box`);
    inside(el, c, `${label} inside the card`);
    within(el, band.top, band.bottom, `${label} inside the visible band`);
    atFloor(el, label);
    expect(el.scrollHeight, `${label}'s content inside its border`).toBeLessThanOrEqual(
      el.clientHeight + 1,
    );
    const b = el.getBoundingClientRect();
    for (const part of el.children) {
      inside(part, b, `${label}'s ${part.className} inside it`);
    }
  }
  region.scrollTop = 0;
  const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
  within(body, band.top, band.bottom, "the question inside the visible band");
}

/** Where the card's floors outgrow the band: `opensAtItsAnswer`, and every option whole inside the
 *  card and the band once the card, and the options region, scroll to it. */
function scrollsToEachControl(card: HTMLElement, vv: number): void {
  opensAtItsAnswer(card, vv);
  const band = visibleBand(vv);
  const region = card.querySelector<HTMLElement>(".user-input-options")!;
  for (const el of card.querySelectorAll<HTMLElement>(".user-input-option")) {
    const label = String(el.querySelector(".user-input-option-title")!.textContent);
    region.scrollTop += el.getBoundingClientRect().top - region.getBoundingClientRect().top;
    card.scrollTop += el.getBoundingClientRect().top - card.getBoundingClientRect().top;
    inside(el, region.getBoundingClientRect(), `${label} inside the options' box`);
    inside(el, card.getBoundingClientRect(), `${label} inside the scrolled card`);
    within(el, band.top, band.bottom, `${label} inside the visible band`);
  }
  region.scrollTop = 0;
  card.scrollTop = 0;
}

/** The card whole inside the frame and the band, a region a finger scrolls, and, as it opens, the
 *  answer box, Send and Skip whole inside it and the band. */
function opensAtItsAnswer(card: HTMLElement, vv: number): void {
  const band = visibleBand(vv);
  const f = frame!.getBoundingClientRect();
  within(card, f.top, f.bottom, "the card inside the frame");
  within(card, band.top, band.bottom, "the card inside the visible band");
  expect(getComputedStyle(card).overflowY, "a card a finger scrolls").toMatch(/^(auto|scroll)$/);
  const actions = [...card.querySelectorAll<HTMLElement>(".dock-ask-actions button")];
  expect(actions.length, "Send and Skip").toBeGreaterThanOrEqual(2);
  for (const el of [card.querySelector<HTMLElement>(".dock-ask-text")!, ...actions]) {
    const label = el.tagName === "TEXTAREA" ? "the answer box" : String(el.textContent);
    inside(el, card.getBoundingClientRect(), `${label} inside the card`);
    within(el, band.top, band.bottom, `${label} inside the visible band`);
  }
  for (const el of actions) {
    expect(
      el.getBoundingClientRect().height,
      `${el.textContent} at the hit floor`,
    ).toBeGreaterThanOrEqual(hitFloor() - 0.5);
  }
  card.scrollTop = 0;
}

/** The answer box's placeholder on its lines, uncut: rendered as the box's text, which sizes the box
 *  the way the placeholder does. */
function placeholderWhole(card: HTMLElement): void {
  const box = card.querySelector<HTMLTextAreaElement>(".dock-ask-text")!;
  const value = box.value;
  box.value = box.placeholder;
  try {
    expect(box.scrollWidth, "the placeholder inside the box's width").toBeLessThanOrEqual(
      box.clientWidth,
    );
    expect(box.scrollHeight, "the placeholder's lines inside the box").toBeLessThanOrEqual(
      box.clientHeight + 1,
    );
  } finally {
    box.value = value;
  }
}

/** Two of the question's own lines, from the pitch of its first two line boxes. */
function twoQuestionLines(card: HTMLElement): number {
  const range = document.createRange();
  range.selectNodeContents(card.querySelector(".dock-ask-question")!);
  const tops = [...new Set([...range.getClientRects()].map((r) => Math.round(r.top)))].sort(
    (a, b) => a - b,
  );
  expect(tops.length, "a question of two lines or more").toBeGreaterThanOrEqual(2);
  return 2 * (tops[1]! - tops[0]!);
}

/** At its floor or under it, `min-block-size` the computed floor. */
function atItsFloor(el: HTMLElement): boolean {
  return (
    el.getBoundingClientRect().height <= Number.parseFloat(getComputedStyle(el).minBlockSize) + 1
  );
}

/** Whether every member the question's second line outranks has given all it can: the steer stack all
 *  of its height, a composer, the card's options and its answer box down to their floors. */
function lowerFloorsSpent(card: HTMLElement): boolean {
  const bar = card.closest(".bottom-bar")!;
  const shown = (sel: string): HTMLElement[] =>
    [...bar.querySelectorAll<HTMLElement>(sel)].filter((el) => el.checkVisibility());
  return (
    shown(":scope > .steer-stack").every((el) => el.clientHeight <= 1) &&
    shown(':scope > :is([id="prompt-box"], [id="run-composer"])').every(atItsFloor) &&
    shown(".user-input-options, .dock-ask-text").every(atItsFloor)
  );
}

/** The question region at two of the question's lines or more (one, where the floors below it in the
 *  bar's order are spent), scrolling by touch to its end or the clamp's Show more. */
function questionScrollsToItsEnd(card: HTMLElement): void {
  const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
  const twoLines = twoQuestionLines(card);
  expect(twoLines, "two lines of type").toBeGreaterThan(20);
  expect(body.clientHeight, "one line of the question").toBeGreaterThanOrEqual(twoLines / 2 - 1);
  if (body.clientHeight < twoLines - 1) {
    expect(lowerFloorsSpent(card), "under two lines only where every lower floor is spent").toBe(
      true,
    );
  }
  // A script can scroll an `overflow: hidden` box, a finger cannot.
  expect(getComputedStyle(body).overflowY, "a region a finger scrolls").toMatch(/^(auto|scroll)$/);
  body.scrollTop = body.scrollHeight;
  const more = card.querySelector<HTMLElement>(".dock-ask-more:not([hidden])");
  const end = more ?? card.querySelector<HTMLElement>(".dock-ask-question")!;
  expect(end.getBoundingClientRect().bottom, "the question's end in reach").toBeLessThanOrEqual(
    body.getBoundingClientRect().bottom + 0.5,
  );
}

describe("the agent-question card", () => {
  for (const host of ["chat", "run"] as const satisfies readonly Host[]) {
    for (const [name, fixture] of Object.entries(FIXTURES)) {
      for (const [width, height, vv] of VIEWPORTS) {
        for (const theme of ["light", "dark"]) {
          const at = `${String(width)}x${String(height)}${vv < height ? ` keyboard up (${String(vv)}px)` : ""}`;
          it(`${host}: ${name} at ${at}, ${theme}`, async () => {
            await browser.viewport(width, height);
            document.documentElement.setAttribute("data-pointer", width < 1000 ? "coarse" : "fine");
            document.documentElement.setAttribute("data-theme", theme);
            style = mountAppCSS();
            setSessions([makeSession({ id: "c1" })]);
            setActive("c1");
            const { dock, bar, composer } = host === "chat" ? mountChat(height) : mountRun(height);
            if (host === "chat") {
              mountDecisionDock(dock);
            } else {
              mountRunDecisionDock(dock, () => "r1");
            }
            pushDecision({
              kind: "user_input",
              chatID: "c1",
              ...(host === "run" ? { runID: "r1" } : {}),
              requestID: 1,
              payload: { request_id: 1, question: fixture.question, options: [...fixture.options] },
              submit: () => Promise.resolve("answered"),
            });
            await settle();
            const card = dock.querySelector<HTMLElement>(":scope > .dock-user-input")!;
            await expect
              .poll(() => dock.dataset["dockPhase"], { message: "the tray settled" })
              .toBeUndefined();
            // A raised keyboard means a focused field: here the card's own answer box, the state the
            // reader is in while answering. After the frames that publish the unshrunk height: what
            // `dock-ask.ts` writes once the keyboard shrinks the visual viewport and leaves the layout
            // viewport alone.
            const keyboard = vv < height;
            if (keyboard) {
              card.querySelector<HTMLElement>(".dock-ask-text")!.focus();
            } else if (document.activeElement instanceof HTMLElement) {
              // At rest nothing holds the typing, whatever the composer's `autofocus` took.
              document.activeElement.blur();
            }
            await settle();
            paintViewportHeight(vv, 1);
            await settle();

            const sideways = height <= SIDEWAYS_MAX_H;
            const tight = vv <= TIGHT_MAX_VV;
            const short = vv <= SHORT_MAX_VV;
            const strip = bar.querySelector(".run-bar-host");
            if (sideways || tight || (keyboard && short)) {
              expect(composer.checkVisibility(), "the composer aside").toBe(false);
            }
            if (host === "chat") {
              expect(strip!.checkVisibility(), "the run strip aside only for the keyboard").toBe(
                !(sideways || (keyboard && short)),
              );
            }
            if (host === "run") {
              const execHost = bar.querySelector<HTMLElement>("#run-exec-dock")!;
              const shown = execHost.getBoundingClientRect().height;
              if (shown > 0) {
                const runDock = execHost.querySelector<HTMLElement>(".ev-dock")!;
                expect(shown, "the run dock whole or gone").toBeGreaterThanOrEqual(
                  runDock.scrollHeight - 0.5,
                );
              }
            }
            unscrolled(card, "the card");
            inOneBand(card, vv);
            placeholderWhole(card);

            // A phone on its side or a tight band leaves the card its floors and little more: there
            // the question region keeps two lines in view and scrolls to the rest. Everywhere else
            // the region stays unscrolled, and a clamped question keeps Show more inside it.
            const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
            if (sideways || tight) {
              questionScrollsToItsEnd(card);
            } else {
              unscrolled(body, "the question");
              const b = body.getBoundingClientRect();
              for (const more of card.querySelectorAll(".dock-ask-more:not([hidden])")) {
                within(more, b.top, b.bottom, "Show more inside the question's box");
              }
            }
          });
        }
      }
    }
  }

  it("keeps the question's paragraph break", async () => {
    await browser.viewport(1280, 800);
    style = mountAppCSS();
    const { dock } = mountChat(800);
    dock.classList.remove("hidden");
    const card = buildUserInputCard(
      { request_id: 1, question: LOOP_QUESTION, options: [] },
      () => undefined,
    );
    dock.append(card);
    await settle();
    const text = card.querySelector(".dock-ask-question")!.firstChild!;
    const rangeOf = (needle: string): DOMRect => {
      const at = LOOP_QUESTION.indexOf(needle);
      const range = document.createRange();
      range.setStart(text, at);
      range.setEnd(text, at + needle.length);
      return range.getBoundingClientRect();
    };
    const first = rangeOf("loop.");
    const ask = rangeOf("What would");
    expect(ask.top, "the ask a blank line below the preamble").toBeGreaterThanOrEqual(
      first.bottom + first.height / 2,
    );
  });
});

describe("a waiting question and the composer", () => {
  const QUESTION = { request_id: 1, question: LOOP_QUESTION, options: [...LOOP_OPTIONS] };
  const DRAFT = "half a thought";

  /** The host's bar with the real dock mounted and a draft in its composer. */
  async function mountDocked(
    host: Host,
    width: number,
    height: number,
    theme = "light",
    text = DRAFT,
  ): Promise<HTMLTextAreaElement> {
    await browser.viewport(width, height);
    document.documentElement.setAttribute("data-pointer", "coarse");
    document.documentElement.setAttribute("data-theme", theme);
    style = mountAppCSS();
    setSessions([makeSession({ id: "c1" })]);
    setActive("c1");
    if (host === "chat") {
      mountDecisionDock(mountChat(height).dock);
    } else {
      mountRunDecisionDock(mountRun(height).dock, () => "r1");
    }
    const draft = frame!.querySelector<HTMLTextAreaElement>(
      host === "chat" ? "#prompt-input" : "#run-composer-input",
    )!;
    draft.value = text;
    await settle();
    return draft;
  }

  /** The server's side of a delivered answer: `decision_settled` retires the card. A run's question carries its run. */
  async function pushQuestion(host: Host, payload = QUESTION): Promise<HTMLElement> {
    pushDecision({
      kind: "user_input",
      chatID: "c1",
      ...(host === "run" ? { runID: "r1" } : {}),
      requestID: 1,
      payload,
      submit: (): Promise<AnswerOutcome> => {
        collapseSettledDecision("c1", "user_input", 1, "user");
        return Promise.resolve("answered");
      },
    });
    await settle();
    return frame!.querySelector<HTMLElement>(".decision-dock > .dock-user-input")!;
  }

  const closings: Record<string, (host: Host, card: HTMLElement) => void> = {
    "an answer": (_host, card) => {
      card.querySelector<HTMLTextAreaElement>(".dock-ask-text")!.value = "Stop and report";
      [...card.querySelectorAll("button")].find((b) => b.textContent === "Send")!.click();
    },
    "a skip": (_host, card) => {
      [...card.querySelectorAll("button")].find((b) => b.textContent === "Skip")!.click();
    },
    "its turn or run ending": (host) => {
      if (host === "chat") {
        dropTurnDecisions("c1");
      } else {
        dropRunRequests("r1");
      }
    },
  };

  for (const host of ["chat", "run"] as const satisfies readonly Host[]) {
    for (const [closing, close] of Object.entries(closings)) {
      it(`${host}: on a phone on its side, puts the composer aside, then back with its draft after ${closing}`, async () => {
        const draft = await mountDocked(host, 568, 320);
        draft.focus();
        const card = await pushQuestion(host);
        expect(draft.checkVisibility(), "the composer aside").toBe(false);
        expect(document.activeElement, "the typing in the card's answer box").toBe(
          card.querySelector(".dock-ask-text"),
        );

        close(host, card);
        await settle();
        expect(draft.checkVisibility(), "the composer back").toBe(true);
        expect(draft.value, "the draft untouched").toBe(DRAFT);
      });
    }

    it(`${host}: leaves an upright phone's composer and focus where they were`, async () => {
      const draft = await mountDocked(host, 390, 844);
      draft.focus();
      await pushQuestion(host);
      expect(draft.checkVisibility(), "the composer").toBe(true);
      expect(document.activeElement, "focus").toBe(draft);
    });

    it(`${host}: follows the composer aside when the phone turns on its side under a question`, async () => {
      const draft = await mountDocked(host, 390, 844);
      draft.focus();
      const card = await pushQuestion(host);
      expect(document.activeElement, "focus before the turn").toBe(draft);

      const resized = new Promise((r) => {
        window.addEventListener("resize", r, { once: true });
      });
      await browser.viewport(568, 320);
      await resized;
      await settle();
      expect(draft.checkVisibility(), "the composer aside").toBe(false);
      expect(document.activeElement, "the typing in the card's answer box").toBe(
        card.querySelector(".dock-ask-text"),
      );
      expect(draft.value, "the draft untouched").toBe(DRAFT);
    });
  }

  it("chat: on a phone on its side, keeps the gap above a queued steer", async () => {
    await mountDocked("chat", 568, 320);
    const stack = queueSteers("chat", 1);
    await pushQuestion("chat");
    const tray = frame!.querySelector<HTMLElement>("#decision-dock")!;
    expect(
      stack.getBoundingClientRect().top - tray.getBoundingClientRect().bottom,
      "the gap between the card and the steer below it",
    ).toBeGreaterThan(1);
  });

  it("chat: on a phone on its side, leaves focus held outside the composer where it is", async () => {
    await mountDocked("chat", 568, 320);
    const other = document.createElement("input");
    document.body.append(other);
    try {
      other.focus();
      await pushQuestion("chat");
      expect(document.activeElement, "focus").toBe(other);
    } finally {
      other.remove();
    }
  });

  it("chat: on a phone on its side, a later resize never pulls focus back to the card", async () => {
    const draft = await mountDocked("chat", 568, 320);
    draft.focus();
    const card = await pushQuestion("chat");
    const box = card.querySelector<HTMLElement>(".dock-ask-text")!;
    expect(document.activeElement, "the typing in the card's answer box").toBe(box);

    box.blur();
    window.dispatchEvent(new Event("resize"));
    await settle();
    expect(document.activeElement, "focus after the resize").toBe(document.body);
  });

  it("a pinch zoom leaves the band flags to the unzoomed band", async () => {
    await browser.viewport(390, 844);
    style = mountAppCSS();
    const root = document.documentElement;
    paintViewportHeight(844 / 2.7, 2.7);
    expect(root.hasAttribute("data-tight-band"), "tight band").toBe(false);
    expect(root.hasAttribute("data-short-band"), "short band").toBe(false);
    expect(root.hasAttribute("data-keyboard"), "a keyboard").toBe(false);
    paintViewportHeight(261, 1);
    expect(root.hasAttribute("data-tight-band"), "a keyboard's band").toBe(true);
    expect(root.hasAttribute("data-keyboard"), "the keyboard").toBe(true);
  });

  it("run: on a tight band, puts an idle composer aside and hands the card the typing", async () => {
    const draft = await mountDocked("run", 320, 568);
    paintViewportHeight(261, 1);
    const card = await pushQuestion("run");
    expect(draft.checkVisibility(), "the composer aside").toBe(false);
    expect(document.activeElement, "the typing in the card's answer box").toBe(
      card.querySelector(".dock-ask-text"),
    );

    dropRunRequests("r1");
    await settle();
    expect(draft.checkVisibility(), "the composer back").toBe(true);
    expect(draft.value, "the draft untouched").toBe(DRAFT);
  });

  // The run composer sends to the selected step (`run-composer.ts`), the card to its own request, so
  // a question arriving mid-typing leaves the cursor in the composer, in either tab. One allocation for
  // both, floors in order: the card's controls (its options included) and two lines of its question,
  // one queued steer, the composer at one line; then the draft, then the rest of the question. Each
  // cell asserts that order against the visual band of whichever box holds the typing. On a phone on
  // its side the controls' floors alone outgrow the band, so there the card scrolls to each of them.
  const KEYBOARD_UP: readonly (readonly [number, number, number])[] = [
    [320, 568, 261],
    [375, 667, 343],
    [390, 844, 388],
    [430, 932, 429],
    [844, 390, 170],
    [667, 375, 160],
  ];
  /** The composers' own ceilings: `#prompt-input` 12.5rem (15-input.css), `#run-composer-input` 8rem (18-pages.css). */
  const DRAFT_CEILING_PX: Record<Host, number> = { chat: 200, run: 128 };
  type Typing = "composer" | "card";
  const CELLS: readonly { lines: number; steers: number; typing: Typing; theme: string }[] = [
    ...(["composer", "card"] as const).flatMap((typing) =>
      ["light", "dark"].map((theme) => ({ lines: 1, steers: 0, typing, theme })),
    ),
    { lines: 4, steers: 0, typing: "composer", theme: "light" },
    { lines: 8, steers: 0, typing: "composer", theme: "light" },
    { lines: 1, steers: 2, typing: "composer", theme: "light" },
    { lines: 8, steers: 2, typing: "composer", theme: "light" },
    { lines: 1, steers: 2, typing: "card", theme: "light" },
  ];
  const holder: Record<Typing, string> = {
    composer: "the composer",
    card: "the card's answer box",
  };
  for (const host of ["chat", "run"] as const satisfies readonly Host[]) {
    for (const [name, fixture] of Object.entries(FIXTURES)) {
      for (const [width, height, vv] of KEYBOARD_UP) {
        // On a phone on its side the composer steps aside for the card, which takes the typing.
        const cells = height <= SIDEWAYS_MAX_H ? CELLS.filter((c) => c.typing === "card") : CELLS;
        for (const { lines, steers, typing, theme } of cells) {
          const queued = steers > 0 ? ` and ${String(steers)} queued steers` : "";
          it(`${host}: ${name} arriving beside a ${String(lines)}-line draft${queued}, ${holder[typing]} holding the typing, at ${String(width)}x${String(height)} keyboard up (${String(vv)}px), ${theme}`, async () => {
            const text =
              lines === 1
                ? DRAFT
                : Array.from({ length: lines }, (_, i) => `line ${String(i + 1)}`).join("\n");
            const draft = await mountDocked(host, width, height, theme, text);
            const stack = queueSteers(host, steers);
            draft.focus();
            paintViewportHeight(vv, 1);
            await settle();
            const card = await pushQuestion(host, {
              request_id: 1,
              question: fixture.question,
              options: [...fixture.options],
            });
            // The tray's entering phase pins its height until it settles.
            const tray = card.parentElement!;
            await expect
              .poll(() => tray.dataset["dockPhase"], { message: "the tray settled" })
              .toBeUndefined();
            const answer = card.querySelector<HTMLElement>(".dock-ask-text")!;
            if (typing === "card") {
              answer.focus();
            }
            // The resize the viewport change queued publishes the layout height; the keyboard's band again.
            paintViewportHeight(vv, 1);
            await settle();

            expect(document.activeElement, "the typing where the reader put it").toBe(
              typing === "card" ? answer : draft,
            );
            expect(draft.value, "the draft untouched").toBe(text);
            const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
            if (card.scrollHeight > card.clientHeight + 1) {
              // The controls give last: the card scrolls only where the floors below them are spent.
              expect(
                lowerFloorsSpent(card) && body.clientHeight < twoQuestionLines(card) - 1,
                "the card scrolls only past every lower floor and the question's second line",
              ).toBe(true);
              scrollsToEachControl(card, vv);
            } else {
              inOneBand(card, vv);
            }
            const atFloor = (el: HTMLElement): boolean =>
              el.clientHeight <= Number.parseFloat(getComputedStyle(el).minBlockSize) + 1;
            if (typing === "card") {
              expect(
                draft.checkVisibility(),
                "the composer aside while the card holds the typing",
              ).toBe(false);
            } else {
              const box = draft.closest(".prompt-box")!.getBoundingClientRect();
              within(draft, box.top, box.bottom, "the draft inside its composer");
              expect(draft.clientHeight, "the composer at one line or more").toBeGreaterThanOrEqual(
                Number.parseFloat(getComputedStyle(draft).minHeight) - 0.5,
              );
              // Under its own ceiling and still scrolling: the bar took the draft's height.
              if (draft.clientHeight + 1 < Math.min(draft.scrollHeight, DRAFT_CEILING_PX[host])) {
                expect(atFloor(body), "the question at its floor before the draft gives").toBe(
                  true,
                );
                if (stack.checkVisibility()) {
                  expect(atFloor(stack), "one steer row before the draft gives").toBe(true);
                }
              }
            }
            if (steers > 0) {
              if (vv <= TIGHT_MAX_VV) {
                expect(stack.checkVisibility(), "the steer stack aside").toBe(false);
              } else {
                const band = visibleBand(vv);
                const s = stack.getBoundingClientRect();
                within(stack, band.top, band.bottom, "the steer stack inside the visible band");
                expect(getComputedStyle(stack).overflowY, "the rest a scroll away").toMatch(
                  /^(auto|scroll)$/,
                );
                const floor = Number.parseFloat(getComputedStyle(stack).minBlockSize);
                const row = stack.firstElementChild!.getBoundingClientRect().height;
                if (floor + 1 < row) {
                  // The floors outgrew the band, and the steer row gave before the question's did.
                  expect(
                    stack.clientHeight,
                    "the steer stack at its lowered floor",
                  ).toBeLessThanOrEqual(floor + 1);
                  expect(body.clientHeight, "the question's two lines kept").toBeGreaterThanOrEqual(
                    twoQuestionLines(card) - 1,
                  );
                } else {
                  expect(
                    Math.abs(row - floor),
                    "the stack's floor one steer row",
                  ).toBeLessThanOrEqual(1);
                  within(stack.firstElementChild!, s.top, s.bottom, "one queued steer whole");
                }
                if (stack.clientHeight > floor + 1) {
                  unscrolled(body, "the question, while more than one steer row shows");
                }
              }
            }
            if (!fixture.long && vv > TIGHT_MAX_VV && lines === 1 && steers === 0) {
              unscrolled(body, "the question");
            } else {
              questionScrollsToItsEnd(card);
            }
          });
        }
      }
    }
  }

  for (const [width, height, vv] of [
    [375, 667, 343],
    [320, 568, 261],
  ] as const) {
    it(`chat: keeps the controls whole when a second question queues behind the first at ${String(width)}x${String(height)} keyboard up (${String(vv)}px)`, async () => {
      const text = Array.from({ length: 8 }, (_, i) => `line ${String(i + 1)}`).join("\n");
      const draft = await mountDocked("chat", width, height, "light", text);
      draft.focus();
      paintViewportHeight(vv, 1);
      const card = await pushQuestion("chat");
      await expect
        .poll(() => card.parentElement!.dataset["dockPhase"], { message: "the tray settled" })
        .toBeUndefined();
      pushDecision({
        kind: "user_input",
        chatID: "c1",
        requestID: 2,
        payload: { ...QUESTION, request_id: 2 },
        submit: () => Promise.resolve("answered"),
      });
      await settle();
      const depth = card.parentElement!.querySelector<HTMLElement>(".dock-depth")!;
      expect(depth.checkVisibility(), "the depth row, aside on a tight band").toBe(
        vv > TIGHT_MAX_VV,
      );
      unscrolled(card, "the card");
      inOneBand(card, vv);
    });
  }

  // The server caps each option's title and description at 512 bytes (`internal/translate/display_text.go`).
  const AT_THE_CAP: UserInputOption[] = [
    {
      title: "Cancel",
      description: "Stop retrying and hand the step back. ".repeat(14).slice(0, 512),
    },
    { title: "step_".repeat(103).slice(0, 512) },
  ];
  for (const host of ["chat", "run"] as const satisfies readonly Host[]) {
    for (const typing of ["card", "composer"] as const) {
      it(`${host}: keeps Send and Skip in reach beside options at the server's display cap, ${holder[typing]} holding the typing, at 320x568 keyboard up (261px)`, async () => {
        const draft = await mountDocked(host, 320, 568);
        draft.focus();
        paintViewportHeight(261, 1);
        await settle();
        const card = await pushQuestion(host, {
          request_id: 1,
          question: LOOP_QUESTION,
          options: [...AT_THE_CAP],
        });
        await expect
          .poll(() => card.parentElement!.dataset["dockPhase"], { message: "the tray settled" })
          .toBeUndefined();
        if (typing === "card") {
          card.querySelector<HTMLElement>(".dock-ask-text")!.focus();
        }
        paintViewportHeight(261, 1);
        await settle();

        opensAtItsAnswer(card, 261);
        const region = card.querySelector<HTMLElement>(".user-input-options")!;
        expect(
          region.getBoundingClientRect().height,
          "the options inside their ceiling",
        ).toBeLessThanOrEqual(Number.parseFloat(getComputedStyle(region).maxBlockSize) + 0.5);
        expect(region.scrollWidth, "no option wider than the region").toBeLessThanOrEqual(
          region.clientWidth,
        );
        for (const option of region.querySelectorAll<HTMLElement>(".user-input-option")) {
          const b = option.getBoundingClientRect();
          for (const text of option.querySelectorAll(
            ".user-input-option-title, .user-input-option-desc",
          )) {
            inside(text, b, `${text.className} inside its option`);
          }
        }
        region.scrollTop = region.scrollHeight;
        const last = region.lastElementChild!;
        expect(
          last.getBoundingClientRect().bottom,
          "the options' end in reach",
        ).toBeLessThanOrEqual(region.getBoundingClientRect().bottom + 0.5);
      });
    }
  }

  it("chat: on a phone on its side under a keyboard, leaves the card where the reader scrolled it", async () => {
    await mountDocked("chat", 844, 390);
    paintViewportHeight(170, 1);
    const card = await pushQuestion("chat");
    await expect
      .poll(() => card.parentElement!.dataset["dockPhase"], { message: "the tray settled" })
      .toBeUndefined();
    expect(card.scrollHeight, "a card that scrolls").toBeGreaterThan(card.clientHeight + 1);
    card.scrollTop = 0;
    paintViewportHeight(170, 1);
    window.dispatchEvent(new Event("resize"));
    await settle();
    expect(card.scrollTop, "the reader's scroll").toBe(0);
  });

  it("chat: re-measures the card's floor when the keyboard rises under a free-form question", async () => {
    const text = Array.from({ length: 8 }, (_, i) => `line ${String(i + 1)}`).join("\n");
    const draft = await mountDocked("chat", 320, 568, "light", text);
    const card = await pushQuestion("chat", {
      request_id: 1,
      question: LOOP_QUESTION,
      options: [],
    });
    await expect
      .poll(() => card.parentElement!.dataset["dockPhase"], { message: "the tray settled" })
      .toBeUndefined();
    draft.focus();
    paintViewportHeight(261, 1);
    await settle();
    unscrolled(card, "the card");
    inOneBand(card, 261);
    const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
    expect(draft.scrollHeight, "the draft scrolls").toBeGreaterThan(draft.clientHeight + 1);
    expect(body.clientHeight, "the question at its floor").toBeLessThanOrEqual(
      Number.parseFloat(getComputedStyle(body).minBlockSize) + 1,
    );
  });

  it("chat: on a phone on its side, measures the card's floor when its hidden view is shown again", async () => {
    // The resize the viewport change queues repaints the floor, so it lands before the view hides.
    const resized = new Promise((r) => {
      window.addEventListener("resize", r, { once: true });
    });
    await mountDocked("chat", 568, 320);
    await resized;
    paintViewportHeight(320, 1);
    await settle();
    const view = frame!.querySelector<HTMLElement>("#chat-view")!;
    view.classList.add("hidden");
    const card = await pushQuestion("chat");
    const tray = card.parentElement!;
    await expect
      .poll(() => tray.dataset["dockPhase"], { message: "the tray settled" })
      .toBeUndefined();
    view.classList.remove("hidden");
    await expect
      .poll(() => Number.parseFloat(tray.style.getPropertyValue("--dock-floor")), {
        message: "the floor measured on the shown card",
      })
      .toBeGreaterThan(card.querySelector(".dock-ask-actions")!.getBoundingClientRect().height);
    await settle();
    unscrolled(card, "the card");
    inOneBand(card, 320);
  });

  for (const host of ["chat", "run"] as const satisfies readonly Host[]) {
    it(`${host}: keeps the composer at one line on a band shorter than the card's floors`, async () => {
      const text = Array.from({ length: 8 }, (_, i) => `line ${String(i + 1)}`).join("\n");
      const draft = await mountDocked(host, 375, 667, "light", text);
      draft.focus();
      paintViewportHeight(200, 1);
      const card = await pushQuestion(host);
      await expect
        .poll(() => card.parentElement!.dataset["dockPhase"], { message: "the tray settled" })
        .toBeUndefined();
      paintViewportHeight(200, 1);
      await settle();
      const box = draft.closest(".prompt-box")!.getBoundingClientRect();
      within(draft, box.top, box.bottom, "the draft inside its composer");
      expect(draft.clientHeight, "the composer at one line or more").toBeGreaterThanOrEqual(
        Number.parseFloat(getComputedStyle(draft).minHeight) - 0.5,
      );
    });
  }

  it("chat: hands the steer row its floor back when the card's answer box takes the typing", async () => {
    const draft = await mountDocked("chat", 375, 667);
    const stack = queueSteers("chat", 2);
    draft.focus();
    paintViewportHeight(343, 1);
    const card = await pushQuestion("chat", {
      request_id: 1,
      question: LOOP_QUESTION,
      options: [...DESCRIBED_OPTIONS],
    });
    await expect
      .poll(() => card.parentElement!.dataset["dockPhase"], { message: "the tray settled" })
      .toBeUndefined();
    paintViewportHeight(343, 1);
    await settle();
    const row = (): number => stack.firstElementChild!.getBoundingClientRect().height;
    const floor = (): number => Number.parseFloat(getComputedStyle(stack).minBlockSize);
    expect(floor(), "beside the composer the steer row gives").toBeLessThan(row() - 1);

    // Moving the typing raises no viewport event: the bar's own change repaints it.
    card.querySelector<HTMLElement>(".dock-ask-text")!.focus();
    await settle();
    expect(draft.checkVisibility(), "the composer aside").toBe(false);
    expect(Math.abs(floor() - row()), "the steer row's floor back").toBeLessThanOrEqual(1);
    const s = stack.getBoundingClientRect();
    within(stack.firstElementChild!, s.top, s.bottom, "one queued steer whole");
  });

  it("the band check fails a card whose buttons sit below the band", async () => {
    const draft = await mountDocked("chat", 320, 568);
    draft.focus();
    paintViewportHeight(261, 1);
    const card = await pushQuestion("chat");
    await expect
      .poll(() => card.parentElement!.dataset["dockPhase"], { message: "the tray settled" })
      .toBeUndefined();
    paintViewportHeight(261, 1);
    await settle();
    inOneBand(card, 261);
    card.querySelector<HTMLElement>(".dock-ask-actions")!.style.translate = "0 120px";
    expect(() => {
      inOneBand(card, 261);
    }).toThrow();
  });

  it("run: on a tight band, keeps a composer that holds the typing", async () => {
    const draft = await mountDocked("run", 320, 568);
    draft.focus();
    paintViewportHeight(261, 1);
    const card = await pushQuestion("run");
    expect(draft.checkVisibility(), "the composer").toBe(true);
    expect(document.activeElement, "focus").toBe(draft);

    card.querySelector<HTMLElement>(".dock-ask-text")!.focus();
    await settle();
    expect(draft.checkVisibility(), "the composer aside once the card holds the typing").toBe(
      false,
    );
    expect(draft.value, "the draft untouched").toBe(DRAFT);
  });
});

// A press focuses a button at pointerdown and the click lands at pointerup, so a bar that reflows on
// focus moves the button out from under the pointer and the click goes nowhere. Each press is at the
// control's own place, with the real pointer, in the states a reader presses from.
describe("a press on a waiting question's controls", () => {
  type Typing = "composer" | "card" | null;
  const STATES: readonly {
    readonly name: string;
    readonly size: readonly [number, number, number];
    readonly pointer: "fine" | "coarse";
    readonly typing: Typing;
  }[] = [
    { name: "a 1280x600 window", size: [1280, 600, 600], pointer: "fine", typing: null },
    { name: "a 1024x560 window", size: [1024, 560, 560], pointer: "fine", typing: null },
    {
      name: "a 430x932 phone, keyboard up (429px), the composer typing",
      size: [430, 932, 429],
      pointer: "coarse",
      typing: "composer",
    },
    {
      name: "a 430x932 phone, keyboard up (429px), the card's answer box typing",
      size: [430, 932, 429],
      pointer: "coarse",
      typing: "card",
    },
  ];
  const ANSWER = "Stop and report";
  /** What each control sends: the wire's action and answer. */
  const SENDS: Record<string, readonly [string, string | undefined]> = {
    Cancel: ["answered", "Cancel"],
    "Keep Iterating": ["answered", "Keep Iterating"],
    Send: ["answered", ANSWER],
    Skip: ["dismissed", undefined],
  };

  for (const host of ["chat", "run"] as const satisfies readonly Host[]) {
    for (const { name, size, pointer, typing } of STATES) {
      const targets = [
        ...Object.keys(SENDS),
        ...(typing === "composer" ? [] : ["the answer box"]),
        ...(host === "chat" && typing === null ? ["the run strip"] : []),
      ];
      for (const target of targets) {
        it(`${host}: a press on ${target} lands, in ${name}`, async () => {
          const [width, height, vv] = size;
          await browser.viewport(width, height);
          document.documentElement.setAttribute("data-pointer", pointer);
          style = mountAppCSS();
          setSessions([makeSession({ id: "c1" })]);
          setActive("c1");
          const { dock, bar, composer } = host === "chat" ? mountChat(height) : mountRun(height);
          if (host === "chat") {
            mountDecisionDock(dock);
          } else {
            mountRunDecisionDock(dock, () => "r1");
          }
          const sent: (readonly unknown[])[] = [];
          pushDecision({
            kind: "user_input",
            chatID: "c1",
            ...(host === "run" ? { runID: "r1" } : {}),
            requestID: 1,
            payload: { request_id: 1, question: LOOP_QUESTION, options: [...LOOP_OPTIONS] },
            submit: (...args) => {
              sent.push(args);
              return Promise.resolve("answered");
            },
          });
          await settle();
          const card = dock.querySelector<HTMLElement>(":scope > .dock-user-input")!;
          await expect
            .poll(() => dock.dataset["dockPhase"], { message: "the tray settled" })
            .toBeUndefined();
          const answer = card.querySelector<HTMLTextAreaElement>(".dock-ask-text")!;
          answer.value = ANSWER;
          if (typing === "card") {
            answer.focus();
          } else if (typing === "composer") {
            composer.querySelector<HTMLElement>("textarea")!.focus();
          } else if (document.activeElement instanceof HTMLElement) {
            document.activeElement.blur();
          }
          paintViewportHeight(vv, 1);
          await settle();

          const strip = bar.querySelector<HTMLElement>(".run-bar-host");
          const buttons = [
            ...card.querySelectorAll<HTMLElement>(".user-input-option, .dock-ask-actions button"),
          ];
          const el =
            target === "the answer box"
              ? answer
              : target === "the run strip"
                ? strip!.querySelector<HTMLElement>(".run-bar-toggle")!
                : buttons.find(
                    (b) =>
                      (b.querySelector(".user-input-option-title") ?? b).textContent === target,
                  )!;
          expect(el, target).toBeInstanceOf(HTMLElement);
          const shown = (): readonly boolean[] => [
            composer.checkVisibility(),
            strip?.checkVisibility() ?? false,
          ];
          const before = el.getBoundingClientRect();
          const restingShown = shown();
          let clicks = 0;
          let moved = 0;
          let clickShown: readonly boolean[] = [];
          // In capture, so it reads the layout the click landed in before any handler answers it.
          const onClick = (e: MouseEvent): void => {
            if (e.target instanceof Node && el.contains(e.target)) {
              clicks++;
              const r = el.getBoundingClientRect();
              moved = Math.max(Math.abs(r.top - before.top), Math.abs(r.left - before.left));
              clickShown = shown();
            }
          };
          document.addEventListener("click", onClick, { capture: true });
          try {
            await userEvent.click(el);
          } finally {
            document.removeEventListener("click", onClick, { capture: true });
          }

          expect(clicks, `the click on ${target}`).toBe(1);
          expect(moved, `${target} where the pointer went down`).toBeLessThanOrEqual(0.5);
          expect(clickShown, "the composer and the run strip as they were").toEqual(restingShown);
          if (typing === null) {
            expect(restingShown, "the composer and the run strip beside the card").toEqual([
              true,
              host === "chat",
            ]);
          }
          if (target in SENDS) {
            expect(sent, `what ${target} sends`).toEqual([SENDS[target]]);
          } else {
            expect(sent, "nothing sent").toEqual([]);
          }
          if (target === "the answer box") {
            expect(document.activeElement, "the typing in the answer box").toBe(answer);
          }
        });
      }
    }
  }
});
