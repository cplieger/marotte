// Workflow-run actions. Cancel, pause and resume are KAS's own verbs; cancel doubles as the
// tab-close gesture for a launcher-owned run tab.

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import { retryOutcomeNotice } from "../run-controls.js";
import { invalidateRun, invalidateRunControls } from "../run-store.js";
import { errorAbout, noticeAbout } from "./subject.js";
import { decodeRunRetriedResponse, decodeRunStepMessageResponse } from "../wire/decoders.gen.js";
import type {
  RunRetriedResponse,
  RunStepMessageRequest,
  RunStepMessageResponse,
  RunStepSteerRequest,
} from "../wire/types.gen.js";
import type {
  RecipesResponse,
  RunAnswerRequest,
  RunLaunchRequest,
  RunLaunchedResponse,
  SessionListResponse,
} from "../types.js";
import { decodeSessionListResponse } from "../wire/decoders.gen.js";

/** A run's notification subject, the `run:` key `noticeSubject` names by label. */
const onRun = (workflowID: string): string => `run:${workflowID}`;

/** The launchable recipe list, bundled + workspace. */
export const loadRecipes = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  RecipesResponse
>({
  name: "runs.recipes",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/recipes" }),
  error: "Could not load workflows",
});

/** The current run inventory: the history page's endpoint, but its own action, so history
 *  cancelling its dispatch on teardown cannot cost the Workflows tab its refresh. */
export const loadRuns = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  SessionListResponse
>({
  name: "runs.list",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/sessions" }),
  // The same reply as chat.load_sessions, so one generated decoder.
  decode: decodeSessionListResponse,
  error: false,
});

/** Launch one PARENTLESS run. A 409 (the recipe already has a live run) is surfaced verbatim. */
export const launchRun = apiAction<RunLaunchRequest, RunLaunchedResponse>({
  name: "runs.launch",
  request: (body) => ({ method: "POST", path: "/api/runs", body }),
  error: (_args, err) => serverSentence(err) ?? "Could not launch",
});

/** Ask a run to stop. The reply confirms the ASK — cancel is a node-boundary
 *  verb, so the terminal state (and the run_finished event) follows at the
 *  in-flight node's end. */
export const cancelRun = apiAction<string, { ok: boolean }>({
  name: "runs.cancel",
  request: (workflowID) => ({
    method: "POST",
    path: `/api/runs/${encodeURIComponent(workflowID)}/cancel`,
  }),
  error: errorAbout(onRun, "Could not cancel the run"),
});

/** One run-control verb: POST to a sub-path, no body, `{ok:true}` back. */
function runControl(verb: string, errorText: string) {
  return apiAction<string, { ok: boolean }>({
    name: `runs.${verb}`,
    request: (workflowID) => ({
      method: "POST",
      path: `/api/runs/${encodeURIComponent(workflowID)}/${verb}`,
    }),
    error: errorAbout(onRun, errorText),
  });
}

/** Stop a run at its next node boundary, keeping it resumable. The reply confirms the ASK; the
 *  paused state arrives as a run_progress invalidation. */
export const pauseRun = runControl("pause", "Could not pause the run");

/** Reset a failed run's failed and aborted steps (plus ancestors) and re-drive it, keeping every
 *  completed step. Not a `runControl`: it decodes the OUTCOME (which nodes were reset), reports a
 *  zero-node reset as a no-op rather than a success, and refetches the run and its affordance,
 *  since a no-op produces no `run_progress` frame. Refusals show the server's sentence alone. */
export const retryRun = apiAction<string, RunRetriedResponse>({
  name: "runs.retry",
  request: (workflowID) => ({
    method: "POST",
    path: `/api/runs/${encodeURIComponent(workflowID)}/retry`,
  }),
  decode: (data) => decodeRunRetriedResponse(data),
  error: errorAbout(onRun, (_args, err) => serverSentence(err) ?? "Could not retry the run"),
  onSuccess: (res, workflowID) => {
    const notice = retryOutcomeNotice(res.retried_node_ids.length);
    noticeAbout(onRun(workflowID), notice.text, notice.level);
    // BOTH, even on a zero-node outcome: the status and the verbs it offers can have moved.
    invalidateRun(workflowID);
    invalidateRunControls(workflowID);
  },
});

/** Re-drive a paused run. Works even when the launching process is gone (KAS reloads the run
 *  from disk), so it is offered on any paused run. */
export const resumeRun = runControl("resume", "Could not resume the run");

/** Answer the question a parked workflow step asked. The server claims the ask BEFORE it sends,
 *  so one surface wins; a 409 means settled (nothing to redo) or the run is BETWEEN steps (the
 *  question comes back on a fresh `run_input_needed`), and only the server's sentence tells
 *  them apart. NO argument-composite idempotency key: inside that cache's window a repeat
 *  replays a cached success and the answer never reaches the step. */
export const answerRunInput = apiAction<{ workflowID: string } & RunAnswerRequest, { ok: boolean }>(
  {
    name: "runs.answer_input",
    request: ({ workflowID, ask_id, text }) => ({
      method: "POST",
      path: `/api/runs/${encodeURIComponent(workflowID)}/answer`,
      body: { ask_id, text },
    }),
    // The server's sentence ALONE: a static `error` string PREFIXES the message (`emitErrorToast`),
    // which contradicts the 409's "already answered" and leaks the empty-body `HTTP 500`.
    error: errorAbout(
      ({ workflowID }: { workflowID: string }) => onRun(workflowID),
      (_args, err) => serverSentence(err) ?? "Could not send your answer to the step",
    ),
  },
);

/** One run step's address, `/api/runs/{id}/steps/{path}` with `{path}` one encoded `nodePathKey`; its
 *  verbs are sub-paths. The workflow id and the node path are independent, so neither is derived from
 *  the other. */
export function runStepURL(workflowID: string, nodePath: string): string {
  return `/api/runs/${encodeURIComponent(workflowID)}/steps/${encodeURIComponent(nodePath)}`;
}

interface StepAddress {
  workflowID: string;
  nodePath: string;
}

/** Send the user's words to one run step; the server picks the verb from the step's live state (steer,
 *  answer, or resume). The rejection carries the server's sentence and its machine reason (`code`) for
 *  the run composer to word, which owns the failure, so there is no toast. The idempotency key is the
 *  message id, so a retry of a send whose reply was lost replays its answer instead of delivering the
 *  words twice; a retry of a refused send therefore needs a fresh id. */
export const messageRunStep = apiAction<
  StepAddress & RunStepMessageRequest,
  RunStepMessageResponse
>({
  name: "runs.message_step",
  idempotencyKey: ({ message_id }) => message_id,
  request: ({ workflowID, nodePath, text, message_id }) => ({
    method: "POST",
    path: `${runStepURL(workflowID, nodePath)}/message`,
    body: { text, message_id },
  }),
  decode: decodeRunStepMessageResponse,
  error: false,
});

/** Delete one unread row of a step's dock (the chat's steer_remove, on the step's session). Rejects
 *  with the server's sentence and status, so Edit can tell a refusal from a lost reply. */
export const removeRunStepSteer = apiAction<StepAddress & RunStepSteerRequest, { deleted: string }>(
  {
    name: "runs.remove_step_steer",
    request: ({ workflowID, nodePath, steer_id }) => ({
      method: "POST",
      path: `${runStepURL(workflowID, nodePath)}/steer-remove`,
      body: { steer_id },
    }),
    error: false,
  },
);

/** Discard every unread row of a step's dock (the chat's steer_clear, on the step's session). */
export const clearRunStepSteers = apiAction<StepAddress, { ok: boolean }>({
  name: "runs.clear_step_steers",
  request: ({ workflowID, nodePath }) => ({
    method: "POST",
    path: `${runStepURL(workflowID, nodePath)}/steer-clear`,
  }),
  error: errorAbout(
    ({ workflowID }: { workflowID: string }) => onRun(workflowID),
    "Could not discard",
  ),
});

/** The server's error sentence, or null. `@cplieger/fetch` falls back to the literal
 *  `HTTP <status>` for an empty body, and a transport failure (`status === 0`) carries a browser
 *  sentence, so both are compared exactly. */
function serverSentence(err: {
  readonly message: string;
  readonly status?: number;
}): string | null {
  const status = err.status ?? 0;
  if (status < 400 || err.message === "" || err.message === `HTTP ${String(status)}`) {
    return null;
  }
  return err.message;
}

/** Let a parked step carry on with NO answer. Not Resume: KAS's resume leaves the step's
 *  `need_input` signal, so it re-parks; setting the status clears it and the step runs its
 *  default continuation. */
export const continueRunStep = apiAction<{ workflowID: string; nodeID: string }, { ok: boolean }>({
  name: "runs.continue_step",
  request: ({ workflowID, nodeID }) => ({
    method: "POST",
    path: `/api/runs/${encodeURIComponent(workflowID)}/step`,
    body: { node_id: nodeID, status: "running" },
  }),
  error: errorAbout(({ workflowID }) => onRun(workflowID), "Could not let the step continue"),
});

export const extendRunRepeat = apiAction<
  { workflowID: string; nodeID: string; iterations: number },
  { ok: boolean }
>({
  name: "runs.extend",
  request: ({ workflowID, nodeID, iterations }) => ({
    method: "POST",
    path: `/api/runs/${encodeURIComponent(workflowID)}/extend`,
    body: { node_id: nodeID, iterations },
  }),
  error: errorAbout(
    ({ workflowID }) => onRun(workflowID),
    (_args, err) => serverSentence(err) ?? "Couldn't add iterations",
  ),
  onSuccess: (_res, { workflowID }) => {
    invalidateRunControls(workflowID);
  },
});

export const finishRunRepeat = apiAction<{ workflowID: string; nodeID: string }, { ok: boolean }>({
  name: "runs.finish_loop",
  request: ({ workflowID, nodeID }) => ({
    method: "POST",
    path: `/api/runs/${encodeURIComponent(workflowID)}/finish-loop`,
    body: { node_id: nodeID },
  }),
  error: errorAbout(
    ({ workflowID }) => onRun(workflowID),
    (_args, err) => serverSentence(err) ?? "Couldn't end the loop",
  ),
  onSuccess: (_res, { workflowID }) => {
    invalidateRunControls(workflowID);
  },
});

/** Delete a run and its on-disk state, from any status (KAS cancels a live run first). The one
 *  run verb that cannot be undone, so the caller confirms first (`modals.ts`). */
export const deleteRun = apiAction<string, { ok: boolean }>({
  name: "runs.delete",
  request: (workflowID) => ({
    method: "DELETE",
    path: `/api/runs/${encodeURIComponent(workflowID)}`,
  }),
  error: errorAbout(onRun, "Could not delete the run"),
});
