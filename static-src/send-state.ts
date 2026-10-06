// ---------------------------------------------------------------------------
// Derives the send-button state (disconnected > sendBlocked > streaming > idle) from
// the SSE status, the agent being unreachable and the active chat's thinking flag, as
// one `computed` an `effect` pushes to prompt-input.ts. The error face means "a send
// cannot succeed NOW" — the stream is down, `bridge_start_failed`, or a 409
// reason:"starting" — so a failed send, which leaves the chat promptable, goes to
// failure-notice.ts instead. Nothing here sets `disabled`: the composer stays live.
// ---------------------------------------------------------------------------

import { signal, computed, effect } from "@cplieger/reactive";
import { activeSession } from "./store.js";
import { setSendState } from "./prompt-input.js";
import type { SendState } from "./prompt-input.js";
import type { ConnectionStatus } from "./types.js";

const sseStatus = signal<ConnectionStatus>("connecting");
/** The reason a send cannot succeed right now, or "" when none is known. Fed by
 *  two writers with distinct meanings — setAgentDown (no agent behind this
 *  chat) and reportSendRefused (admission refused, holder cannot take a
 *  steer) — sharing one signal because they share the surface AND the
 *  lifecycle: cleared on every new attempt, on chat switch, on turn end and on
 *  SSE reconnect. */
const sendBlocked = signal("");

const sendState = computed<SendState>(() => {
  if (sseStatus.value === "disconnected") {
    return { kind: "error", reason: "Disconnected from the server. Reconnecting…" };
  }
  if (sendBlocked.value !== "") {
    return { kind: "error", reason: sendBlocked.value };
  }
  const session = activeSession.value;
  if (session === undefined) {
    return { kind: "idle" };
  }
  if (session.thinking) {
    return { kind: "streaming" };
  }
  return { kind: "idle" };
});

effect(() => {
  setSendState(sendState.value);
});

// Clear a stale send-blocked state when the active chat changes. The signal
// is global but it is raised for ONE chat: a bridge that could not start (or an
// admission refusal) belongs to the chat that asked, and the next chat may be
// perfectly sendable. The errors that set it emit no turn_closed to clear it, so
// without this one chat's dead bridge decorates the button on every chat.
let sendBlockedActiveID = "";
effect(() => {
  const id = activeSession.value?.id ?? "";
  if (id !== sendBlockedActiveID) {
    sendBlockedActiveID = id;
    if (sendBlocked.peek() !== "") {
      sendBlocked.value = "";
    }
  }
});

export function setSSEStatus(s: ConnectionStatus): void {
  if (sseStatus.peek() === s) {
    return;
  }
  sseStatus.value = s;
  if (s === "connected") {
    sendBlocked.value = "";
  }
}

/** Report that there is no agent to send to: the ACP subprocess for this chat
 *  could not be started. Advisory — it changes the button's face and tooltip and
 *  never whether a send is allowed, because the next prompt is what retries the
 *  spawn. Anything that is merely a FAILED ATTEMPT goes to failure-notice.ts. */
export function setAgentDown(reason: string): void {
  sendBlocked.value = reason;
}

/** Report that the server refused a send it cannot deliver right now: the
 *  prompt admission answered 409 reason:"starting". The caller (submit.ts)
 *  owns the copy; this renders it on the send button's error face. Advisory
 *  exactly like setAgentDown — the composer stays live and the next Send is
 *  the retry, which is also what clears it (clearAgentDown runs on every
 *  attempt). */
export function reportSendRefused(reason: string): void {
  sendBlocked.value = reason;
}

/** Clear the send-blocked state. Called on every send attempt and at every
 *  turn end, so a bridge that has since started — or an admission slot that has
 *  since freed — leaves no residue on the button. */
export function clearAgentDown(): void {
  sendBlocked.value = "";
}
