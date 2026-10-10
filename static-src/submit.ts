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
import { get, isThinking, hasMessage, hasQueued, holdsSteer } from "./store.js";
import { queuePrompt, steerChat } from "./actions/chat.js";
import type { ActionOutcome } from "./actions/index.js";
import { clearAgentDown, reportSendRefused } from "./send-state.js";
import { restoreFailedSend } from "./composer-state.js";
import { isSendable } from "./composer-value.js";
import { invokesCatalogCommand } from "./slash-menu.js";
import { carriesContextMention } from "./context-mentions.js";
import { chatNotice } from "./notice-subject.js";

type SubmitResult = "sent" | "steered" | "queued" | "held" | "failed";

/** The send-error face for 409 "starting": holder-neutral (a shell holder is not "starting"). The
 *  next Send retries and clears it. */
const STARTING_FACE = "The chat is busy right now. Send again to retry";

/** The 409 reason:"chat_not_found" face: the chat was deleted underneath the send. */
const GONE_FACE = "This chat no longer exists, so the message was not sent";

/** Each chat's last failed TYPED attempt, so a retry of the same text reuses its message id:
 *  `hasMessage` reads the id (`turn_open`'s `prompt.id`) to tell a taken send from a refused one,
 *  so a fresh id would hand the text back twice. Per chat, because drafts are; only a typed send
 *  on that chat writes or clears its record, and the chat's deletion drops it. */
const typedRetries = new Map<string, { text: string; messageID: string }>();

/** The typed sends still in flight, by chat, so a deletion can reach them; each leaves when it
 *  settles. Not store presence: a tab close removes the store row too, and a closed chat keeps
 *  its retry record. */
const inFlightTyped = new Map<string, Set<TypedSend>>();

/** The id to send under: the failed attempt's when this is a retry of it. */
function messageIDFor(chatID: string, text: string): string {
  const failed = typedRetries.get(chatID);
  return failed?.text === text ? failed.messageID : newMessageID();
}

/** The server deleted `chatID`: drop its retry record and mark its in-flight sends so their
 *  failures restore nothing. A tab close must not call this: a reopened chat retries an
 *  uncertain send under its original id. */
export function forgetDeletedChat(chatID: string): void {
  for (const send of inFlightTyped.get(chatID) ?? []) {
    send.deleted = true;
  }
  typedRetries.delete(chatID);
}

/** Runs `send` registered as in flight, so a deletion meanwhile reaches it. */
async function whileInFlight(
  send: TypedSend,
  run: (send: TypedSend) => Promise<SubmitResult>,
): Promise<SubmitResult> {
  const sends = inFlightTyped.get(send.chatID) ?? new Set<TypedSend>();
  inFlightTyped.set(send.chatID, sends.add(send));
  try {
    return await run(send);
  } finally {
    sends.delete(send);
    if (sends.size === 0) {
      inFlightTyped.delete(send.chatID);
    }
  }
}

/** A send reached the server: a typed one supersedes its chat's retry record. */
function settled(send: Send): void {
  if (send.kind === "typed") {
    typedRetries.delete(send.chatID);
  }
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
  const send: TypedSend = {
    kind: "typed",
    chatID,
    text,
    messageID: messageIDFor(chatID, text),
    attachments: takeAttachments(),
    // Read BEFORE the await: this is the attachment state the send is taking with
    // it, and recordFailure hands the token back so a restore into a state that was
    // dropped meanwhile (the chat was closed) is refused rather than recreating it.
    attachGen: attachmentGeneration(chatID),
    deleted: false,
  };
  return whileInFlight(send, (s) =>
    isThinking(chatID) ? busy(s, CONVERT_BUDGET) : prompt(s, CONVERT_BUDGET),
  );
}

/** Sends a message marotte writes on the user's behalf, labelled `displayText` in the transcript:
 *  a prompt when idle, a labelled queue row when busy whatever the chat's interrupt mode. No
 *  composer is involved: no typed command, no attachment, nothing restored on failure. */
export function submitLabelled(
  chatID: string,
  text: string,
  displayText: string,
): Promise<SubmitResult> {
  if (chatID === "" || text === "") {
    return Promise.resolve("failed");
  }
  const send: Send = { kind: "labelled", chatID, text, messageID: newMessageID(), displayText };
  return isThinking(chatID) ? busy(send, CONVERT_BUDGET) : prompt(send, CONVERT_BUDGET);
}

/** One send's inputs, carried unchanged through every conversion. Only a typed send came from
 *  the composer, so only it owns a retry record and gets its words back on failure. */
type Send = TypedSend | LabelledSend;

interface TypedSend {
  readonly kind: "typed";
  readonly chatID: string;
  readonly text: string;
  readonly messageID: string;
  readonly attachments: readonly AttachedFile[];
  readonly attachGen: number;
  /** The server deleted the chat while this send was in flight (`forgetDeletedChat`). */
  deleted: boolean;
}

interface LabelledSend {
  readonly kind: "labelled";
  readonly chatID: string;
  readonly text: string;
  readonly messageID: string;
  readonly displayText: string;
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

/** The busy verb, read per hop: a labelled send queues, since `_session/steer` carries no label;
 *  a typed one takes the chat's mode. Every busy hop comes through here, so the prompt-only hold
 *  lives here. */
function busy(send: Send, convertBudget: number): Promise<SubmitResult> {
  if (send.kind === "labelled") {
    return queue(send, convertBudget);
  }
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
  const { chatID, text, messageID } = send;
  const result = await sendPromptTo(chatID, text, { messageID, ...extras(send) });
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
  settled(send);
  return "sent";
}

/** The action's rollback has already un-drawn a still-pending chip by the time the outcome
 *  resolves, so a conversion leaves nothing behind. */
async function steer(send: TypedSend, convertBudget: number): Promise<SubmitResult> {
  const { chatID, text, messageID, attachments } = send;
  const outcome = await steerChat.dispatch({
    chatID,
    text: withAttachmentPaths(text, attachments),
    messageID,
  }).outcome;
  if (outcome.status !== "success" && holdsSteer(chatID, messageID)) {
    // A frame confirmed the steer before its reply was lost, so it is delivered.
    settled(send);
    return "steered";
  }
  return settleBusy(send, outcome, "steered", convertBudget);
}

/** Hold the send as a follow-up for the end of the running turn. Unlike a steer it
 *  keeps its attachments as real attachments: the server reads them when the row is
 *  sent, so they reach the model as content blocks. */
async function queue(send: Send, convertBudget: number): Promise<SubmitResult> {
  const { chatID, text, messageID } = send;
  const outcome = await queuePrompt.dispatch({ chatID, text, messageID, ...extras(send) }).outcome;
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
    settled(send);
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

/** What rides a prompt or a queued row beside its text: a typed send's attachments, a labelled
 *  send's label. */
function extras(send: Send): { attachments?: readonly AttachedFile[]; displayText?: string } {
  if (send.kind === "labelled") {
    return { displayText: send.displayText };
  }
  return send.attachments.length > 0 ? { attachments: send.attachments } : {};
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

/** Nothing goes back when the server already holds it (a persisted `turn_open` or a queued row);
 *  otherwise text and pills return, since the composer cleared on Send. */
function recordFailure(send: Send): void {
  if (send.kind === "labelled" || send.deleted) {
    return;
  }
  const { chatID, text, messageID, attachments, attachGen } = send;
  typedRetries.set(chatID, { text, messageID });
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
