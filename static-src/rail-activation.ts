// Which turn the reader is in, from the scroll offset alone. The transcript has one READING LINE
// and a jump lands the target's top ON it, so a click cannot mark turn N and then have activation
// re-mark N+1.

/** The resident cards' tops in the SCROLLER's frame, ascending. Two parallel arrays, because the
 *  read is a binary search over `tops` on every scroll frame. */
export interface TurnOffsets {
  readonly ids: readonly string[];
  readonly tops: readonly number[];
}

/** One measured card. `null` is a real answer for a card the engine reports no box for. Measure
 *  with rects, NEVER `offsetTop`: `content-visibility: auto` on `.msg-row` makes the row a
 *  containing block, so an offsetParent-relative read returned 0 for a block whose true position
 *  was 2203. */
export interface CardTop {
  readonly id: string;
  readonly top: number | null;
}

/** The scroller facts no table can carry. `atLiveEdge` is the PUBLISHED verdict, not a fresh
 *  bottom test: a wheel-up inside the tolerance band parks the reader while a raw test still
 *  answers true, so only the published one is about the READER. */
export interface RailGeom {
  clientHeight: number;
  atLiveEdge: boolean;
}

/** Not a turn id, so a caller reads it as "keep the mark you have". */
const KEEP = "";

/** Build the table from measured cards, ascending. A card with no box is SKIPPED: its marker
 *  still renders and is still clickable, it just cannot be landed on. */
export function buildOffsets(cards: Iterable<CardTop>): TurnOffsets {
  const rows: CardTop[] = [];
  for (const card of cards) {
    if (card.id === "" || card.top === null || !Number.isFinite(card.top)) {
      continue;
    }
    rows.push(card);
  }
  rows.sort((a, b) => (a.top ?? 0) - (b.top ?? 0));
  return { ids: rows.map((r) => r.id), tops: rows.map((r) => r.top ?? 0) };
}

/** The index of the row that carries the mark for turn `n` when `n` has no row of its own: the
 *  last entry at or below `n`, `0` when every entry is above it, `-1` for an empty set. A binned
 *  map has one row for k turns, and an undrawn turn has none. */
export function markerSlotFor(shown: readonly { readonly n: number }[], n: number): number {
  let slot = -1;
  for (let i = 0; i < shown.length; i++) {
    if ((shown[i]?.n ?? Number.POSITIVE_INFINITY) <= n) {
      slot = i;
    }
  }
  return slot >= 0 ? slot : shown.length === 0 ? -1 : 0;
}

/** The ids of the cards any part of which is inside the viewport. Card `i` spans `tops[i]` to
 *  `tops[i+1]`, the last one to the end of the transcript. Empty for no table or no viewport. */
export function turnsInView(
  scrollTop: number,
  offsets: TurnOffsets,
  clientHeight: number,
): ReadonlySet<string> {
  const out = new Set<string>();
  const n = offsets.ids.length;
  if (n === 0 || clientHeight <= 0) {
    return out;
  }
  const bottom = scrollTop + clientHeight;
  // The last card whose top is at or above the viewport's top: it reaches into view.
  let lo = 0;
  let hi = n - 1;
  let first = 0;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if ((offsets.tops[mid] ?? 0) <= scrollTop) {
      first = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  for (let i = first; i < n && (offsets.tops[i] ?? 0) < bottom; i++) {
    const id = offsets.ids[i];
    if (id !== undefined) {
      out.add(id);
    }
  }
  return out;
}

/** The turn the reading line is in, or `KEEP` when there is no answer. Pure and total: it holds
 *  no state, so it cannot drift and cannot skip. */
export function activeTurnAt(
  scrollTop: number,
  offsets: TurnOffsets,
  line: number,
  geom: RailGeom,
): string {
  const n = offsets.ids.length;
  if (n === 0 || geom.clientHeight === 0) {
    return KEEP;
  }
  if (!(scrollTop > 0)) {
    return offsets.ids[0] ?? KEEP;
  }
  if (geom.atLiveEdge) {
    return offsets.ids[n - 1] ?? KEEP;
  }
  const target = scrollTop + line;
  let lo = 0;
  let hi = n - 1;
  let best = 0;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if ((offsets.tops[mid] ?? 0) <= target) {
      best = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return offsets.ids[best] ?? KEEP;
}
