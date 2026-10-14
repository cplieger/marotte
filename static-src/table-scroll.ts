// A markdown table wider than the reply scrolls inside its own box (13-messages.css). Chrome and
// Firefox make a scroll container with no focusable child keyboard-focusable; Safari does not, so
// the columns past the edge would be out of a keyboard reader's reach there. A table that overflows
// is given a tab stop and a name; one that fits is left out of the tab order. The Safari gap:
// https://developer.apple.com/forums/thread/810694. Chrome's behaviour:
// https://developer.chrome.com/blog/keyboard-focusable-scrollers.

const LABEL = "Scrollable table";

/** Tab stop and name on a table that overflows, neither on one that fits. */
function sync(table: HTMLElement): void {
  if (table.scrollWidth > table.clientWidth) {
    table.setAttribute("tabindex", "0");
    table.setAttribute("aria-label", LABEL);
  } else {
    table.removeAttribute("tabindex");
    table.removeAttribute("aria-label");
  }
}

/** Tables the shared observer watches, so a discarded one can be let go of. */
const observed = new Set<HTMLElement>();

function release(table: HTMLElement): void {
  resizes.unobserve(table);
  observed.delete(table);
}

/** The box changes with the reply's width (rotation, a resized side panel) and with the first layout
 *  of a table that was rendered detached, so the verdict is re-read then. */
const resizes = new ResizeObserver((entries) => {
  for (const entry of entries) {
    const table = entry.target as HTMLElement;
    // Supplement only: a removed table's last report may arrive, and is let go of if it does. Engines
    // differ on whether it does (WebKit may not deliver it), so `releaseScrollableTablesIn` is the mechanism.
    if (!table.isConnected) {
      release(table);
      continue;
    }
    sync(table);
  }
});

/** Settle every table in or at `root`. Idempotent. Run when a block completes and when a replay ends:
 *  the renderer attaches no observer, and those are the first points a table is whole. */
export function syncScrollableTables(root: HTMLElement): void {
  const tables = root.querySelectorAll<HTMLElement>("table");
  for (const table of root.tagName === "TABLE" ? [root, ...tables] : tables) {
    resizes.observe(table);
    observed.add(table);
    sync(table);
  }
}

/** Stop watching every table inside `root`, `root` included. Call it where `root` is about to be
 *  discarded; a released table that stays in the page is no longer re-read on a resize. */
export function releaseScrollableTablesIn(root: HTMLElement): void {
  for (const table of [...observed]) {
    if (root === table || root.contains(table)) {
      release(table);
    }
  }
}

/** Number of tables the shared observer watches. Test-only. */
// deadset:ignore DS1004 -- test seam: observes the shared table observer's watched set
export function tableObservationCount(): number {
  return observed.size;
}
