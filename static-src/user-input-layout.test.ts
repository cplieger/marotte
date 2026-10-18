// The agent-question card as the chat's dock shows it on a phone, under the shipped stylesheet: the
// question, the options, the answer box and Send/Skip all inside the card's own visible box.

import { describe, it, expect, afterEach } from "vitest";
import { page as browser } from "vitest/browser";
import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildUserInputCard } from "./user-input.js";
import type { UserInputOption } from "./types.js";

/** kiro-cli's own loop prompt. */
const LOOP_QUESTION =
  "... but it's failed 7 times in a row. I'm clearly stuck in a loop.\n\nWhat would you like me to do?";

const OPTIONS: UserInputOption[] = [
  { title: "Try a different approach" },
  { title: "Stop and wait for me" },
];

let style: HTMLStyleElement | null = null;
let frame: HTMLElement | null = null;

afterEach(async () => {
  frame?.remove();
  frame = null;
  style?.remove();
  style = null;
  document.documentElement.removeAttribute("data-pointer");
  document.documentElement.style.removeProperty("--dock-viewport-h");
  await browser.viewport(1280, 720);
});

/** The chat view from the shipped markup in a frame as tall as the layout viewport, the card in
 *  its dock. */
async function mountCard(
  width: number,
  height: number,
  options: readonly UserInputOption[],
): Promise<HTMLElement> {
  await browser.viewport(width, height);
  document.documentElement.setAttribute("data-pointer", "coarse");
  style = mountAppCSS();
  const start = indexHtml.indexOf('<div id="chat-view"');
  const end = indexHtml.indexOf('<div id="settings-view"', start);
  expect(start, "#chat-view in index.html").toBeGreaterThan(-1);
  frame = document.createElement("main");
  frame.id = "chat-area";
  frame.style.cssText = `display:flex;flex-direction:column;block-size:${String(height)}px`;
  frame.insertAdjacentHTML("beforeend", indexHtml.slice(start, end));
  document.body.prepend(frame);
  const dock = frame.querySelector<HTMLElement>("#decision-dock")!;
  dock.classList.remove("hidden");
  const card = buildUserInputCard(
    { request_id: 1, question: LOOP_QUESTION, options: [...options] },
    () => undefined,
  );
  dock.append(card);
  return card;
}

async function settle(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
}

function unscrolled(box: HTMLElement, what: string): void {
  expect(box.scrollHeight, `${what} holds its content without scrolling`).toBeLessThanOrEqual(
    box.clientHeight + 1,
  );
}

function controlsInside(card: HTMLElement, what: string): void {
  const c = card.getBoundingClientRect();
  const controls = [
    card.querySelector<HTMLElement>(".dock-ask-text")!,
    ...card.querySelectorAll<HTMLElement>(".dock-ask-actions button"),
  ];
  expect(controls.length, "the answer box, Send and Skip").toBe(3);
  for (const el of controls) {
    const r = el.getBoundingClientRect();
    const name = `${what}: ${el.tagName === "TEXTAREA" ? "the answer box" : String(el.textContent)}`;
    expect(r.top, `${name} top inside the card`).toBeGreaterThanOrEqual(c.top - 0.5);
    expect(r.bottom, `${name} bottom inside the card`).toBeLessThanOrEqual(c.bottom + 0.5);
  }
}

describe("the agent-question card on a phone", () => {
  for (const [width, height] of [
    [320, 568],
    [390, 844],
    [430, 932],
  ] as const) {
    for (const options of [OPTIONS, []]) {
      const kind = options.length > 0 ? "with options" : "free-form";
      it(`shows the whole loop question, ${kind}, and its controls at ${String(width)}x${String(height)}`, async () => {
        const card = await mountCard(width, height, options);
        await settle();
        const what = `${String(width)}px ${kind}`;
        unscrolled(card, `${what} card`);
        unscrolled(card.querySelector<HTMLElement>(".dock-ask-body")!, `${what} question`);
        unscrolled(card.querySelector<HTMLElement>(".user-input-options")!, `${what} options`);
        controlsInside(card, what);
      });
    }
  }

  // A raised keyboard shrinks the visual viewport and leaves the layout viewport alone (no
  // `interactive-widget` in the meta), so only the published `--dock-viewport-h` moves.
  for (const [width, height, vv] of [
    [390, 844, 388],
    [430, 932, 429],
  ] as const) {
    it(`keeps the answer box, Send and Skip in the card with the keyboard up at ${String(width)}px`, async () => {
      const card = await mountCard(width, height, OPTIONS);
      // After the frames that publish the unshrunk height.
      await settle();
      document.documentElement.style.setProperty("--dock-viewport-h", `${String(vv)}px`);
      await settle();
      const what = `${String(width)}px keyboard up`;
      unscrolled(card, `${what} card`);
      controlsInside(card, what);
      expect(card.getBoundingClientRect().height, `${what} card within the band`).toBeLessThan(vv);
    });
  }
});
