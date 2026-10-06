// Reverts iOS Smart Punctuation (`--` to an em dash, curly quotes), which breaks CLI flags and
// shell quoting. No WebKit attribute disables it for a textarea
// (https://stackoverflow.com/q/48678359). The revert is one undo step. No WebKit attribute disables
// it for a textarea (https://stackoverflow.com/q/48678359).

import { applyEdit, type Edit } from "./text-edit.js";

const DASHES: ReadonlySet<string> = new Set(["\u2013", "\u2014"]);
const QUOTES: Readonly<Record<string, string>> = {
  "\u2018": "'",
  "\u2019": "'",
  "\u201C": '"',
  "\u201D": '"',
};

/** The one span that differs between two values: where it starts, what was there and what is
 *  there now. */
function changedSpan(before: string, after: string): { at: number; was: string; now: string } {
  const max = Math.min(before.length, after.length);
  let at = 0;
  while (at < max && before[at] === after[at]) {
    at++;
  }
  let tail = 0;
  while (tail < max - at && before[before.length - 1 - tail] === after[after.length - 1 - tail]) {
    tail++;
  }
  return {
    at,
    was: before.slice(at, before.length - tail),
    now: after.slice(at, after.length - tail),
  };
}

/** The edit that reverts a substitution between `before` and `after`, or null. A dash counts
 *  only when it replaced a hyphen, in this event or in the backspace just before it
 *  (`hyphenDeletedAt`), so a dash picked from the long-press menu after other text survives. */
export function smartRevert(
  before: string,
  after: string,
  hyphenDeletedAt: number | null,
): Edit | null {
  const { at, was, now } = changedSpan(before, after);
  if (DASHES.has(now) && (was === "-" || (was === "" && hyphenDeletedAt === at))) {
    return { start: at, end: at + 1, text: "--", caret: at + 2 };
  }
  const straight = Object.hasOwn(QUOTES, now) ? QUOTES[now] : undefined;
  return straight === undefined ? null : { start: at, end: at + 1, text: straight, caret: at + 1 };
}

/** Where a backspace from `before` to `after` removed exactly one hyphen. */
function removedHyphenAt(before: string, after: string): number | null {
  const { at, was, now } = changedSpan(before, after);
  return was === "-" && now === "" ? at : null;
}

/** Revert substitutions in `el`. */
export function guardSmartPunctuation(el: HTMLTextAreaElement): void {
  let before = el.value;
  let hyphenAt: number | null = null;
  let reverting = false;
  // A substitution may arrive with no `beforeinput` of its own, so the value after the previous
  // input is the fallback snapshot.
  el.addEventListener("beforeinput", () => {
    before = el.value;
  });
  el.addEventListener("input", (ev) => {
    if (reverting) {
      return;
    }
    const prev = before;
    const deleted = hyphenAt;
    before = el.value;
    hyphenAt = null;
    if (!(ev instanceof InputEvent) || ev.isComposing) {
      return;
    }
    if (ev.inputType === "deleteContentBackward") {
      hyphenAt = removedHyphenAt(prev, el.value);
      return;
    }
    if (ev.inputType !== "insertText" && ev.inputType !== "insertReplacementText") {
      return;
    }
    const edit = smartRevert(prev, el.value, deleted);
    if (edit === null) {
      return;
    }
    reverting = true;
    try {
      applyEdit(el, edit);
    } finally {
      reverting = false;
    }
    before = el.value;
  });
}
