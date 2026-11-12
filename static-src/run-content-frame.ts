// Which run a frame's content belongs to, and the scope that draws a run's frames. Pure, because the
// SharedWorker's host imports it beside the page.

import { validTopic } from "@cplieger/sse";

/** The frames the server publishes on a run's topic (`translate.entryScope.event` decides which). */
const RUN_CONTENT: ReadonlySet<string> = new Set([
  "turn_opened",
  "entry_opened",
  "entry_delta",
  "entry_sealed",
  "entry_appended",
  "turn_closed",
]);

/** `ids.ValidChatID`'s rule, which a workflow id passes at every server door. */
const RUN_ID = /^[A-Za-z0-9_-]{1,128}$/;

/** The workflow id beside `turn` on a run's log frame, read off a raw payload; "" when absent. */
export function rawWorkflowID(payload: unknown): string {
  if (typeof payload !== "object" || payload === null) {
    return "";
  }
  const id = (payload as Record<string, unknown>)["workflow_id"];
  return typeof id === "string" ? id : "";
}

/** The run whose log an envelope carries, "" for a broadcast. A decoded `ServerEvent` reads the
 *  same as the raw JSON. */
export function runContentOf(envelope: unknown): string {
  if (typeof envelope !== "object" || envelope === null) {
    return "";
  }
  const o = envelope as Record<string, unknown>;
  const type = o["type"];
  return typeof type === "string" && RUN_CONTENT.has(type) ? rawWorkflowID(o["payload"]) : "";
}

/** The hub topic a run's log rides, the client's spelling of `marotte.RunTopic`. */
export function runTopic(workflowID: string): string {
  return `run:${workflowID}`;
}

/** The scope a page showing `workflowID` ("" for none) draws: broadcasts plus that run's topic.
 *  An id no run can carry draws broadcasts only; one too long for a topic draws everything. */
export function runScopeOf(workflowID: string): readonly string[] | null {
  if (!RUN_ID.test(workflowID)) {
    return [];
  }
  const topic = runTopic(workflowID);
  return validTopic(topic) ? [topic] : null;
}
