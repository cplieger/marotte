import { join } from "@cplieger/keyenc";
import type { TurnSummary } from "./rail-merge.js";
import { severityOf } from "./turn-severity.js";
import type { TurnSeverity } from "./turn-severity.js";

/** The thinnest row the map draws, in CSS px: 29-turns.css's `track / slots` pitch fills the track
 *  and relies on no layout asking for thinner. */
const MIN_PITCH_PX = 2;

export interface TurnBin {
  /** Stable while the bin's `n` range holds, so a repaint updates the row in place. */
  readonly key: string;
  readonly first: TurnSummary;
  readonly last: TurnSummary;
  readonly members: readonly TurnSummary[];
  readonly at: number;
  readonly severity: TurnSeverity;
  /** The turn a click on the row lands: the first member carrying the row's worst severity. */
  readonly target: TurnSummary;
}

export interface TurnMapLayout {
  /** The number of turns laid out: the drawn ones, so a turn number the transcript never draws holds no slot. */
  readonly total: number;
  readonly slots: number;
  readonly bins: readonly TurnBin[];
}

export function turnFraction(slot: number, slots: number): number {
  return slots <= 1 ? 0 : slot / (slots - 1);
}

const SEVERITY_RANK: Record<TurnSeverity, number> = {
  clean: 0,
  running: 1,
  stopped: 2,
  broken: 3,
};

const EMPTY: TurnMapLayout = { total: 0, slots: 0, bins: [] };

/** Lay the turns out on a track `trackPx` tall. The turn of rank `r` (its place in `turns`) falls in slot
 *  `floor(r/k)`, so with `k = 1` it sits at `r/(total-1)`; `k` grows only when one slot per turn would
 *  draw rows thinner than `MIN_PITCH_PX`. Rank, not the turn number: the set holds only drawn turns, and a
 *  number missing from it (an agent wake that drew nothing) would otherwise leave an empty slot.
 *  An unmeasured track (`trackPx <= 0`) lays no rows. `turns` must be sorted by `n`, as `mergeTurnSets`
 *  returns it. */
export function binTurns(turns: readonly TurnSummary[], trackPx: number): TurnMapLayout {
  const total = turns.length;
  if (total === 0 || trackPx <= 0) {
    return EMPTY;
  }
  const cap = Math.max(1, Math.floor(trackPx / MIN_PITCH_PX));
  const k = Math.ceil(total / cap);
  const slots = Math.ceil(total / k);

  const groups = new Map<number, TurnSummary[]>();
  for (const [rank, t] of turns.entries()) {
    const slot = Math.floor(rank / k);
    const group = groups.get(slot);
    if (group === undefined) {
      groups.set(slot, [t]);
    } else {
      group.push(t);
    }
  }

  const bins: TurnBin[] = [];
  for (const [slot, members] of [...groups].sort((a, b) => a[0] - b[0])) {
    const first = members[0];
    const last = members[members.length - 1];
    if (first === undefined || last === undefined) {
      continue;
    }
    let target = first;
    for (const m of members) {
      if (SEVERITY_RANK[severityOf(m.outcome)] > SEVERITY_RANK[severityOf(target.outcome)]) {
        target = m;
      }
    }
    bins.push({
      key: join(String(slot * k + 1), String((slot + 1) * k)),
      first,
      last,
      members,
      at: turnFraction(slot, slots),
      severity: severityOf(target.outcome),
      target,
    });
  }
  return { total, slots, bins };
}
