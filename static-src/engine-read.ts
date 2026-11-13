// The one owner of "read this engine-backed data again when the engine is ready". The server refuses an engine-backed
// read at once while no engine is ready, so a panel whose last answer was refused reads again on BUS_ENGINE_READY.
// A panel registers each such read here and keeps no ready handler of its own.

import { onBus, BUS_ENGINE_READY } from "./bus.js";

/** One registered read. */
export interface EngineRead {
  /** Records a read sent and returns its settle: true when the engine answered it, false when it was refused or
   *  failed, null when it was abandoned (aborted) with no answer. A later begin supersedes it, so a superseded
   *  read's settle changes nothing. Call the settle once the answer is applied: a re-ask it owes is sent from it. */
  begin(): (answered: boolean | null) => void;
  /** Forgets the read out and the last answer, for a panel that closed: its next open reads afresh. */
  reset(): void;
}

interface Entry {
  readonly reask: () => void;
  readonly shown: () => boolean;
  /** The newest begin's number. */
  sent: number;
  /** The newest begin has not settled. */
  out: boolean;
  /** A ready event came while the newest read was out: its answer may predate the engine's return. */
  stale: boolean;
  /** The newest settled answer was refused or failed. */
  refused: boolean;
}

const entries = new Set<Entry>();

/**
 * Registers a panel's engine-backed read. `reask` sends it again (and calls `begin`); `shown` says whether the panel
 * is on screen, since an off-screen panel reads on its next open. A ready event re-asks a read whose last answer was
 * refused; one that lands while a read is out sends no second read beside it but owes exactly one more after it
 * settles, answered or refused.
 */
export function engineRead(reask: () => void, shown: () => boolean): EngineRead {
  const e: Entry = { reask, shown, sent: 0, out: false, stale: false, refused: false };
  entries.add(e);
  return {
    begin: () => {
      const n = ++e.sent;
      e.out = true;
      return (answered) => {
        if (n !== e.sent || !e.out) {
          return;
        }
        e.out = false;
        if (answered !== null) {
          e.refused = !answered;
        }
        if (e.stale) {
          e.stale = false;
          if (e.shown()) {
            e.reask();
          }
        }
      };
    },
    reset: () => {
      e.sent++;
      e.out = false;
      e.stale = false;
      e.refused = false;
    },
  };
}

onBus(BUS_ENGINE_READY, () => {
  for (const e of [...entries]) {
    if (e.out) {
      e.stale = true;
    } else if (e.refused && e.shown()) {
      e.reask();
    }
  }
});
