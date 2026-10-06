// The run pane's step transcript: ONE source, the run's own entry log.

import { el } from "@cplieger/reactive";
import {
  buildDetachedBody,
  disposeDetachedBody,
  finalizeDetachedBody,
  updateDetachedBody,
} from "./messages-blocks.js";
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
export type StepHostFor = (nodePath: string) => HTMLElement;

/** A run's projected step transcripts. One per run tab. */
export interface RunStepStream {
  /** Paint every step turn the map holds. Idempotent and safe on every repaint. */
  apply(paints: ReadonlyMap<string, readonly RunStepPaint[]>): void;
  /** Drop every render this stream holds. Called when the tab retargets. */
  dispose(): void;
}

/** One rendered step turn. */
interface TurnRender {
  /** The box this turn's entries were mounted into, a child of the step's host. */
  box: HTMLElement;
  /** The entry shape already mounted, for the update-versus-rebuild decision. */
  shape: readonly string[];
  /** Whether the settled body has been sealed. */
  sealed: boolean;
}

/** The pane's ROOT LANE is `""`. */
const ROOT_LANE = "";

/** No chat owns these entries, and passing "" is what says so. */
const NO_CHAT = "";

export function createRunStepStream(hostFor: StepHostFor): RunStepStream {
  const renders = new Map<string, TurnRender>();

  function paintTurn(nodePath: string, entry: RunStepPaint): void {
    const { turn, live } = entry;
    const shape = blockShape(turn.body);
    let rec = renders.get(turn.id);
    if (rec === undefined) {
      const box = el("div", { className: "ev-d-turn" });
      hostFor(nodePath).append(box);
      rec = { box, shape: [], sealed: false };
      renders.set(turn.id, rec);
    }
    // The dispatcher's incremental update appends past a watermark, so it holds only while the
    // mounted prefix is unchanged. Tail growth keeps it; a re-read filling a hole inserts a
    // same-kind entry mid-turn, which is what `blockShape` carries `seq` for.
    if (shapeExtends(rec.shape, shape) && rec.shape.length > 0) {
      updateDetachedBody(rec.box, turn, NO_CHAT, ROOT_LANE, live);
    } else {
      disposeDetachedBody(turn.id, ROOT_LANE);
      rec.box.replaceChildren();
      rec.sealed = false;
      buildDetachedBody(rec.box, turn, NO_CHAT, ROOT_LANE, live);
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

  return {
    apply(paints) {
      const seen = new Set<string>();
      for (const [nodePath, turns] of paints) {
        for (const entry of turns) {
          seen.add(entry.turn.id);
          paintTurn(nodePath, entry);
        }
      }
      // A turn the log no longer holds takes its render with it, or its disposers stay registered
      // in `messages-blocks.ts` under a key nothing ever disposes.
      for (const [turnID, rec] of renders) {
        if (seen.has(turnID)) {
          continue;
        }
        disposeDetachedBody(turnID, ROOT_LANE);
        rec.box.remove();
        renders.delete(turnID);
      }
    },
    dispose() {
      for (const turnID of renders.keys()) {
        disposeDetachedBody(turnID, ROOT_LANE);
      }
      renders.clear();
    },
  };
}
