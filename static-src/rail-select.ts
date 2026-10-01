// Where a marker sits on the rail, and which turns get one. Position is a function of
// the marker's SLOT in the shown set: `k` markers sit at one pitch, so a dropped
// turn leaves no gap behind it. The set itself is a function of rank alone.
//
// TWO REGIMES, and `railSpan` is the crossover: a set that fits at the relaxed pitch
// is spread from the top at it, and one that does not takes the whole track.

import type { TurnSummary } from "./rail-merge.js";
import { severityOf } from "./turn-severity.js";

/** The clear between two adjacent hit targets. */
const MARKER_CLEAR_PX = 4;

/** The marker box for a pre-layout track: the FINE tier's own floor rather than a
 *  third number, so the next render re-reads it. */
export const MARKER_FALLBACK_PX = 24;

/** The two pixel numbers the layout needs, both off the tier's own hit floor. */
export interface RailMetrics {
  /** One marker's hit TARGET: 24px on a fine pointer, 44px on a coarse one. */
  markerPx: number;
  /** The minimum separation between two markers' tops. */
  pitchPx: number;
}

/** Read the tier's marker box off the track's computed style. `--hit-floor` rather
 *  than a constant because a marker's TARGET is sized from that same token, so a
 *  hard-coded pitch would place 44px targets 28px apart on a coarse pointer. The target
 *  and not the painted box, which is `--rail-mark` and 20px smaller under a finger. */
export function railMetrics(track: HTMLElement): RailMetrics {
  const markerPx = floorPx(track) ?? MARKER_FALLBACK_PX;
  return { markerPx, pitchPx: markerPx + MARKER_CLEAR_PX };
}

/** `--hit-floor` in PX, or null for a track the token does not reach.
 *
 *  Assigned to a real length property rather than read off the custom property: an
 *  unregistered one's computed value is its own token stream, so `--hit-floor` reads
 *  back `1.5rem` and a `parseFloat` answers 1.5. The probe is the track's SIBLING,
 *  because `.turn-rail:empty` ties the rail's own box to whether it has children. */
function floorPx(track: HTMLElement): number | null {
  const host = track.parentElement ?? track.ownerDocument.body;
  const probe = track.ownerDocument.createElement("div");
  probe.style.cssText = "position:absolute;visibility:hidden;block-size:var(--hit-floor)";
  host.appendChild(probe);
  const px = parseFloat(getComputedStyle(probe).blockSize);
  probe.remove();
  return Number.isFinite(px) && px > 0 ? px : null;
}

/** THE PITCH A YOUNG RAIL SPACES ITS MARKERS AT: one marker box of clear, against
 *  `MARKER_CLEAR_PX`'s 4px floor a full track compresses to.
 *
 *  A set shorter than the track is spread from the TOP at this pitch rather than
 *  across the whole track, which is what puts turn 2 one gap under turn 1 instead of
 *  at the foot of the column beside the resume control. The gaps then tighten as
 *  markers accumulate and reach the floor as the track fills, so the compression is
 *  continuous and the downsample takes over from it rather than from a jump. */
export function relaxedPitch(markerPx: number): number {
  return markerPx * 2;
}

/** How much of the track's travel the shown set occupies, 0..1: the relaxed pitch
 *  while every marker fits at it, all of it once they do not.
 *
 *  `slots` is the SHOWN set's size, not the session's, and every `railAt` one render
 *  publishes has to carry the same span. `1` for a track with no travel, because a
 *  fraction of nothing is unused rather than wrong. */
export function railSpan(slots: number, trackPx: number, markerPx: number): number {
  const travel = Math.max(0, trackPx - markerPx);
  if (travel === 0) {
    return 1;
  }
  return Math.min(1, (Math.max(0, slots - 1) * relaxedPitch(markerPx)) / travel);
}

/** A marker's position on the axis: 0 for the first slot, `span` for the last, as a
 *  fraction of the track's travel. Published as `--rail-at`, so the arithmetic below
 *  and the rendered `top` cannot disagree.
 *
 *  `span` is REQUIRED rather than defaulted to 1, because 1 is the stretched layout
 *  this argument exists to stop: a caller that forgot it would put the last marker at
 *  the foot of the track and nothing would say so. */
export function railAt(slot: number, slots: number, span: number): number {
  return (slot / Math.max(1, slots - 1)) * span;
}

/** A marker's own top. The travel span is the track minus one marker box, so both
 *  ends sit fully inside it. */
export function slotPosition(
  slot: number,
  slots: number,
  trackPx: number,
  markerPx: number,
): number {
  const travel = Math.max(0, trackPx - markerPx);
  return railAt(slot, slots, railSpan(slots, trackPx, markerPx)) * travel;
}

/** How many markers a track of this height holds at the tier's separation. */
export function maxMarkers(trackPx: number, pitchPx: number): number {
  return Math.max(1, Math.floor(trackPx / pitchPx));
}

/** Which turns get a marker. IT TAKES NO SCROLL STATE, and that is the invariant
 *  rather than an omission: a set that moves with the reader is presence churn they
 *  see. `hits` is the search-hit rank passed IN, read once per render, so a
 *  module-level read of the search state cannot put presence back on their keystrokes.
 *
 *  The FIRST and LAST turn are always kept; the remaining slots up to the track's
 *  capacity go, in order, to the turns that did not end clean, then to the live
 *  search hits, then to a uniform spread of the rest. Because the caller lays the
 *  result out by slot, `k <= maxMarkers` is what keeps every pair `pitchPx` apart;
 *  the one exception is a track with room for a single marker, which still shows both
 *  ends. */
export function selectMarkers(
  turns: readonly TurnSummary[],
  trackPx: number,
  pitchPx: number,
  hits: ReadonlySet<number>,
): TurnSummary[] {
  const last = turns.length - 1;
  if (last < 0) {
    return [];
  }
  const cap = maxMarkers(trackPx, pitchPx);
  const picked = new Set<number>([0, last]);
  const tiers: ((t: TurnSummary) => boolean)[] = [
    (t) => severityOf(t.outcome) !== "clean",
    (t) => hits.has(t.n),
    () => true,
  ];
  for (const wanted of tiers) {
    const candidates: number[] = [];
    for (let i = 0; i <= last; i++) {
      const t = turns[i];
      if (t !== undefined && !picked.has(i) && wanted(t)) {
        candidates.push(i);
      }
    }
    for (const i of spread(candidates, cap - picked.size)) {
      picked.add(i);
    }
  }

  const out: TurnSummary[] = [];
  for (const i of [...picked].sort((a, b) => a - b)) {
    const t = turns[i];
    if (t !== undefined) {
      out.push(t);
    }
  }
  return out;
}

/** A uniform spread of `candidates` over `slots` places: all of them when they fit,
 *  the middle one for a single place, `Math.round`-spaced picks otherwise. */
function spread(candidates: readonly number[], slots: number): number[] {
  if (slots <= 0 || candidates.length === 0) {
    return [];
  }
  if (slots >= candidates.length) {
    return [...candidates];
  }
  if (slots === 1) {
    return [candidates[Math.floor((candidates.length - 1) / 2)] ?? 0];
  }
  const out = new Set<number>();
  for (let k = 0; k < slots; k++) {
    const at = Math.round((k * (candidates.length - 1)) / (slots - 1));
    const i = candidates[at];
    if (i !== undefined) {
      out.add(i);
    }
  }
  return [...out];
}
