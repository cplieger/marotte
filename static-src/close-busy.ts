// What closing a tab stops: the server's close cancels the chat's reply, its questions, its
// workflow runs and its unread queued messages, tangents included, with no undo. An idle close
// asks nothing.

import { chatOutstanding } from "./chat-settled.js";
import { confirm } from "./confirm.js";
import { sessionDrain } from "./queue-slots.js";
import { get } from "./store.js";

/** One chat row the close takes with it: its ref and the name its tab shows. */
export interface ClosingChat {
  readonly ref: string;
  readonly name: string;
}

/** The live work a close stops, by kind. */
interface CloseImpact {
  readonly replies: readonly ClosingChat[];
  readonly asks: readonly ClosingChat[];
  readonly runs: number;
  readonly queued: number;
}

/** Read every term from the signals the tab strip paints from, so the confirm never disagrees
 *  with the dots. Queued counts unread steers plus follow-ups whose turn has not opened. */
function closeStopsWork(chats: readonly ClosingChat[]): CloseImpact {
  const replies: ClosingChat[] = [];
  const asks: ClosingChat[] = [];
  let runs = 0;
  let queued = 0;
  for (const chat of chats) {
    const outstanding = chatOutstanding(chat.ref);
    if (outstanding.turn) {
      replies.push(chat);
    }
    if (outstanding.asks) {
      asks.push(chat);
    }
    runs += outstanding.runs;
    const session = get(chat.ref);
    if (session !== undefined) {
      queued += (session.steers ?? []).length;
      queued += sessionDrain(session).rows.filter((r) => r.wait.kind !== "sent").length;
    }
  }
  return { replies, asks, runs, queued };
}

function isIdle(impact: CloseImpact): boolean {
  return (
    impact.replies.length === 0 &&
    impact.asks.length === 0 &&
    impact.runs === 0 &&
    impact.queued === 0
  );
}

function count(n: number, one: string, many: string): string {
  return `${String(n)} ${n === 1 ? one : many}`;
}

function listOf(parts: readonly string[]): string {
  if (parts.length <= 2) {
    return parts.join(" and ");
  }
  return `${parts.slice(0, -1).join(", ")}, and ${parts.at(-1) ?? ""}`;
}

/** A per-chat term: `bare` when the closed tab's own chat is the only one, else naming each. */
function perChat(
  chats: readonly ClosingChat[],
  closingRef: string,
  bare: string,
  named: string,
): string {
  if (chats.length === 1 && chats[0]?.ref === closingRef) {
    return bare;
  }
  return `${named} ${listOf(chats.map((c) => c.name))}`;
}

/** The confirm's text for closing the tab named `name`, whose own chat is `ref` ("" for a tab
 *  that is not a chat). */
function closeImpactText(name: string, ref: string, impact: CloseImpact): string {
  const parts: string[] = [];
  if (impact.replies.length > 0) {
    const named = impact.replies.length === 1 ? "stops the reply in" : "stops the replies in";
    parts.push(perChat(impact.replies, ref, "stops the reply", named));
  }
  if (impact.asks.length > 0) {
    parts.push(
      perChat(
        impact.asks,
        ref,
        "cancels the question waiting for your answer",
        impact.asks.length === 1
          ? "cancels the question waiting for your answer in"
          : "cancels the questions waiting for your answer in",
      ),
    );
  }
  if (impact.runs > 0) {
    parts.push(`cancels ${count(impact.runs, "workflow run", "workflow runs")}`);
  }
  if (impact.queued > 0) {
    parts.push(`drops ${count(impact.queued, "queued message", "queued messages")}`);
  }
  return `Closing ${name} ${listOf(parts)}.`;
}

/** The gate tabs.ts runs before a person's close of the tab named `name` (chat `ref`, or "")
 *  taking `chats` with it: a plain `true` when none has live work, else the destructive
 *  confirm's answer. */
export function confirmBusyClose(
  name: string,
  ref: string,
  chats: readonly ClosingChat[],
): true | Promise<boolean> {
  const impact = closeStopsWork(chats);
  if (isIdle(impact)) {
    return true;
  }
  return confirm(closeImpactText(name, ref, impact), "Close and stop", "destructive");
}
