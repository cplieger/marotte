// A server notification about `target`, as internal/notice would send it. Kind defaults to the
// permission floor, which no per-kind switch can mute.

import type { PushTarget } from "../push-subject.js";
import type { NotificationPayload, PushKind } from "../wire/types.gen.js";

export function noticeFor(
  target: PushTarget,
  body: string,
  kind: PushKind = "permission",
  title = "A chat",
): NotificationPayload {
  switch (target.kind) {
    case "chat":
      return { chat_id: target.chatID, kind, title, body };
    case "run":
      return { subject: `run:${target.workflowID}`, kind, title, body };
    case "pr":
      return { subject: `pr:${target.identity}`, kind, title, body };
    case "workspace":
      return { kind, title, body };
  }
}
