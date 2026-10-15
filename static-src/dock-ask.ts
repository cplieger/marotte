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
  const root = document.documentElement;
  let frame = 0;
  const paint = (): void => {
    frame = 0;
    root.style.setProperty(DOCK_VIEWPORT_PROP, `${String(Math.round(vv.height))}px`);
  };
  const schedule = (): void => {
    if (frame === 0) {
      frame = requestAnimationFrame(paint);
    }
  };
  vv.addEventListener("resize", schedule);
  paint();
}

/** The card's ONE right-aligned button row, holding every button it has. */
export function askActions(...buttons: readonly HTMLElement[]): HTMLElement {
  return el("div", { className: "dock-ask-actions" }, ...buttons);
}
