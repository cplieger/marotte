// A dead key cancelled by Backspace can commit as the accent plus a literal U+0008 (seen in a
// prompt recorded as "`\b"; VS Code filters the same macOS artifact). Each U+0008 that lands in
// a text field is applied as the backspace it stands for. The terminal's `.term-input` is
// excluded: there U+0008 is a keystroke the shell must receive.

import { applyEdit, type Edit } from "./text-edit.js";

const BS = "\b";

type TextField = HTMLTextAreaElement | HTMLInputElement;

/** The edit applying every U+0008 in `value` to the code point before it, or null. The span runs
 *  from the first character deleted to the last U+0008, so it never splits a surrogate pair. */
function backspaceEdit(value: string, caret: number): Edit | null {
  const kept: { readonly ch: string; readonly at: number }[] = [];
  let start = -1;
  let end = -1;
  let at = 0;
  for (const ch of value) {
    if (ch === BS) {
      const from = kept.pop()?.at ?? at;
      start = start === -1 ? from : Math.min(start, from);
      end = at + 1;
    } else {
      kept.push({ ch, at });
    }
    at += ch.length;
  }
  if (start === -1) {
    return null;
  }
  const inSpan = kept.filter((k) => k.at >= start && k.at < end);
  const text = inSpan.map((k) => k.ch).join("");
  let moved = caret;
  if (caret >= end) {
    moved = caret - (end - start - text.length);
  } else if (caret > start) {
    moved = start + inSpan.filter((k) => k.at < caret).reduce((n, k) => n + k.ch.length, 0);
  }
  return { start, end, text, caret: moved };
}

function textField(target: EventTarget | null): TextField | null {
  if (target instanceof HTMLTextAreaElement) {
    return target.classList.contains("term-input") ? null : target;
  }
  // `selectionStart` is null on input types with no text selection (number, email, …).
  return target instanceof HTMLInputElement && target.selectionStart !== null ? target : null;
}

function resolve(ev: Event): void {
  const el = textField(ev.target);
  if (el === null || (ev instanceof InputEvent && ev.isComposing)) {
    return;
  }
  const edit = backspaceEdit(el.value, el.selectionStart ?? el.value.length);
  if (edit !== null) {
    applyEdit(el, edit);
  }
}

/** Resolve U+0008 in every text field of the page. Capture phase, so every other listener reads
 *  the resolved value. */
export function guardBackspaceChars(): void {
  document.addEventListener("input", resolve, { capture: true });
  document.addEventListener("compositionend", resolve, { capture: true });
}
