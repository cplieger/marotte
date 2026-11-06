// "Quote in reply" and "Ask in a tangent": buttons beside a text selection inside one source of transcript content.
// The first appends the selected text to the composer as a quote naming that source; the second starts a tangent with
// that quote in the tangent's composer. The buttons follow the selection: they show while one qualifies and go when it
// stops.

import { el, effect } from "@cplieger/ui";
import { createPopover, type PopoverController } from "@cplieger/ui/popover";
import { openTangentChat } from "./chat.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { setComposerValue } from "./composer-value.js";
import { $ } from "./dom.js";
import { appendQuote, codeSpan, quoteSelection } from "./message-quote.js";
import { activeSession } from "./store.js";
import { chatNotice } from "./notice-subject.js";
import { NO_CONVERSATION_TANGENT, tangentRefusal } from "./tangent-refusal.js";
import * as toast from "./toast.js";

/** A control or chrome: its text is a UI label, never quoted. */
const CONTROL = [
  "button",
  '[role="button"]',
  "summary",
  "input",
  "textarea",
  "select",
  '[contenteditable]:not([contenteditable="false"])',
  '[role^="menu"]',
  ".uip-popover",
  `[${CHROME_ATTR}]`,
].join(",");

/** Controls whose text is content, a run's or a file's name, so they quote as the source around them does. Chrome
 *  inside one is still chrome. */
const NAMING_CONTROL =
  ".run-bar-open, .run-head, .run-step-head, .turn-wake-run, .turn-wake-row-run, .tool-file-link, .steer-note-run";

function isControl(e: Element): boolean {
  return e.matches(CONTROL) && (!e.matches(NAMING_CONTROL) || e.matches(`[${CHROME_ATTR}]`));
}

function textOf(root: Element, selector: string): string {
  return root.querySelector(selector)?.textContent.trim() ?? "";
}

function toolLabel(card: Element, inner: Element): string {
  // A steering box's title is its content, so naming it would repeat the quote.
  if (card.getAttribute("data-kind") === "steering") {
    return "From the steering box";
  }
  const title = textOf(card, ".tool-title");
  if (title === "") {
    return card.getAttribute("data-kind") === "hook" ? "From a hook" : "From a tool call";
  }
  if (card.getAttribute("data-kind") === "hook") {
    return `From the hook ${codeSpan(title)}`;
  }
  return inner.closest(".tool-output") !== null
    ? `From the output of ${codeSpan(title)}`
    : `From the tool call ${codeSpan(title)}`;
}

function steerLabel(note: Element): string {
  switch (note.getAttribute("data-origin")) {
    case "agent":
      return "From a workflow result";
    case "user":
      return note.hasAttribute("data-caller")
        ? "From a mid-turn message"
        : "From your mid-turn message";
    default:
      return "From a workflow message";
  }
}

/** `From run <name>`, the name as a code span only when it holds a backtick, which the drawn quote band would
 *  otherwise read as one. */
function runLabel(root: Element, nameSelector: string): string {
  const name = textOf(root, nameSelector);
  if (name === "") {
    return "From a run";
  }
  return `From run ${name.includes("`") ? codeSpan(name) : name}`;
}

/** The kinds of content a quote names, each its own source so a selection crossing two refuses. `inner` is the
 *  element the selection starts in. This table names sources; it does not decide what quotes. */
const LABELLED: readonly { root: string; label: (source: Element, inner: Element) => string }[] = [
  { root: ".message.assistant:not(.compaction-body)", label: () => "From Kiro's reply" },
  { root: ".compaction-body", label: () => "From the compaction summary" },
  {
    root: ".turn-req",
    label: (req) =>
      req.closest<HTMLElement>(".turn-header")?.dataset["trigger"] === "user"
        ? "From your earlier message"
        : "From the turn header",
  },
  { root: ".turn-header", label: () => "From the turn header" },
  { root: ".turn-notice", label: () => "From the turn's notice" },
  { root: ".turn-info-panel", label: () => "From the turn details" },
  { root: ".plan-message", label: () => "From Kiro's plan" },
  { root: ".tool-call", label: toolLabel },
  { root: ".reasoning-block", label: () => "From Kiro's thinking" },
  { root: ".steer-note", label: steerLabel },
  { root: ".run-card", label: (card) => runLabel(card, ".run-name") },
  { root: ".run-bar-row", label: (row) => runLabel(row, ".ev-sum-label") },
  { root: "#run-bar-fold", label: () => "From the run list" },
];

const LABELLED_ROOTS = LABELLED.map((s) => s.root).join(",");

/** The line for content in no labelled kind, whose source is its transcript row. */
const TRANSCRIPT_LABEL = "From the transcript";

/** A transcript row is a direct child of one of these, or of a content region. */
const ROW_PARENT = ".transcript-view, .turn, .turn-body";

/** The regions holding transcript content; everything else on the page (composer, sidebar, Settings, popovers) is
 *  not quotable. */
function contentRegions(): readonly Element[] {
  return [$.messages, $.runBar, $.runBarFold];
}

function elementOf(node: Node): Element | null {
  return node instanceof Element ? node : node.parentElement;
}

/** The innermost transcript row holding `inner`, or null for an element no row holds. */
function rowOf(inner: Element, region: Element): Element | null {
  for (let e: Element | null = inner; e !== null && e !== region; e = e.parentElement) {
    const parent = e.parentElement;
    if (parent === region || parent?.matches(ROW_PARENT) === true) {
      return e;
    }
  }
  return null;
}

/**
 * The one quotability rule: text inside a content region is quotable unless it or an ancestor is a control or
 * chrome. Returns the source holding `node` (its innermost labelled kind, else its transcript row), or null.
 */
function sourceOf(node: Node): Element | null {
  const inner = elementOf(node);
  const region = contentRegions().find((r) => inner !== null && r.contains(inner));
  if (inner === null || region === undefined) {
    return null;
  }
  for (let e: Element | null = inner; e !== null && region.contains(e); e = e.parentElement) {
    if (isControl(e)) {
      return null;
    }
  }
  const labelled = inner.closest(LABELLED_ROOTS);
  return labelled !== null && region.contains(labelled) ? labelled : rowOf(inner, region);
}

/** Whether `a` to `b` covers no text, so an end there selects nothing outside the source. */
function coversNoText(a: [Node, number], b: [Node, number]): boolean {
  const gap = document.createRange();
  gap.setStart(...a);
  gap.setEnd(...b);
  return gap.toString() === "";
}

/** Whether `range` covers text of `e`. Touching it without covering its text, as a range ending at its start
 *  does, is not. */
function coversTextOf(range: Range, e: Element): boolean {
  // Clipping to a range that misses `e` collapses `part`, so it reads "".
  const part = document.createRange();
  part.selectNodeContents(e);
  if (range.compareBoundaryPoints(Range.START_TO_START, part) > 0) {
    part.setStart(range.startContainer, range.startOffset);
  }
  if (range.compareBoundaryPoints(Range.END_TO_END, part) < 0) {
    part.setEnd(range.endContainer, range.endOffset);
  }
  return part.toString() !== "";
}

/** Whether `range` covers text `source` does not own: a control's or chrome's, such as a code block's title bar,
 *  or a labelled kind's nested inside it, which is a second source. */
function coversForeignText(range: Range, source: Element): boolean {
  for (const e of source.querySelectorAll(`${CONTROL},${LABELLED_ROOTS}`)) {
    if ((isControl(e) || e.matches(LABELLED_ROOTS)) && coversTextOf(range, e)) {
      return true;
    }
  }
  return false;
}

/** The source holding the whole of `range` with none of its foreign text, or null. An end outside the source
 *  counts as inside when no text lies between it and the source, as a triple-click's end at the next block's
 *  start does. */
function sourceOfRange(range: Range): Element | null {
  const source = sourceOf(range.startContainer) ?? sourceOf(range.endContainer);
  if (source === null) {
    return null;
  }
  if (
    !source.contains(range.startContainer) &&
    !coversNoText([range.startContainer, range.startOffset], [source, 0])
  ) {
    return null;
  }
  if (
    !source.contains(range.endContainer) &&
    !coversNoText([source, source.childNodes.length], [range.endContainer, range.endOffset])
  ) {
    return null;
  }
  return coversForeignText(range, source) ? null : source;
}

/** The line naming where `range`'s text comes from. */
function sourceLabel(source: Element, range: Range): string {
  const inner = elementOf(range.startContainer);
  const entry = LABELLED.find((s) => source.matches(s.root));
  if (entry === undefined) {
    return TRANSCRIPT_LABEL;
  }
  return entry.label(source, inner !== null && source.contains(inner) ? inner : source);
}

/** The current selection and its quote block, or null when it does not qualify. */
function selectedQuote(): { range: Range; block: string } | null {
  const sel = document.getSelection();
  // Only range 0 is checked, and Firefox can hold several whose text `toString()` joins
  // (https://developer.mozilla.org/en-US/docs/Web/API/Selection/addRange).
  if (sel === null || sel.isCollapsed || sel.rangeCount !== 1) {
    return null;
  }
  const range = sel.getRangeAt(0);
  const source = sourceOfRange(range);
  if (source === null) {
    return null;
  }
  // The selection's own text keeps the rendered line breaks a Range's text drops.
  const block = quoteSelection(sel.toString(), sourceLabel(source, range));
  return block === "" ? null : { range, block };
}

/** The anchor point: the end of the selection's last line box. */
function endRect(range: Range): DOMRect {
  const rects = range.getClientRects();
  const last = rects.item(rects.length - 1) ?? range.getBoundingClientRect();
  return new DOMRect(last.right, last.bottom, 0, 0);
}

/** Append `block` to whatever chat the composer holds now, focused with the caret at the end. */
function writeQuote(block: string): void {
  const input = $.promptInput;
  setComposerValue(appendQuote(input.value, block));
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
}

/** A popover button that presses without moving focus or collapsing the selection it quotes. */
function quoteButton(label: string): HTMLElement {
  const btn = el("button", { type: "button", className: "selection-quote-btn" }, label);
  // `mousedown`, not `pointerdown`: WebKit fires no `click` for a tap whose `pointerdown` was cancelled.
  btn.addEventListener("mousedown", (ev) => {
    ev.preventDefault();
  });
  btn.addEventListener("pointerdown", () => {
    if (btn.getAttribute("aria-disabled") !== "true") {
      btn.classList.add("is-pressed");
    }
  });
  for (const type of ["pointerup", "pointercancel", "pointerleave"] as const) {
    btn.addEventListener(type, () => {
      btn.classList.remove("is-pressed");
    });
  }
  return btn;
}

/** Clears iOS's end-of-selection drag handle, whose knob hangs below the last line box. */
const TOUCH_OFFSET_PX = 24;
const MOUSE_OFFSET_PX = 4;

let wired = false;

/** Watch the document's selection and offer "Quote in reply" and "Ask in a tangent" for one inside one transcript
 *  source. Idempotent. */
export function initSelectionQuote(): void {
  if (wired) {
    return;
  }
  wired = true;
  let range: Range | null = null;
  const btn = quoteButton("Quote in reply");
  const askBtn = quoteButton("Ask in a tangent");
  const panel = el("div", { className: "selection-quote" }, btn, askBtn);
  const pop: PopoverController = createPopover(
    { getBoundingClientRect: () => (range === null ? new DOMRect() : endRect(range)) },
    panel,
    // The selection decides dismissal: a click elsewhere collapses it, and an outside-click close would race it.
    { placement: "bottom", align: "start", offset: MOUSE_OFFSET_PX, closeOnOutside: false },
  );

  let pointerHeld = false;
  let touch = false;
  let frame: number | null = null;
  const evaluate = (): void => {
    frame = null;
    const found = pointerHeld ? null : selectedQuote();
    if (found === null) {
      range = null;
      pop.hide();
      return;
    }
    range = found.range;
    pop.setOptions({ offset: touch ? TOUCH_OFFSET_PX : MOUSE_OFFSET_PX });
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
        touch = ev.pointerType === "touch";
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

  /** The qualifying selection's quote, the popover dismissed and the selection cleared; null when it stopped
   *  qualifying. */
  const takeQuote = (): string | null => {
    const found = selectedQuote();
    pop.hide();
    range = null;
    if (found === null) {
      return null;
    }
    document.getSelection()?.removeAllRanges();
    return found.block;
  };

  btn.addEventListener("click", () => {
    const block = takeQuote();
    if (block !== null) {
      writeQuote(block);
    }
  });

  // `aria-disabled`, not `disabled`: a disabled button fires no click and takes no focus, and a tap must still post
  // the refusal.
  effect(() => {
    const refusal = tangentRefusal(activeSession.value);
    if ((askBtn.dataset["tooltip"] ?? null) === refusal) {
      return;
    }
    if (refusal === null) {
      askBtn.removeAttribute("aria-disabled");
      delete askBtn.dataset["tooltip"];
    } else {
      askBtn.setAttribute("aria-disabled", "true");
      askBtn.dataset["tooltip"] = refusal;
    }
  });
  askBtn.addEventListener("click", () => {
    // Re-read at click time, as the Chat options row does: the popover outlives a chat switch.
    // A refusal is posted too, as the Chat options row posts it: touch never shows the tooltip.
    const session = activeSession.peek();
    if (session === undefined) {
      toast.error(NO_CONVERSATION_TANGENT);
      return;
    }
    const refusal = tangentRefusal(session);
    if (refusal !== null) {
      chatNotice(session.id, refusal, "error");
      return;
    }
    const block = takeQuote();
    if (block === null) {
      return;
    }
    // "" when the fork failed or the reader moved elsewhere first: the quote has no tangent composer to land in.
    void openTangentChat(session.id).then((id) => {
      if (id !== "") {
        writeQuote(block);
      }
    });
  });
}
