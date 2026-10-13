// "Deliver now": a workflow step's message to the launching chat sits in KAS's steering buffer, which KAS reads at
// the model call of a running turn. An idle chat starts no turn for it, so the card keeps reading "not delivered
// yet" until something wakes the chat. The banner offers that wake, once per chat however many cards wait.

import { effect, touch } from "@cplieger/reactive";
import { clearBannerCodes, showBanner } from "./banner-stack.js";
import { submitLabelled } from "./submit.js";
import { activeSession, messagesVersionOf, turnLive } from "./store.js";
import { payloadOf } from "./turns.js";
import type { Session } from "./types.js";
import { WORKFLOW_MESSAGE_ID_PREFIX } from "./workflow-delivery.js";

const BANNER_CODE = "workflow_updates_waiting";

/** The prompt that starts the turn. KAS's first model call of it reads every unread buffered message, so this carries
 *  none of their text. */
const WAKE_PROMPT = "Workflow updates are waiting for you. Read them and continue.";

/** What the transcript shows in place of the prompt. */
const WAKE_LABEL = "Deliver workflow updates";

/** Chats whose wake is sent and has not yet shown as a running turn, so a second press cannot queue a duplicate. */
const sending = new Set<string>();

/** How many resident workflow messages from a step wait unread: logged at their send, with no take-up folded onto
 *  them. A notice KAS logged when it read it has no take-up to wait for, so it never counts. */
export function waitingWorkflowMessages(s: Session): number {
  let waiting = 0;
  for (const turn of s.turns.values()) {
    for (const e of turn.entries) {
      const steer = payloadOf(e, "steer");
      if (
        e.id.startsWith(WORKFLOW_MESSAGE_ID_PREFIX) &&
        steer?.origin === "step" &&
        steer.produced_ts !== undefined &&
        steer.state === undefined &&
        steer.read_ts === undefined
      ) {
        waiting++;
      }
    }
  }
  return waiting;
}

/** How many updates the banner states: none while a turn runs, because delivery is then imminent. */
function bannerCount(s: Session): number {
  return turnLive(s) || s.thinking ? 0 : waitingWorkflowMessages(s);
}

function bannerText(count: number): string {
  return count === 1
    ? "A workflow update is waiting for the agent."
    : `${String(count)} workflow updates are waiting for the agent.`;
}

function deliver(chatID: string): void {
  if (sending.has(chatID)) {
    return;
  }
  sending.add(chatID);
  void submitLabelled(chatID, WAKE_PROMPT, WAKE_LABEL).then((result) => {
    if (result === "failed") {
      sending.delete(chatID);
    }
  });
}

/** Keep the active chat's banner in step with its transcript. Called once from the composition root. */
export function initWorkflowDeliverNow(): void {
  effect(() => {
    const s = activeSession.value;
    if (s === undefined) {
      return;
    }
    touch(messagesVersionOf(s.id));
    const count = bannerCount(s);
    if (count === 0) {
      sending.delete(s.id);
      clearBannerCodes(s.id, [BANNER_CODE]);
      return;
    }
    showBanner(s.id, BANNER_CODE, bannerText(count), "info", false, {
      label: "Deliver now",
      onClick: () => {
        deliver(s.id);
      },
    });
  });
}
