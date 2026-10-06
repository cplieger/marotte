// A card header IS its disclosure's hit target, so the same gesture folds every
// card. An ACTIVATION SURFACE, not a second control: the header forwards its click
// to the button that owns the disclosure, so `aria-expanded`, the keyboard path and
// the focus ring stay on that button. Not `role="button"`: these headers hold real
// `<button>`s, and nesting them is axe's `nested-interactive`. Two gestures pass
// through: a click on a nested control (skipped by ELEMENT KIND, which also ends the
// forwarded click) and a click that ends a text SELECTION.

/** Anything inside a header row that owns its own click.
 *
 *  Matched by kind rather than by name. `summary` and `label` are here because
 *  both activate something else when clicked; `[contenteditable]` because a
 *  click there places a caret. */
const OWNS_ITS_CLICK =
  "a[href],area[href],button,input,select,textarea,summary,label," +
  "[role=button],[role=link],[role=checkbox],[role=menuitem],[contenteditable]";

/** Make every part of `row` activate `control`, except a nested control and a
 *  click that ends a selection inside the row.
 *
 *  `control` keeps the disclosure: it carries `aria-expanded`, it is what Tab
 *  reaches, and it is what the stylesheet keys the chevron's rotation off. */
export function wireRowToggle(row: HTMLElement, control: HTMLElement): void {
  row.addEventListener("click", (e: Event) => {
    const target = e.target;
    if (!(target instanceof Element)) {
      return;
    }
    const owner = target.closest(OWNS_ITS_CLICK);
    // Bounded to the row: `closest` walks past it, and the row itself may be a
    // control in some future caller.
    if (owner !== null && owner !== row && row.contains(owner)) {
      return;
    }
    if (hasSelectionIn(row)) {
      return;
    }
    // A control out of the document is one the reader cannot see, and its listeners
    // ride along on the detached element — a tool card takes its chevron away once
    // it has nothing left to reveal — so an unrefused forward would still fire it.
    if (!control.isConnected) {
      return;
    }
    control.click();
  });
}

/** Whether a live text selection sits inside `row`.
 *
 *  Checked at CLICK time, which is when the answer is available: a drag inside
 *  the row leaves the selection standing through `mouseup`, and a selection
 *  made anywhere else was already collapsed by this click's own `mousedown`. */
function hasSelectionIn(row: HTMLElement): boolean {
  const sel = row.ownerDocument.defaultView?.getSelection() ?? null;
  if (sel === null || sel.isCollapsed || sel.rangeCount === 0) {
    return false;
  }
  const anchor = sel.getRangeAt(0).commonAncestorContainer;
  return row.contains(anchor) || anchor.contains(row);
}
