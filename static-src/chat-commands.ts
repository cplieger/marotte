// Primitives for commands that affect a chat's turn state. Each sets thinking before
// posting; any failure but a 409 clears it and locks nothing, so Send again retries. A
// leaf: mode changes ride a switch_mode permission request, /compact is intercepted
// earlier (typed-commands.ts).

import { newMessageID } from "./transport.js";
import { getCurrentModel } from "./session-context.js";
import {
  switchModel as switchModelAction,
  sendPrompt as sendPromptAction,
} from "./actions/chat.js";

/** Options for the low-level prompt sender. */
export interface SendPromptOpts {
  model?: string;
  attachments?: readonly unknown[];
  /** Reuse a specific user-message id instead of minting a fresh one: submit.ts
   *  passes a failed attempt's id so its retry addresses the same message. */
  messageID?: string;
}

/** Every answer `sendPromptTo` gives; a caller's branch over it should be total. */
export type SendPromptResult = "sent" | "queued" | "starting" | "gone" | "failed";

/**
 * Post a prompt to a chat once and report the outcome: "sent" on the admission ack,
 * "queued" on a plain 409 (a steerable turn), "starting" on reason "starting", "gone" on
 * reason "chat_not_found" (tombstoned, terminal), "failed" otherwise. What a busy chat
 * means is submit.ts's; user sends go through `submitPrompt`.
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
    model: opts.model ?? getCurrentModel(),
    ...(opts.attachments !== undefined ? { attachments: opts.attachments } : {}),
  });
  return result ?? "failed";
}

/**
 * Send a standalone switch_model command; a pick during a turn is applied after it. True
 * when accepted; false means clear any in-flight UI state.
 */
export async function switchModel(chatID: string, model: string): Promise<boolean> {
  if (chatID === "") {
    return false;
  }
  const result = await switchModelAction.dispatch({ chatID, model });
  return result !== null && result;
}
