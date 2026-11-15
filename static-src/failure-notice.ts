// A send or turn failure reaches the user as a bottom-right toast: the glance, while the transcript is the record.
// Not a retry surface.

import { join } from "@cplieger/keyenc";
import { BUS_COMMAND_FAILED, onBus } from "./bus.js";
import { error as toastError, errorWithAction, type ToastRetry } from "./toast.js";
import { getActiveTabId, tabIdFor } from "./tabs.js";
import { truncate } from "./strings.js";
import { named, noticeSubject } from "./notice-subject.js";
import type { ErrorCode } from "./wire/types.gen.js";

/**
 * A failed prompt is reported twice by design (POST body and SSE `error` frame) milliseconds apart; 5s leaves room
 * for a slow POST.
 */
const DEDUPE_WINDOW_MS = 5_000;

/** The server caps prose at 2 KiB (rpcerr.Text); the untruncated reason is on the turn's divider. */
const MAX_TOAST_CHARS = 240;

/** An empty server message is a server bug, so this points at the log. */
const NO_REASON = "The request failed. Check the server log for the cause.";

/** Per chat: one shared slot is overwritten by another chat's failure and un-latches the twin. */
const latched = new Map<string, { key: string; at: number }>();

/** The live toast per chat, so the dead-POST rescue can retract one. Keyed by
 *  chat because two chats can fail independently and each owns its own notice. */
const live = new Map<string, () => void>();

/** The error code that raised each chat's `live` notice, so a retraction can name the failure it answers. */
const liveCode = new Map<string, ErrorCode>();

/** Remedy-bearing toasts, per failure: sticky, so only an identical repeat replaces one. */
const remedies = new Map<string, () => void>();

/**
 * Report a failure. `chatID` may be any chat; "" is workspace-global. `action` is the route's own remedy: it takes
 * the action slot and makes the toast sticky. `code` is the server's error code, when the failure came with one.
 */
export function reportFailure(
  chatID: string,
  message: string,
  action?: ToastRetry,
  turnScoped = false,
  chatName = "",
  code?: ErrorCode,
): void {
  if (suppressedAsAlreadyOnScreen(chatID, action, turnScoped)) {
    return;
  }
  const reason = message.trim() !== "" ? message.trim() : NO_REASON;
  // Dedupe on the text: both channels render the same server-side prose. keyenc because a reason is arbitrary text.
  const key = join(chatID, reason);
  const now = Date.now();
  const last = latched.get(chatID);
  if (last?.key === key && now - last.at < DEDUPE_WINDOW_MS) {
    latched.set(chatID, { key, at: now });
    return;
  }
  // One failed attempt reports twice: the compaction notice, which says whether the next turn end tries again, and the
  // turn-scoped reason, which would erase that; the reason stays in the chat's "Compaction failed" row.
  const twinOfCompactionNotice =
    turnScoped && code === "compaction_failed" && liveCode.get(chatID) === "compaction_failed";
  if (twinOfCompactionNotice && last !== undefined && now - last.at < DEDUPE_WINDOW_MS) {
    return;
  }
  // A sticky remedy is replaced by its own repeat and by nothing else; an ordinary
  // notice is replaced by whatever this chat reports next.
  if (action !== undefined) {
    remedies.get(key)?.();
  } else {
    clearFailure(chatID);
  }
  // Latch after the retraction: `clearFailure` drops the latch.
  latched.set(chatID, { key, at: now });
  const dismiss = raise(chatID, truncate(reason, MAX_TOAST_CHARS), action, chatName);
  if (action !== undefined) {
    remedies.set(key, dismiss);
  } else {
    live.set(chatID, dismiss);
    if (code !== undefined) {
      liveCode.set(chatID, code);
    }
  }
}

// Over the bus: `raise` reaches the tab store, which reaches the transport, so a direct call would close a cycle.
onBus(BUS_COMMAND_FAILED, ({ chatID, chatName, message }) => {
  reportFailure(chatID, message, undefined, false, chatName);
});

/**
 * Whether the reader already sees the durable report: only a `turnScoped` failure has an inline home, with no remedy,
 * on a non-empty chat, on the active tab in a visible window.
 */
function suppressedAsAlreadyOnScreen(
  chatID: string,
  action: ToastRetry | undefined,
  turnScoped: boolean,
): boolean {
  if (!turnScoped || action !== undefined || chatID === "") {
    return false;
  }
  return tabIdFor("chat", chatID) === getActiveTabId() && document.visibilityState === "visible";
}

/**
 * Retract this chat's ordinary failure notice (`reportFailure`'s replace; exported as a test seam). Remedy-bearing
 * notices are out of reach.
 */
export function clearFailure(chatID: string): void {
  const dismiss = live.get(chatID);
  if (dismiss === undefined) {
    return;
  }
  live.delete(chatID);
  liveCode.delete(chatID);
  // The latch too, or the retracted text stays suppressed for the rest of the window.
  latched.delete(chatID);
  dismiss();
}

/** Retract this chat's notice only if a `compaction_failed` report raised it: the compaction it reported has now run. */
export function clearCompactionFailure(chatID: string): void {
  if (liveCode.get(chatID) === "compaction_failed") {
    clearFailure(chatID);
  }
}

/** Named for its chat (`noticeSubject`). A remedy takes the action slot, sticky; otherwise the slot is Open. */
function raise(
  chatID: string,
  reason: string,
  action: ToastRetry | undefined,
  chatName: string,
): () => void {
  const subject = noticeSubject(chatID, chatName);
  const message = named(subject, reason);
  if (action !== undefined) {
    return toastError(message, action);
  }
  return subject.open === undefined ? toastError(message) : errorWithAction(message, subject.open);
}

/** Test-only: drop the dedupe latch and every tracked toast handle. */
// deadset:ignore DS1102 -- test seam: resets the dedupe latch and tracked toast handles
export function _resetForTest(): void {
  latched.clear();
  live.clear();
  liveCode.clear();
  remedies.clear();
}
