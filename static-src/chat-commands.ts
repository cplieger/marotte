// Primitives for commands that affect a chat's turn state. Each sets thinking before
// posting; any failure but a 409 clears it and locks nothing, so Send again retries. A
// leaf: mode changes ride a switch_mode permission request, /compact is intercepted
// earlier (typed-commands.ts).

import { newMessageID } from "./transport.js";
import { getCurrentModel } from "./session-context.js";
import { sendPrompt as sendPromptAction } from "./actions/chat.js";

/** Options for the low-level prompt sender. */
interface SendPromptOpts {
  model?: string;
  attachments?: readonly unknown[];
  /** Reuse a specific user-message id instead of minting a fresh one: submit.ts
   *  passes a failed attempt's id so its retry addresses the same message. */
  messageID?: string;
  /** The label of a message marotte writes on the user's behalf (`_meta.kiro.displayText`). */
  displayText?: string;
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
    ...(opts.displayText !== undefined ? { displayText: opts.displayText } : {}),
  });
  return result ?? "failed";
}
