import { describe, it, expect, afterEach } from "vitest";
import { page as browser } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { makeSession } from "./__test-helpers__/model.js";
import {
  _resetForTest as resetDocks,
  collapseSettledDecision,
  dropTurnDecisions,
  mountDecisionDock,
  pushDecision,
  type AnswerOutcome,
} from "./decision-dock.js";
import { paintViewportHeight } from "./dock-ask.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import { setActive, setSessions } from "./store.js";
import { buildUserInputCard } from "./user-input.js";
import type { UserInputOption } from "./types.js";

const LOOP_QUESTION =
  "I've been trying to use \"execute_bash\" but it's failed 7 times in a row. I'm clearly stuck in a loop.\n\nWhat would you like me to do?";
const LOOP_OPTIONS: UserInputOption[] = [{ title: "Cancel" }, { title: "Keep Iterating" }];

/** `long`: clamped to four lines behind Show more. */
const FIXTURES: Record<
  string,
  { readonly question: string; readonly options: UserInputOption[]; readonly long: boolean }
> = {
  "the loop prompt": { question: LOOP_QUESTION, options: LOOP_OPTIONS, long: false },
  "the loop prompt, free-form": { question: LOOP_QUESTION, options: [], long: false },
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
  for (const attr of ["data-pointer", "data-theme", "data-short-band"]) {
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

function within(el: Element, top: number, bottom: number, what: string): void {
  const r = el.getBoundingClientRect();
  expect(r.top, `${what} top`).toBeGreaterThanOrEqual(top - 0.5);
  expect(r.bottom, `${what} bottom`).toBeLessThanOrEqual(bottom + 0.5);
}

function unscrolled(box: HTMLElement, what: string): void {
  expect(box.scrollHeight, `${what} holds its content without scrolling`).toBeLessThanOrEqual(
    box.clientHeight + 1,
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
            const { dock, bar, composer } = host === "chat" ? mountChat(height) : mountRun(height);
            dock.classList.remove("hidden");
            const card = buildUserInputCard(
              { request_id: 1, question: fixture.question, options: [...fixture.options] },
              () => undefined,
            );
            dock.append(card);
            // After the frames that publish the unshrunk height: what `dock-ask.ts` writes once a
            // raised keyboard shrinks the visual viewport and leaves the layout viewport alone.
            await settle();
            paintViewportHeight(vv);
            await settle();

            // The band is a model of focus panning: vv tall, ending at the frame's bottom, or from
            // the bar's top when the bar is taller. Physical iOS panning is a device check.
            const f = frame!.getBoundingClientRect();
            const bandTop = Math.min(bar.getBoundingClientRect().top, f.bottom - vv);
            const bandBottom = bandTop + vv;

            const sideways = height <= SIDEWAYS_MAX_H;
            if (sideways) {
              expect(composer.checkVisibility(), "the composer aside").toBe(false);
              const strip = bar.querySelector(".run-bar-host");
              expect(strip?.checkVisibility() ?? false, "the run strip aside").toBe(false);
            } else {
              within(composer, f.top, f.bottom, "the composer inside the view");
              if (host === "run") {
                within(composer, bandTop, bandBottom, "the run composer inside the visible band");
              }
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
            const c = card.getBoundingClientRect();
            const controls = [
              card.querySelector<HTMLElement>(".dock-ask-text")!,
              ...card.querySelectorAll<HTMLElement>(".dock-ask-actions button"),
            ];
            expect(controls.length, "the answer box, Send and Skip").toBe(3);
            for (const el of controls) {
              const label = el.tagName === "TEXTAREA" ? "the answer box" : String(el.textContent);
              within(el, c.top, c.bottom, `${label} inside the card`);
              within(el, bandTop, bandBottom, `${label} inside the visible band`);
            }
            for (const options of card.querySelectorAll(".user-input-options:not(:empty)")) {
              within(options, c.top, c.bottom, "the options");
            }

            // A 320px band beside the run composer, and a long question on a phone on its side,
            // leave the card its floors and no more: there the question region shows at least its
            // first two lines and scrolls. Everywhere else the region stays unscrolled, and a clamped
            // question keeps Show more inside it.
            const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
            within(body, bandTop, bandBottom, "the question inside the visible band");
            if ((host === "run" && vv === 261) || (sideways && fixture.long)) {
              // `min-block-size: 2lh`, resolved by the engine.
              const twoLines = Number.parseFloat(getComputedStyle(body).minBlockSize);
              expect(twoLines, "the question's two-line floor").toBeGreaterThan(20);
              expect(body.clientHeight, "two lines of the question").toBeGreaterThanOrEqual(
                twoLines - 1,
              );
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

describe("a waiting question on a phone on its side", () => {
  const QUESTION = { request_id: 1, question: LOOP_QUESTION, options: [...LOOP_OPTIONS] };

  /** The chat bar with the real dock mounted, a draft in the composer and the composer focused. */
  async function mountDockedChat(width: number, height: number): Promise<HTMLTextAreaElement> {
    await browser.viewport(width, height);
    document.documentElement.setAttribute("data-pointer", "coarse");
    style = mountAppCSS();
    setSessions([makeSession({ id: "c1" })]);
    setActive("c1");
    const { dock } = mountChat(height);
    mountDecisionDock(dock);
    const draft = frame!.querySelector<HTMLTextAreaElement>("#prompt-input")!;
    draft.value = "half a thought";
    draft.focus();
    await settle();
    return draft;
  }

  /** The server's side of a delivered answer: `decision_settled` retires the card. */
  function pushQuestion(): void {
    pushDecision({
      kind: "user_input",
      chatID: "c1",
      requestID: 1,
      payload: QUESTION,
      submit: (): Promise<AnswerOutcome> => {
        collapseSettledDecision("c1", "user_input", 1, "user");
        return Promise.resolve("answered");
      },
    });
  }

  const closings: Record<string, (card: HTMLElement) => void> = {
    "an answer": (card) => {
      card.querySelector<HTMLTextAreaElement>(".dock-ask-text")!.value = "Stop and report";
      [...card.querySelectorAll("button")].find((b) => b.textContent === "Send")!.click();
    },
    "a skip": (card) => {
      [...card.querySelectorAll("button")].find((b) => b.textContent === "Skip")!.click();
    },
    "the turn ending": () => {
      dropTurnDecisions("c1");
    },
  };

  for (const [closing, close] of Object.entries(closings)) {
    it(`puts the composer aside, then back with its draft after ${closing}`, async () => {
      const draft = await mountDockedChat(568, 320);
      pushQuestion();
      await settle();
      const card = frame!.querySelector<HTMLElement>("#decision-dock > .dock-user-input")!;
      expect(draft.checkVisibility(), "the composer aside").toBe(false);
      expect(document.activeElement, "the typing in the card's answer box").toBe(
        card.querySelector(".dock-ask-text"),
      );

      close(card);
      await settle();
      expect(draft.checkVisibility(), "the composer back").toBe(true);
      expect(draft.value, "the draft untouched").toBe("half a thought");
    });
  }

  it("leaves an upright phone's composer and focus where they were", async () => {
    const draft = await mountDockedChat(390, 844);
    pushQuestion();
    await settle();
    expect(draft.checkVisibility(), "the composer").toBe(true);
    expect(document.activeElement, "focus").toBe(draft);
  });
});
