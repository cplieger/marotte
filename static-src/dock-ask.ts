// ---------------------------------------------------------------------------
// The ASK CARD's shared regions, built once for both cards that ask the reader
// a question in prose: the agent's own structured question (`user-input.ts`) and
// a parked workflow step's (`run-input.ts`).
//
// They were two implementations of one component. Each built its own clamped
// question head, its own answer box and its own button row from the same code
// with a different class prefix, declared its own `CLAMP_LINES = 4`, and carried
// its own copy of the skin — so the two drifted, and the user-input card was the
// one that lost: it still carried the `margin-block-end` on every region that it
// had as a <dialog>, and it kept Send inside the answer box's own row. Measured
// on an 800px card, its question sat 40px above its answer box against the
// run-input card's 8px, its box was 62px narrower, and its two controls were on
// two right-aligned lines rather than one row.
//
// So ONE class vocabulary, `dock-ask-*`, with one rule per region in
// `css/26-dock.css`. A card is left with only what the other genuinely has no
// counterpart for: the option cards here, the who-is-asking line and the
// after-restart note there, and each card's own buttons and key binding.
//
// What is deliberately NOT here is a whole-card builder. The two cards differ in
// what sits between the head and the answer box, in their button sets and in
// which keystroke sends, so a single builder would need a slot per difference —
// more machinery than two call sites earn, and it would put each card's own
// decisions behind a parameter.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { attachClamp } from "./clamp-text.js";

/** Lines the question shows before its opener. FOUR, the count `.steer-text`
 *  already uses one region down the same bar and for the same reason: the dock is
 *  a region of the bar, and the bar grows UPWARD into the transcript, so a
 *  question the agent wrote at length costs the reader the conversation it is
 *  about. The stylesheet clamps to this same count (`clamp-line-count.test.ts`
 *  holds the two together). */
const CLAMP_LINES = 4;

/** What a run ask reads as when its question text did not survive. Shared with the
 *  card so the dock's line and the card's heading cannot disagree.
 *
 *  Here rather than in either sharer, for the reason CLAMP_LINES is: `decision-dock`
 *  builds the card and the card needs the string, so declaring it in the dock made
 *  `run-input` import its own parent — a cycle knip reports. This module owns
 *  `askHead`, which is what the string is passed to. */
export const RUN_INPUT_FALLBACK = "A step is waiting for your answer";

/** The caller's opening row count, as a unitless number CSS multiplies by a line box.
 *  A custom property rather than a class, because the value is the caller's. */
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

/** The clamped question and its opener.
 *
 *  The opener is a SIBLING of the clamped element, or the clamp would hide its own
 *  opener; `attachClamp` keeps it hidden until measurement says the text overflows,
 *  and the dock releases it when the card leaves (`releaseClampsIn` in `swap`). */
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

/** The typed-answer box, alone on its row, growing to what is typed into it.
 *
 *  `rows` is the caller's OPENING height and no longer its size: three rows for a
 *  free-form question, one under an option list. The growth and the derived ceiling
 *  are CSS (`css/26-dock.css`), so `--ask-rows` is written here as the one
 *  CSS-readable owner of that count. */
export function askEditor(opts: {
  readonly rows: string;
  readonly placeholder: string;
  readonly label: string;
  /** Seeds the box with text a previous send is still holding, so a retryable
   *  refusal does not re-offer the question with an empty box. A property write
   *  rather than an attribute, which is what a textarea's value is after first
   *  paint; "" is the ordinary case and writes the same empty box. */
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

/** THE CARD'S CEILING FOLLOWS THE VISUAL VIEWPORT, because the layout viewport does
 *  not see the keyboard: the meta carries no `interactive-widget`, so a raised keyboard
 *  moves neither `dvh` nor `svh` while this bar stays anchored to the layout viewport's
 *  bottom. `pill-expand.ts` is the precedent. One frame-coalesced `resize` listener,
 *  installed on the first answer box; a platform without the API publishes nothing. */
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
