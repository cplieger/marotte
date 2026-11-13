// Service-worker push messages:
//   "arrived"  landed while this page was focused, so the worker showed no OS notification and
//              posted it here alone: the page shows it unless its chat or run is on screen.
//   "clicked"  the subject becomes a route through push-subject.ts (the worker's own).
//   "subscription_changed"  the endpoint rotated, so the derived presence tag moved.

import { openPushTarget } from "../notification-open.js";
import { showNotice } from "../notify.js";
import { type PushPageMessage, type PushTarget, parsePushTarget } from "../push-subject.js";
import { getActiveTabId, tabIdFor } from "../tabs.js";
import type { NotificationPayload, PushKind } from "../wire/types.gen.js";

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
    (m.id === undefined || typeof m.id === "string") &&
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
  const notice: NotificationPayload = {
    id: msg.id ?? "",
    kind,
    title: msg.title ?? "",
    body: msg.body ?? "",
  };
  if (msg.chatId !== "") {
    notice.chat_id = msg.chatId;
  }
  if (msg.subject !== undefined && msg.subject !== "") {
    notice.subject = msg.subject;
  }
  return notice;
}

/** The open tab `target` names, "" when it names none or that tab is closed. */
function tabOf(target: PushTarget): string {
  switch (target.kind) {
    case "chat":
      return tabIdFor("chat", target.chatID);
    case "run":
    case "run_ask":
      return tabIdFor("run", target.workflowID);
    case "pr":
    case "rule":
    case "workspace":
      return "";
  }
}

/** True only while the page is visible AND `target`'s tab is the active one. The one screen check
 *  a page still makes: the server pushed after judging this profile without a page on that tab,
 *  so nobody judged this page's screen. */
function targetOnScreen(target: PushTarget): boolean {
  if (document.visibilityState !== "visible") {
    return false;
  }
  const tab = tabOf(target);
  return tab !== "" && tab === getActiveTabId();
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
        const target = parsePushTarget({ chatId: msg.chatId, subject: msg.subject ?? "" });
        if (notice !== null && !targetOnScreen(target)) {
          showNotice(notice);
        }
        return;
      }
    }
  });
}
