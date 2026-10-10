import { get } from "./store.js";
import { mergeRefused, mergeTangent, readMergeStatus, type MergeAnswer } from "./actions/chat.js";
import { BUS_COMMAND_FAILED, emitBus } from "./bus.js";
import { newOpID } from "./transport.js";
import { actionNotice, chatNotice, subjectName } from "./notice-subject.js";

/** A merge this device asked for, until it settles. `accepted`: the server answered that it held
 *  the op, so a later "absent" means its outcome expired rather than that it never started. */
interface Continuation {
  readonly tangentID: string;
  readonly name: string;
  phase: "sending" | "unknown" | "accepted";
}

/** By op_id. Each entry leaves through {@link settle}, the one place a merge's outcome reaches the
 *  user; an entry whose status read failed waits for the next SSE connection's
 *  {@link reconcileMerges}. */
const asked = new Map<string, Continuation>();

export async function mergeTangentChat(tangentID: string): Promise<void> {
  if (tangentID === "" || get(tangentID)?.tangent !== true) {
    return;
  }
  const opID = newOpID();
  asked.set(opID, { tangentID, name: subjectName(tangentID), phase: "sending" });
  const outcome = await mergeTangent.dispatch({ chatID: tangentID, opID }).outcome;
  const merge = asked.get(opID);
  if (merge === undefined) {
    return;
  }
  if (outcome.status === "success") {
    settle(opID, outcome.value);
    return;
  }
  // Refused before the server accepted anything, so no terminal frame follows.
  if (outcome.status === "error" && mergeRefused(outcome.error)) {
    asked.delete(opID);
    actionNotice(
      merge.tangentID,
      `Merge failed: ${outcome.error.message}`,
      "error",
      undefined,
      merge.name,
    );
    return;
  }
  merge.phase = "unknown";
  await reconcile(opID);
  if (asked.get(opID)?.phase === "unknown") {
    chatNotice(
      merge.tangentID,
      "Could not confirm whether the merge started",
      "warning",
      merge.name,
    );
  }
}

/** Ask the server where every unsettled merge stands, since the frames that would have settled
 *  them may have been sent while this device was not connected. */
export function reconcileMerges(): void {
  for (const opID of asked.keys()) {
    void reconcile(opID);
  }
}

/** One status read; a failed read leaves the entry for the next connection. An admission still
 *  in flight is skipped: its own reply settles it. */
async function reconcile(opID: string): Promise<void> {
  const merge = asked.get(opID);
  if (merge === undefined || merge.phase === "sending") {
    return;
  }
  const outcome = await readMergeStatus.dispatch({ chatID: merge.tangentID, opID }).outcome;
  if (outcome.status === "success") {
    settle(opID, outcome.value);
  }
}

/** A failed merge's reason goes to failure-notice.ts as the server wrote it, which is what lets its
 *  latch fold this report into the one every device raises from the tangent_merge_failed frame. */
function settle(opID: string, answer: MergeAnswer): void {
  const merge = asked.get(opID);
  if (merge === undefined) {
    return;
  }
  switch (answer.state) {
    case "admitting":
      return;
    case "running":
      merge.phase = "accepted";
      return;
    case "succeeded":
      asked.delete(opID);
      switchToParent(answer.parentChatID);
      return;
    case "failed":
      asked.delete(opID);
      emitBus(BUS_COMMAND_FAILED, {
        chatID: merge.tangentID,
        chatName: merge.name,
        message: answer.message,
      });
      return;
    case "absent":
      asked.delete(opID);
      chatNotice(
        merge.tangentID,
        merge.phase === "accepted"
          ? "Could not confirm how the merge ended"
          : "The merge did not start",
        "warning",
        merge.name,
      );
      return;
  }
}

/** Whichever chat is on screen. */
function switchToParent(parentID: string): void {
  // Lazy: chat.ts reaches the page graph.
  void import("./chat.js").then(({ switchSession }) => switchSession(parentID));
}

export function noteTangentMerged(opID: string | undefined, parentID: string): void {
  if (opID !== undefined) {
    settle(opID, { state: "succeeded", parentChatID: parentID });
  }
}

export function noteTangentMergeFailed(opID: string | undefined, message: string): void {
  if (opID !== undefined) {
    settle(opID, { state: "failed", message });
  }
}
