// A server notification about `target`, as internal/notice would send it. Kind defaults to the
// permission floor, which no per-kind switch can mute.

import type { PushTarget } from "../push-subject.js";
import type { NotificationPayload, PushKind } from "../wire/types.gen.js";

let minted = 0;

export function noticeFor(
  target: PushTarget,
  body: string,
  kind: PushKind = "permission",
  title = "A chat",
): NotificationPayload {
  // A fresh id per call: a page shows one notice id once, and each call is a new notice.
  const id = `notice-${String(++minted)}`;
  switch (target.kind) {
    case "chat":
      return { id, chat_id: target.chatID, kind, title, body };
    case "run":
      return { id, subject: `run:${target.workflowID}`, kind, title, body };
    case "run_ask":
      return { id, subject: `runask:${target.position}`, kind, title, body };
    case "pr":
      return { id, subject: `pr:${target.identity}`, kind, title, body };
    case "rule":
      return { id, subject: `rule:${target.ruleID}`, kind, title, body };
    case "workspace":
      return { id, kind, title, body };
  }
}
