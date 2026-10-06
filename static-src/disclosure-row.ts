// A card header is its disclosure's hit target: an activation surface forwarding to the one control, which keeps
// `aria-expanded`, the Tab stop and the chevron rotation.

/**
 * Anything in a header row that owns its click, by kind: `summary` and `label` activate something else,
 * `[contenteditable]` places a caret.
 */
const OWNS_ITS_CLICK =
  "a[href],area[href],button,input,select,textarea,summary,label," +
  "[role=button],[role=link],[role=checkbox],[role=menuitem],[contenteditable]";

/** Make every part of `row` activate `control`, except a nested control and a click that ends a selection in the row. */
export function wireRowToggle(row: HTMLElement, control: HTMLElement): void {
  row.addEventListener("click", (e: Event) => {
    const target = e.target;
    if (!(target instanceof Element)) {
      return;
    }
    const owner = target.closest(OWNS_ITS_CLICK);
    // Bounded to the row: `closest` walks past it, and the row may itself be a control.
    if (owner !== null && owner !== row && row.contains(owner)) {
      return;
    }
    if (hasSelectionIn(row)) {
      return;
    }
    // A detached control keeps its listeners (a tool card removes its chevron), so an unrefused forward would fire it.
    if (!control.isConnected) {
      return;
    }
    control.click();
  });
}

/**
 * Checked at click time: a drag leaves the selection standing through `mouseup`, while a selection elsewhere was
 * collapsed by this click's mousedown.
 */
function hasSelectionIn(row: HTMLElement): boolean {
  const sel = row.ownerDocument.defaultView?.getSelection() ?? null;
  if (sel === null || sel.isCollapsed || sel.rangeCount === 0) {
    return false;
  }
  const anchor = sel.getRangeAt(0).commonAncestorContainer;
  return row.contains(anchor) || anchor.contains(row);
}
