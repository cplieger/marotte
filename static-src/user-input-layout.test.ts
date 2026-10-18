import { describe, it, expect, afterEach } from "vitest";
import { page as browser } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildExecPage, type ExecPageView } from "./exec-view/page.js";
import { buildUserInputCard } from "./user-input.js";
import type { UserInputOption } from "./types.js";

const LOOP_QUESTION =
  "I've been trying to use \"execute_bash\" but it's failed 7 times in a row. I'm clearly stuck in a loop.\n\nWhat would you like me to do?";
const LOOP_OPTIONS: UserInputOption[] = [{ title: "Cancel" }, { title: "Keep Iterating" }];

const FIXTURES: Record<string, { readonly question: string; readonly options: UserInputOption[] }> =
  {
    "the loop prompt": { question: LOOP_QUESTION, options: LOOP_OPTIONS },
    "the loop prompt, free-form": { question: LOOP_QUESTION, options: [] },
    "a 600-character free-form question": {
      question:
        "I finished the first pass over the dock styles and found two places where the question card's height is decided: the shared card ceiling, which follows the visual viewport, and the run tab's bottom bar, which caps the dock, the ask and the composer together at three quarters of the screen. Changing either one moves every card kind that shares it, so before I go further I need to know which of these you want: should I keep the change to the question card only, or apply the same ceiling to the permission, approval and elicitation cards as well?",
      options: [],
    },
    "a long question with options": {
      question: `${Array.from({ length: 12 }, (_, i) => `Attempt ${String(i + 1)} failed on the same build step with the same error.`).join(" ")}\n\nWhat would you like me to do?`,
      options: LOOP_OPTIONS,
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
  [1280, 800, 800],
];

type Host = "chat" | "run";

let style: HTMLStyleElement | null = null;
let frame: HTMLElement | null = null;
let view: ExecPageView | null = null;

afterEach(async () => {
  view?.dispose();
  view = null;
  frame?.remove();
  frame = null;
  style?.remove();
  style = null;
  for (const attr of ["data-pointer", "data-theme"]) {
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
            document.documentElement.style.setProperty("--dock-viewport-h", `${String(vv)}px`);
            await settle();

            // The band is a model of focus panning: vv tall, ending at the frame's bottom, or from
            // the bar's top when the bar is taller. Physical iOS panning is a device check.
            const f = frame!.getBoundingClientRect();
            const bandTop = Math.min(bar.getBoundingClientRect().top, f.bottom - vv);
            const bandBottom = bandTop + vv;

            within(composer, f.top, f.bottom, "the composer inside the view");
            if (host === "run") {
              within(composer, bandTop, bandBottom, "the run composer inside the visible band");
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

            // A 320px band beside the run composer holds the card's floors and no more: there the
            // question shows its first two lines and scrolls; everywhere else it shows whole.
            const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
            within(body, bandTop, bandBottom, "the question inside the visible band");
            if (host === "run" && vv === 261) {
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

  // A phone on its side: the view may be too short for the card's floors beside the composer, so the
  // card may give and scroll; the composer may not leave the view.
  for (const host of ["chat", "run"] as const satisfies readonly Host[]) {
    for (const [width, height] of [
      [844, 390],
      [568, 320],
    ] as const) {
      it(`${host}: keeps the composer in a ${String(width)}x${String(height)} view`, async () => {
        await browser.viewport(width, height);
        document.documentElement.setAttribute("data-pointer", "coarse");
        style = mountAppCSS();
        const { dock, composer } = host === "chat" ? mountChat(height) : mountRun(height);
        dock.classList.remove("hidden");
        const { question, options } = FIXTURES["the loop prompt"]!;
        dock.append(
          buildUserInputCard({ request_id: 1, question, options: [...options] }, () => undefined),
        );
        await settle();
        const f = frame!.getBoundingClientRect();
        within(composer, f.top, f.bottom, "the composer inside the view");
      });
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
