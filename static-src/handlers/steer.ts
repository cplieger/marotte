// ---------------------------------------------------------------------------
// Mid-turn steering SSE handlers.
//
// `steer_queued` says KAS's buffer holds the message and the agent has not read
// it, and it confirms the row the sending device drew at submit by adopting
// KAS's own id onto it. What a steer BECOMES is an entry —
// `entry_appended{steer}` with state `read` or `dropped` — so the dock row
// leaves inside `appendEntry` and the transcript's record is that entry at its
// own `seq`.
//
// This is not the only writer of `session.steers` — `chat.steer`'s
// `optimistic` draws the row on submit and `rollback` un-draws it on
// refusal, reconciled by the derivable id so the optimistic and confirmed
// rows never duplicate.
//
// A second event, `agent_notice`, lands here because it arrives on the same
// KAS channel: a workflow step's or subagent's progress line, split out by
// the server before it reaches the client.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { recordSteerQueued } from "../store.js";
import { info, success, error } from "../toast.js";

onSSE("steer_queued", (chatID, p) => {
  recordSteerQueued(chatID, { id: p.steer_id, text: p.text, origin: p.origin });
});

// ---------------------------------------------------------------------------
// The agent's own notices: a workflow step's or subagent's progress line,
// delivered through the same steering buffer and split into its own event.
//
// A toast, deliberately: nobody is waiting on it or can discard it, and it
// has no later state to update. Not a transcript row either — the step's
// output already lands in its delegated-work block.
// ---------------------------------------------------------------------------
onSSE("agent_notice", (_chatID, p) => {
  const text = p.text.trim();
  if (text === "") {
    return;
  }
  switch (p.severity) {
    case "success":
      success(text);
      return;
    case "warning":
    case "error":
      // Both take the error face — warning is closer to error than aside.
      error(text);
      return;
    default:
      info(text);
  }
});
