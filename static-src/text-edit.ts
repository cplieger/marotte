// One textarea replacement the browser's own undo can take back:
// `execCommand("insertText")` is deprecated and still the only insertion API that
// keeps native undo history, where assigning `.value` resets it.

/** Replace `[start, end)` with `text`, then put the caret at `caret`. */
export interface Edit {
  readonly start: number;
  readonly end: number;
  readonly text: string;
  readonly caret: number;
}

/** `silent` swallows the `input` event the edit produces, for a write that is
 *  navigation rather than typing (history recall). */
interface ApplyOpts {
  readonly silent?: boolean;
}

/** Apply `edit` as one undo step. Falls back to `setRangeText` plus a bubbling
 *  `input` event, which costs the undo history only. */
export function applyEdit(
  el: HTMLTextAreaElement | HTMLInputElement,
  edit: Edit,
  opts: ApplyOpts = {},
): void {
  const silent = opts.silent === true;
  // On `window` in the capture phase, so it runs ahead of every other input
  // listener whatever the at-target ordering.
  const swallow = (ev: Event): void => {
    if (ev.target === el) {
      ev.stopImmediatePropagation();
    }
  };
  if (silent) {
    window.addEventListener("input", swallow, { capture: true });
  }
  try {
    if (!insertNatively(el, edit)) {
      el.setRangeText(edit.text, edit.start, edit.end, "end");
      if (!silent) {
        el.dispatchEvent(new Event("input", { bubbles: true }));
      }
    }
  } finally {
    if (silent) {
      window.removeEventListener("input", swallow, { capture: true });
    }
  }
  el.setSelectionRange(edit.caret, edit.caret);
}

/** The native path, or false when the browser declined it. execCommand acts on
 *  the focused element, so an unfocused box always takes the fallback. */
function insertNatively(el: HTMLTextAreaElement | HTMLInputElement, edit: Edit): boolean {
  if (document.activeElement !== el) {
    return false;
  }
  if (edit.start === edit.end && edit.text === "") {
    return true;
  }
  el.setSelectionRange(edit.start, edit.end);
  // eslint-disable-next-line @typescript-eslint/no-deprecated -- the only insertion API that keeps native undo.
  return document.execCommand(edit.text === "" ? "delete" : "insertText", false, edit.text);
}
