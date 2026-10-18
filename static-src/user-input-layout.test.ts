import { describe, it, expect, afterEach } from "vitest";
import { page as browser } from "vitest/browser";
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

/** A visual viewport under 20rem, where the chat's run strip steps aside, and the run composer unless it holds the focus. */
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
  for (const attr of ["data-pointer", "data-theme", "data-short-band", "data-tight-band"]) {
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

/** The visual viewport, `vv` tall over the frame, modelled from the box holding the typing alone:
 *  beside the composer's draft it ends at the draft's bottom (iOS pans to show the focused field and
 *  no further, the most the composer's pill row allows), and with the card's own answer box holding it,
 *  at the frame's bottom, where the bar ends. Physical iOS panning is a device check. */
function visibleBand(field: Element, vv: number): { top: number; bottom: number } {
  const f = frame!.getBoundingClientRect();
  const end =
    field.closest(".dock-card") === null ? field.getBoundingClientRect().bottom : f.bottom;
  return { top: Math.max(f.top, end - vv), bottom: end };
}

function unscrolled(box: HTMLElement, what: string): void {
  expect(box.scrollHeight, `${what} holds its content without scrolling`).toBeLessThanOrEqual(
    box.clientHeight + 1,
  );
}

/** The answer box, Send, Skip and the question region inside the card and inside one visual
 *  band with `field`, the box holding the typing. */
function inOneBand(card: HTMLElement, field: Element, vv: number): void {
  const controls = [
    card.querySelector<HTMLElement>(".dock-ask-text")!,
    ...card.querySelectorAll<HTMLElement>(".dock-ask-actions button"),
  ];
  expect(controls.length, "the answer box, Send and Skip").toBe(3);
  const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
  const band = visibleBand(field, vv);
  within(field, band.top, band.bottom, "the box holding the typing inside the visible band");
  const c = card.getBoundingClientRect();
  for (const el of controls) {
    const label = el.tagName === "TEXTAREA" ? "the answer box" : String(el.textContent);
    within(el, c.top, c.bottom, `${label} inside the card`);
    within(el, band.top, band.bottom, `${label} inside the visible band`);
  }
  for (const options of card.querySelectorAll(".user-input-options:not(:empty)")) {
    within(options, c.top, c.bottom, "the options");
  }
  within(body, band.top, band.bottom, "the question inside the visible band");
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

/** The question region at two of the question's lines or more, scrolling by touch to its end or
 *  the clamp's Show more. */
function questionScrollsToItsEnd(card: HTMLElement): void {
  const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
  const twoLines = twoQuestionLines(card);
  expect(twoLines, "two lines of type").toBeGreaterThan(20);
  expect(body.clientHeight, "two lines of the question").toBeGreaterThanOrEqual(twoLines - 1);
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
            paintViewportHeight(vv, 1);
            await settle();

            const sideways = height <= SIDEWAYS_MAX_H;
            const tight = vv <= TIGHT_MAX_VV;
            const strip = bar.querySelector(".run-bar-host");
            if (sideways) {
              expect(composer.checkVisibility(), "the composer aside").toBe(false);
              expect(strip?.checkVisibility() ?? false, "the run strip aside").toBe(false);
            } else if (host === "run" && tight) {
              expect(composer.checkVisibility(), "the run composer aside").toBe(false);
            } else if (tight) {
              expect(strip!.checkVisibility(), "the run strip aside").toBe(false);
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
            const typingInComposer = composer.checkVisibility();
            const field = typingInComposer
              ? composer.querySelector("textarea")!
              : card.querySelector<HTMLElement>(".dock-ask-text")!;
            inOneBand(card, field, vv);

            // A long question on a phone on its side or beside an aside run composer, or any question
            // beside a composer holding the typing on a tight band, leaves the card its floors and no
            // more: there the question region keeps two lines in view and scrolls to the rest.
            // Everywhere else the region stays unscrolled, and a clamped question keeps Show more
            // inside it.
            const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
            if (
              (fixture.long && (sideways || (host === "run" && tight))) ||
              (typingInComposer && tight)
            ) {
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
    const stack = frame!.querySelector<HTMLElement>("#steer-stack")!;
    const row = document.createElement("li");
    row.className = "steer-row";
    row.textContent = "and check the logs";
    stack.append(row);
    stack.classList.remove("hidden");
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

  it("a pinch zoom leaves the band flags to the unzoomed band", () => {
    style = mountAppCSS();
    paintViewportHeight(844 / 2.7, 2.7);
    expect(document.documentElement.hasAttribute("data-tight-band"), "tight band").toBe(false);
    expect(document.documentElement.hasAttribute("data-short-band"), "short band").toBe(false);
    paintViewportHeight(261, 1);
    expect(document.documentElement.hasAttribute("data-tight-band"), "a keyboard's band").toBe(
      true,
    );
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
  // both, in order: the card's controls and two lines of its question, then the composer at one line or
  // more, then the rest of the question. Each cell asserts that order against the visual band.
  const KEYBOARD_UP: readonly (readonly [number, number, number])[] = [
    [320, 568, 261],
    [375, 667, 343],
    [390, 844, 388],
    [430, 932, 429],
  ];
  /** The composers' own ceilings: `#prompt-input` 12.5rem (15-input.css), `#run-composer-input` 8rem (18-pages.css). */
  const DRAFT_CEILING_PX: Record<Host, number> = { chat: 200, run: 128 };
  const CELLS = [
    ...["light", "dark"].map((theme) => ({ lines: 1, typing: true, theme })),
    ...[1, 4, 8].flatMap((lines) => [
      ...(lines === 1 ? [] : [{ lines, typing: true, theme: "light" }]),
      { lines, typing: false, theme: "light" },
    ]),
  ];
  for (const host of ["chat", "run"] as const satisfies readonly Host[]) {
    for (const [name, fixture] of Object.entries(FIXTURES)) {
      for (const [width, height, vv] of KEYBOARD_UP) {
        for (const { lines, typing, theme } of CELLS) {
          const drafted = `a ${String(lines)}-line draft${typing ? " the composer holds the typing in" : ""}`;
          it(`${host}: ${name} arriving beside ${drafted} at ${String(width)}x${String(height)} keyboard up (${String(vv)}px), ${theme}`, async () => {
            const text =
              lines === 1
                ? DRAFT
                : Array.from({ length: lines }, (_, i) => `line ${String(i + 1)}`).join("\n");
            const draft = await mountDocked(host, width, height, theme, text);
            if (typing) {
              draft.focus();
            }
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
            // The resize the viewport change queued publishes the layout height; the keyboard's band again.
            paintViewportHeight(vv, 1);
            await settle();

            if (typing) {
              expect(document.activeElement, "the cursor still in the composer").toBe(draft);
            }
            expect(draft.value, "the draft untouched").toBe(text);
            unscrolled(card, "the card");
            const composerShown = draft.checkVisibility();
            const field = composerShown
              ? draft
              : card.querySelector<HTMLElement>(".dock-ask-text")!;
            inOneBand(card, field, vv);
            const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
            if (composerShown) {
              const box = draft.closest(".prompt-box")!.getBoundingClientRect();
              within(draft, box.top, box.bottom, "the draft inside its composer");
              expect(draft.clientHeight, "the composer at one line or more").toBeGreaterThanOrEqual(
                Number.parseFloat(getComputedStyle(draft).minHeight) - 0.5,
              );
              // Under its own ceiling and still scrolling: the bar took the draft's height.
              if (draft.clientHeight + 1 < Math.min(draft.scrollHeight, DRAFT_CEILING_PX[host])) {
                expect(
                  body.clientHeight,
                  "the question at its floor before the draft gives",
                ).toBeLessThanOrEqual(Number.parseFloat(getComputedStyle(body).minBlockSize) + 1);
              }
            }
            if (!fixture.long && vv > TIGHT_MAX_VV && lines === 1) {
              unscrolled(body, "the question");
            } else {
              questionScrollsToItsEnd(card);
            }
          });
        }
      }
    }
  }

  it("chat: keeps the controls whole when a second question queues behind the first", async () => {
    const text = Array.from({ length: 8 }, (_, i) => `line ${String(i + 1)}`).join("\n");
    const draft = await mountDocked("chat", 320, 568, "light", text);
    draft.focus();
    paintViewportHeight(261, 1);
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
    expect(depth.checkVisibility(), "the depth row").toBe(true);
    unscrolled(card, "the card");
    inOneBand(card, draft, 261);
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
    inOneBand(card, draft, 261);
    const body = card.querySelector<HTMLElement>(".dock-ask-body")!;
    expect(draft.scrollHeight, "the draft scrolls").toBeGreaterThan(draft.clientHeight + 1);
    expect(body.clientHeight, "the question at its floor").toBeLessThanOrEqual(
      Number.parseFloat(getComputedStyle(body).minBlockSize) + 1,
    );
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
    inOneBand(card, draft, 261);
    card.querySelector<HTMLElement>(".dock-ask-actions")!.style.translate = "0 120px";
    expect(() => {
      inOneBand(card, draft, 261);
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
