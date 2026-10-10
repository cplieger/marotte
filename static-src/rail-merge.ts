// The turn map's SET of turns: the session-wide index, extended forwards by the resident window.
// Neither side alone answers "which turns exist" — the index cannot see the turn running now, the
// window cannot see the turns paged out. Pure and DOM-free, like `turns.ts` beside it.

import { OUTCOME_LABEL } from "./turn-severity.js";
import { turnLedger } from "./turns.js";
import type { Turn, TurnOutcome } from "./turns.js";

/** One row of the session-wide turn index. Mirrors marotte.TurnSummary. */
export interface TurnSummary {
  id: string;
  first_line?: string;
  outcome: TurnOutcome;
  n: number;
  ts: number;
  agent_initiated?: boolean;
  /** Absent for a running turn or a close that stamped none; never 0. */
  elapsed_ms?: number;
}

/** Rows the index answered with. */
interface ValidatedIndex {
  turns: TurnSummary[];
}

/** The preview line's cap, matching `internal/chat/entrylog.go`'s `firstLineMax`. */
const FIRST_LINE_MAX = 120;

/** Validate the index, which arrives through an unchecked cast rather than a generated decoder.
 *  A non-finite `n` reaches `calc(NaN * …)`, an invalid declaration the browser drops, so a bad
 *  row would pin a pill to the top of the map rather than misplace it. Identity and position
 *  are DROPPED, description is COERCED. One `console.warn` per call, which is one per fetch. */
export function validateTurnIndex(raw: unknown): ValidatedIndex {
  if (!Array.isArray(raw)) {
    return { turns: [] };
  }
  const rows: TurnSummary[] = [];
  const badTs: number[] = [];
  let dropped = 0;
  for (const row of raw as readonly unknown[]) {
    if (typeof row !== "object" || row === null) {
      dropped++;
      continue;
    }
    const r = row as Record<string, unknown>;
    const id = r["id"];
    const n = r["n"];
    if (
      typeof id !== "string" ||
      id === "" ||
      typeof n !== "number" ||
      !Number.isInteger(n) ||
      n < 1
    ) {
      dropped++;
      continue;
    }
    const ts = r["ts"];
    const tsOK = typeof ts === "number" && Number.isFinite(ts) && ts >= 0;
    if (!tsOK) {
      badTs.push(rows.length);
    }
    const outcome = r["outcome"];
    const firstLine = r["first_line"];
    const out: TurnSummary = {
      id,
      n,
      ts: tsOK ? ts : 0,
      // `OUTCOME_LABEL` is total by type, so this is not a second spelling of the union.
      outcome:
        typeof outcome === "string" && Object.hasOwn(OUTCOME_LABEL, outcome)
          ? (outcome as TurnOutcome)
          : "unknown",
      agent_initiated: r["agent_initiated"] === true,
    };
    if (typeof firstLine === "string" && firstLine !== "") {
      out.first_line = firstLine;
    }
    const elapsed = r["elapsed_ms"];
    if (typeof elapsed === "number" && Number.isFinite(elapsed) && elapsed > 0) {
      out.elapsed_ms = elapsed;
    }
    rows.push(out);
  }
  fillBadTimestamps(rows, badTs);
  if (dropped > 0) {
    console.warn("turn map: dropped unreadable index rows", dropped);
  }
  return { turns: rows };
}

/** Sit an unreadable `ts` on a neighbour rather than leaving it at the epoch, so the decoded row
 *  carries a plausible start time. This stays because `ts` is still the wire's field and
 *  answering for a malformed one is the validator's job, not because a surface renders it. */
function fillBadTimestamps(rows: TurnSummary[], bad: readonly number[]): void {
  if (bad.length === 0) {
    return;
  }
  const isBad = new Set(bad);
  for (const i of bad) {
    let fill = 0;
    for (let j = i - 1; j >= 0; j--) {
      if (!isBad.has(j)) {
        fill = rows[j]?.ts ?? 0;
        break;
      }
    }
    if (fill === 0) {
      for (let j = i + 1; j < rows.length; j++) {
        if (!isBad.has(j)) {
          fill = rows[j]?.ts ?? 0;
          break;
        }
      }
    }
    const row = rows[i];
    if (row !== undefined) {
      row.ts = fill;
    }
  }
}

/** Merge the resident window into the fetched index, BY `n`: that is the POSITION the turn map
 *  renders, and the appender assigns it at open and stores it (section 8.10), so both sides name
 *  a turn by the same number and no turn can take two slots. Resident wins per field with no
 *  exemption, because it sees the turn running now and a window never holds part of a turn
 *  (section 6.3). */
export function mergeTurnSets(
  resident: readonly Turn[],
  indexed: readonly TurnSummary[],
): TurnSummary[] {
  const byN = new Map<number, TurnSummary>();
  for (const row of indexed) {
    byN.set(row.n, row);
  }
  for (const t of resident) {
    byN.set(t.n, residentRow(t));
  }
  return [...byN.values()].sort((a, b) => a.n - b.n);
}

function residentRow(t: Turn): TurnSummary {
  const out: TurnSummary = {
    id: t.id,
    n: t.n,
    ts: t.ts,
    outcome: t.outcome,
    agent_initiated: t.trigger === undefined,
  };
  const line = firstLine(t.trigger?.text ?? "");
  if (line !== "") {
    out.first_line = line;
  }
  const { elapsedMs } = turnLedger(t);
  if (elapsedMs > 0) {
    out.elapsed_ms = elapsedMs;
  }
  return out;
}

/** A request's first line, whitespace-collapsed and rune-safe. A TWIN of `internal/chat/entrylog.go`
 *  `firstLineOf`, because the index cannot answer for the turn that is running and the two spellings
 *  must agree for every turn it can; `internal/chat/testdata/first_line.json` pins both. */
function firstLine(s: string): string {
  // `strings.Fields` splits on Unicode White_Space. Not `\s` or `trim()`: both take U+FEFF and
  // leave U+0085.
  const collapsed = (s.split("\n", 1)[0] ?? "")
    .split(/\p{White_Space}+/u)
    .filter((w) => w !== "")
    .join(" ");
  // Code points, not graphemes: the Go twin counts runes and the cap must match.
  const runes = Array.from(collapsed);
  return runes.length > FIRST_LINE_MAX
    ? runes.slice(0, FIRST_LINE_MAX).join("") + "\u2026"
    : collapsed;
}
