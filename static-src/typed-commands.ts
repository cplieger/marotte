// Typed-command intercepts: the verbs marotte owns, checked before send. A slash command KAS does
// not parse reaches the MODEL as prose, which answers as though it ran (`/compact` "done" with no
// summarization), so a deterministic verb owns its handler here. slash-menu.ts lists these.

import { compactChat } from "./actions/chat.js";
import { get, isEmptyChat, isThinking, setThinking } from "./store.js";
import { chatNotice } from "./notice-subject.js";

/** Handles a typed command. Returns true when it consumed the input, so the
 *  caller must NOT also send it as a prompt. */
type TypedHandler = (chatID: string) => boolean;

/** The table: the bare verb, case-insensitive after the slash, matched only when the input is the
 *  verb ALONE (`/compact this file` is a sentence). */
interface Verb {
  readonly description: string;
  readonly run: TypedHandler;
}

const HANDLERS: Readonly<Record<string, Verb>> = {
  compact: {
    description: "Compact the context now",
    run: (chatID) => {
      void compactChat.dispatch({ chatID });
      return true;
    },
  },
  drop: { description: "Stop waiting on a turn that is not answering", run: dropTurn },
  memories: {
    description: "Open the agent's memories",
    run: () => {
      // Lazy: route-apply reaches the whole page graph.
      void import("./route-apply.js").then(({ applyRoute }) =>
        applyRoute({ kind: "docs", tab: "memories" }),
      );
      return true;
    },
  },
  rewind: { description: "Rewind the last turn and its file changes", run: rewindLatest },
  tangent: {
    description: "Start a tangent from this conversation",
    run: (chatID) => {
      if (isEmptyChat(get(chatID))) {
        chatNotice(chatID, "Send a message first, then start a tangent from it.");
        return true;
      }
      // Lazy: chat.ts imports submit.ts, which imports this module.
      void import("./chat.js").then(({ openTangentChat }) => openTangentChat(chatID));
      return true;
    },
  },
};

/** One marotte verb as the `/` menu lists it. */
export interface MarotteCommand {
  readonly name: string;
  readonly description: string;
}

/** Every verb the table claims, in table order. */
export const MAROTTE_COMMANDS: readonly MarotteCommand[] = Object.entries(HANDLERS).map(
  ([name, v]) => ({ name, description: v.description }),
);

/** `/rewind`: the newest prompt turn, through the footer's own confirm. */
function rewindLatest(chatID: string): boolean {
  if (isThinking(chatID)) {
    chatNotice(chatID, "Rewind is unavailable while the agent is working.");
    return true;
  }
  void import("./messages.js").then(async ({ rewindLatestTurn }) => {
    if (!(await rewindLatestTurn(chatID))) {
      chatNotice(chatID, "There is no turn to rewind.");
    }
  });
  return true;
}

/**
 * `/drop`: end the turn CLIENT-SIDE and put the composer back in prompt mode, for a turn the
 * engine stopped answering. While `thinking` is set Send means STEER and Cancel waits on KAS, so
 * the only recovery was a reload. ONE write; the reactive chain (send-state.ts, prompt-input.ts,
 * submit.ts) does the rest, and it asks the engine for nothing. `thinking` is client-owned, and
 * `BUS_RECONCILE` already clears it. A live turn loses nothing: the next prompt 409s and steers.
 * KAS's steering buffer is NOT cleared; that is `steer_clear`, the chip row's Discard ×.
 */
function dropTurn(chatID: string): boolean {
  if (!isThinking(chatID)) {
    // Already idle. Claimed anyway rather than passed through: sending "/drop"
    // to the model is worse than doing nothing, and the toast says why nothing
    // happened.
    chatNotice(chatID, "No turn is running on this chat.");
    return true;
  }
  setThinking(chatID, false);
  chatNotice(chatID, "Dropped the turn locally. The agent was not told to stop.");
  return true;
}

/**
 * Consume a typed command if the table claims it. Everything else falls through unchanged,
 * including slash text KAS parses; that list is KAS's.
 */
export function handleTypedCommand(chatID: string, text: string): boolean {
  if (chatID === "") {
    return false;
  }
  const trimmed = text.trim();
  if (!trimmed.startsWith("/")) {
    return false;
  }
  const verb = trimmed.slice(1).toLowerCase();
  if (verb === "" || /\s/.test(verb)) {
    return false;
  }
  if (!Object.hasOwn(HANDLERS, verb)) {
    return false;
  }
  return HANDLERS[verb]?.run(chatID) ?? false;
}
