// The ask card's shared regions for both prose-question cards (`user-input.ts`, `run-input.ts`): one implementation,
// so the two cannot drift.

import { el } from "@cplieger/reactive";
import { attachClamp } from "./clamp-text.js";

/**
 * Four lines, as `.steer-text` uses: the bar grows upward into the transcript. The stylesheet clamps to the same count
 * (`clamp-line-count.test.ts`).
 */
const CLAMP_LINES = 4;

/**
 * What a run ask reads as when its question did not survive, shared by the dock and the card. Declared here: in
 * `decision-dock` it would make `run-input` import its own parent (a cycle).
 */
export const RUN_INPUT_FALLBACK = "A step is waiting for your answer";

/** A custom property because the value is the caller's. */
const ASK_ROWS_PROP = "--ask-rows";

/** `visualViewport.height` in px, on `documentElement`, read by `.dock-card`'s
 *  ceiling in `css/26-dock.css`. */
const DOCK_VIEWPORT_PROP = "--dock-viewport-h";

export interface AskHead {
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

export interface AskEditor {
  /** The answer box's own row. No button shares it. */
  readonly editor: HTMLElement;
  readonly input: HTMLTextAreaElement;
}

/**
 * The answer box, alone on its row, growing with its text. `rows` is the opening height; growth and ceiling are CSS
 * (`css/26-dock.css`), read through `--ask-rows`.
 */
export function askEditor(opts: {
  readonly rows: string;
  readonly placeholder: string;
  readonly label: string;
  /** Seeds the box with held text so a retryable refusal keeps it. A property write, as a textarea's value is. */
  readonly value?: string;
}): AskEditor {
  const input = el("textarea", {
    className: "dock-ask-text",
    rows: opts.rows,
    placeholder: opts.placeholder,
    "aria-label": opts.label,
  }) as HTMLTextAreaElement;
  input.style.setProperty(ASK_ROWS_PROP, opts.rows);
  input.value = opts.value ?? "";
  publishViewportHeight();
  return { editor: el("div", { className: "dock-ask-editor" }, input), input };
}

let viewportWired = false;

/**
 * The card's ceiling follows the visual viewport: with no `interactive-widget` in the meta, a raised keyboard moves
 * neither `dvh` nor `svh`. One frame-coalesced listener (precedent: `pill-expand.ts`).
 */
function publishViewportHeight(): void {
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
