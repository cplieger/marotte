// What pressing Send means: one prompt POST, and the server's run order decides where it goes (it
// starts, steers into the running turn, or queues behind what runs: a turn, a compaction). The dock
// and the composer draw the answer's `run`, so every device shows the same order.

import { sendPromptTo } from "./chat-commands.js";
import { handleTypedCommand, terminalCommandRefusal } from "./typed-commands.js";
import { newMessageID } from "./transport.js";
import {
  takeAttachments,
  hasAttachments,
  addAttachmentTo,
  attachmentGeneration,
  type AttachedFile,
} from "./attachments.js";
import { hasMessage, hasQueued } from "./store.js";
import { clearAgentDown, reportSendRefused } from "./send-state.js";
import { restoreFailedSend } from "./composer-state.js";
import { isSendable } from "./composer-value.js";
import { invokesCatalogCommand } from "./slash-menu.js";
import { carriesContextMention } from "./context-mentions.js";
import { chatNotice } from "./notice-subject.js";

export type SubmitResult = "sent" | "steered" | "queued" | "held" | "failed";

/** The send-error face for 409 "starting": holder-neutral (a rewind holder is not "starting"). The
 *  next Send retries and clears it. */
const STARTING_FACE = "The chat is busy right now. Send again to retry";

/** The 409 reason:"chat_not_found" face: the chat was deleted underneath the send. */
const GONE_FACE = "This chat no longer exists, so the message was not sent";

/** The 409 reason:"chat_closed" face: the chat was closed after the send began. */
const CLOSED_FACE = "This chat was closed, so the message was not sent";

/** The 409 reason:"full" face: the queue holds as many messages as it takes. */
const FULL_FACE = "The queue is full. Send again once a queued message has gone";

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

/** The message ids of this chat's typed sends still in flight: a close names them, since the
 *  server may have admitted one whose turn has not opened here yet. */
export function inFlightMessageIDs(chatID: string): string[] {
  return [...(inFlightTyped.get(chatID) ?? [])].map((s) => s.messageID);
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

/** Sends one prompt; the server places it. A hard failure restores text and attachments under the
 *  same message id. `opts.attachments` is for a send not drawn from the live composer: it is sent in
 *  place of the pill row, which is left untouched. */
export async function submitPrompt(
  chatID: string,
  text: string,
  opts: { attachments?: readonly AttachedFile[] } = {},
): Promise<SubmitResult> {
  const named = opts.attachments;
  if (
    chatID === "" ||
    !isSendable(text, named === undefined ? hasAttachments() : named.length > 0)
  ) {
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
  const refusal = invokesCatalogCommand(text) ? null : terminalCommandRefusal(text);
  if (refusal !== null) {
    chatNotice(chatID, refusal.notice);
    return refusal.keepText ? holdText(chatID, text) : "sent";
  }
  const send: TypedSend = {
    kind: "typed",
    chatID,
    text,
    messageID: messageIDFor(chatID, text),
    attachments: named ?? takeAttachments(),
    // Read BEFORE the await: this is the attachment state the send is taking with
    // it, and recordFailure hands the token back so a restore into a state that was
    // dropped meanwhile (the chat was closed) is refused rather than recreating it.
    attachGen: attachmentGeneration(chatID),
    deleted: false,
  };
  return whileInFlight(send, post);
}

/** Hands `text` back to the composer once its own clear, after submitPrompt's synchronous
 *  prefix, has run. */
async function holdText(chatID: string, text: string): Promise<"held"> {
  await Promise.resolve();
  restoreFailedSend(chatID, text);
  return "held";
}

/** Sends a message marotte writes on the user's behalf, labelled `displayText` in the transcript;
 *  the server never steers a labelled send. No composer is involved: no typed command, no
 *  attachment, nothing restored on failure. */
export function submitLabelled(
  chatID: string,
  text: string,
  displayText: string,
): Promise<SubmitResult> {
  if (chatID === "" || text === "") {
    return Promise.resolve("failed");
  }
  return post({ kind: "labelled", chatID, text, messageID: newMessageID(), displayText });
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

/** Why `text` can only be sent as a prompt, or null. A saved prompt, a steering command and a
 *  context reference are resolved only by a new turn, so the POST says so (`prompt_only`) and the
 *  server refuses to steer it or hold it behind a running turn. */
function promptOnlyReason(text: string): string | null {
  if (invokesCatalogCommand(text)) {
    return "That command runs as a new turn. Send it when the agent is idle.";
  }
  if (carriesContextMention(text)) {
    return "Context references are added to a new turn. Send it when the agent is idle.";
  }
  return null;
}

/** Post the send once and fold the answer: a placed message is settled, a refusal keeps the text
 *  and says why. */
async function post(send: Send): Promise<SubmitResult> {
  const { chatID, text, messageID } = send;
  const reason = send.kind === "typed" ? promptOnlyReason(text) : null;
  const result = await sendPromptTo(chatID, text, {
    messageID,
    ...extras(send),
    ...(reason !== null ? { promptOnly: true } : {}),
  });
  switch (result) {
    case "sent":
    case "steered":
    case "queued":
      settled(send);
      return result;
    case "prompt_only":
      // The chat is busy and the text needs a turn of its own: it stays in the composer.
      chatNotice(chatID, reason ?? PROMPT_ONLY_NOTICE);
      recordFailure(send);
      return "held";
    case "full":
      recordFailure(send);
      reportSendRefused(FULL_FACE);
      return "failed";
    case "starting":
      // Nothing can land now, and the refusal appended nothing, so the text goes back.
      recordFailure(send);
      reportSendRefused(STARTING_FACE);
      return "failed";
    case "gone":
      // The chat is tombstoned, so there is no record to prompt: the text goes back and the face
      // says what happened.
      recordFailure(send);
      reportSendRefused(GONE_FACE);
      return "failed";
    case "closed":
      // A close of the chat committed after this send began, so the reopened chat never runs it.
      recordFailure(send);
      reportSendRefused(CLOSED_FACE);
      return "failed";
    case "failed":
      recordFailure(send);
      return "failed";
  }
}

/** The notice for a `prompt_only` refusal of text this page did not class as prompt-only. */
const PROMPT_ONLY_NOTICE = "That message runs as a new turn. Send it when the agent is idle.";

/** What rides a prompt or a queued row beside its text: a typed send's attachments, a labelled
 *  send's label. */
function extras(send: Send): { attachments?: readonly AttachedFile[]; displayText?: string } {
  if (send.kind === "labelled") {
    return { displayText: send.displayText };
  }
  return send.attachments.length > 0 ? { attachments: send.attachments } : {};
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
