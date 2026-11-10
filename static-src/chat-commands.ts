// Primitives for commands that affect a chat's turn state: one prompt POST, whose answer says where
// the server put the message. Mode changes ride a switch_mode permission request; /compact is
// intercepted earlier (typed-commands.ts).

import { newMessageID } from "./transport.js";
import { get as getSession } from "./store.js";
import { sendPrompt as sendPromptAction, type SendPromptAnswer } from "./actions/chat.js";

/** Options for the low-level prompt sender. */
interface SendPromptOpts {
  attachments?: readonly unknown[];
  /** Reuse a specific user-message id instead of minting a fresh one: submit.ts
   *  passes a failed attempt's id so its retry addresses the same message. */
  messageID?: string;
  /** The label of a message marotte writes on the user's behalf (`_meta.kiro.displayText`). */
  displayText?: string;
  /** The text is resolved only by a turn of its own, so the server never steers it. */
  promptOnly?: boolean;
}

/** Every answer `sendPromptTo` gives; a caller's branch over it should be total. */
export type SendPromptResult = SendPromptAnswer | "failed";

/**
 * Post a prompt to a chat once and report the outcome: where the server put it ("sent", "steered",
 * "queued"), a 409's class (`SendPromptAnswer`), or "failed". What a refusal means to the composer
 * is submit.ts's; user sends go through `submitPrompt`.
 */
export async function sendPromptTo(
  chatID: string,
  text: string,
  opts: SendPromptOpts = {},
): Promise<SendPromptResult> {
  const result = await sendPromptAction.dispatch({
    chatID,
    text,
    messageID: opts.messageID ?? newMessageID(),
    // The chat's own model: a pick made in another chat never rides this one's prompt.
    model: getSession(chatID)?.model ?? "",
    ...(opts.attachments !== undefined ? { attachments: opts.attachments } : {}),
    ...(opts.displayText !== undefined ? { displayText: opts.displayText } : {}),
    ...(opts.promptOnly === true ? { promptOnly: true } : {}),
  });
  return result ?? "failed";
}
