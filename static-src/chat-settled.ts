// Two settle predicates over what a chat still has outstanding. `chatSettled` counts live runs, for
// the attention fold that hides a `done` dot while a launched run runs; `noticeSettled` does not,
// because the server withholds `agent_finished` while the chat holds a live lease. Every read is
// TRACKED. Subagents are not an input: a delegate runs inside its launching turn.

import { hasPendingDecision } from "./decision-dock.js";
import { liveRunIDsForChat } from "./run-store.js";
import { turnLive, watchSession } from "./store.js";

/** What is still outstanding for a chat. Every field is a reason `chatSettled` answers no. */
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

function turnOutstanding(chatID: string): boolean {
  const session = watchSession(chatID);
  return session !== undefined && turnLive(session);
}

/**
 * Everything outstanding for `chatID`, each term named; an unknown chat has none, so no
 * existence check is needed.
 */
export function chatOutstanding(chatID: string): ChatOutstanding {
  // No early return for an empty id: each read answers it correctly, and skipping them would
  // leave a calling effect subscribed to nothing.
  return {
    turn: turnOutstanding(chatID),
    runs: liveRunIDsForChat(chatID).length,
    asks: hasPendingDecision(chatID),
  };
}

/** Is this chat fully settled — nothing running, nothing parked, nothing asked? */
export function chatSettled(chatID: string): boolean {
  const outstanding = chatOutstanding(chatID);
  return !outstanding.turn && outstanding.runs === 0 && !outstanding.asks;
}

/** Is this chat free of an open turn and a pending ask, the two holds the server's `agent_finished`
 *  does not wait for? */
export function noticeSettled(chatID: string): boolean {
  return !turnOutstanding(chatID) && !hasPendingDecision(chatID);
}
