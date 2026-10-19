// The prose-question card's regions (`user-input.ts`).

import { el } from "@cplieger/reactive";
import { attachClamp } from "./clamp-text.js";

/**
 * Four lines, as `.steer-text` uses: the bar grows upward into the transcript. The stylesheet clamps to the same count
 * (`clamp-line-count.test.ts`).
 */
const CLAMP_LINES = 4;

/** A custom property because the value is the caller's. */
const ASK_ROWS_PROP = "--ask-rows";

/** `visualViewport.height` in px, on `documentElement`, read by `.dock-card`'s
 *  ceiling in `css/26-dock.css`. */
const DOCK_VIEWPORT_PROP = "--dock-viewport-h";

/** Under this visual-viewport height the run dock steps aside for an ask card (`[data-short-band]`,
 *  `css/18-pages.css`): from here down, the dock, a card's floors and the composer outgrow the bar. */
const SHORT_BAND_REM = 40;

/** Under this one the steer stack and an idle composer step aside for a question card too (`[data-tight-band]`,
 *  `css/26-dock.css`): a four-line question at 320px wide needs about 306px of band beside the composer. */
const TIGHT_BAND_REM = 20;

/** A band this far under the layout viewport is a soft keyboard (`[data-keyboard]`, `css/26-dock.css`): one shrinks
 *  only the visual viewport, by about 10rem or more even on a phone on its side, while the bar iPadOS shows over a
 *  hardware keyboard takes a few rem and a desktop window keeps the two equal. */
const KEYBOARD_MIN_REM = 8;

interface AskHead {
  /** The region to append anything else the card says about its ask to. */
  readonly body: HTMLElement;
  /** The question itself, clamped. */
  readonly text: HTMLElement;
}

/** The clamped question and its opener, a sibling so the clamp cannot hide it. Released by the dock in `swap`. */
export function askHead(question: string): AskHead {
  const text = el("strong", { className: "dock-ask-question" }, question);
  const more = el("button", {
    className: "dock-ask-more",
    type: "button",
  }) as HTMLButtonElement;
  const body = el("div", { className: "dock-ask-body" }, text, more);
  attachClamp(text, more, { lines: CLAMP_LINES });
  return { body, text };
}

/**
 * The answer box, growing with its text. `rows` is the opening height; growth and ceiling are CSS
 * (`css/26-dock.css`), read through `--ask-rows`.
 */
export function askEditor(opts: {
  readonly rows: string;
  readonly placeholder: string;
  readonly label: string;
}): HTMLTextAreaElement {
  const input = el("textarea", {
    className: "dock-ask-text",
    rows: opts.rows,
    placeholder: opts.placeholder,
    "aria-label": opts.label,
  }) as HTMLTextAreaElement;
  input.style.setProperty(ASK_ROWS_PROP, opts.rows);
  publishViewportHeight();
  return input;
}

let viewportWired = false;

/**
 * Publish `--dock-viewport-h` on the root, once: a bottom-bar ceiling follows the visual viewport because, with no
 * `interactive-widget` in the meta, a raised keyboard moves neither `dvh` nor `svh`. One frame-coalesced listener
 * (precedent: `pill-expand.ts`).
 */
export function publishViewportHeight(): void {
  const vv = window.visualViewport;
  if (viewportWired || vv == null) {
    return;
  }
  viewportWired = true;
  let frame = 0;
  const paint = (): void => {
    frame = 0;
    paintViewportHeight(vv.height, vv.scale);
  };
  const schedule = (): void => {
    if (frame === 0) {
      frame = requestAnimationFrame(paint);
    }
  };
  vv.addEventListener("resize", schedule);
  paint();
}

/** Write what the bar reads for a visual viewport `height` px tall at pinch-zoom `scale`: `--dock-viewport-h`, and
 *  `data-short-band`, `data-tight-band` and `data-keyboard` from the unzoomed band, `height * scale` (CSSOM View
 *  divides the visual viewport by its scale), so zooming in to read never hides the composer or the run dock. */
export function paintViewportHeight(height: number, scale: number): void {
  const root = document.documentElement;
  root.style.setProperty(DOCK_VIEWPORT_PROP, `${String(Math.round(height))}px`);
  const rem = Number.parseFloat(getComputedStyle(root).fontSize);
  const band = height * scale;
  root.toggleAttribute("data-short-band", band < SHORT_BAND_REM * rem);
  root.toggleAttribute("data-tight-band", band < TIGHT_BAND_REM * rem);
  root.toggleAttribute("data-keyboard", root.clientHeight - band >= KEYBOARD_MIN_REM * rem);
  for (const follow of bandFollowers) {
    follow();
  }
}

const bandFollowers: (() => void)[] = [];

/** Call `follow` after each band `paintViewportHeight` writes, once the stylesheet can read it. */
export function followBand(follow: () => void): void {
  bandFollowers.push(follow);
}

/** The card's ONE right-aligned button row, holding every button it has. */
export function askActions(...buttons: readonly HTMLElement[]): HTMLElement {
  return el("div", { className: "dock-ask-actions" }, ...buttons);
}
