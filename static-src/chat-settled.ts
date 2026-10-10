// "Is everything this chat started actually over?": ONE predicate for every out-of-page cue,
// since a turn ending is not the work ending. Every read is TRACKED. Subagents are not an
// input: a delegate runs inside its launching turn.

import { hasPendingDecision } from "./decision-dock.js";
import { liveRunIDsForChat } from "./run-store.js";
import { turnLive, watchSession } from "./store.js";

/** What is still outstanding for a chat. Every field is a reason a cue must wait. */
interface ChatOutstanding {
  /** This chat's own turn is running (`turnLive`: thinking, an open turn, or a row
   *  that states no liveness at all). */
  readonly turn: boolean;
  /**
   * How many live runs this chat launched, parked ones INCLUDED: a run stopped on a person is
   * not finished (see `hasLiveRunForChat`).
   */
  readonly runs: number;
  /** An unanswered decision sits in this chat's dock queue. */
  readonly asks: boolean;
}

/**
 * Everything outstanding for `chatID`, each term named; an unknown chat has none, so no
 * existence check is needed.
 */
export function chatOutstanding(chatID: string): ChatOutstanding {
  // No early return for an empty id: each read answers it correctly, and skipping them would
  // leave a calling effect subscribed to nothing.
  const session = watchSession(chatID);
  return {
    turn: session !== undefined && turnLive(session),
    runs: liveRunIDsForChat(chatID).length,
    asks: hasPendingDecision(chatID),
  };
}

/** Is this chat fully settled — nothing running, nothing parked, nothing asked? */
export function chatSettled(chatID: string): boolean {
  const outstanding = chatOutstanding(chatID);
  return !outstanding.turn && outstanding.runs === 0 && !outstanding.asks;
}
