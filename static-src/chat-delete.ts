// Deleting a chat that has no open tab for good: the one door the History page's chat rows and the
// sidebar History section's closed rows share, so both ask the same question and send the same delete.

import { deleteChat as deleteChatAction, loadSessions } from "./actions/chat.js";
import { retryNetwork } from "./actions/index.js";
import { serverImpactOf } from "./actions/close-impact.js";
import { deleteStopsWork } from "./close-busy.js";
import { confirm } from "./confirm.js";
import { actionNotice } from "./notice-subject.js";
import { descendantChatsOf } from "./tabs.js";
import type { WorkflowRun } from "./types.js";
import type { CloseImpact } from "./wire/types.gen.js";

/** How many of `runs` each chat launched, by chat id; deleting a chat deletes them too. */
export function runsLaunched(runs: readonly WorkflowRun[]): Map<string, number> {
  const launched = new Map<string, number>();
  for (const r of runs) {
    const parent = r.parent_chat_id ?? "";
    if (parent !== "") {
      launched.set(parent, (launched.get(parent) ?? 0) + 1);
    }
  }
  return launched;
}

/**
 * Ask, then run `delete_chat`: the chat file, its `runCount` runs, its KAS session chain, and the
 * chat tabs under its tabs, whose work the confirm names. Not recoverable. True when gone.
 */
export async function confirmDeleteChat(
  chatID: string,
  title: string,
  runCount: number,
): Promise<boolean> {
  const runs =
    runCount === 0
      ? ""
      : ` It also deletes the ${runCount === 1 ? "workflow run" : `${String(runCount)} workflow runs`} it started.`;
  const stops = deleteStopsWork(descendantChatsOf(chatID));
  const ok = await confirm({
    title: "Delete this conversation?",
    body: `"${title}" and its stored history are removed for good.${runs}${
      stops.text === "" ? "" : ` ${stops.text}`
    }`,
    confirmLabel: "Delete",
    variant: "destructive",
  });
  return ok && deleteChatAsked(chatID, title, stops.impact);
}

/** `confirmDeleteChat` for a caller holding no run list: reads `/api/sessions` for the runs the
 *  confirm names, and asks nothing when that read fails, since the confirm could not say them. */
export async function deleteClosedChat(chatID: string, title: string): Promise<boolean> {
  const listed = await loadSessions.dispatch(undefined);
  if (listed === null) {
    return false;
  }
  return confirmDeleteChat(chatID, title, runsLaunched(listed.runs).get(chatID) ?? 0);
}

/** Send the delete with what the confirm named; a stale confirm (another device changed what the delete stops in
 *  the chats under its tabs) asks again on the merge, and each answer is a person's, so the loop needs no cap.
 *  The tab is not closed here: the membership coordinator closes tabs for a deleted chat under the same lock. */
async function deleteChatAsked(
  chatID: string,
  title: string,
  named: CloseImpact,
): Promise<boolean> {
  let impact = named;
  for (;;) {
    const args = { id: chatID, impact };
    const outcome = await deleteChatAction.dispatch(args).outcome;
    if (outcome.status !== "error") {
      return outcome.status === "success";
    }
    const server = serverImpactOf(outcome.error);
    if (server === undefined) {
      if (outcome.error.code !== "cancelled") {
        const retry = retryNetwork(outcome.error)
          ? {
              onClick: () => {
                void deleteChatAction.dispatch(args);
              },
            }
          : undefined;
        actionNotice(chatID, `Could not delete chat: ${outcome.error.message}`, "error", retry);
      }
      return false;
    }
    const again = deleteStopsWork(descendantChatsOf(chatID), { sent: impact, server });
    const yes = await confirm({
      title: `Delete "${title}" anyway?`,
      body: `What deleting it also stops changed.${again.text === "" ? "" : ` ${again.text}`}`,
      confirmLabel: "Delete",
      variant: "destructive",
    });
    if (!yes) {
      return false;
    }
    impact = again.impact;
  }
}
