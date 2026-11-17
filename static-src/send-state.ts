// Derives the send-button state from the global pieces of the world:

import { signal, computed, effect } from "@cplieger/reactive";
import { activeSession } from "./store.js";
import { setSendState } from "./prompt-input.js";
import type { SendState } from "./prompt-input.js";
import type { ConnectionStatus } from "./types.js";

const sseStatus = signal<ConnectionStatus>("connecting");
/** Why a send cannot succeed right now, and whether the next send can clear it; null when none is known. */
const sendBlocked = signal<{ reason: string; retry: boolean } | null>(null);

const sendState = computed<SendState>(() => {
  if (sseStatus.value === "disconnected") {
    return { kind: "error", reason: "Disconnected from the server. Reconnecting…", retry: true };
  }
  const blocked = sendBlocked.value;
  if (blocked !== null) {
    return { kind: "error", ...blocked };
  }
  const session = activeSession.value;
  if (session === undefined) {
    return { kind: "idle" };
  }
  // Cancel shows exactly while the server names something it would stop (`RunView.cancel`).
  if (session.run?.cancel !== undefined && session.turn_dropped !== true) {
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
    if (sendBlocked.peek() !== null) {
      sendBlocked.value = null;
    }
  }
});

export function setSSEStatus(s: ConnectionStatus): void {
  if (sseStatus.peek() === s) {
    return;
  }
  sseStatus.value = s;
  if (s === "connected") {
    sendBlocked.value = null;
  }
}

/** Report that there is no agent to send to: the agent engine is not running or this chat's
 *  session could not open. Advisory — it changes the button's face and tooltip and never whether
 *  a send is allowed, because the next prompt retries it. Anything that is merely a FAILED
 *  ATTEMPT goes to failure-notice.ts. */
export function setAgentDown(reason: string): void {
  sendBlocked.value = { reason, retry: true };
}

/** Put a refusal sentence on the send button's error face. The caller owns the words and says
 *  whether sending again can clear the refusal. */
export function reportSendRefused(reason: string, opts: { retry: boolean }): void {
  sendBlocked.value = { reason, retry: opts.retry };
}

/** Clear the send-blocked state. Called on every send attempt and at every turn end, so a session
 *  that has since opened — or an admission slot that has since freed — leaves no residue on the
 *  button. */
export function clearAgentDown(): void {
  sendBlocked.value = null;
}
