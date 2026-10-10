// A ROW `steer_queued` confirms the row its id names, a chat's or (when it names a run) a run
// step's: the sending device drew it at submit, so the optimistic and confirmed rows share the
// derived id. A BATCH frame (`replaces`) says KAS holds several rows under one id and adds no row.
// A row leaves the dock on its `steer` entry.

import { onSSE } from "../bus.js";
import { recordSteerQueued } from "../store.js";
import { recordStepSteerQueued } from "../run-step-steers.js";
import { notice } from "../toast.js";
import { named, noticeSubject } from "../notice-subject.js";
import type { NoticeLevel } from "../wire/types.gen.js";

onSSE("steer_queued", (chatID, p) => {
  const frame = {
    id: p.steer_id,
    text: p.text,
    origin: p.origin,
    replaces: p.replaces,
    state: p.state,
  };
  // A run step's row names its run and step and no chat.
  if (p.workflow_id !== undefined && p.workflow_id !== "") {
    recordStepSteerQueued(p.workflow_id, p.node_path ?? "", frame);
    return;
  }
  recordSteerQueued(chatID, frame);
});

// A toast: nobody can discard a notice and it has no later state, and the step's
// output already lands in its delegated-work block.
onSSE("agent_notice", (chatID, p) => {
  const text = p.text.trim();
  if (text === "") {
    return;
  }
  const subject = noticeSubject(chatID);
  notice(named(subject, text), agentNoticeLevel(p.severity), subject.open);
});

/** KAS's four steering severities; anything else is the neutral info level. */
function agentNoticeLevel(severity: string): NoticeLevel | "success" {
  switch (severity) {
    case "success":
    case "warning":
    case "error":
      return severity;
    default:
      return "info";
  }
}
