// The newest line a workflow step printed, for the run tab's one-line glance at a running step: from
// the turns the run's log holds, or, for a step whose transcript the page does not hold, from that
// step's tail cell, seeded by the run read's `step_starts` and moved by the step's own frames.

import { signal, type Signal } from "@cplieger/reactive";
import type { Entry, OpenEntry, TurnState } from "./types.js";
import type { RunStepStart } from "./wire/types.gen.js";

/** Longer than any dock row shows; the CSS ellipsizes the rest. */
const MAX_CHARS = 200;

/** The newest line the step printed across `turns` (oldest first): a continuation or retry turn
 *  that has printed nothing yet leaves the previous turn's line standing. "" when none printed. */
export function lastPrintedLine(turns: readonly TurnState[]): string {
  for (let i = turns.length - 1; i >= 0; i--) {
    const turn = turns[i];
    const line = turn === undefined ? "" : turnLine(turn);
    if (line !== "") {
      return line;
    }
  }
  return "";
}

/** The newest thing the step's own lane printed in `turn`: its streaming text or thinking, else
 *  its newest sealed text, thinking or tool call (by title). */
function turnLine(turn: TurnState): string {
  const open = turn.openEntries.get("");
  if (open !== undefined) {
    const line = lastLine(open.text);
    if (line !== "") {
      return line;
    }
  }
  for (let i = turn.entries.length - 1; i >= 0; i--) {
    const e = turn.entries[i];
    const line = e === undefined ? "" : entryLine(e);
    if (line !== "") {
      return line;
    }
  }
  return "";
}

/** What one sealed entry of the step's own lane prints: a text's or thinking's last line, a tool
 *  call's title. The server's twin, which seeds a tail cell, is `internal/agent` `printedLine`. */
function entryLine(e: Entry): string {
  if ((e.lane ?? "") !== "") {
    return "";
  }
  const field =
    e.kind === "text" || e.kind === "thinking" ? "text" : e.kind === "tool_call" ? "title" : "";
  if (field === "") {
    return "";
  }
  const p = e.payload;
  const value =
    p !== null && typeof p === "object" ? (p as Record<string, unknown>)[field] : undefined;
  return typeof value === "string" ? lastLine(value) : "";
}

/** Markdown structure that says nothing on its own line: a code fence, a table's rule, a thematic
 *  break. */
const STRUCTURE_ONLY = [
  /^(?:`{3,}|~{3,})[^`]*$/,
  /^\|?(?:\s*:?-+:?\s*\|)*\s*:?-+:?\s*\|?$/,
  /^([-*_])(?:\s*\1){2,}$/,
];

/** Leading block markers: heading hashes, quote marks, list bullets and numbers. */
const BLOCK_MARKERS = /^(?:#{1,6}\s+|>\s?|[-*+]\s+|\d{1,9}[.)]\s+)+/;

/** The last line of `text` that reads as prose, its markdown block markers dropped. */
function lastLine(text: string): string {
  const lines = text.split("\n");
  for (let i = lines.length - 1; i >= 0; i--) {
    const raw = (lines[i] ?? "").trim();
    if (raw === "" || STRUCTURE_ONLY.some((re) => re.test(raw))) {
      continue;
    }
    const line = raw.replace(BLOCK_MARKERS, "").trim();
    if (line !== "") {
      return capped(line);
    }
  }
  return "";
}

/** `line` cut to `MAX_CHARS` code points, so the cut never splits a surrogate pair. */
function capped(line: string): string {
  if (line.length <= MAX_CHARS) {
    return line;
  }
  return Array.from(line).slice(0, MAX_CHARS).join("");
}

/** One step's line while its transcript is not held. */
interface TailCell {
  readonly line: Signal<string>;
  /** The root lane's streaming entry, whose text the line reads while it grows. */
  open: { id: string; n: number; text: string } | undefined;
  /** The line was moved by a frame since the run entered this page's scope, so a read's older
   *  seed does not overwrite it. */
  live: boolean;
}

/** Root-lane deltas of an entry a turn's cell has not seen open, or past a gap in the one it holds,
 *  by `n`, until a read's row reaches them. */
interface EarlyDeltas {
  readonly id: string;
  readonly deltas: Map<number, string>;
}

/** Bounds one turn's parked deltas, and how many turns no read names park; a named turn's parking
 *  is bounded by the run's own turns, so a wide run's gapped steps never evict each other. */
const EARLY_DELTAS = 256;
const EARLY_TURNS = 8;

interface RunTails {
  /** Each step turn a read or a `turn_opened` named, by turn id, to its node path. */
  readonly paths: Map<string, string>;
  readonly cells: Map<string, TailCell>;
  /** By turn id: a read written mid-entry lands after the deltas sent after it. */
  readonly early: Map<string, EarlyDeltas>;
}

const tails = new Map<string, RunTails>();

function runTails(workflowID: string): RunTails {
  let t = tails.get(workflowID);
  if (t === undefined) {
    t = { paths: new Map(), cells: new Map(), early: new Map() };
    tails.set(workflowID, t);
  }
  return t;
}

function cellAt(t: RunTails, path: string): TailCell {
  let c = t.cells.get(path);
  if (c === undefined) {
    c = { line: signal(""), open: undefined, live: false };
    t.cells.set(path, c);
  }
  return c;
}

/** The cell a frame of `turnID` moves, undefined for a turn no read or frame has named. */
function cellOf(workflowID: string, turnID: string): TailCell | undefined {
  const t = tails.get(workflowID);
  const path = t?.paths.get(turnID);
  return t === undefined || path === undefined ? undefined : cellAt(t, path);
}

/** The node path a step turn belongs to, undefined when no read or `turn_opened` named it. */
export function stepTurnPath(workflowID: string, turnID: string): string | undefined {
  return tails.get(workflowID)?.paths.get(turnID);
}

/** Record a step turn's path: a new turn of the step keeps the line its last turn printed. */
export function noteStepTurn(workflowID: string, turnID: string, path: string): void {
  const t = runTails(workflowID);
  t.paths.set(turnID, path);
  cellAt(t, path).open = undefined;
}

/** Seed each in-flight step's cell from a run read's rows: a cell no frame has moved since the run
 *  entered scope takes the row, and a cell holding the row's streaming entry at an earlier delta
 *  moves to it; the held deltas past the row follow. */
export function seedStepTails(
  workflowID: string,
  starts: Iterable<readonly [string, RunStepStart]>,
): void {
  for (const [path, row] of starts) {
    if (row.turn === undefined || row.turn === "") {
      continue;
    }
    const t = runTails(workflowID);
    t.paths.set(row.turn, path);
    const c = cellAt(t, path);
    const open = row.open?.turn === row.turn && (row.open.lane ?? "") === "" ? row.open : undefined;
    const held = open === undefined || c.open?.id !== open.id ? undefined : c.open;
    const ahead =
      open !== undefined &&
      (held === undefined ? !c.live || c.open === undefined : open.n > held.n);
    if (held === undefined ? !c.live : ahead) {
      c.line.value = (open === undefined ? "" : lastLine(open.text)) || (row.tail ?? "");
    }
    if (open !== undefined && ahead) {
      c.open = { id: open.id, n: open.n, text: open.text };
      followEarly(t, row.turn, c);
    }
  }
}

/** A root-lane entry opened on a turn the log does not hold. */
export function tailOpened(workflowID: string, open: OpenEntry): void {
  const c = cellOf(workflowID, open.turn);
  if (c === undefined || (open.lane ?? "") !== "") {
    return;
  }
  c.open = { id: open.id, n: open.n, text: open.text };
  c.live = true;
  tails.get(workflowID)?.early.delete(open.turn);
  moveLine(c, open.text, 0);
}

/** A delta on a root-lane entry of such a turn, or of a turn no read has named yet. One past a gap
 *  in the held entry, or for an entry the cell does not hold, waits for a read's row to reach it;
 *  one the cell holds already is dropped. */
export function tailDelta(
  workflowID: string,
  turnID: string,
  entryID: string,
  lane: string,
  n: number,
  delta: string,
): void {
  if (lane !== "") {
    return;
  }
  const c = cellOf(workflowID, turnID);
  if (c?.open?.id === entryID && n <= c.open.n) {
    return;
  }
  const t = runTails(workflowID);
  if (c?.open?.id === entryID && n === c.open.n + 1) {
    applyDelta(c, n, delta);
    followEarly(t, turnID, c);
    return;
  }
  holdEarly(t, turnID, entryID, n, delta);
  if (c?.open?.id === entryID) {
    followEarly(t, turnID, c);
  }
}

/** Apply the held deltas that follow the cell's open entry, and drop those it already holds. */
function followEarly(t: RunTails, turnID: string, c: TailCell): void {
  const early = t.early.get(turnID);
  if (c.open === undefined || early?.id !== c.open.id) {
    return;
  }
  for (const n of early.deltas.keys()) {
    if (n <= c.open.n) {
      early.deltas.delete(n);
    }
  }
  for (let n = c.open.n + 1; early.deltas.has(n); n++) {
    applyDelta(c, n, early.deltas.get(n) ?? "");
    early.deltas.delete(n);
  }
  if (early.deltas.size === 0) {
    t.early.delete(turnID);
  }
}

function holdEarly(t: RunTails, turnID: string, entryID: string, n: number, delta: string): void {
  let early = t.early.get(turnID);
  if (early?.id !== entryID) {
    t.early.delete(turnID);
    early = { id: entryID, deltas: new Map() };
    t.early.set(turnID, early);
    const unnamed = [...t.early.keys()].filter((turn) => !t.paths.has(turn));
    for (const old of unnamed.slice(0, Math.max(0, unnamed.length - EARLY_TURNS))) {
      t.early.delete(old);
    }
  }
  // A row reaches the newest deltas, which arrived after the read was written, so the oldest go.
  if (early.deltas.size >= EARLY_DELTAS) {
    const oldest = early.deltas.keys().next();
    if (oldest.done !== true) {
      early.deltas.delete(oldest.value);
    }
  }
  early.deltas.set(n, delta);
}

function applyDelta(c: TailCell, n: number, delta: string): void {
  const open = c.open;
  if (open === undefined) {
    return;
  }
  const from = open.text.length;
  c.open = { id: open.id, n, text: open.text + delta };
  c.live = true;
  moveLine(c, c.open.text, from);
}

/** Such a turn's root-lane entry sealed after `n` deltas: the line it printed stands over a read
 *  written before the seal, unless deltas the cell never applied leave it behind, when the next
 *  read's row sets it. */
export function tailSealed(workflowID: string, turnID: string, entryID: string, n: number): void {
  const c = cellOf(workflowID, turnID);
  if (c?.open?.id === entryID) {
    c.live = c.open.n >= n;
    c.open = undefined;
    const t = tails.get(workflowID);
    if (t?.early.get(turnID)?.id === entryID) {
      t.early.delete(turnID);
    }
  }
}

/** A sealed entry appended to such a turn. */
export function tailAppended(workflowID: string, entry: Entry): void {
  const c = cellOf(workflowID, entry.turn);
  const line = entryLine(entry);
  if (c === undefined || line === "") {
    return;
  }
  c.live = true;
  c.line.value = line;
}

/** The line `text` prints, read from the line holding offset `from` on: the text before it was
 *  read already. Only when that slice prints nothing is the whole text read again. */
function moveLine(c: TailCell, text: string, from: number): void {
  const start = from === 0 ? 0 : text.lastIndexOf("\n", from - 1) + 1;
  const line = lastLine(text.slice(start)) || (start === 0 ? "" : lastLine(text));
  if (line !== "") {
    c.line.value = line;
  }
}

/** A step's tail line, tracked. */
export function stepTail(workflowID: string, path: string): string {
  return cellAt(runTails(workflowID), path).line.value;
}

/** The run left this page's scope: its frames stop arriving, so the next read seeds its cells. */
export function leaveStepTails(workflowID: string): void {
  const t = tails.get(workflowID);
  t?.early.clear();
  for (const c of t?.cells.values() ?? []) {
    c.open = undefined;
    c.live = false;
  }
}

export function forgetStepTails(workflowID: string): void {
  tails.delete(workflowID);
}
