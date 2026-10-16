// Derives the send-button state from the global pieces of the world:

import { signal, computed, effect } from "@cplieger/reactive";
import { activeSession, watchBusy } from "./store.js";
import { setSendState } from "./prompt-input.js";
import type { SendState } from "./prompt-input.js";
import type { ConnectionStatus } from "./types.js";

const sseStatus = signal<ConnectionStatus>("connecting");
/** The reason a send cannot succeed right now, or "" when none is known. */
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
  if (watchBusy(session.id)) {
    return { kind: "streaming" };
  }
  return { kind: "idle" };
});

effect(() => {
  setSendState(sendState.value);
});

// Clear a stale send-blocked state when the active chat changes. The signal is global but it is
// raised for ONE chat: a session door that failed (or an admission refusal) belongs to the chat
// that asked, and the next chat may be perfectly sendable.
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

/** Report that there is no agent to send to: the agent engine is not running or this chat's
 *  session could not open. Advisory — it changes the button's face and tooltip and never whether
 *  a send is allowed, because the next prompt retries it. Anything that is merely a FAILED
 *  ATTEMPT goes to failure-notice.ts. */
export function setAgentDown(reason: string): void {
  sendBlocked.value = reason;
}

/** Report that the server refused a send it cannot deliver right now: the prompt admission
 *  answered 409 reason:"starting". The caller (submit.ts) owns the copy; this renders it on the
 *  send button's error face. */
export function reportSendRefused(reason: string): void {
  sendBlocked.value = reason;
}

/** Clear the send-blocked state. Called on every send attempt and at every turn end, so a session
 *  that has since opened — or an admission slot that has since freed — leaves no residue on the
 *  button. */
export function clearAgentDown(): void {
  sendBlocked.value = "";
}
