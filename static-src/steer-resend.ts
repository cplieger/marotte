// Carrying an unread steer into the next turn: KAS drains its steering buffer at every
// turn boundary, so a steer the agent never read has left it, and this module re-sends
// that text as a new turn rather than leaving the reader to retype it. It cannot loop —
// a turn a resend opened ends with an empty waiting set, so nothing arms again.

import { clearSteers } from "./actions/chat.js";
import { sendPromptTo } from "./chat-commands.js";
import { restoreFailedSend } from "./composer-state.js";
import { reportSendRefused } from "./send-state.js";
import { newMessageID } from "./transport.js";

/** Blank line between concatenated messages. Grounded twice rather than chosen:
 *  kiro-cli's own TUI joins queued steer messages with a blank line into one slot,
 *  and `submit.ts`'s `withAttachmentPaths` already joins a message and its trailing
 *  lines the same way. No prefix, no header, no "resent:" marker — the text is the
 *  reader's own words and marotte does not put words in their mouth. */
const JOIN = "\n\n";

/** How many boundaries one armed text may be offered at. Two: the send, plus one
 *  retry for the honest race where a turn started underneath it. */
const MAX_ATTEMPTS = 2;

/** The send-error face for a resend the chat would not take. Reuses the existing
 *  failed-send surface, so the next Send is the retry, and says whose message it is
 *  because the reader did not press Send for this one. */
const REFUSED_FACE = "Couldn't send your unread message — it's back in the message box";

/** One boundary's batch: the text the resend sends, and the entries it re-sends.
 *  The ids travel because the server's ledger, the merge's union rule and the note's
 *  own `resent?` flag all read them, so a carry that kept only the words left the
 *  record unable to say that these words had been offered before. */
interface Armed {
  readonly text: string;
  readonly ids: readonly string[];
}

const armed = new Map<string, Armed>();
const attempts = new Map<string, number>();
const leadFirst = new Map<string, string>();

/** Record which waiting row the send-now arrow wants FIRST. An id, never a text, so a
 *  row confirmed between the click and the boundary is carried too. */
export function preferSteerFirst(chatID: string, steerID: string): void {
  if (chatID === "" || steerID === "") {
    return;
  }
  leadFirst.set(chatID, steerID);
}

/** Its cancel never landed, so no boundary is coming and the rows are still waiting. */
export function forgetSteerPreference(chatID: string): void {
  leadFirst.delete(chatID);
}

/** Arm the slot from a turn boundary, and LEAVE AN ARMED SLOT ALONE. The one text
 *  producer, called at both doors. Yielding is what lets a refused send re-offer its
 *  own text at the next boundary. */
export function noteBoundaryDrop(
  chatID: string,
  entries: readonly { id: string; text: string }[],
): void {
  if (chatID === "" || armed.has(chatID)) {
    return;
  }
  // One filtered set feeds both halves, so a row whose words are carried is a row
  // whose id is carried and the order is the order the reader sees.
  const carried = leadPreferred(chatID, entries).filter((e) => e.text !== "");
  const text = join(carried.map((e) => e.text));
  if (text === "") {
    return;
  }
  leadFirst.delete(chatID);
  armed.set(chatID, { text, ids: carried.map((e) => e.id) });
}

/** Drop everything for this chat without sending: the chat is gone. */
export function forgetSteerResend(chatID: string): void {
  armed.delete(chatID);
  attempts.delete(chatID);
  leadFirst.delete(chatID);
}

/** The arrow's row first, then the rest in arrival order. A preference naming a row
 *  this boundary is not carrying is ignored, never fatal: the others are still owed
 *  a turn. */
function leadPreferred(
  chatID: string,
  entries: readonly { id: string; text: string }[],
): readonly { id: string; text: string }[] {
  const wanted = leadFirst.get(chatID);
  if (wanted === undefined) {
    return entries;
  }
  const lead = entries.find((e) => e.id === wanted);
  if (lead === undefined) {
    return entries;
  }
  return [lead, ...entries.filter((e) => e.id !== wanted)];
}

/** Send whatever is armed for this chat as a new turn. Safe to call when nothing is
 *  armed — it runs on every `turn_closed` that settles a chat, which is the only door:
 *  not the `steer{dropped}` entry a KAS clear writes while the turn is still finishing,
 *  and not the cancel POST's own resolution, where `cancelTurn`'s optimistic `thinking`
 *  clear makes an earlier send take a 409 that `submit.ts` converts back into a STEER —
 *  into the buffer this boundary just drained. */
export function runArmedResend(chatID: string): void {
  const batch = armed.get(chatID);
  if (batch === undefined) {
    return;
  }
  armed.delete(chatID);
  void send(chatID, batch);
}

async function send(chatID: string, batch: Armed): Promise<void> {
  const { text, ids } = batch;
  // A steer POST confirmed just before this boundary can still be in KAS's buffer, and
  // would be injected into the very turn this opens — the reader's message twice. Ahead
  // of the prompt by SCOPE rather than an await: both actions hold `chat:<id>`, FIFO.
  void clearSteers.dispatch({ chatID });
  // `sendPromptTo`, never `submitPrompt`: that one takes the composer's staged
  // attachments, and a steer's text already carries its own `Attached file:` lines.
  // `resends` names the entries these words came from, so the turn this opens records
  // the resend rather than reading as an unrelated message the reader typed twice.
  const outcome = await sendPromptTo(chatID, text, {
    messageID: newMessageID(),
    ...(ids.length > 0 ? { resends: ids } : {}),
  });
  if (outcome === "sent") {
    attempts.delete(chatID);
    return;
  }
  const spent = (attempts.get(chatID) ?? 0) + 1;
  // "queued" and "starting" both mean the chat is busy again, so the next settled
  // boundary carries it. "failed" is a real refusal and "gone" a tombstoned chat with
  // no later boundary, so both stop here.
  if (outcome !== "failed" && outcome !== "gone" && spent < MAX_ATTEMPTS) {
    attempts.set(chatID, spent);
    // Yields like the boundary producer, so a capture made since the refusal outranks
    // this retry. Re-armed as-is: same batch, already ordered.
    if (!armed.has(chatID)) {
      armed.set(chatID, batch);
    }
    return;
  }
  attempts.delete(chatID);
  restoreFailedSend(chatID, text);
  reportSendRefused(REFUSED_FACE);
}

function join(texts: readonly string[]): string {
  return texts.filter((t) => t !== "").join(JOIN);
}
