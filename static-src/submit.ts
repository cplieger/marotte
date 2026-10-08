// What pressing Send means: idle sends a prompt; mid-turn, Steer mode joins the running turn
// (`_session/steer`) and Queue mode hands the server a follow-up (`queue_prompt`). Follow-ups live
// on `Chat.QueuedPrompts`, so every device draws the same rows.

import { sendPromptTo } from "./chat-commands.js";
import { handleTypedCommand } from "./typed-commands.js";
import { newMessageID } from "./transport.js";
import {
  takeAttachments,
  hasAttachments,
  addAttachmentTo,
  attachmentGeneration,
  type AttachedFile,
} from "./attachments.js";
import { get, isThinking, hasMessage, hasQueued } from "./store.js";
import { queuePrompt, steerChat } from "./actions/chat.js";
import type { ActionOutcome } from "./actions/index.js";
import { clearAgentDown, reportSendRefused } from "./send-state.js";
import { restoreFailedSend } from "./composer-state.js";
import { isSendable } from "./composer-value.js";
import { invokesCatalogCommand } from "./slash-menu.js";
import { carriesContextMention } from "./context-mentions.js";
import { chatNotice } from "./notice-subject.js";

export type SubmitResult = "sent" | "steered" | "queued" | "held" | "failed";

/** The send-error face for 409 "starting": holder-neutral (a shell holder is not "starting"). The
 *  next Send retries and clears it. */
const STARTING_FACE = "The chat is busy right now. Send again to retry";

/** The 409 reason:"chat_not_found" face: the chat was deleted underneath the send. */
const GONE_FACE = "This chat no longer exists, so the message was not sent";

/** The last failed attempt, so a retry of the SAME text on the SAME chat reuses its message id:
 *  `hasMessage` reads the id (`turn_open`'s `prompt.id`) to tell a taken send from a refused one,
 *  so a fresh id would hand the text back twice. One slot: there is one composer. */
let lastFailed: { chatID: string; text: string; messageID: string } | undefined;

/** The id to send under: the failed attempt's when this is a retry of it. */
function messageIDFor(chatID: string, text: string): string {
  if (lastFailed?.chatID === chatID && lastFailed.text === text) {
    return lastFailed.messageID;
  }
  return newMessageID();
}

/** Sends a prompt on an idle chat, or the interrupt mode's busy verb on a busy one. Conversion runs
 *  both ways (409-busy → busy verb, `no_turn` → prompt) on a shared budget; a hard failure
 *  restores text and attachments under the same message id. */
export async function submitPrompt(chatID: string, text: string): Promise<SubmitResult> {
  if (chatID === "" || !isSendable(text, hasAttachments())) {
    return "failed";
  }
  // A new attempt IS the retry, so a stale "no agent" verdict goes now (nothing on the failure path
  // clears it). Toasts stay: they report what happened. Ahead of typed commands: a command is an attempt.
  clearAgentDown();
  // Typed commands marotte owns are intercepted BEFORE anything else: before the
  // attachments are taken and before a message id is minted. A command is not a
  // prompt, so it must not consume an attachment or leave a user bubble behind.
  if (handleTypedCommand(chatID, text)) {
    return "sent";
  }
  // Held before the attachments are taken, so a busy chat keeps its pill row. The
  // text goes back once the composer's own clear (after this synchronous prefix) has run.
  const reason = isThinking(chatID) ? promptOnlyReason(text) : null;
  if (reason !== null) {
    chatNotice(chatID, reason);
    await Promise.resolve();
    restoreFailedSend(chatID, text);
    return "held";
  }
  const send: Send = {
    chatID,
    text,
    messageID: messageIDFor(chatID, text),
    attachments: takeAttachments(),
    // Read BEFORE the await: this is the attachment state the send is taking with
    // it, and recordFailure hands the token back so a restore into a state that was
    // dropped meanwhile (the chat was closed) is refused rather than recreating it.
    attachGen: attachmentGeneration(chatID),
  };
  if (isThinking(chatID)) {
    return busy(send, CONVERT_BUDGET);
  }
  return prompt(send, CONVERT_BUDGET);
}

/** One send's inputs, carried unchanged through every conversion. */
interface Send {
  chatID: string;
  text: string;
  messageID: string;
  attachments: readonly AttachedFile[];
  attachGen: number;
}

/** Why `text` can only be sent as a prompt, or null. A saved prompt, a steering
 *  command and a context reference are resolved on the prompt path alone, so a
 *  steer would deliver them to the model as prose and a queued row would keep them. */
function promptOnlyReason(text: string): string | null {
  if (invokesCatalogCommand(text)) {
    return "That command runs as a new turn. Send it when the agent is idle.";
  }
  if (carriesContextMention(text)) {
    return "Context references are added to a new turn. Send it when the agent is idle.";
  }
  return null;
}

/** The busy verb the chat's mode names, read per hop. Every busy hop comes through here, so the
 *  prompt-only hold lives here. */
function busy(send: Send, convertBudget: number): Promise<SubmitResult> {
  const reason = promptOnlyReason(send.text);
  if (reason !== null) {
    chatNotice(send.chatID, reason);
    recordFailure(send);
    return Promise.resolve("held");
  }
  return get(send.chatID)?.interrupt_mode === "queue"
    ? queue(send, convertBudget)
    : steer(send, convertBudget);
}

/** How many prompt⇄steer conversions one submit may make. Two allows the honest
 *  double race (steer → the turn is gone → prompt → a new turn started → steer)
 *  and stops there. */
const CONVERT_BUDGET = 2;

/** Post one prompt, converting a plain 409 into the busy verb while budget remains. */
async function prompt(send: Send, convertBudget: number): Promise<SubmitResult> {
  const { chatID, text, messageID, attachments } = send;
  const result = await sendPromptTo(chatID, text, {
    messageID,
    ...(attachments.length > 0 ? { attachments } : {}),
  });
  if (result === "queued") {
    // Plain 409: a steerable turn started underneath us. The conversion is gated on
    // the ABSENCE of the "starting" reason — that refusal's holder cannot receive a
    // steer and takes the branch below.
    if (convertBudget > 0) {
      return busy(send, convertBudget - 1);
    }
    recordFailure(send);
    reportSendRefused(STARTING_FACE);
    return "failed";
  }
  if (result === "starting") {
    // 409 "starting": nothing can land now, and the refusal appended nothing, so the text goes back.
    recordFailure(send);
    reportSendRefused(STARTING_FACE);
    return "failed";
  }
  if (result === "gone") {
    // 409 reason:"chat_not_found": the chat is tombstoned, so there is no record to prompt
    // and no turn to steer into. Terminal for this send, and it must not fall through to
    // "sent": the text goes back to the composer and the face says what happened.
    recordFailure(send);
    reportSendRefused(GONE_FACE);
    return "failed";
  }
  if (result === "failed") {
    recordFailure(send);
    return "failed";
  }
  lastFailed = undefined;
  return "sent";
}

/** Post one steer. The action's rollback has already un-drawn the optimistic chip
 *  by the time the outcome resolves, so a conversion leaves nothing behind. */
async function steer(send: Send, convertBudget: number): Promise<SubmitResult> {
  const { chatID, text, messageID, attachments } = send;
  const outcome = await steerChat.dispatch({
    chatID,
    text: withAttachmentPaths(text, attachments),
    messageID,
  }).outcome;
  return settleBusy(send, outcome, "steered", convertBudget);
}

/** Hold the send as a follow-up for the end of the running turn. Unlike a steer it
 *  keeps its attachments as real attachments: the server reads them when the row is
 *  sent, so they reach the model as content blocks. */
async function queue(send: Send, convertBudget: number): Promise<SubmitResult> {
  const { chatID, text, messageID, attachments } = send;
  const outcome = await queuePrompt.dispatch({
    chatID,
    text,
    messageID,
    ...(attachments.length > 0 ? { attachments } : {}),
  }).outcome;
  return settleBusy(send, outcome, "queued", convertBudget);
}

/** The shared tail of both busy verbs: `no_turn` becomes a prompt; any other failure surfaces the
 *  server's words through the send-error face. Branches on the outcome's CODE. */
function settleBusy(
  send: Send,

  outcome: ActionOutcome<void>,
  ok: SubmitResult,
  convertBudget: number,
): Promise<SubmitResult> | SubmitResult {
  if (outcome.status === "success") {
    lastFailed = undefined;
    return ok;
  }
  const error = outcome.status === "error" ? outcome.error : undefined;
  if (error?.code === "no_turn" && convertBudget > 0) {
    return prompt(send, convertBudget - 1);
  }
  recordFailure(send);
  if (error !== undefined) {
    reportSendRefused(error.message);
  }
  return "failed";
}

/** Fold attachment paths into the steer text: `_session/steer` takes a plain string. Uses the
 *  server's own fallback wording ("Attached file: <path>", command/prompt_attachments.go), so the
 *  model learns one convention. */
function withAttachmentPaths(text: string, attachments: readonly AttachedFile[]): string {
  const lines = attachments
    .map((a) => a.path)
    .filter((path) => path !== "")
    .map((path) => `Attached file: ${path}`);
  if (lines.length === 0) {
    return text;
  }
  return text === "" ? lines.join("\n") : `${text}\n\n${lines.join("\n")}`;
}

/** Put a failed send back in the composer and remember its id. Nothing goes back when the server
 *  already holds it (a persisted `turn_open` or a queued row); otherwise text and pills return,
 *  since the composer cleared on Send. */
function recordFailure(send: Send): void {
  const { chatID, text, messageID, attachments, attachGen } = send;
  lastFailed = { chatID, text, messageID };
  if (hasMessage(chatID, messageID) || hasQueued(chatID, messageID)) {
    return;
  }
  // Named chat, not "the composer on screen": the send is asynchronous, so by the
  // time a failure lands the user may be looking at a different conversation.
  restoreFailedSend(chatID, text);
  for (const a of attachments) {
    if (a.path !== "") {
      // With the send's own generation, so a chat CLOSED in the meantime is not
      // handed its files back: the close forgot them on purpose, and a stash entry
      // written after it would reappear the next time the chat is opened.
      addAttachmentTo(chatID, a.path, attachGen);
    }
  }
}
