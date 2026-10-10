// The one owner of a workflow run's state: refetched on invalidation, cached as read but for each
// node's start (`attemptStart`), never accumulated from an SSE payload.

import { signal, touch, type Signal } from "@cplieger/reactive";
import { apiGetOrError, apiGetTyped } from "./api-client.js";
import { nodePathKey } from "./run-node-key.js";
import { forgetSubject, observeStamp } from "./subject-versions.js";
import type { Entry, OpenEntry, TurnState } from "./types.js";
import { decodeLiveRunsResponse, decodeRunControlsResponse } from "./wire/decoders.gen.js";
import type {
  ConnectedPayload,
  LiveRun,
  RunControlsResponse,
  RunStepEnd,
  RunStepStart,
} from "./wire/types.gen.js";
import {
  classifyRunNodeStatus,
  classifyRunStatus,
  runStatusActive,
  type ClassifiedRunNodeStatus,
  type ClassifiedRunStatus,
} from "./run-status.js";

/** One node of KAS's execution tree, from `state.root`. */
export interface RunNode {
  nodeId: string;
  /** Open: KAS adds node types between releases (`run-exec-source.ts` `kindOf` maps one it does not
   *  know onto a container). */
  type: string;
  status: ClassifiedRunNodeStatus;
  agentName?: string;
  modelId?: string;
  effortLevel?: string;
  sessionId?: string;
  /** When the node's current attempt began: the run log's where it holds the current one
   *  (`attemptStart`), the earliest of its own and its children's for a container it holds none
   *  for, else KAS's own stamp, which a resume moves. */
  startedAt?: string;
  endedAt?: string;
  iteration?: number;
  branchId?: string;
  children?: RunNode[];
  artifacts?: Record<string, string>;
  capturedOutput?: string;
  watchTerminal?: boolean;
  failureReason?: string;
  completionSignal?: "success" | "need_input" | "error";
  continuationAttempts?: number;
}

/** A run's whole state. `runLabel` is the name a launcher gave this execution and `workflowName`
 *  the recipe's; `stopInitiator`/`stopReason` are set only when a person stopped it, which is
 *  what separates "the user cancelled this" from "it failed" in the card's alert. */
export interface RunState {
  workflowId: string;
  workflowName?: string;
  runLabel?: string;
  status?: ClassifiedRunStatus;
  inputs?: Record<string, string>;
  artifacts?: Record<string, string>;
  capturedOutputs?: Record<string, string>;
  root?: RunNode;
  pauseReason?: string;
  pauseDetail?: { class?: string; code?: string; occurredAt?: string };
  stopInitiator?: string;
  stopReason?: string;
  /** A pause asked for that lands at the end of the current step. */
  pausePending?: { initiator?: string; reason?: string; requestedAt?: string };
  parentSessionId?: string;
}

/** The alert a person's own stop or pause reads, or undefined when no person stopped the run.
 *  The reason is set only when the cause was not the button. */
export function userStopSentence(state: RunState): string | undefined {
  if (state.stopInitiator !== "user") {
    return undefined;
  }
  const lead =
    state.status === "completed"
      ? "Marked complete by you"
      : state.status === "paused"
        ? "Paused by you"
        : "Stopped by you";
  const why = state.stopReason ?? "";
  return why === "" ? lead : `${lead}: ${why}`;
}

/** The alert for a pause that has been asked for and not landed yet. */
export function pausePendingSentence(state: RunState): string | undefined {
  return state.pausePending !== undefined && state.status === "running"
    ? "Pausing after the current step"
    : undefined;
}

export interface RunInspect {
  workflowId: string;
  state?: RunState;
  /** KAS's node PLAN, forwarded verbatim. `unknown` because the client walks it structurally, so
   *  typing it would re-model a structure marotte does not own; `run-exec-source.ts` narrows it
   *  at the point of use. */
  nodePlan?: unknown;
}

interface RawRunNode extends Omit<RunNode, "status" | "children"> {
  status: string;
  children?: RawRunNode[];
}

interface RawRunState extends Omit<RunState, "status" | "root"> {
  status?: string;
  root?: RawRunNode;
}

interface RawRunInspect extends Omit<RunInspect, "state"> {
  state?: RawRunState;
  /** marotte's own key beside KAS's reply: the steps whose newest turn in the run's log closed broken. */
  step_ends?: Record<string, RunStepEnd>;
  /** marotte's own key beside KAS's reply: when each node's current attempt began in the run's log. */
  step_starts?: Record<string, RunStepStart>;
}

function classifyRunNode(node: RawRunNode): RunNode {
  const { status, children, ...rest } = node;
  const out: RunNode = { ...rest, status: classifyRunNodeStatus(status) };
  if (children !== undefined) {
    out.children = children.map(classifyRunNode);
  }
  return out;
}

function classifyRunState(state: RawRunState, starts: StepStarts): RunState {
  const { status, root, ...rest } = state;
  const out: RunState = { ...rest };
  const classified = classifyRunStatus(status);
  if (classified !== undefined) {
    out.status = classified;
  }
  if (root !== undefined) {
    out.root = withAttemptStarts(classifyRunNode(root), undefined, [], starts);
  }
  return out;
}

/** The run log's attempt starts by node path, the server's `workflow.PathKey`. */
type StepStarts = ReadonlyMap<string, RunStepStart>;

/** `node`'s subtree with each start its attempt's (`RunNode.startedAt`). */
function withAttemptStarts(
  node: RunNode,
  parent: RunNode | undefined,
  trail: readonly string[],
  starts: StepStarts,
): RunNode {
  const path = [...trail, nodePathSegment(node, parent)];
  const out: RunNode = { ...node };
  const log = starts.get(nodePathKey(path));
  let startedAt = attemptStart(node.startedAt, node.status, log);
  if (node.children !== undefined) {
    const kids = node.children.map((k) => withAttemptStarts(k, node, path, starts));
    out.children = kids;
    // A repeat's pass opens no turn, yet KAS restamps it on a resume; a container cannot start after its
    // first child, whose start the log keeps.
    if (log === undefined && startedAt !== undefined) {
      for (const k of kids) {
        startedAt = earlier(startedAt, k.startedAt);
      }
    }
  }
  if (startedAt !== undefined) {
    out.startedAt = startedAt;
  }
  return out;
}

/** The earlier stamp, compared as instants: KAS's and the log's RFC 3339 spellings differ in their fraction. */
function earlier(a: string, b: string | undefined): string {
  return b !== undefined && Date.parse(b) < Date.parse(a) ? b : a;
}

/** When a node's current attempt began. KAS restamps `startedAt` each time it re-enters a node, a resume
 *  included, so its stamp alone restarts every clock at zero; the run log's turn_open does not move. KAS's
 *  stamp stands only where the log holds no attempt, or an ENDED one under a node KAS shows in flight, which
 *  is a new attempt (a retry) the log has not opened yet. Paused time counts, as in the run's own window
 *  (`exec-view/model.ts` `window`): KAS records no pause spans. */
function attemptStart(
  kas: string | undefined,
  status: ClassifiedRunNodeStatus,
  log: RunStepStart | undefined,
): string | undefined {
  if (kas === undefined || log === undefined) {
    return kas;
  }
  return log.ended && nodeInFlight(status) ? kas : log.started_at;
}

function nodeInFlight(status: ClassifiedRunNodeStatus): boolean {
  switch (status) {
    case "pending":
    case "running":
    case "paused":
    case "unknown":
      return true;
    case "completed":
    case "failed":
    case "aborted":
    case "skipped":
      return false;
  }
}

/** Per-run signals, created on demand. A signal per run rather than one version counter so a
 *  card re-renders for its OWN run only: a workspace running four scheduled workflows would
 *  otherwise repaint every card on every frame of any of them. */
const cells = new Map<string, Signal<RunState | undefined>>();

/** Runs with a fetch in flight, and runs invalidated while one was. Together they collapse an
 *  event storm into at most two requests: KAS emits a `run_progress` per node event, and the
 *  state that matters is the one AFTER the last of them. `stale` carries the CAUSE the trailing
 *  fetch will run under, so the token below survives a coalesce. */
const inFlight = new Set<string>();
const stale = new Map<string, string>();

/** The invalidation CAUSE behind each run's current answer: recorded when its fetch is ISSUED
 *  and kept once that fetch has answered, which is what makes one cause cost one request per run
 *  however the two callers interleave — a second invalidation naming a cause this run was
 *  already READ for is a no-op, because that read was issued after the cause. */
const answeredCause = new Map<string, string>();

/** The ladder behind a run read that produced nothing: three attempts at 1s doubling, one per
 *  workflow id. Bounded because a failed read is not always transient — `handleRun` answers 503
 *  for `workflow.ErrUnknownMethod`, an engine with no workflow support at all, which no number
 *  of attempts can talk into describing a run. */
const RUN_RETRY_LIMIT = 3;
const RUN_RETRY_BASE_MS = 1000;

/** The status a read gets for a run the server can describe no further, and the one failure the
 *  ladder is skipped for outright. */
const RUN_GONE_STATUS = 404;

interface RunRetry {
  timer: ReturnType<typeof setTimeout> | undefined;
  attempts: number;
}

const runRetries = new Map<string, RunRetry>();

/** Per-run node plans, beside the signal rather than inside it. */
const plans = new Map<string, unknown>();

/** Per-run step ends by node path, beside the signal for the plan's reason. */
const stepEnds = new Map<string, ReadonlyMap<string, RunStepEnd>>();

/** Per-run attempt starts by node path, replaced by each read and moved by each landed frame (`frameAttempt`). */
const stepStarts = new Map<string, StepStarts>();

function cell(workflowID: string): Signal<RunState | undefined> {
  let c = cells.get(workflowID);
  if (c === undefined) {
    c = signal<RunState | undefined>(undefined);
    cells.set(workflowID, c);
  }
  return c;
}

/** Subscribe to a run's state. `undefined` until the first fetch resolves, which is what a
 *  caller renders a loading row for. */
export function runState(workflowID: string): RunState | undefined {
  return cell(workflowID).value;
}

/** Read a run's state WITHOUT subscribing. For a caller that must not re-run when the run
 *  changes. */
export function peekRunState(workflowID: string): RunState | undefined {
  return cells.get(workflowID)?.peek();
}

/** Apply a `run_progress` frame to the cached tree, and report whether it landed. `false` means
 *  the caller must refetch, and there are exactly three reasons for it: the frame names no node
 *  (`loop_iteration` and `steps_queued` change the tree's SHAPE, `paused` is run-level with its
 *  reason on `inspect` alone), the run is not cached at all (nothing to patch — a client that
 *  missed the start), or the path addresses a node this tree does not hold yet (a step inside a
 *  freshly-created iteration container). */
export function applyRunProgress(p: RunProgressFrame): boolean {
  if (p.workflow_id === "" || p.node_path === undefined || p.node_path.length === 0) {
    return false;
  }
  const c = cells.get(p.workflow_id);
  const root = c?.peek()?.root;
  if (c === undefined || root === undefined) {
    return false;
  }
  const key = nodePathKey(p.node_path);
  const starts = stepStarts.get(p.workflow_id);
  const log = starts?.get(key);
  const attempt = frameAttempt(log, nonEmpty(p.started_at), nonEmpty(p.ended_at));
  const next = patchNode(root, undefined, p.node_path, p, attempt);
  if (next === undefined) {
    return false;
  }
  if (attempt !== log && attempt !== undefined) {
    stepStarts.set(p.workflow_id, new Map(starts).set(key, attempt));
  }
  if (next === root) {
    // The frame addressed a node this tree holds and moved nothing about it: a watch poll
    // re-stating `running`, or a duplicate frame across a resume.
    return true;
  }
  const state = c.peek();
  if (state === undefined) {
    return false;
  }
  c.value = { ...state, root: next };
  return true;
}

/** The fields of a `run_progress` payload this store reads. Declared here rather than imported
 *  from the generated type so the store's contract is the four fields it applies, and a test can
 *  hand it a literal. */
export interface RunProgressFrame {
  workflow_id: string;
  node_path?: readonly string[];
  status?: string;
  started_at?: string;
  ended_at?: string;
  failure_reason?: string;
}

/** Rebuild `node`'s subtree with the addressed descendant patched, or `undefined` when this tree
 *  does not hold it. */
function patchNode(
  node: RunNode,
  parent: RunNode | undefined,
  trail: readonly string[],
  p: RunProgressFrame,
  attempt: RunStepStart | undefined,
): RunNode | undefined {
  const [head, ...rest] = trail;
  if (head === undefined || nodePathSegment(node, parent) !== head) {
    return undefined;
  }
  if (rest.length === 0) {
    return patchedLeaf(node, p, attempt);
  }
  const kids = node.children;
  if (kids === undefined) {
    return undefined;
  }
  for (const [i, k] of kids.entries()) {
    const patched = patchNode(k, node, rest, p, attempt);
    if (patched === k) {
      // Found, and unchanged. Rebuilding the spine over an identical child would hand
      // `applyRunProgress` a new root for a tree that did not move.
      return node;
    }
    if (patched !== undefined) {
      return { ...node, children: kids.with(i, patched) };
    }
  }
  return undefined;
}

/** The path's attempt once a frame lands, as the server's run log folds its turns: a start opens a new attempt
 *  unless the current one has not ended (a resume, or a duplicate frame), and an end stamp ends it whatever
 *  status word KAS sent beside it. Kept current per frame because a landed frame triggers no refetch, and a
 *  retry read against a stale open attempt would carry the previous attempt's start. */
function frameAttempt(
  log: RunStepStart | undefined,
  framedStart: string | undefined,
  framedEnd: string | undefined,
): RunStepStart | undefined {
  if (framedStart !== undefined && (log === undefined || log.ended)) {
    return { started_at: framedStart, ended: false };
  }
  if (log !== undefined && !log.ended && framedEnd !== undefined) {
    return { ...log, ended: true };
  }
  return log;
}

/** The addressed node with the frame's fields written over it, or the SAME node when the frame
 *  moves none of them. Every field is set only when the frame carries it, because a frame states
 *  what changed: `node_complete` carries no `started_at` and must not lose the one node_start
 *  left. A frame's start is the server's arrival stamp, so it yields to the attempt's. */
function patchedLeaf(
  node: RunNode,
  p: RunProgressFrame,
  attempt: RunStepStart | undefined,
): RunNode {
  const status = nodeStatus(p.status);
  const framed = nonEmpty(p.started_at);
  const startedAt = framed === undefined ? undefined : (attempt?.started_at ?? framed);
  const endedAt = nonEmpty(p.ended_at);
  const failureReason = nonEmpty(p.failure_reason);
  const moved =
    (status !== undefined && status !== node.status) ||
    (startedAt !== undefined && startedAt !== node.startedAt) ||
    (endedAt !== undefined && endedAt !== node.endedAt) ||
    (failureReason !== undefined && failureReason !== node.failureReason);
  if (!moved) {
    return node;
  }
  return {
    ...node,
    ...(status === undefined ? {} : { status }),
    ...(startedAt === undefined ? {} : { startedAt }),
    ...(endedAt === undefined ? {} : { endedAt }),
    ...(failureReason === undefined ? {} : { failureReason }),
  };
}

/** The value, or `undefined` for absent and for the empty string — which is what an omitted
 *  `omitempty` string field decodes to and means "unchanged", never "clear this". */
function nonEmpty(v: string | undefined): string | undefined {
  return v === undefined || v === "" ? undefined : v;
}

/** KAS's NodeState status words, as the tree spells them. The frame carries the status as a
 *  plain string — it is forwarded from KAS rather than enumerated server-side — so this is where
 *  it is narrowed. */
const NODE_STATUSES = [
  "pending",
  "running",
  "paused",
  "completed",
  "failed",
  "aborted",
  "skipped",
] as const;

/** The frame's status, or `undefined` for absent, empty, or a word this client does not know. */
function nodeStatus(v: string | undefined): RunNode["status"] | undefined {
  return NODE_STATUSES.find((s) => s === v);
}

/** Re-read a run from the server. Safe to call on every SSE frame: a second call while a fetch
 *  is in flight sets a flag rather than issuing a request, and one trailing fetch runs when the
 *  first settles. */
export function invalidateRun(workflowID: string, cause = ""): void {
  if (workflowID === "") {
    return;
  }
  if (cause !== "" && answeredCause.get(workflowID) === cause) {
    return;
  }
  if (inFlight.has(workflowID)) {
    stale.set(workflowID, cause);
    return;
  }
  void fetchRun(workflowID, cause);
}

/** Re-read every run this client holds state for. The gap-recovery half of the push contract:
 *  `run_progress` frames are APPLIED rather than refetched, so an outage that swallows them
 *  leaves a node reading `running` with its clock ticking and nothing to notice it. */
export function invalidateCachedRuns(cause = ""): void {
  for (const id of cells.keys()) {
    invalidateRun(id, cause);
  }
}

async function fetchRun(workflowID: string, cause = ""): Promise<void> {
  inFlight.add(workflowID);
  // Recorded at ISSUE, which is what lets ONE rule in `invalidateRun` serve both cases: a
  // same-cause invalidation arriving while this read is open is already covered.
  if (cause !== "") {
    answeredCause.set(workflowID, cause);
  }
  let answered = false;
  // The status of a read that produced nothing, which is what tells a SETTLED answer from the
  // absence of one: `handleRun` answers 404 only where the engine described this run and refused,
  // so no number of retries can change it. 0 (no request) and every other status are worth
  // re-asking.
  let failed = 0;
  try {
    const r = await apiGetOrError<RawRunInspect>(`/api/runs/${encodeURIComponent(workflowID)}`);
    const d = r.data;
    if (d?.state !== undefined) {
      // The plan and the step ends BEFORE the state, because the state assignment is what wakes
      // every reader: a subscriber that re-rendered between them would draw a repeat's bound from
      // the previous plan.
      if (d.nodePlan === undefined) {
        plans.delete(workflowID);
      } else {
        plans.set(workflowID, d.nodePlan);
      }
      stepEnds.set(workflowID, new Map(Object.entries(d.step_ends ?? {})));
      const starts = new Map(Object.entries(d.step_starts ?? {}));
      stepStarts.set(workflowID, starts);
      cell(workflowID).value = classifyRunState(d.state, starts);
      answered = true;
    } else {
      failed = r.status;
    }
  } finally {
    inFlight.delete(workflowID);
    if (answered) {
      cancelRunRetry(workflowID);
    } else {
      if (cause !== "") {
        // The read produced nothing, so it claims nothing: a cause standing over an answer nobody
        // got would turn the gap's own recovery into a no-op.
        answeredCause.delete(workflowID);
      }
      if (failed === RUN_GONE_STATUS) {
        // CANCELLED rather than merely not armed: a rung an earlier transient armed is still due,
        // and the answer it would collect is this one.
        cancelRunRetry(workflowID);
      } else {
        scheduleRunRetry(workflowID, cause, failed);
      }
    }
  }
  const next = stale.get(workflowID);
  if (next !== undefined) {
    stale.delete(workflowID);
    await fetchRun(workflowID, next);
  }
}

/** Re-ask for a run whose read produced nothing, bounded. */
function scheduleRunRetry(workflowID: string, cause: string, status: number): void {
  // The CONTINUATION as well as the door: its one caller is that `finally`, reached by the first
  // failed read and by every rung's, so the count is KEPT rather than reset, or the ladder would
  // have no end.
  const ladder = runRetries.get(workflowID) ?? { timer: undefined, attempts: 0 };
  if (ladder.attempts >= RUN_RETRY_LIMIT) {
    // No toast: a background chat's run is invisible either way, so a failure to re-read it earns a
    // log line rather than an overlay over whatever the reader is doing.
    console.warn(
      `[run] gave up re-reading ${workflowID} after ${String(RUN_RETRY_LIMIT)} retries (last status ${String(status)}); its card keeps what it last showed`,
    );
    runRetries.delete(workflowID);
    return;
  }
  if (ladder.timer !== undefined) {
    // A trailing fetch can fail while a rung is already armed. The newest failure owns the rung,
    // and the count it inherits is what keeps the pair inside the same three.
    clearTimeout(ladder.timer);
  }
  const delay = RUN_RETRY_BASE_MS * 2 ** ladder.attempts;
  ladder.attempts++;
  ladder.timer = setTimeout(() => {
    ladder.timer = undefined;
    invalidateRun(workflowID, cause);
  }, delay);
  runRetries.set(workflowID, ladder);
}

/** Forget a run's ladder, rungs and all: a read that ANSWERED has nothing left to retry, a read
 *  the server SETTLED has nothing left to learn, and a forgotten run has nothing left to read. */
function cancelRunRetry(workflowID: string): void {
  const timer = runRetries.get(workflowID)?.timer;
  if (timer !== undefined) {
    clearTimeout(timer);
  }
  runRetries.delete(workflowID);
}

/** Externally-owned reasons a run's state cell must be KEPT, registered by the composition root
 *  so this module stays a leaf — importing `tabs.ts` here would invert the dependency direction,
 *  which is why `store.ts`'s eviction exemptions take the same shape. Nothing registered means
 *  nothing demands a cell. */
const stateDemands: ((workflowID: string) => boolean)[] = [];

/** Register one demand predicate. Returns its unregister. */
export function registerRunStateDemand(fn: (workflowID: string) => boolean): () => void {
  stateDemands.push(fn);
  return () => {
    const i = stateDemands.indexOf(fn);
    if (i >= 0) {
      stateDemands.splice(i, 1);
    }
  };
}

/** The name a lifecycle frame carried, kept SYNCHRONOUSLY because a notice for the run can
 *  arrive before its state fetch settles. Fetched state outranks it. */
const frameLabels = new Map<string, string>();

export function noteRunLabel(workflowID: string, name: string | undefined): void {
  if (workflowID !== "" && name !== undefined && name !== "") {
    frameLabels.set(workflowID, name);
  }
}

/** Forget a run's cached state — the cache's ONLY bound, held back by a REGISTERED DEMAND: any
 *  predicate answering true keeps everything below, because no call site can enumerate this
 *  store's readers. */
export function forgetRun(workflowID: string): void {
  if (stateDemands.some((fn) => fn(workflowID))) {
    return;
  }
  cells.delete(workflowID);
  stale.delete(workflowID);
  answeredCause.delete(workflowID);
  cancelRunRetry(workflowID);
  plans.delete(workflowID);
  stepEnds.delete(workflowID);
  stepStarts.delete(workflowID);
  controlCells.delete(workflowID);
  controlsInFlight.delete(workflowID);
  controlsStale.delete(workflowID);
  launchedBy.delete(workflowID);
  frameLabels.delete(workflowID);
  runLogs.delete(workflowID);
  logVersions.delete(workflowID);
}

/** What this run is CALLED, or `""` when nothing has been fetched for it yet: the launcher's
 *  label for this execution first, the recipe's name second. UNTRACKED, like `runPlan`. */
export function runLabelOf(workflowID: string): string {
  const state = peekRunState(workflowID);
  const label = state?.runLabel ?? "";
  const fetched = label === "" ? (state?.workflowName ?? "") : label;
  return fetched !== "" ? fetched : (frameLabels.get(workflowID) ?? "");
}

// What may be done to a run: `GET /api/runs/{id}/controls`.

const controlCells = new Map<string, Signal<RunControlsResponse | undefined>>();
const controlsInFlight = new Set<string>();
const controlsStale = new Set<string>();

function controlCell(workflowID: string): Signal<RunControlsResponse | undefined> {
  let c = controlCells.get(workflowID);
  if (c === undefined) {
    c = signal<RunControlsResponse | undefined>(undefined);
    controlCells.set(workflowID, c);
  }
  return c;
}

/** Subscribe to what a run offers. */
export function runControls(workflowID: string): RunControlsResponse | undefined {
  return controlCell(workflowID).value;
}

/** Re-read what a run offers. THREE triggers, never one per repaint: a tab open (`run-view.ts`),
 *  that run's own `run_finished` (`handlers/run.ts`), and a retry that succeeded
 *  (`actions/runs.ts`). */
export function invalidateRunControls(workflowID: string): void {
  if (workflowID === "") {
    return;
  }
  if (controlsInFlight.has(workflowID)) {
    controlsStale.add(workflowID);
    return;
  }
  void fetchRunControls(workflowID);
}

async function fetchRunControls(workflowID: string): Promise<void> {
  // Claimed HERE rather than by the caller, so the trailing call below re-arms the guard with no
  // window a coincident invalidation could slip a third request into.
  controlsInFlight.add(workflowID);
  try {
    const d = await apiGetTyped(
      `/api/runs/${encodeURIComponent(workflowID)}/controls`,
      decodeRunControlsResponse,
    );
    if (d !== null) {
      controlCell(workflowID).value = d;
    }
  } finally {
    controlsInFlight.delete(workflowID);
  }
  if (controlsStale.delete(workflowID)) {
    await fetchRunControls(workflowID);
  }
}

/** A run's node plan, read WITHOUT subscribing. Untracked deliberately: every reader hands it to
 *  `runToExec` inside a pass the state signal already woke, so a tracked read would add a
 *  dependency that can never fire independently. */
export function runPlan(workflowID: string): unknown {
  return plans.get(workflowID);
}

/** A run's step ends by node path, read WITHOUT subscribing, for `runPlan`'s reason. */
export function runStepEnds(workflowID: string): ReadonlyMap<string, RunStepEnd> {
  return stepEnds.get(workflowID) ?? new Map();
}

/** Which chat's agent launched a run, learned from the SSE envelope, and empty for a parentless
 *  run. */
const launchedBy = new Map<string, string>();

/** A parentless run's own surface, and NOT a chat id. Its lifecycle frames arrive with an EMPTY
 *  envelope chat id, but its ASKS are keyed to this synthetic value, because the dock queues per
 *  chat and a card with no key reaches no host. */
const RUN_CHAT_PREFIX = "run:";

export function noteRunChat(workflowID: string, chatID: string): void {
  // The synthetic key is rejected HERE rather than at each caller, because "which chat launched
  // this run" is this module's own question: recording it would nest the run's tab under a
  // conversation that does not exist, and the caller that reads it (`runChatID`) cannot tell a real
  // id from a synthetic one afterwards.
  if (workflowID === "" || chatID === "" || chatID.startsWith(RUN_CHAT_PREFIX)) {
    return;
  }
  launchedBy.set(workflowID, chatID);
}

export function runChatID(workflowID: string): string {
  return launchedBy.get(workflowID) ?? "";
}

// The live-runs inventory: which chats have a run in flight.

/** One live run: the chat that launched it ("" for a parentless run), and whether it is still
 *  EXECUTING as opposed to parked. `executing` is read by the chat row's workflow mark as its
 *  floor for a run whose fetched cell has not arrived (`chat-run-dots.ts`); every other reader
 *  takes the whole row. */
export interface LiveRunRow {
  readonly chat: string;
  readonly executing: boolean;
}

/** A row handed OUT, carrying the workflow id the map holds it under. The id is the map's KEY
 *  rather than a field of the row, so a reader given the row alone cannot name the run it
 *  describes. */
export interface LiveRunEntry extends LiveRunRow {
  readonly id: string;
}

const liveRunChats = new Map<string, LiveRunRow>();

/** Bumped by every writer of the map above, so a reactive reader can subscribe to the inventory
 *  CHANGING. A plain `Map` is not a signal, so a `computed` over it would track nothing and
 *  never re-evaluate. */
const liveRunsVersion = signal(0);

/** `peek()` on the write is the idiom `run-dots.ts` records: a `+ 1` off `.value` subscribes the
 *  writing effect to the signal it is about to write, which the reactive layer refuses with
 *  `Cycle detected`. */
function bumpLiveRuns(): void {
  liveRunsVersion.value = liveRunsVersion.peek() + 1;
}

/** Record a run as live, saying whether it is executing. Parentless runs ("" chat) are tracked
 *  too: they exempt no chat, but their presence mirrors the server's inventory, which is what
 *  the dot painter reads. */
export function noteRunLive(workflowID: string, chatID: string, executing: boolean): void {
  if (workflowID === "") {
    return;
  }
  liveRunChats.set(workflowID, { chat: chatID, executing });
  bumpLiveRuns();
}

/** Drop a run that reached a terminal status. */
export function noteRunSettled(workflowID: string): void {
  liveRunChats.delete(workflowID);
  bumpLiveRuns();
}

/** Whether this chat has ANY live run, parked ones included — a parked run's ask is precisely
 *  the one that must survive, so narrowing this to `executing` would strand it. Its consumer is
 *  the ask sweep in `handlers/run.ts`. */
export function hasLiveRunForChat(chatID: string): boolean {
  return anyRunForChat(chatID, () => true);
}

/** The live runs this chat launched, in the order they were recorded. Parked runs are INCLUDED
 *  like `hasLiveRunForChat`; parentless ones are EXCLUDED, their own tab dot already surfacing
 *  them (`run-dots.ts`). */
export function liveRunsForChat(chatID: string): LiveRunEntry[] {
  touch(liveRunsVersion);
  if (chatID === "") {
    return [];
  }
  const out: LiveRunEntry[] = [];
  for (const [id, r] of liveRunChats) {
    if (r.chat === chatID) {
      out.push({ id, ...r });
    }
  }
  return out;
}

/** This run's live row WITHOUT subscribing, or `undefined` when nothing holds it live.
 *  `peekRunState`'s twin, and for its reason: a `forgetRun` demand predicate runs outside any
 *  effect of its own — sometimes inside another module's — so a tracked read there would hand
 *  that effect a dependency on the whole inventory. */
export function peekLiveRun(workflowID: string): LiveRunRow | undefined {
  return liveRunChats.get(workflowID);
}

/** The ids alone, for a caller that asks only how many or which. */
export function liveRunIDsForChat(chatID: string): string[] {
  return liveRunsForChat(chatID).map((r) => r.id);
}

/** The scan both readers share. Not an index: the single-run rule bounds live runs to a handful,
 *  and a second map keyed by chat would be one more thing the rebuild could leave inconsistent. */
function anyRunForChat(chatID: string, pass: (r: LiveRunRow) => boolean): boolean {
  if (chatID === "") {
    return false;
  }
  for (const r of liveRunChats.values()) {
    if (r.chat === chatID && pass(r)) {
      return true;
    }
  }
  return false;
}

/** Externally-owned "this client now knows about this run", REGISTERED rather than imported
 *  because `run-dots.ts` imports this module and the reverse edge would close a cycle.
 *  Unregistered, the rebuild seeds state and repaints nothing. */
let noteRunKnown: ((workflowID: string) => void) | null = null;

/** Register the observer the rebuild reports each live run to. Last wins. */
export function registerLiveRunObserver(fn: (workflowID: string) => void): void {
  noteRunKnown = fn;
}

/** Rebuild the inventory from the server. A FAILED fetch keeps the event-fed state: a stale
 *  exemption costs memory, a wrongly-evicted live chat costs correctness, and the next gap or
 *  boot retries. */
export async function rebuildLiveRuns(cause = "", signal?: AbortSignal): Promise<void> {
  const d = await apiGetTyped("/api/runs/live", decodeLiveRunsResponse, signal);
  if (d === null) {
    return;
  }
  adoptLiveRuns(d.runs, cause);
  // AFTER the adoption: the `runs` digest stamp certifies the inventory now held.
  observeStamp(d.subject);
}

/** Adopt an inventory somebody else already read. */
export function adoptLiveRuns(rows: readonly LiveRun[], cause?: string): void {
  liveRunChats.clear();
  for (const r of rows) {
    if (r.workflow_id !== "") {
      liveRunChats.set(r.workflow_id, { chat: r.chat_id, executing: r.executing });
      noteRunChat(r.workflow_id, r.chat_id);
      noteRunKnown?.(r.workflow_id);
      if (cause !== undefined) {
        invalidateRun(r.workflow_id, cause);
      }
    }
  }
  // ONE bump for the whole adoption rather than one per row.
  bumpLiveRuns();
}

/** Take the inventory off the connect handshake, and fall back to the fetch when the frame does
 *  not state one. */
export function adoptConnectRuns(p: ConnectedPayload): void {
  if (!p.live_runs_stated) {
    void rebuildLiveRuns("connect");
    return;
  }
  adoptLiveRuns(p.live_runs ?? []);
}

// A run owns its record, so its steps' entries arrive as the six entry events with an EMPTY chat id
// and land here rather than in a chat's transcript. `store.ts` owns POSITION and every hole rule
// for a chat's log and this owns them for a run's.

/** One run's log: the turns by id, in the order their steps opened, plus the turns a `seq` gap
 *  or a lost `turn_opened` left incomplete. */
interface RunLog {
  turns: Map<string, TurnState>;
  order: string[];
  holes: Set<string>;
}

const runLogs = new Map<string, RunLog>();

/** Per-run log versions, beside the state cells rather than inside them: a step's entries and
 *  the run's tree move on different clocks, so a reader of one must not repaint for the other. */
const logVersions = new Map<string, Signal<number>>();

function logCell(workflowID: string): Signal<number> {
  let c = logVersions.get(workflowID);
  if (c === undefined) {
    c = signal(0);
    logVersions.set(workflowID, c);
  }
  return c;
}

function log(workflowID: string): RunLog {
  let l = runLogs.get(workflowID);
  if (l === undefined) {
    l = { turns: new Map(), order: [], holes: new Set() };
    runLogs.set(workflowID, l);
  }
  return l;
}

function bumpLog(workflowID: string): void {
  const c = logCell(workflowID);
  c.value = c.peek() + 1;
}

/** Subscribe to a run's log: the turns it holds in the order they opened. The pane reads the
 *  turn whose `turn_open.node_path` is the step it renders. */
export function runTurns(workflowID: string): readonly [string, TurnState][] {
  touch(logCell(workflowID));
  const l = runLogs.get(workflowID);
  if (l === undefined) {
    return [];
  }
  return l.order.flatMap((id) => {
    const t = l.turns.get(id);
    return t === undefined ? [] : [[id, t] as [string, TurnState]];
  });
}

/** The turns this run's log is missing entries for. The pane re-reads such a turn through the
 *  step GET, which serves the whole turn; the digest's own repair is the range read
 *  (`run-turn-range.ts`), and both clear the marker through `clearRunHole`. */
export function runTurnHoles(workflowID: string): readonly string[] {
  touch(logCell(workflowID));
  return [...(runLogs.get(workflowID)?.holes ?? [])];
}

/** The newest `seq` this client holds for one step turn, which is what a `run_turn` repair asks
 *  past. `entries[i].seq === i` is the log's invariant, so the length answers it; a turn the
 *  store does not hold answers `undefined`, which asks for the WHOLE turn. */
export function runTurnHeldSeq(workflowID: string, turnID: string): number | undefined {
  const state = runLogs.get(workflowID)?.turns.get(turnID);
  return state === undefined || state.entries.length === 0 ? undefined : state.entries.length - 1;
}

/** Retire a hole the reader's own repair closed. The chat store's twin restores residency when
 *  no repair is left out; here the range read and the pane's step GET are the repairs, so the
 *  reader that adopts that answer is what says the turn is whole again. */
export function clearRunHole(workflowID: string, turnID: string): void {
  const l = runLogs.get(workflowID);
  if (!l?.holes.delete(turnID)) {
    return;
  }
  bumpLog(workflowID);
}

/** The `run_turn` digest ref for one open step turn. The server spells it `<workflowID>/<turn>`
 *  (`internal/subject`), split on the FIRST separator. */
function runTurnRef(workflowID: string, turnID: string): string {
  return `${workflowID}/${turnID}`;
}

/** Record where this client stands on an OPEN step turn. The version is spelled `<turn>:<newest
 *  sealed seq>`, which is what the server answers for both turn kinds (`internal/agent`
 *  `turnVersion`), so a held stamp can be current. */
function stampRunTurn(workflowID: string, turnID: string, state: TurnState): void {
  if (state.closeAt !== undefined) {
    return;
  }
  observeStamp({
    kind: "run_turn",
    ref: runTurnRef(workflowID, turnID),
    version: `${turnID}:${String(state.entries.length - 1)}`,
  });
}

/** The range read a hole needs, injected because this module never fetches this route:
 *  `run-turn-range.ts` owns the read and this module owns the detection, which is the seam
 *  `store.ts` and `store-load.ts` already use for the chat's twin. */
let repairRunTurn: ((workflowID: string, turnID: string, afterSeq?: number) => void) | undefined;

export function registerRunTurnRepair(
  fn: (workflowID: string, turnID: string, afterSeq?: number) => void,
): void {
  repairRunTurn = fn;
}

/** Mark a turn this client cannot complete from the stream, and ask for the repair. */
function markRunHole(workflowID: string, turnID: string): void {
  log(workflowID).holes.add(turnID);
  repairRunTurn?.(workflowID, turnID, runTurnHeldSeq(workflowID, turnID));
}

/** Create the turn a run-scoped `turn_opened` announced, with its `turn_open` as `entries[0]`.
 *  Idempotent by turn id, like the chat store's. */
export function openRunTurn(workflowID: string, entry: Entry): void {
  const l = log(workflowID);
  if (l.turns.has(entry.turn)) {
    return;
  }
  const state: TurnState = { entries: [entry], openEntries: new Map() };
  l.turns.set(entry.turn, state);
  l.order.push(entry.turn);
  stampRunTurn(workflowID, entry.turn, state);
  bumpLog(workflowID);
}

/** Append one sealed entry of a run's log at the position its `seq` claims. Same three answers
 *  as the chat store: the next `seq` appends, a `seq` already held under the same id is a
 *  redelivery and is dropped, anything else is a hole. */
export function appendRunEntry(workflowID: string, entry: Entry): void {
  const l = log(workflowID);
  const state = l.turns.get(entry.turn);
  if (state === undefined) {
    markRunHole(workflowID, entry.turn);
    bumpLog(workflowID);
    return;
  }
  if (entry.seq !== state.entries.length) {
    if (state.entries[entry.seq]?.id === entry.id) {
      return;
    }
    markRunHole(workflowID, entry.turn);
    bumpLog(workflowID);
    return;
  }
  state.entries.push(entry);
  if (entry.kind === "turn_close") {
    state.closeAt = entry.seq;
    // The step's turn is settled, so its ref no longer exists: dropping the stamp is what stops the
    // digest naming a turn nothing will append to again.
    forgetSubject("run_turn", runTurnRef(workflowID, entry.turn));
  } else {
    stampRunTurn(workflowID, entry.turn, state);
  }
  bumpLog(workflowID);
}

/** Seat a lane's open entry of a run's turn. */
export function openRunEntry(workflowID: string, open: OpenEntry): void {
  const state = runLogs.get(workflowID)?.turns.get(open.turn);
  if (state === undefined) {
    markRunHole(workflowID, open.turn);
    bumpLog(workflowID);
    return;
  }
  const lane = open.lane ?? "";
  state.openEntries.set(lane, { ...open, lane });
  bumpLog(workflowID);
}

/** Replace one turn's open tails with the ones a WHOLE-TURN read answered, which is
 *  authoritative about them: a tail whose entry has since sealed is absent from it, and seating
 *  without replacing leaves that stale tail beside the sealed entry it became for as long as the
 *  pane lives, because no later frame addresses its lane. */
export function adoptRunOpenEntries(
  workflowID: string,
  turnID: string,
  open: readonly OpenEntry[],
): void {
  const state = runLogs.get(workflowID)?.turns.get(turnID);
  if (state === undefined) {
    return;
  }
  state.openEntries = new Map();
  for (const o of open) {
    if (o.turn !== turnID) {
      continue;
    }
    const lane = o.lane ?? "";
    state.openEntries.set(lane, { ...o, lane });
  }
  bumpLog(workflowID);
}

/** Extend a lane's open entry by one delta. `n` is the running count AFTER the delta, so `open.n
 *  + 1` is the only admissible value. */
export function applyRunDelta(
  workflowID: string,
  turnID: string,
  entryID: string,
  lane: string,
  n: number,
  delta: string,
): void {
  const state = runLogs.get(workflowID)?.turns.get(turnID);
  if (state === undefined) {
    markRunHole(workflowID, turnID);
    bumpLog(workflowID);
    return;
  }
  const open = state.openEntries.get(lane);
  if (open?.id !== entryID || n !== open.n + 1) {
    markRunHole(workflowID, turnID);
    bumpLog(workflowID);
    return;
  }
  state.openEntries.set(lane, { ...open, text: open.text + delta, n });
  bumpLog(workflowID);
}

/** Seal a lane's open entry: build the `Entry` from the open state and hand it to
 *  `appendRunEntry`, so the hole check applies to the seal's own `seq`. */
export function sealRunEntry(
  workflowID: string,
  turnID: string,
  entryID: string,
  lane: string,
  seq: number,
  ts: number,
  n: number,
): void {
  const state = runLogs.get(workflowID)?.turns.get(turnID);
  if (state === undefined) {
    markRunHole(workflowID, turnID);
    bumpLog(workflowID);
    return;
  }
  const open = state.openEntries.get(lane);
  if (open?.id !== entryID || n !== open.n) {
    markRunHole(workflowID, turnID);
    bumpLog(workflowID);
    return;
  }
  state.openEntries.delete(lane);
  appendRunEntry(workflowID, {
    id: open.id,
    turn: turnID,
    lane,
    kind: open.kind,
    payload: { text: open.text },
    seq,
    ts,
  });
}

// Derived reads: functions over the cached value, never stored beside it — a second copy of "how
// many steps finished" is a second thing that can be wrong.

/** A repeat child carrying no `iteration` falls back to its `nodeId`: a row in the wrong place
 *  beats content that vanishes, the same call the server's own `runNodePath` makes when a frame
 *  carries no path. */
export function nodePathSegment(node: RunNode, parent: RunNode | undefined): string {
  if (parent?.type === "repeat" && node.iteration !== undefined) {
    return `iter-${String(node.iteration)}`;
  }
  return node.nodeId;
}

/** Whether a run is still this process's to finish. Drives the elapsed clock and the card's
 *  open-by-default state. `paused` counts as live: it is stopped waiting for something, not
 *  over. */
export function runIsLive(state: RunState | undefined): boolean {
  const status = state?.status;
  return status === undefined ? false : runStatusActive(status);
}

/** Whether a pause REASON means a step is waiting on a person — the reason half of
 *  `isNeedInputPark`, which is the question every surface asks. */
export function isNeedInputPause(reason: string | undefined): boolean {
  if (reason === undefined || reason === "") {
    return false;
  }
  if (reason === "Step requested user input via send_message.") {
    return true;
  }
  return (
    reason.startsWith("Step '") &&
    (reason.endsWith("' is waiting for user input.") ||
      reason.endsWith("' is waiting for the next user message."))
  );
}

/** The paused node whose own completion signal says it is waiting on a person. Depth-first,
 *  first match wins. */
function needInputNode(n: RunNode | undefined): RunNode | undefined {
  if (n === undefined) {
    return undefined;
  }
  if (n.status === "paused" && n.completionSignal === "need_input") {
    return n;
  }
  for (const child of n.children ?? []) {
    const hit = needInputNode(child);
    if (hit !== undefined) {
      return hit;
    }
  }
  return undefined;
}

/** Whether a run is parked on a PERSON — the one pause a reader has to act on. TWO ARMS, because
 *  neither answers alone: the run's own pause reason, which is what a plain step's park writes,
 *  and a paused node's completion signal, which is the only thing left of a park that happened
 *  inside a parallel branch. Gated on `paused`, like the dot vocabulary's own arm: a reason or a
 *  signal outliving its pause must never paint a finished run as awaiting input. */
export function isNeedInputPark(state: RunState | undefined): boolean {
  if (state?.status !== "paused") {
    return false;
  }
  return isNeedInputPause(state.pauseReason) || needInputNode(state.root) !== undefined;
}

/** Only a class whose `pauseReason` does NOT already name its cause earns a label.
 *  `continuation-exhausted`'s reason states the attempt count itself, so labelling it would
 *  stutter; it renders its code bare. */
const PAUSE_CLASS_LABEL: Readonly<Record<string, string>> = {
  "transient-error": "after a transient error",
};

/** `class` is arbitrary wire text, and a bare index read on an object literal answers
 *  `Object.prototype`'s member for `constructor`/`toString`/… — which would render a function's
 *  source into the run card's alert. Own membership is the question. */
function pauseClassLabel(cls: string): string | undefined {
  return Object.hasOwn(PAUSE_CLASS_LABEL, cls) ? PAUSE_CLASS_LABEL[cls] : undefined;
}

/** The pause's machine detail as one phrase, or undefined when there is none. Upstream 2.21.1
 *  made `pauseDetail.class` a two-member enum, and both render sites had folded the single
 *  member into prose — so an exhausted continuation budget read as "a transient error", which it
 *  is not and which points the reader at the wrong next action. */
export function pauseDetailPhrase(detail: RunState["pauseDetail"]): string | undefined {
  const code = detail?.code;
  if (code === undefined || code === "") {
    return undefined;
  }
  const cls = detail?.class;
  const label = pauseClassLabel(cls === undefined || cls === "" ? "transient-error" : cls);
  return label === undefined ? `(${code})` : `${label} (${code})`;
}
