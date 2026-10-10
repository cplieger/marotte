// Service-worker push messages:
//   "arrived"  landed on a focused page, so the worker showed no OS notification: the page
//              delivers it as it delivers its own `notification` frame.
//   "clicked"  the subject becomes a route through push-subject.ts (the worker's own).
//   "subscription_changed"  the endpoint rotated, so the derived presence tag moved.

import { deliverNotification } from "../agent-finished-cue.js";
import { openPushTarget } from "../notification-open.js";
import { parsePushTarget } from "../push-subject.js";
import type { NotificationPayload, PushKind } from "../wire/types.gen.js";

interface PushPageMessage {
  type: "push";
  reason: "clicked" | "arrived" | "subscription_changed";
  chatId: string;
  /** The notification's subject when it has no chat behind it: a run or a pull request,
   *  kind-prefixed so the route is keyed on what the subject IS. */
  subject?: string;
  /** The notification's kind on an arrival, "" otherwise. */
  kind?: string;
  /** The notification's title: the name of the tab its click opens, as the push was sent. */
  title?: string;
  body?: string;
}

/** Every kind `marotte.PushKind` sends, a missing one a type error; an arrival of any other is
 *  not delivered. */
const PUSH_KINDS: Readonly<Record<PushKind, true>> = {
  agent_finished: true,
  permission: true,
  pr_status: true,
  run_outcome: true,
};

function isPushKind(kind: string): kind is PushKind {
  return Object.hasOwn(PUSH_KINDS, kind);
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
    (m.subject === undefined || typeof m.subject === "string") &&
    (m.kind === undefined || typeof m.kind === "string") &&
    (m.title === undefined || typeof m.title === "string") &&
    (m.body === undefined || typeof m.body === "string")
  );
}

/** The server's notification an arrival carried, null when its kind is not one the server sends. */
function arrivedNotice(msg: PushPageMessage): NotificationPayload | null {
  const kind = msg.kind ?? "";
  if (!isPushKind(kind)) {
    return null;
  }
  const notice: NotificationPayload = { kind, title: msg.title ?? "", body: msg.body ?? "" };
  if (msg.chatId !== "") {
    notice.chat_id = msg.chatId;
  }
  if (msg.subject !== undefined && msg.subject !== "") {
    notice.subject = msg.subject;
  }
  return notice;
}

/** Where a clicked notification goes. */
export function routePushMessage(msg: PushPageMessage): void {
  openPushTarget(parsePushTarget({ chatId: msg.chatId, subject: msg.subject ?? "" }));
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
    switch (msg.reason) {
      case "subscription_changed":
        onSubscriptionChanged();
        return;
      case "clicked":
        routePushMessage(msg);
        return;
      case "arrived": {
        const notice = arrivedNotice(msg);
        if (notice !== null) {
          deliverNotification(msg.chatId, notice);
        }
        return;
      }
    }
  });
}
