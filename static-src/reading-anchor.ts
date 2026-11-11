// Where the reader's text stands, measured from a transcript view's own boxes. Geometry with no state: the scroll
// owner (scroll-controller.ts) decides when an anchor is taken and what a drift is corrected with.

/** Where the reading line sits, as a fraction of the scrollport from its top. Scroller geometry, so a jump's landing
 *  and the turn the rail calls active are the same line. */
const READING_LINE_FRACTION = 1 / 3;

/** A transcript entry's element, stamped by the renderer (messages-blocks.ts `stampEntry`, `stampOpen`) with its
 *  identity, which a rebuilt element for the same entry carries again. */
export const ENTRY_STAMP = "[data-entry-seq], [data-open-id]";

/** The scroller and view an anchor is measured in, and the view children that hold no reader text. */
export interface AnchorScope {
  readonly scroll: HTMLElement;
  view(): HTMLElement;
  furniture(child: Element): boolean;
}

/** Where a reader's text stands, by identity: elements best first, finest first, each with its signed top offset from
 *  the scrollport's top, never the reading line, which a scrollport resize moves under still text. An element gone
 *  from the view is found again `up` parents above whatever element now matches `sel`, then down its `path` of child
 *  indices (null `sel`: the node itself is the identity). Nodes are held weakly, so a parked anchor keeps no
 *  discarded DOM alive. `takenTop` is the view's scroll offset (`viewOffset`) when the marks were measured. */
export interface ReadingAnchor {
  readonly marks: readonly {
    readonly node: WeakRef<Element>;
    readonly sel: string | null;
    readonly up: number;
    readonly path: readonly number[];
    readonly offset: number;
  }[];
  takenTop: number;
}

/** Px from the scrollport's top to the reading line. */
export function readingLineOffset(scroll: HTMLElement): number {
  return scroll.clientHeight * READING_LINE_FRACTION;
}

/** The view's true scroll offset, from rects: it moves exactly as the content scrolls and never with layout inside the
 *  view. Read instead of `scrollTop`, which an engine reports rounded or floored while the marks are fractional
 *  layout boxes, so mixing the two reads a rounding difference as drift. */
export function viewOffset(scope: AnchorScope): number {
  return (
    scope.scroll.getBoundingClientRect().top +
    scope.scroll.clientTop -
    scope.view().getBoundingClientRect().top
  );
}

/** The selector for every element that renders `stamped`'s entry: a sealed one by turn and seq, an open one by turn
 *  and id, which its element keeps once the entry seals. */
function entryKey(stamped: Element): string {
  const q = (name: string): string => CSS.escape(stamped.getAttribute(name) ?? "");
  if (stamped.hasAttribute("data-entry-seq")) {
    return `[data-entry-turn="${q("data-entry-turn")}"][data-entry-seq="${q("data-entry-seq")}"]`;
  }
  const turn = q("data-open-turn");
  const id = q("data-open-id");
  return `[data-open-turn="${turn}"][data-open-id="${id}"], [data-entry-turn="${turn}"][data-entry-id="${id}"]`;
}

/** How `el` is found again once its own node is gone: by its stamp, else through the first entry inside it (a tool
 *  group renders a run of entries and carries none of its own), else by node alone. */
function identity(el: Element): { sel: string | null; up: number } {
  if (el.matches(ENTRY_STAMP)) {
    return { sel: entryKey(el), up: 0 };
  }
  const inner = el.querySelector(ENTRY_STAMP);
  if (inner === null) {
    return { sel: null, up: 0 };
  }
  let up = 0;
  for (let p: Element | null = inner; p !== null && p !== el; p = p.parentElement) {
    up++;
  }
  return { sel: entryKey(inner), up };
}

/** The index of the element of `els` on `line`: the first, in document order, whose box ends below it, provided that
 *  box starts above `limit`; -1 when none. Bisected, because block flow stacks siblings down the page; `skip`s and
 *  box-less elements are passed over. */
function onLine(
  els: HTMLCollection,
  line: number,
  limit: number,
  skip: (el: Element) => boolean,
): number {
  const counts = (el: Element | undefined): el is Element =>
    el !== undefined && !skip(el) && el.getClientRects().length > 0;
  let lo = 0;
  let hi = els.length;
  let found = -1;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    let j = mid;
    while (j < hi && !counts(els[j])) {
      j++;
    }
    const el = els[j];
    if (j === hi || el === undefined) {
      hi = mid;
    } else if (el.getBoundingClientRect().bottom > line) {
      found = j;
      hi = mid;
    } else {
      lo = j + 1;
    }
  }
  return found >= 0 && (els[found]?.getBoundingClientRect().top ?? limit) < limit ? found : -1;
}

/** A turn's spacer, standing in for rows the window has not mounted: it holds no text, and its size changes as they
 *  mount. */
function isSpacer(el: Element): boolean {
  return el.classList.contains("turn-space");
}

/** Whether `line` falls in one of `card`'s spacers, its own or its body's. */
function inSpacer(card: Element, line: number): boolean {
  for (const sp of card.querySelectorAll(":scope > .turn-space, :scope > * > .turn-space")) {
    const r = sp.getBoundingClientRect();
    if (r.top <= line && line < r.bottom) {
      return true;
    }
  }
  return false;
}

/** The mounted row of `card` nearest `line` (an open tool group's member cards count as rows): its body's laid-out rows in the scrollport (`top` to `limit`) first,
 *  else any of them, else the card's own header or footer; null when the card shows nothing. */
function nearestRow(card: Element, line: number, top: number, limit: number): Element | null {
  const rows: Element[] = [];
  const chrome: Element[] = [];
  for (const kid of card.children) {
    if (isSpacer(kid)) {
      continue;
    }
    if (kid.classList.contains("turn-body")) {
      for (const row of kid.children) {
        if (isSpacer(row) || row.getClientRects().length === 0) {
          continue;
        }
        // An open group is a column of rows; a closed one (its region inert) shows its header alone.
        const members = [
          ...row.querySelectorAll(":scope > .tool-group-body > [data-entry-seq]"),
        ].filter((m) => m.closest("[inert]") === null && m.getClientRects().length > 0);
        rows.push(...(members.length > 0 ? members : [row]));
      }
    } else if (kid.getClientRects().length > 0) {
      chrome.push(kid);
    }
  }
  const dist = (el: Element): number => {
    const r = el.getBoundingClientRect();
    return r.bottom <= line ? line - r.bottom : r.top > line ? r.top - line : 0;
  };
  const nearest = (els: Element[]): Element | null => {
    let best: Element | null = null;
    for (const el of els) {
      if (best === null || dist(el) < dist(best)) {
        best = el;
      }
    }
    return best;
  };
  const seen = rows.filter((el) => {
    const r = el.getBoundingClientRect();
    return r.bottom > top && r.top < limit;
  });
  return nearest(seen) ?? nearest(rows) ?? nearest(chrome);
}

function lineAt(scope: AnchorScope): number {
  return scope.scroll.getBoundingClientRect().top + readingLineOffset(scope.scroll);
}

/** Where the reader's text stands, found from the view's own boxes rather than a hit-test, which an overlay (the
 *  phone's side menu) blinds; null for a view with nothing laid out. Marks, finest first: the deepest block on the
 *  reading line (the first starting below it where the line falls in a gap), each stamped entry holding it, the view
 *  child holding it, then up to `reach` other children either side, nearest first, the one above first. */
export function readingAnchor(
  scope: AnchorScope,
  reach = Number.POSITIVE_INFINITY,
): ReadingAnchor | null {
  const box = scope.scroll.getBoundingClientRect();
  const line = lineAt(scope);
  const limit = box.top + scope.scroll.clientHeight;
  const furniture = (c: Element): boolean => scope.furniture(c);
  const kids = scope.view().children;
  let at = onLine(kids, line, limit, (c) => furniture(c) || isSpacer(c));
  // Nothing reaches the line: the last child that starts above it, else the first.
  for (let i = kids.length - 1; at < 0 && i >= 0; i--) {
    const c = kids[i];
    if (c !== undefined && !furniture(c) && c.getClientRects().length > 0) {
      at = i;
      if (c.getBoundingClientRect().top <= line) {
        break;
      }
    }
  }
  const card = kids[at];
  if (card === undefined) {
    return null;
  }
  // Invariant: anchors are mounted rows only, and only the owner picks them; a spacer is never one. A line in a
  // spacer holds the mounted row nearest it, so the window plans from a real ordinal, never a priced one.
  if (inSpacer(card, line)) {
    const row = nearestRow(card, line, box.top, limit);
    if (row === null) {
      return null;
    }
    const marks: ReadingAnchor["marks"][number][] = [];
    const offset = (el: Element): number => el.getBoundingClientRect().top - box.top;
    marks.push({ node: new WeakRef(row), ...identity(row), path: [], offset: offset(row) });
    const stamp = row.matches(ENTRY_STAMP) ? row : row.querySelector(ENTRY_STAMP);
    if (stamp !== null && stamp !== row) {
      marks.push({ node: new WeakRef(stamp), ...identity(stamp), path: [], offset: offset(stamp) });
    }
    marks.push({ node: new WeakRef(card), sel: null, up: 0, path: [], offset: offset(card) });
    return { marks, takenTop: viewOffset(scope) };
  }
  const chain = [card];
  for (;;) {
    const inner = chain[chain.length - 1]?.children;
    const i = inner === undefined ? -1 : onLine(inner, line, limit, isSpacer);
    const next = inner?.[i];
    // Inline content is not descended, nor a block among inline siblings (a display equation inside a streamed
    // paragraph): a streamed paragraph's chunk spans number by delta timing, so a rebuilt paragraph's child
    // indices name other text.
    const prev = next?.previousElementSibling;
    if (
      next === undefined ||
      getComputedStyle(next).display.startsWith("inline") ||
      (prev != null && getComputedStyle(prev).display.startsWith("inline"))
    ) {
      break;
    }
    chain.push(next);
  }
  const marks: ReadingAnchor["marks"][number][] = [];
  const mark = (
    el: Element,
    id: { sel: string | null; up: number },
    path: readonly number[],
  ): void => {
    marks.push({
      node: new WeakRef(el),
      ...id,
      path,
      offset: el.getBoundingClientRect().top - box.top,
    });
  };
  const byNode = { sel: null, up: 0 };
  // Finest first: the block, then the stamps around it, the block found again below the nearest one by its path. With
  // no stamp on the chain, the base is the deepest row-level element holding an entry: above the rows, the window
  // mounts and unmounts children, so a path from the card or the body names other text after a rebuild.
  const stamped = chain.filter((el) => el.matches(ENTRY_STAMP)).reverse();
  const block = chain[chain.length - 1] ?? card;
  const rows = chain.findIndex((el) => el.classList.contains("turn-body")) + 1 || 1;
  const base =
    stamped[0] ??
    chain
      .slice(rows)
      .reverse()
      .find((el) => el.querySelector(ENTRY_STAMP) !== null);
  if (block !== base) {
    const path = base === undefined ? [] : chain.slice(chain.indexOf(base) + 1);
    mark(
      block,
      base === undefined ? byNode : identity(base),
      path.map((el) => Array.prototype.indexOf.call(el.parentElement?.children ?? [], el)),
    );
  }
  if (base !== undefined && stamped[0] === undefined) {
    mark(base, identity(base), []);
  }
  for (const el of stamped) {
    mark(el, identity(el), []);
  }
  if (block !== card && !stamped.includes(card)) {
    // By node alone: a keyed reconcile keeps a child's element for as long as its key stays.
    mark(card, byNode, []);
  }
  for (let d = 1; d <= reach && (at - d >= 0 || at + d < kids.length); d++) {
    for (const kid of [kids[at - d], kids[at + d]]) {
      if (kid !== undefined && !furniture(kid) && kid.getClientRects().length > 0) {
        mark(kid, byNode, []);
      }
    }
  }
  return { marks, takenTop: viewOffset(scope) };
}

/** `anchor`'s first mark that has a box in the view, found by node or by key, with that mark. */
export function resolveAnchor(
  view: HTMLElement,
  anchor: ReadingAnchor,
): { el: Element; offset: number } | null {
  for (const m of anchor.marks) {
    const node = m.node.deref();
    let el: Element | null | undefined =
      node?.isConnected === true && view.contains(node)
        ? node
        : m.sel === null
          ? null
          : view.querySelector(m.sel);
    if (el !== node) {
      for (let i = 0; i < m.up; i++) {
        el = el?.parentElement;
      }
      for (const i of m.path) {
        el = el?.children[i];
      }
    }
    if (el !== null && el !== undefined && view.contains(el) && el.getClientRects().length > 0) {
      return { el, offset: m.offset };
    }
  }
  return null;
}

/** How far `anchor`'s text has moved down the screen from where it was taken (negative: up), or null when no mark
 *  has a box in the view. */
export function screenDrift(scope: AnchorScope, anchor: ReadingAnchor): number | null {
  const r = resolveAnchor(scope.view(), anchor);
  if (r === null) {
    return null;
  }
  return r.el.getBoundingClientRect().top - scope.scroll.getBoundingClientRect().top - r.offset;
}

/** How far layout moved `anchor`'s text in content space since it was taken: a scroll of any origin moves the view
 *  and the text on screen by the same amount, so it cancels and only a size change above the text is left. */
export function layoutDrift(scope: AnchorScope, anchor: ReadingAnchor): number | null {
  const drift = screenDrift(scope, anchor);
  return drift === null ? null : drift + (viewOffset(scope) - anchor.takenTop);
}
