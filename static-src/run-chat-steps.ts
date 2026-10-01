// ---------------------------------------------------------------------------
// The run pane's step transcript: ONE source, the run's own entry log.
//
// A run owns its record: every step's content is appended to
// `runs/<workflowId>/entries.jsonl` as one turn per step INSTANCE, keyed on the
// node path, and `run-store.ts` holds those turns for this client. So one source
// serves a chat-parented and a parentless run alike, live and on reload, and the
// two routes that used to serve them separately are gone with the frames they
// folded: `run_step` was a live-only projection nothing persisted, and the
// launching chat's `wf:` lane does not exist, because a step's entries are the
// RUN's.
//
// What this module owns is the RENDER LIFECYCLE per step turn: the shape watermark
// that decides update-versus-rebuild, the seal latch, and the `(turn, lane)` key
// the four detached calls have to agree on. What it does NOT own is where a step's
// entries come from (`run-store.ts`'s) or which host they go in (the detail
// pane's).
//
// It must be reached by a LAZY `import()` from `run-view.ts`. Hard requirement
// rather than tidiness: it calls `messages-blocks.ts`, whose graph runs through the
// editor openers and the navigator into `chat.ts` — the entire transcript stack —
// which is precisely what that lazy import exists to keep out of a run tab's
// static graph.
// ---------------------------------------------------------------------------

import {
  buildDetachedBody,
  disposeDetachedBody,
  finalizeDetachedBody,
  updateDetachedBody,
} from "./messages-blocks.js";
import { blockShape, shapeExtends } from "./subagent-slice.js";
import type { Turn } from "./turns.js";

/** One step turn as the pane renders it: the REAL turn out of the run's log, plus
 *  whether more may still arrive in it.
 *
 *  `live` is the turn's OWN `turn_close` absence and never the run's status: a
 *  `parallel` node holds several open turns in one log, and a settled step inside a
 *  running run is settled. */
export interface RunStepPaint {
  readonly turn: Turn;
  readonly live: boolean;
}

/** Where a step's entries go. The exec page's detail pane answers this; declared
 *  here because it is this module's requirement, not the pane's. */
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
  /** The box this turn's entries were mounted into, a child of the step's host.
   *
   *  A box per TURN rather than the host itself, because a path can hold several
   *  turns: a resume after a restart, and a heal after the hosting bridge was
   *  retired, re-open the same path as a NEW turn, so the log carries several closed
   *  turns for it. Each renders under its own `(turn, lane)` key, and appending in
   *  file order is what keeps them in the order the log holds them. */
  box: HTMLElement;
  /** The entry shape already mounted, for the update-versus-rebuild decision. */
  shape: readonly string[];
  /** Whether the settled body has been sealed. */
  sealed: boolean;
}

/** The pane's ROOT LANE is `""`.
 *
 *  A step IS its turn, so the step's own text, thinking and tool calls carry no lane
 *  value, and a delegate the step dispatched carries its uuid — which renders as a
 *  card for that delegate, exactly as in a chat's transcript. Every lane predicate in
 *  `messages-blocks.ts` compares against this root, so nothing here strips a lane off
 *  an entry to make it draw. */
const ROOT_LANE = "";

/** No chat owns these entries, and passing "" is what says so.
 *
 *  `messages-blocks.ts` reads the chat id for real: it keys tool-call signals by
 *  `(chatID, toolCallID)` and builds a delegate's page link as
 *  `/chat/<chatID>/subagent/<id>`. A step's delegate lives in the RUN's log, not in
 *  any chat's, so that link has no destination — and an empty id is what makes it
 *  correctly absent rather than pointing at a page that would project nothing. */
const NO_CHAT = "";

export function createRunStepStream(hostFor: StepHostFor): RunStepStream {
  /** turn id → its render. Keyed by TURN rather than by node path, because the four
   *  detached calls are keyed by `(turn, lane)` and a path can hold several turns. */
  const renders = new Map<string, TurnRender>();

  function paintTurn(nodePath: string, entry: RunStepPaint): void {
    const { turn, live } = entry;
    const shape = blockShape(turn.body);
    let rec = renders.get(turn.id);
    if (rec === undefined) {
      const box = document.createElement("div");
      hostFor(nodePath).append(box);
      rec = { box, shape: [], sealed: false };
      renders.set(turn.id, rec);
    }
    // The dispatcher's incremental update appends past a watermark, so it holds only
    // while the mounted prefix is unchanged. Tail growth keeps it; a re-read filling a
    // hole inserts a same-kind entry mid-turn, which is what `blockShape` carries `seq`
    // for. Comparing the prefix keeps the reader's place on every streamed chunk.
    if (shapeExtends(rec.shape, shape) && rec.shape.length > 0) {
      updateDetachedBody(rec.box, turn, NO_CHAT, ROOT_LANE, live);
    } else {
      disposeDetachedBody(turn.id, ROOT_LANE);
      rec.box.replaceChildren();
      rec.sealed = false;
      buildDetachedBody(rec.box, turn, NO_CHAT, ROOT_LANE, live);
    }
    rec.shape = shape;

    // Settled: flush the markdown streams and collapse the reasoning traces, so a
    // finished step does not sit under a caret. Guarded, because every later repaint of
    // a finished run lands here too. Per TURN, so a step that closed while its siblings
    // carry on is finished.
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
      // A turn the log no longer holds takes its render with it, or its disposers stay
      // registered in `messages-blocks.ts` under a key nothing ever disposes.
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
