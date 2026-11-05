// "Quote in reply": a button beside a text selection inside one reply, which appends the selected text to the
// composer as a quote. The button follows the selection: it shows while one qualifies and goes when it stops.

import { el } from "@cplieger/ui";
import { createPopover, type PopoverController } from "@cplieger/ui/popover";
import { CHROME_ATTR } from "./chrome-attr.js";
import { setComposerValue } from "./composer-value.js";
import { $ } from "./dom.js";
import { appendQuote, quoteSelection } from "./message-quote.js";

const REPLY = ".message.assistant";

/** Whether `reply` is reply prose in the transcript: a compaction summary shares the prose skin without being one. */
function isReply(reply: Element, transcript: Element): boolean {
  return transcript.contains(reply) && !reply.classList.contains("compaction-body");
}

function elementOf(node: Node): Element | null {
  return node instanceof Element ? node : node.parentElement;
}

/** Whether `a` to `b` covers no text, so an end there selects nothing outside the reply. */
function coversNoText(a: [Node, number], b: [Node, number]): boolean {
  const gap = document.createRange();
  gap.setStart(...a);
  gap.setEnd(...b);
  return gap.toString() === "";
}

/** Whether `range` covers text inside a `[data-vk-chrome]` element of `reply`, such as a code block's title bar:
 *  UI text, not the reply. Touching one without covering its text, as a range ending at its start does, is not. */
function coversChrome(range: Range, reply: Element): boolean {
  for (const chrome of reply.querySelectorAll(`[${CHROME_ATTR}]`)) {
    // Clipping to a range that misses `chrome` collapses `part`, so it reads "".
    const part = document.createRange();
    part.selectNodeContents(chrome);
    if (range.compareBoundaryPoints(Range.START_TO_START, part) > 0) {
      part.setStart(range.startContainer, range.startOffset);
    }
    if (range.compareBoundaryPoints(Range.END_TO_END, part) < 0) {
      part.setEnd(range.endContainer, range.endOffset);
    }
    if (part.toString() !== "") {
      return true;
    }
  }
  return false;
}

/** The reply holding the whole of `range` with none of its UI text, or null. An end outside the reply counts as
 *  inside when no text lies between it and the reply, as a triple-click's end at the next block's start does. */
function replyOfRange(range: Range, transcript: Element): HTMLElement | null {
  const start = elementOf(range.startContainer);
  const end = elementOf(range.endContainer);
  const reply = start?.closest<HTMLElement>(REPLY) ?? end?.closest<HTMLElement>(REPLY) ?? null;
  if (reply === null || !isReply(reply, transcript)) {
    return null;
  }
  const startIn = reply.contains(range.startContainer);
  const endIn = reply.contains(range.endContainer);
  if (!startIn && !coversNoText([range.startContainer, range.startOffset], [reply, 0])) {
    return null;
  }
  if (
    !endIn &&
    !coversNoText([reply, reply.childNodes.length], [range.endContainer, range.endOffset])
  ) {
    return null;
  }
  return coversChrome(range, reply) ? null : reply;
}

/** The current selection and its quote block, or null when it does not qualify. */
function selectedQuote(transcript: Element): { range: Range; block: string } | null {
  const sel = document.getSelection();
  // Only range 0 is checked, and Firefox can hold several whose text `toString()` joins
  // (https://developer.mozilla.org/en-US/docs/Web/API/Selection/addRange).
  if (sel === null || sel.isCollapsed || sel.rangeCount !== 1) {
    return null;
  }
  const range = sel.getRangeAt(0);
  if (replyOfRange(range, transcript) === null) {
    return null;
  }
  // The selection's own text keeps the rendered line breaks a Range's text drops.
  const block = quoteSelection(sel.toString());
  return block === "" ? null : { range, block };
}

/** The anchor point: the end of the selection's last line box. */
function endRect(range: Range): DOMRect {
  const rects = range.getClientRects();
  const last = rects.item(rects.length - 1) ?? range.getBoundingClientRect();
  return new DOMRect(last.right, last.bottom, 0, 0);
}

let wired = false;

/** Watch the document's selection and offer "Quote in reply" for one inside a reply in `$.messages`. Idempotent. */
export function initSelectionQuote(): void {
  if (wired) {
    return;
  }
  wired = true;
  let range: Range | null = null;
  const btn = el("button", { type: "button", className: "selection-quote-btn" }, "Quote in reply");
  const panel = el("div", { className: "selection-quote" }, btn);
  const pop: PopoverController = createPopover(
    { getBoundingClientRect: () => (range === null ? new DOMRect() : endRect(range)) },
    panel,
    // The selection decides dismissal: a click elsewhere collapses it, and an outside-click close would race it.
    { placement: "bottom", align: "start", offset: 4, closeOnOutside: false },
  );

  let pointerHeld = false;
  let frame: number | null = null;
  const evaluate = (): void => {
    frame = null;
    const found = pointerHeld ? null : selectedQuote($.messages);
    if (found === null) {
      range = null;
      pop.hide();
      return;
    }
    range = found.range;
    pop.show();
  };
  const schedule = (): void => {
    frame ??= requestAnimationFrame(evaluate);
  };

  // Mid-drag the selection changes every frame; the button waits for the release.
  document.addEventListener(
    "pointerdown",
    (ev) => {
      if (!(ev.target instanceof Node && panel.contains(ev.target))) {
        pointerHeld = true;
      }
    },
    { capture: true, passive: true },
  );
  for (const type of ["pointerup", "pointercancel"] as const) {
    document.addEventListener(
      type,
      () => {
        pointerHeld = false;
        schedule();
      },
      { capture: true, passive: true },
    );
  }
  document.addEventListener("selectionchange", schedule);
  $.messagesWrap.addEventListener(
    "scroll",
    () => {
      pop.hide();
    },
    { passive: true },
  );

  // Pressing the button must not move focus or collapse the selection it quotes.
  btn.addEventListener("pointerdown", (ev) => {
    ev.preventDefault();
    btn.classList.add("is-pressed");
  });
  for (const type of ["pointerup", "pointercancel", "pointerleave"] as const) {
    btn.addEventListener(type, () => {
      btn.classList.remove("is-pressed");
    });
  }
  btn.addEventListener("click", () => {
    const found = selectedQuote($.messages);
    pop.hide();
    range = null;
    if (found === null) {
      return;
    }
    const input = $.promptInput;
    setComposerValue(appendQuote(input.value, found.block));
    document.getSelection()?.removeAllRanges();
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
  });
}
