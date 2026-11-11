// The run pane's step transcript: ONE source, the run's own entry log, mounted as a window. The newest slice is
// built when the step is shown, an older one seats each time the reader nears the top, and while the reader follows
// the live edge the rows far above them are unmounted again.

import { el } from "@cplieger/reactive";
import {
  blockElement,
  buildDetachedBody,
  disposeDetachedBody,
  dropHead,
  finalizeDetachedBody,
  mountedWindow,
  mountHeadRange,
  rebuildDetached,
  updateDetachedBody,
} from "./messages-blocks.js";
import {
  entryAt,
  entryRenders,
  planResidency,
  sliceTurn,
  STEP_BUDGET,
  turnFolds,
  turnSpan,
  type EntryRange,
  type ResidencyAnchor,
} from "./block-window.js";
import { buildRequestHeader } from "./fundamentals/turn-header.js";
import { onHookVisibility } from "./hook-fold.js";
import { blockShape, shapeExtends } from "./subagent-slice.js";
import type { Turn } from "./turns.js";

/** One step turn as the pane renders it: the REAL turn out of the run's log, plus whether more
 *  may still arrive in it. `live` is the turn's OWN `turn_close` absence and never the run's
 *  status: a `parallel` node holds several open turns in one log, and a settled step inside a
 *  running run is settled. */
export interface RunStepPaint {
  readonly turn: Turn;
  readonly live: boolean;
}

/** Where a step's entries go. The exec page's detail pane answers this; declared here because it
 *  is this module's requirement, not the pane's. */
type StepHostFor = (nodePath: string) => HTMLElement;

/** The pane's scroller as the window reads it (`scroll-controller.ts` `FollowScroller`). */
export interface StepScroller {
  following(): boolean;
  setPageIn(older: (() => boolean) | null): void;
  keepsFrom(): number;
}

/** A run's projected step transcripts. One per run tab. */
export interface RunStepStream {
  /** Paint every step turn the map holds, within each step's window. Idempotent and safe on every repaint. */
  apply(paints: ReadonlyMap<string, readonly RunStepPaint[]>): void;
  /** Drop every render this stream holds. Called when the tab retargets. */
  dispose(): void;
}

interface TurnRender {
  /** The words that opened the turn (a message that resumed the step), above its box while the turn's head is
   *  mounted. */
  heading: HTMLElement | undefined;
  box: HTMLElement;
  /** The entry shape already mounted, for the update-versus-rebuild decision. */
  shape: readonly string[];
  sealed: boolean;
  /** The last paint and the range it was given, which a Show hook status flip rebuilds from. */
  last: RunStepPaint;
  range: EntryRange;
}

/** Where a step's window begins: every ordinal from here to the step's last is mounted. */
interface WindowStart {
  readonly turnID: string;
  readonly seq: number;
}

/** The pane's ROOT LANE is `""`. */
const ROOT_LANE = "";

/** No chat owns these entries, and passing "" is what says so. */
const NO_CHAT = "";

/** Mounted ordinals past which a following reader's window is trimmed, and the growth asked before the next trim
 *  when one finds nothing far enough above to drop. */
const TRIM_ORDINALS = 4 * STEP_BUDGET.entries;

export function createRunStepStream(hostFor: StepHostFor, scroller?: StepScroller): RunStepStream {
  const renders = new Map<string, TurnRender>();
  const starts = new Map<string, WindowStart>();
  const trimAt = new Map<string, number>();
  /** The last paint per step, which a seat or a hook flip repaints from. */
  const shown = new Map<string, readonly RunStepPaint[]>();

  function paintStep(nodePath: string, turns: readonly RunStepPaint[], seen: Set<string>): void {
    const ts = turns.map((p) => p.turn);
    let start = starts.get(nodePath);
    if (start === undefined || !ts.some((t) => t.id === start?.turnID)) {
      start = newestStart(ts);
    } else if (scroller?.following() === true) {
      start = trimmed(nodePath, ts, start);
    }
    if (start === undefined) {
      return;
    }
    starts.set(nodePath, start);
    const from = ts.findIndex((t) => t.id === start.turnID);
    for (let i = from; i < turns.length; i++) {
      const entry = turns[i];
      if (entry === undefined) {
        continue;
      }
      seen.add(entry.turn.id);
      const range =
        i === from ? { from: start.seq, to: turnSpan(entry.turn) } : wholeOf(entry.turn);
      paintTurn(nodePath, entry, range, () => nextSeated(turns, i));
    }
  }

  /** The element the first seated turn after `i` begins with, which a newly seated older turn goes before. */
  function nextSeated(turns: readonly RunStepPaint[], i: number): HTMLElement | null {
    for (let j = i + 1; j < turns.length; j++) {
      const rec = renders.get(turns[j]?.turn.id ?? "");
      if (rec !== undefined) {
        return rec.heading ?? rec.box;
      }
    }
    return null;
  }

  function paintTurn(
    nodePath: string,
    entry: RunStepPaint,
    range: EntryRange,
    before: () => HTMLElement | null,
  ): void {
    const { turn } = entry;
    const shape = blockShape(turn.body);
    let rec = renders.get(turn.id);
    if (rec === undefined) {
      const box = el("div", { className: "ev-d-turn" });
      const host = hostFor(nodePath);
      const next = before();
      host.insertBefore(box, next?.parentElement === host ? next : null);
      rec = { heading: undefined, box, shape: [], sealed: false, last: entry, range };
      renders.set(turn.id, rec);
    }
    rec.last = entry;
    rec.range = range;
    syncHeading(rec, range.from === 0);
    render(rec, shape);
  }

  /** The turn's request header stands above its box only while its first entry is mounted. */
  function syncHeading(rec: TurnRender, headMounted: boolean): void {
    const request = rec.last.turn.trigger?.text.trim() ?? "";
    if (!headMounted || request === "") {
      rec.heading?.remove();
      rec.heading = undefined;
      return;
    }
    rec.heading ??= buildRequestHeader(request, rec.last.turn.trigger);
    if (rec.heading.nextElementSibling !== rec.box) {
      rec.box.before(rec.heading);
    }
  }

  function render(rec: TurnRender, shape: readonly string[]): void {
    const { turn, live } = rec.last;
    const want = rec.range;
    const have = mountedWindow(turn.id);
    // The dispatcher's incremental update appends past a watermark, so it holds only while the
    // mounted prefix is unchanged. Tail growth keeps it; a re-read filling a hole inserts a
    // same-kind entry mid-turn, which is what `blockShape` carries `seq` for.
    if (have !== undefined && shapeExtends(rec.shape, shape) && rec.shape.length > 0) {
      if (want.from < have.from) {
        mountHeadRange(turn, want, live);
      } else if (want.from > have.from) {
        dropHead(turn, want);
      }
      updateDetachedBody(rec.box, turn, NO_CHAT, ROOT_LANE, live, want);
    } else {
      rebuildDetached(() => {
        disposeDetachedBody(turn.id, ROOT_LANE);
        rec.box.replaceChildren();
        rec.sealed = false;
        buildDetachedBody(rec.box, turn, NO_CHAT, ROOT_LANE, live, want);
      });
    }
    rec.shape = shape;

    // Settled: flush the markdown streams and collapse the reasoning traces, so a finished step
    // does not sit under a caret. Guarded, because every later repaint of a finished run lands here
    // too. Per TURN, so a step that closed while its siblings carry on is finished.
    if (!live && !rec.sealed) {
      rec.sealed = true;
      finalizeDetachedBody(turn.id, ROOT_LANE);
    }
  }

  /** A following reader's window with the rows far above the scrollport dropped, once it has grown past the trim
   *  mark; the start unchanged otherwise. Reads layout, so only past the mark. */
  function trimmed(nodePath: string, ts: readonly Turn[], start: WindowStart): WindowStart {
    const from = ts.findIndex((t) => t.id === start.turnID);
    let mounted = 0;
    for (let i = from; i < ts.length; i++) {
      const t = ts[i];
      mounted += t === undefined ? 0 : turnSpan(t) - (i === from ? start.seq : 0);
    }
    if (mounted <= (trimAt.get(nodePath) ?? TRIM_ORDINALS) || scroller === undefined) {
      return start;
    }
    const keep = scroller.keepsFrom();
    const cut = firstKept(ts.slice(from), start, keep);
    trimAt.set(nodePath, cut === undefined ? mounted + TRIM_ORDINALS : TRIM_ORDINALS);
    return cut ?? start;
  }

  /** The first mounted row whose box reaches below `keep`, as a window start snapped to its prose run; undefined
   *  when that is the window's own start. */
  function firstKept(
    ts: readonly Turn[],
    start: WindowStart,
    keep: number,
  ): WindowStart | undefined {
    for (const t of ts) {
      const span = turnSpan(t);
      for (let seq = t.id === start.turnID ? start.seq : 0; seq < span; seq++) {
        const row = blockElement(t.id, seq);
        if (row === undefined || row.getClientRects().length === 0) {
          continue;
        }
        if (row.getBoundingClientRect().bottom <= keep) {
          continue;
        }
        const at = headOf(t, sliceTurn(t, { from: seq, to: span }, ROOT_LANE).from);
        return t.id === start.turnID && at <= start.seq ? undefined : { turnID: t.id, seq: at };
      }
    }
    return undefined;
  }

  /** Seat the slice above the shown step's window; false when nothing older is left. */
  function older(): boolean {
    for (const [nodePath, turns] of shown) {
      const start = starts.get(nodePath);
      const ts = turns.map((p) => p.turn);
      const next = start === undefined ? undefined : olderStart(ts, start);
      if (next === undefined) {
        continue;
      }
      starts.set(nodePath, next);
      trimAt.delete(nodePath);
      const seen = new Set<string>();
      paintStep(nodePath, turns, seen);
      return true;
    }
    return false;
  }

  function repaintAll(): void {
    const seen = new Set<string>();
    for (const [nodePath, turns] of shown) {
      paintStep(nodePath, turns, seen);
    }
    disposeUnseen(seen);
  }

  function disposeUnseen(seen: ReadonlySet<string>): void {
    // A turn the log or the window no longer holds takes its render with it, or its disposers stay registered in
    // `messages-blocks.ts` under a key nothing ever disposes.
    for (const [turnID, rec] of renders) {
      if (seen.has(turnID)) {
        continue;
      }
      disposeDetachedBody(turnID, ROOT_LANE);
      rec.heading?.remove();
      rec.box.remove();
      renders.delete(turnID);
    }
  }

  // The switch moves which hook entries draw a card (`hookRenders`), which only a rebuild re-decides.
  const stopHookVisibility = onHookVisibility(() => {
    for (const rec of renders.values()) {
      rec.shape = [];
    }
    repaintAll();
  });

  scroller?.setPageIn(older);

  return {
    apply(paints) {
      shown.clear();
      for (const [nodePath, turns] of paints) {
        shown.set(nodePath, turns);
      }
      for (const nodePath of starts.keys()) {
        if (!shown.has(nodePath)) {
          starts.delete(nodePath);
          trimAt.delete(nodePath);
        }
      }
      repaintAll();
    },
    dispose() {
      stopHookVisibility();
      scroller?.setPageIn(null);
      for (const turnID of renders.keys()) {
        disposeDetachedBody(turnID, ROOT_LANE);
      }
      renders.clear();
      starts.clear();
      shown.clear();
    },
  };
}

function wholeOf(t: Turn): EntryRange {
  return { from: 0, to: turnSpan(t) };
}

/** Where the newest slice of `ts` begins, or undefined when no turn renders anything. */
function newestStart(ts: readonly Turn[]): WindowStart | undefined {
  return startOf(ts, planResidency(ts, undefined, ROOT_LANE, STEP_BUDGET));
}

/** Where the slice above `start` begins, or undefined when nothing older renders. */
function olderStart(ts: readonly Turn[], start: WindowStart): WindowStart | undefined {
  const i = ts.findIndex((t) => t.id === start.turnID);
  const prev = ts[i - 1];
  const anchor: ResidencyAnchor | undefined =
    start.seq > 0
      ? { turnID: start.turnID, at: start.seq - 1 }
      : prev === undefined
        ? undefined
        : { turnID: prev.id, at: turnSpan(prev) - 1 };
  if (anchor === undefined) {
    return undefined;
  }
  const next = startOf(ts, planResidency(ts, anchor, ROOT_LANE, STEP_BUDGET, true));
  if (next === undefined) {
    return undefined;
  }
  const j = ts.findIndex((t) => t.id === next.turnID);
  return j < i || (j === i && next.seq < start.seq) ? next : undefined;
}

function startOf(
  ts: readonly Turn[],
  plan: ReadonlyMap<string, EntryRange>,
): WindowStart | undefined {
  for (const t of ts) {
    const range = plan.get(t.id);
    if (range !== undefined) {
      return { turnID: t.id, seq: headOf(t, range.from) };
    }
  }
  return undefined;
}

/** `seq`, or 0 when nothing before it in `t` renders, so a window reaching a turn's first entry holds its head. */
function headOf(t: Turn, seq: number): number {
  const folds = turnFolds(t, ROOT_LANE);
  for (let at = 1; at < seq; at++) {
    const e = entryAt(t, at);
    if (e !== undefined && entryRenders(e, ROOT_LANE, folds)) {
      return seq;
    }
  }
  return 0;
}
