// Service-worker push messages:
//   "arrived"  landed on a focused page, so the worker showed no OS notification: a toast.
//   "clicked"  the subject becomes a route through push-subject.ts (the worker's own).
//   "subscription_changed"  the endpoint rotated, so the derived presence tag moved.

import { openPushTarget } from "../notification-open.js";
import { parsePushTarget } from "../push-subject.js";
import * as toast from "../toast.js";
import { named, noticeSubject } from "../notice-subject.js";

interface PushPageMessage {
  type: "push";
  reason: "clicked" | "arrived" | "subscription_changed";
  chatId: string;
  /** The notification's subject when it has no chat behind it — a pull request's
   *  CI flip. Carries a kind prefix so the route below is keyed on what the subject
   *  IS rather than on a URL the server would have had to assemble. */
  subject?: string;
  /** The chat's name when the push was sent; the page may no longer hold its row. */
  chatName?: string;
  title: string;
  body: string;
}

function isPushMessage(d: unknown): d is PushPageMessage {
  if (typeof d !== "object" || d === null) {
    return false;
  }
  const m = d as Partial<PushPageMessage>;
  return (
    m.type === "push" &&
    (m.reason === "clicked" || m.reason === "arrived" || m.reason === "subscription_changed") &&
    typeof m.chatId === "string" &&
    (m.chatName === undefined || typeof m.chatName === "string")
  );
}

/** Where a clicked notification goes. */
export function routePushMessage(msg: PushPageMessage): void {
  openPushTarget(parsePushTarget({ chatId: msg.chatId, subject: msg.subject ?? "" }));
}

/** The toast text. Title and body both come from the server, which builds them
 *  from a fixed vocabulary ("Permission needed", "Agent finished"), so this is
 *  a join rather than a formatter. */
function notice(msg: PushPageMessage): string {
  const body = msg.body.trim();
  return body === "" ? msg.title : body;
}

/** `onSubscriptionChanged` runs when the worker reports a rotated subscription; the
 *  caller owns the tag re-derivation (app.ts adoptPushTag). */
export function initPushMessages(onSubscriptionChanged: () => void): void {
  if (!("serviceWorker" in navigator)) {
    return;
  }
  navigator.serviceWorker.addEventListener("message", (event: MessageEvent) => {
    const msg: unknown = event.data;
    if (!isPushMessage(msg)) {
      return;
    }
    if (msg.reason === "subscription_changed") {
      onSubscriptionChanged();
      return;
    }
    if (msg.reason === "clicked") {
      routePushMessage(msg);
      return;
    }
    // handlers/run.ts toastCompletion already shows a run completion on a focused page, with the
    // verdict as the toast level; a second toast is one fact twice.
    if (parsePushTarget({ chatId: msg.chatId, subject: msg.subject ?? "" }).kind === "run") {
      return;
    }
    const subject = noticeSubject(msg.chatId, msg.chatName ?? "");
    toast.notice(named(subject, notice(msg)), "info", subject.open);
  });
}
