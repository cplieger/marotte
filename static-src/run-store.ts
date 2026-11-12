// The one owner of a workflow run's state: refetched on invalidation, cached as read but for each
// node's start (`attemptStart`), never accumulated from an SSE payload.

import { signal, touch, type Signal } from "@cplieger/reactive";
import { apiGetOrError, apiGetTyped } from "./api-client.js";
import { nodePathKey, nodePathSegment } from "./run-node-key.js";
import { forgetSubject, observeStamp } from "./subject-versions.js";
import { turnOpenOf, type TurnSource } from "./turns.js";
import { settleWorkflowMessages, turnsInOrder } from "./workflow-delivery.js";
import { forgetStepTails, seedStepTails } from "./run-step-tail.js";
import {
  deltaIsRedelivery,
  mergeOpenTails,
  openIsRedelivery,
  sealIsRedelivery,
} from "./open-tails.js";
import type { Entry, OpenEntry, TurnState } from "./types.js";
import {
  decodeLiveRunsResponse,
  decodeRunControlsResponse,
  decodeRunPlanUpdate,
} from "./wire/decoders.gen.js";
import type {
  ConnectedPayload,
  LiveRun,
  RunControlsResponse,
  RunPlanUpdate,
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
  /** On a repeat an extension grew: its new pass budget in total, replacing the plan's
   *  `maxIterations`. */
  extendedMaxIterations?: number;
  branchId?: string;
  children?: RunNode[];
  artifacts?: Record<string, string>;
  /** A watch's whole capture. A step's comes only from its capture read (`requestStepCapture`): the
   *  run read carries its first lines as `captureSummary`, and `hasCapture` says it captured. */
  capturedOutput?: string;
  captureSummary?: string;
  hasCapture?: boolean;
  watchTerminal?: boolean;
  failureReason?: string;
  /** This store's, from the live frames KAS persists nothing for: why the step paused, and why a
   *  running step is waiting to retry. A start clears both. */
  pauseReason?: string;
  retryReason?: string;
  completionSignal?: "success" | "need_input" | "error";
  continuationAttempts?: number;
  /** KAS's credit-unit total for the step's session, recorded when the step ends; absent when KAS
   *  recorded none. */
  credits?: number;
  /** On a repeat in a glance read: what it ran before its latest pass, which the glance leaves out. */
  earlierPasses?: EarlierPasses;
  /** This store's, not KAS's: orders this node's current execution against every other of every
   *  run. A later execution carries a larger value, and an execution keeps its value however each
   *  source re-reports it. Absent on a node with no start. */
  startGen?: number;
}

/** A glance's fold of a repeat's passes before its latest (`workflow.EarlierPasses`): how many, their
 *  credits, and their work's window in Unix milliseconds (`from` absent when none started; `open`
 *  for a started step with no end). */
export interface EarlierPasses {
  count: number;
  credits: number;
  from?: number;
  to: number;
  open: boolean;
}

/** A run's whole state. `runLabel` is the name a launcher gave this execution, `workflowName` the
 *  recipe's, and `displayName` the server's name for the run (`display_name`), the one a surface
 *  shows; `stopInitiator`/`stopReason` are KAS's attribution of a requested stop or pause
 *  (a person's, an orchestrator's or marotte's sign-in pause) and are unset when the run failed,
 *  which is what separates a requested stop from "it failed" in the card's alert. */
export interface RunState {
  workflowId: string;
  workflowName?: string;
  runLabel?: string;
  displayName?: string;
  status?: ClassifiedRunStatus;
  inputs?: Record<string, string>;
  artifacts?: Record<string, string>;
  capturedOutputs?: Record<string, string>;
  root?: RunNode;
  pauseReason?: string;
  pauseDetail?: { class?: string; code?: string; occurredAt?: string };
  /** KAS's `state.pause`: which loop a cap pause stopped at, its budget and the passes it ran. */
  pause?: { kind?: string; nodeId?: string; maxIterations?: number; iterationsRun?: number };
  stopInitiator?: string;
  stopReason?: string;
  /** A pause asked for that has not landed: KAS is cancelling the in-flight turn. */
  pausePending?: { initiator?: string; reason?: string; requestedAt?: string };
  parentSessionId?: string;
  /** marotte's, from the reply's `plan_update`: the run's newest queued plan revision. */
  planUpdate?: RunPlanUpdate;
  /** marotte's, from the reply's `read_position`: the run's read position, 0 while it is live. Only
   *  a read sets it; a `run_progress` frame keeps the previous read's value. */
  readPosition?: number;
  /** marotte's, from the reply's `auto_resume_stopped_at`: when marotte stopped resuming this paused
   *  run on its own. KAS's own state never says so, since marotte writes no pause for it. */
  autoResumeStoppedAt?: string;
}

/** The reasons marotte pauses a run with (internal/agent/run_gate.go, pinned by
 *  internal/agent/testdata/pause_reasons.json): the sign-in gate's two. KAS attributes every
 *  reasoned pause to the user, so these are the pauses no person asked for. */
const MAROTTE_PAUSE_REASONS: ReadonlySet<string> = new Set([
  "sign in to Kiro to continue; marotte resumes the run when you do",
  "the Kiro sign-in service is unreachable; marotte resumes the run when it answers",
]);

/** The reason marotte stamps beside `user` on a run control its REST API took from outside its
 *  page, such as an agent's curl (internal/agent/run_host.go apiCallerReason, same fixture). */
const API_CALLER_REASON = "asked through marotte's run API by a caller outside its page";

/** KAS's reasons on the pause and the resume a run's owning parent session asks for, which KAS
 *  attributes to `user` and its own classifier names the orchestrator's. Pinned to the bundle by
 *  internal/kascap/parent_control_reason_census_test.go through testdata/parent_control_reasons.json. */
const PARENT_PAUSE_REASON =
  "Pause requested by owning parent/orchestrator; human intent not verified.";
const PARENT_RESUME_REASON =
  "Resume requested by owning parent/orchestrator; human intent not verified.";

/** What an attributed control did to the run. */
type ControlVerb = "paused" | "stopped" | "completed" | "resumed";

const CONTROL_LEAD: Record<ControlVerb, string> = {
  paused: "Paused",
  stopped: "Stopped",
  completed: "Marked complete",
  resumed: "Resumed",
};

/** The one sentence every surface words a KAS attribution with, or undefined when it names no
 *  actor. The parent chat's controls and marotte's REST controls all arrive as `user`, so under
 *  `user` the reason says who asked, and a reason that names an actor is never shown. The
 *  parent's reason is the run's chat whatever the verb (its stop carries no attribution, so it
 *  keeps the pause's). The API reason names an agent,
 *  marotte's own pause reasons name nobody, and no reason is the reader's gesture on its page. */
function attributedSentence(
  verb: ControlVerb,
  initiator: string,
  reason: string,
): string | undefined {
  if (initiator === "") {
    return undefined;
  }
  const lead = CONTROL_LEAD[verb];
  const parent = reason === PARENT_PAUSE_REASON || reason === PARENT_RESUME_REASON;
  if (initiator !== "user") {
    const shown = parent || reason === API_CALLER_REASON ? "" : reason;
    return shown === "" ? `${lead} by ${initiator}` : `${lead} by ${initiator}: ${shown}`;
  }
  if (parent) {
    return `${lead} by its chat`;
  }
  if (reason === API_CALLER_REASON) {
    return `${lead} by an agent`;
  }
  if (MAROTTE_PAUSE_REASONS.has(reason)) {
    return verb === "paused" ? `Paused: ${reason}` : undefined;
  }
  return reason === "" ? `${lead} by you` : `${lead} by you: ${reason}`;
}

/** Whether a paused run was parked by a person, an agent or its parent chat, and not by marotte's sign-in hold, which
 *  marotte resumes itself. It says nothing of whether a resume would hold: `runControls` does. */
export function runPausedByPerson(state: RunState): boolean {
  return (
    state.status === "paused" &&
    state.stopInitiator === "user" &&
    !MAROTTE_PAUSE_REASONS.has(state.stopReason ?? "")
  );
}

/** Whether KAS paused the run itself on a transient fault (a rate limit, a 5xx, a dropped connection): no initiator, a
 *  transient-error class. */
export function runPausedByTransientError(state: RunState): boolean {
  const detail = state.pauseDetail;
  return (
    state.status === "paused" &&
    (state.stopInitiator ?? "") === "" &&
    detail?.code !== undefined &&
    detail.code !== "" &&
    (detail.class === undefined || detail.class === "" || detail.class === "transient-error")
  );
}

/** The alert an attributed stop or pause reads, or undefined when KAS names no initiator. */
export function stopSentence(state: RunState): string | undefined {
  const verb: ControlVerb =
    state.status === "completed" ? "completed" : state.status === "paused" ? "paused" : "stopped";
  return attributedSentence(verb, state.stopInitiator ?? "", state.stopReason ?? "");
}

/** The alert of a paused run marotte stopped resuming on its own, or undefined for any other run. A
 *  throttle is named, since it is the cause a person can wait out. */
export function autoResumeStopSentence(state: RunState): string | undefined {
  if (state.status !== "paused" || state.autoResumeStoppedAt === undefined) {
    return undefined;
  }
  const throttled = /throttl/i.test(state.pauseDetail?.code ?? "");
  return `marotte stopped resuming this run after repeated ${throttled ? "model throttling" : "pauses"}`;
}

/** KAS's attribution on each run's newest resume: `run_start` carries it and no read does. */
const resumedBy = new Map<string, { initiator: string; reason: string }>();

/** Record a `run_start`'s attribution; a start naming no initiator (a launch) clears it. */
export function noteRunResumed(workflowID: string, initiator: string, reason: string): void {
  if (initiator === "") {
    resumedBy.delete(workflowID);
    return;
  }
  resumedBy.set(workflowID, { initiator, reason });
}

/** The alert a running run's attributed resume reads. */
export function resumeSentence(state: RunState): string | undefined {
  const r = resumedBy.get(state.workflowId);
  if (r === undefined || state.status !== "running") {
    return undefined;
  }
  return attributedSentence("resumed", r.initiator, r.reason);
}

/** The alert for a pause that has been asked for and not landed yet. */
export function pausePendingSentence(state: RunState): string | undefined {
  return state.pausePending !== undefined && state.status === "running"
    ? "Pausing: stopping the current turn"
    : undefined;
}

interface RunInspect {
  /** KAS's node PLAN, forwarded verbatim. `unknown` because the client walks it structurally, so
   *  typing it would re-model a structure marotte does not own; `run-exec-source.ts` narrows it
   *  at the point of use. */
  nodePlan?: unknown;
}

interface RawRunNode extends Omit<RunNode, "status" | "children" | "credits" | "earlierPasses"> {
  earlierPasses?: unknown;
  status: string;
  children?: RawRunNode[];
  /** KAS's `[{usage, unit?, unitPlural?}]`, read through `creditTotal`. */
  credits?: unknown;
}

interface RawRunState extends Omit<RunState, "status" | "root" | "displayName"> {
  status?: string;
  root?: RawRunNode;
}

interface RawRunInspect extends Omit<RunInspect, "state"> {
  state?: RawRunState;
  plan_update?: unknown;
  /** marotte's own key beside KAS's reply: the steps whose newest turn in the run's log closed broken. */
  step_ends?: Record<string, RunStepEnd>;
  /** marotte's own key beside KAS's reply: when each node's current attempt began in the run's log. */
  step_starts?: Record<string, RunStepStart>;
  read_position?: unknown;
  /** marotte's own key beside KAS's reply: when marotte stopped resuming the run on its own. */
  auto_resume_stopped_at?: unknown;
  /** marotte's own key beside KAS's reply: the run's display name, "" when nothing names it. */
  display_name?: unknown;
  /** marotte's own key beside KAS's reply: what may be done to the run, decided from this read's state. */
  controls?: unknown;
  /** marotte's own key beside KAS's reply: the read's revision, which `?since` reads from. */
  rev?: unknown;
}

/** One changed node of a `?since` answer: its own members (no `children`), its parent's path ("" for
 *  the root) and its index among the parent's children. Every ancestor of a changed node comes too. */
interface SinceRecord {
  path: string;
  parent: string;
  index: number;
  node: RawRunNode;
}

/** `GET /api/runs/{id}?since=<rev>`: the run read's extras, the head members and nodes that changed
 *  since `rev`, and what is gone. `nodePlan` is present only when it changed (null: removed). */
interface RawRunSince extends Omit<RawRunInspect, "state" | "nodePlan"> {
  head?: Record<string, unknown>;
  head_removed?: string[];
  /** Each a `SinceRecord`, read through `sinceRecordOf`. */
  nodes: unknown[];
  removed?: string[];
  nodePlan?: unknown;
  step_ends_removed?: string[];
  step_starts_removed?: string[];
}

/** The reply's read position, 0 for none or a value that is not a non-negative safe integer. */
function readPositionOf(raw: unknown): number {
  return typeof raw === "number" && Number.isSafeInteger(raw) && raw >= 0 ? raw : 0;
}

/** The reply's verbs, or undefined for none or an answer this build cannot read, which draws no verb. */
function controlsOf(raw: unknown): RunControlsResponse | undefined {
  if (raw === undefined) {
    return undefined;
  }
  try {
    return decodeRunControlsResponse(raw);
  } catch {
    return undefined;
  }
}

/** The reply's plan revision, or undefined for none or one this build cannot read. */
function planUpdateOf(raw: unknown): RunPlanUpdate | undefined {
  if (raw === undefined) {
    return undefined;
  }
  try {
    return decodeRunPlanUpdate(raw);
  } catch {
    return undefined;
  }
}

function classifyRunNode(node: RawRunNode): RunNode {
  const { status, children, credits, earlierPasses, ...rest } = node;
  const out: RunNode = { ...rest, status: classifyRunNodeStatus(status) };
  const total = creditTotal(credits);
  if (total !== undefined) {
    out.credits = total;
  }
  const earlier = earlierPassesOf(earlierPasses);
  if (earlier !== undefined) {
    out.earlierPasses = earlier;
  }
  if (children !== undefined) {
    out.children = children.map(classifyRunNode);
  }
  return out;
}

/** Whether a glance left any pass out: one that folded none is the whole tree. */
function foldsPasses(node: RawRunNode | undefined): boolean {
  return (
    node !== undefined &&
    (node.earlierPasses !== undefined || (node.children ?? []).some(foldsPasses))
  );
}

/** A glance's `earlierPasses`, or undefined for a value of another shape. */
function earlierPassesOf(raw: unknown): EarlierPasses | undefined {
  if (typeof raw !== "object" || raw === null) {
    return undefined;
  }
  const { count, credits, from, to, open } = raw as Record<string, unknown>;
  const ms = (v: unknown): number | undefined =>
    typeof v === "number" && Number.isSafeInteger(v) && v > 0 ? v : undefined;
  if (typeof count !== "number" || !Number.isSafeInteger(count) || count < 0) {
    return undefined;
  }
  const out: EarlierPasses = {
    count,
    credits: typeof credits === "number" && Number.isFinite(credits) && credits > 0 ? credits : 0,
    to: ms(to) ?? 0,
    open: open === true,
  };
  const start = ms(from);
  if (start !== undefined) {
    out.from = start;
  }
  return out;
}

/** The credit-unit sum of KAS's node `credits`, or undefined for no positive total. An element
 *  with another unit or a usage that is not a finite number counts for nothing, as on the server
 *  (`translate.creditsOf`). */
function creditTotal(raw: unknown): number | undefined {
  if (!Array.isArray(raw)) {
    return undefined;
  }
  let total = 0;
  for (const item of raw as unknown[]) {
    if (typeof item !== "object" || item === null) {
      continue;
    }
    const { usage, unit } = item as { usage?: unknown; unit?: unknown };
    if (
      typeof usage === "number" &&
      Number.isFinite(usage) &&
      (unit === undefined || unit === "credit")
    ) {
      total += usage;
    }
  }
  return total > 0 ? total : undefined;
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

/** A signal per run rather than one version counter so a card re-renders for its OWN run only: a
 *  workspace running four unattended workflows would otherwise repaint every card on every frame of
 *  any of them. */
const cells = new Map<string, Signal<RunState | undefined>>();

/** Together they collapse an event storm into at most two requests: KAS emits a `run_progress` per
 *  node event, and the state that matters is the one AFTER the last of them. */
const inFlight = new Map<string, RunShape>();
const stale = new Set<string>();

/** Per run, the revision of the held tree, which the next tree read asks `?since` unless a caller
 *  owes the run a whole read (`wholeOwed`); absent while no tree answer carrying one is held. */
const revs = new Map<string, number>();
const wholeOwed = new Set<string>();

/** Per run, the newest revision the held state reflects, from any answer or an applied `run_rev`. */
const heldRevs = new Map<string, number>();

/** Per run, the widest shape the held state is kept current in; absent is the header alone. A
 *  narrower read or frame landing on a wider body leaves that body stale, so it narrows this. */
const bodies = new Map<string, "glance" | "tree">();

/** What a reader draws of a run, narrowest first: its header (status, name, controls, asks), its
 *  glance (each repeat through its latest pass, which a run card and the run bar draw) or its
 *  whole tree (the run tab). Each is read in its own form of `GET /api/runs/{id}`. */
type RunShape = "header" | "glance" | "tree";

const SHAPE_RANK: Readonly<Record<RunShape, number>> = { header: 0, glance: 1, tree: 2 };

function wider(a: RunShape, b: RunShape): boolean {
  return SHAPE_RANK[a] > SHAPE_RANK[b];
}

/** The runs whose held glance left a pass out. */
const folded = new Set<string>();

/** Whether the held state lists every pass of every repeat: a tree, or a glance that folded none. */
export function runHoldsEveryPass(workflowID: string): boolean {
  const held = bodies.get(workflowID);
  return held === "tree" || (held === "glance" && !folded.has(workflowID));
}

function heldShape(workflowID: string): RunShape {
  return bodies.get(workflowID) ?? "header";
}

/** The shape a read of the run asks: the widest a reader demands (a header when none does), or a
 *  held tree that moves by `?since`, which costs what changed and never needs re-reading whole. */
function readShape(workflowID: string): RunShape {
  const wanted = demandedShape(workflowID) ?? "header";
  return wanted !== "tree" && sinceBase(workflowID) !== undefined ? "tree" : wanted;
}

function sinceBase(workflowID: string): number | undefined {
  return bodies.get(workflowID) === "tree" && cells.get(workflowID)?.peek() !== undefined
    ? revs.get(workflowID)
    : undefined;
}

/** Runs forgotten while their read was out; the read drops its answer and its retry, so a late
 *  response cannot rebuild the state `forgetRun` just released. */
const forgotten = new Set<string>();

/** The ladder behind a run read that produced nothing: three attempts at 1s doubling, one per
 *  workflow id. Bounded because a failed read does not always clear within a few seconds, such as
 *  the server's startup/shutdown 503 (`runtime-health.ts`). */
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

/** One path's current execution, identified by the step session both sources report: the second
 *  `node_start` announces it and the run read carries it. A changed session or an observed end
 *  makes a new execution; a repeated frame or a read of the same session keeps this one. */
interface Execution {
  gen: number;
  /** Absent until a source names it. Only a step leaf has one; a container never does. */
  session?: string;
  ended: boolean;
}

interface RunExecutions {
  readonly paths: Map<string, Execution>;
  /** Counts every frame that changed the cached tree. A read issued at an older count may predate
   *  one of them, so it is discarded and read again rather than written over the newer tree. */
  frames: number;
}

/** Per run, by frame path. Kept across reads, because executions are told apart across them. */
const executions = new Map<string, RunExecutions>();

/** Module-wide rather than per run, so a forgotten run read again still counts upward. */
let lastStartGen = 0;

/** Counts down from 0 for the starts in the passes a held glance folded, when the tree brings
 *  them: each began before its repeat's latest pass, so none may read as newer than a held start. */
let firstStartGen = 0;

function executionsOf(workflowID: string): RunExecutions {
  let run = executions.get(workflowID);
  if (run === undefined) {
    run = { paths: new Map(), frames: 0 };
    executions.set(workflowID, run);
  }
  return run;
}

/** Two sessions that are both known and differ: the one rule both sources use to tell executions
 *  apart. A missing side says nothing. */
function otherSession(known: string | undefined, seen: string | undefined): boolean {
  return known !== undefined && seen !== undefined && known !== seen;
}

/** Stamp a freshly read tree's executions. `glance` is the held glance this read widens, whose
 *  folded passes are history. */
function stampStarts(workflowID: string, root: RunNode, glance?: RunNode): void {
  /** Each glance node's key, true for a repeat that folded passes. */
  const glanceFolds = new Map<string, boolean>();
  const index = (n: RunNode, parent: RunNode | undefined, trail: readonly string[]): void => {
    const path = [...trail, nodePathSegment(n, parent)];
    glanceFolds.set(nodePathKey(path), n.earlierPasses !== undefined);
    for (const k of n.children ?? []) {
      index(k, n, path);
    }
  };
  if (glance !== undefined) {
    index(glance, undefined, []);
  }
  const read: [RunNode, string][] = [];
  const history: [RunNode, string][] = [];
  const walk = (n: RunNode, path: readonly string[], key: string, folded: boolean): void => {
    (folded ? history : read).push([n, key]);
    const folds = glanceFolds.get(key) === true;
    for (const k of n.children ?? []) {
      const childPath = [...path, nodePathSegment(k, n)];
      const childKey = nodePathKey(childPath);
      walk(k, childPath, childKey, folded || (folds && !glanceFolds.has(childKey)));
    }
  };
  const rootPath = [nodePathSegment(root, undefined)];
  walk(root, rootPath, nodePathKey(rootPath), false);
  stampNodes(workflowID, read, history);
}

/** Stamp the executions of freshly read nodes, each with its path key. A path showing another
 *  session, or a live start after an end, holds a new execution. KAS's persisted time is read only
 *  to rank several new executions this one read found; `history`'s are ranked below every other
 *  start (`firstStartGen`). */
function stampNodes(
  workflowID: string,
  read: readonly [RunNode, string][],
  history: readonly [RunNode, string][] = [],
): void {
  const run = executionsOf(workflowID);
  const past = freshOf(run, history);
  past.sort(byStart);
  for (const f of past.toReversed()) {
    firstStartGen--;
    holdExecution(run, f, firstStartGen);
  }
  const fresh = freshOf(run, read);
  fresh.sort(byStart);
  for (const f of fresh) {
    lastStartGen++;
    holdExecution(run, f, lastStartGen);
  }
}

interface FreshStart {
  node: RunNode;
  path: string;
  at: string;
  session: string | undefined;
}

/** Stable, so equal times keep plan order. */
function byStart(a: FreshStart, b: FreshStart): number {
  return Date.parse(a.at) - Date.parse(b.at) || 0;
}

/** The starts of `read` that hold a new execution; the others keep theirs, stamped in place. */
function freshOf(run: RunExecutions, read: readonly [RunNode, string][]): FreshStart[] {
  const fresh: FreshStart[] = [];
  for (const [n, key] of read) {
    const at = nonEmpty(n.startedAt);
    const ex = run.paths.get(key);
    if (at === undefined) {
      // A node back to `pending` is between executions, so its next start is a new one.
      if (ex !== undefined) {
        ex.ended = true;
      }
    } else {
      const live = holdsLiveStart(n);
      const session = nonEmpty(n.sessionId);
      if (ex === undefined || (ex.ended && live) || otherSession(ex.session, session)) {
        fresh.push({ node: n, path: key, at, session });
      } else {
        if (session !== undefined) {
          ex.session ??= session;
        }
        ex.ended = !live;
        n.startGen = ex.gen;
      }
    }
  }
  return fresh;
}

function holdExecution(run: RunExecutions, f: FreshStart, gen: number): void {
  run.paths.set(f.path, {
    gen,
    ...(f.session === undefined ? {} : { session: f.session }),
    ended: !holdsLiveStart(f.node),
  });
  f.node.startGen = gen;
}

/** The held glance's root when a `shape` read widens a glance that folded passes. */
function widenedGlance(workflowID: string, shape: RunShape): RunNode | undefined {
  return shape === "tree" && bodies.get(workflowID) === "glance" && folded.has(workflowID)
    ? peekRunState(workflowID)?.root
    : undefined;
}

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
 *  caller renders a loading row for. Its `root` may be a glance or stale: a reader that draws
 *  nodes reads `runStateAs`. */
export function runState(workflowID: string): RunState | undefined {
  return cell(workflowID).value;
}

/** `runState` for a reader that draws the run's nodes in `shape`: `undefined` while what is held is
 *  narrower, which the reader shows as loading. */
export function runStateAs(workflowID: string, shape: RunShape): RunState | undefined {
  const state = cell(workflowID).value;
  return state === undefined || wider(shape, heldShape(workflowID)) ? undefined : state;
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
  const run = executionsOf(p.workflow_id);
  const ex = run.paths.get(key);
  const session = nonEmpty(p.session_id);
  // KAS sends `node_start` twice per step, the second naming the session, and a read can land
  // before the frame it races, so a start opens an execution only where the path holds none, it
  // ended, or it ran another session.
  const opens =
    nonEmpty(p.started_at) !== undefined &&
    (ex === undefined || ex.ended || otherSession(ex.session, session));
  const leaf = (node: RunNode): RunNode => {
    const out = patchedLeaf(node, p, opens, attempt);
    if (opens) {
      lastStartGen++;
      run.paths.set(key, {
        gen: lastStartGen,
        ...(session === undefined ? {} : { session }),
        ended: !holdsLiveStart(out),
      });
      out.startGen = lastStartGen;
    } else if (ex !== undefined) {
      if (session !== undefined) {
        ex.session ??= session;
      }
      if (!holdsLiveStart(out)) {
        ex.ended = true;
      }
    }
    return out;
  };
  const next = patchNode(root, undefined, p.node_path, leaf);
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
  run.frames++;
  c.value = { ...state, root: next };
  return true;
}

/** The fields of a `run_progress` payload this store reads. Declared here rather than imported
 *  from the generated type so the store's contract is the fields it applies, and a test can
 *  hand it a literal. */
export interface RunProgressFrame {
  workflow_id: string;
  node_path?: readonly string[];
  status?: string;
  started_at?: string;
  ended_at?: string;
  failure_reason?: string;
  pause_reason?: string;
  retry_reason?: string;
  session_id?: string;
  /** node_complete's step credit total; absent on every other frame. */
  credits?: number;
}

/** Rebuild `node`'s subtree with the addressed descendant patched, or `undefined` when this tree
 *  does not hold it. */
function patchNode(
  node: RunNode,
  parent: RunNode | undefined,
  trail: readonly string[],
  leaf: (node: RunNode) => RunNode,
): RunNode | undefined {
  const [head, ...rest] = trail;
  if (head === undefined || nodePathSegment(node, parent) !== head) {
    return undefined;
  }
  if (rest.length === 0) {
    return leaf(node);
  }
  const kids = node.children;
  if (kids === undefined) {
    return undefined;
  }
  for (const [i, k] of kids.entries()) {
    const patched = patchNode(k, node, rest, leaf);
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
 *  left. `opens` says the frame's `started_at` begins a new execution, whose start is the attempt's: the
 *  frame's is the server's arrival stamp. */
function patchedLeaf(
  node: RunNode,
  p: RunProgressFrame,
  opens: boolean,
  attempt: RunStepStart | undefined,
): RunNode {
  const status = nodeStatus(p.status);
  const framed = opens ? nonEmpty(p.started_at) : undefined;
  const startedAt = framed === undefined ? undefined : (attempt?.started_at ?? framed);
  const endedAt = nonEmpty(p.ended_at);
  const failureReason = nonEmpty(p.failure_reason);
  const sessionId = nonEmpty(p.session_id);
  const pauseReason = nonEmpty(p.pause_reason);
  const retryReason = nonEmpty(p.retry_reason);
  const credits = p.credits !== undefined && p.credits > 0 ? p.credits : undefined;
  // A frame that is not a retry wait ends one: KAS re-sends node_start after the wait.
  const retryEnds = retryReason === undefined && node.retryReason !== undefined;
  const moved =
    (status !== undefined && status !== node.status) ||
    startedAt !== undefined ||
    (endedAt !== undefined && endedAt !== node.endedAt) ||
    (failureReason !== undefined && failureReason !== node.failureReason) ||
    (sessionId !== undefined && sessionId !== node.sessionId) ||
    (pauseReason !== undefined && pauseReason !== node.pauseReason) ||
    (retryReason !== undefined && retryReason !== node.retryReason) ||
    (credits !== undefined && credits !== node.credits) ||
    retryEnds;
  if (!moved) {
    return node;
  }
  const out: RunNode = {
    ...node,
    ...(status === undefined ? {} : { status }),
    ...(endedAt === undefined ? {} : { endedAt }),
    ...(failureReason === undefined ? {} : { failureReason }),
    ...(sessionId === undefined ? {} : { sessionId }),
    ...(pauseReason === undefined ? {} : { pauseReason }),
    ...(retryReason === undefined ? {} : { retryReason }),
    ...(credits === undefined ? {} : { credits }),
  };
  if (retryEnds) {
    delete out.retryReason;
  }
  if (status !== undefined && status !== "paused") {
    delete out.pauseReason;
  }
  if (startedAt !== undefined) {
    out.startedAt = startedAt;
    // A new execution has not ended, whatever the previous one left, and runs no earlier session.
    if (endedAt === undefined) {
      delete out.endedAt;
    }
    if (failureReason === undefined) {
      delete out.failureReason;
    }
    if (sessionId === undefined) {
      delete out.sessionId;
    }
  }
  return out;
}

function holdsLiveStart(node: RunNode): boolean {
  return (
    nonEmpty(node.startedAt) !== undefined &&
    node.endedAt === undefined &&
    (node.status === "running" || node.status === "paused")
  );
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
export function invalidateRun(workflowID: string): void {
  if (workflowID === "") {
    return;
  }
  wholeOwed.add(workflowID);
  if (inFlight.has(workflowID)) {
    stale.add(workflowID);
    return;
  }
  void fetchRun(workflowID);
}

/** Whether this client holds a revision of the run's read, so the server's `run_rev` announces
 *  its every change and no frame needs a read of its own. */
export function runRevHeld(workflowID: string): boolean {
  return heldRevs.has(workflowID) && cells.get(workflowID)?.peek() !== undefined;
}

/** Bring a held run to `rev` (a `run_rev`, with the run's `status` there), or re-check it with no
 *  `rev`: a tree reads what changed since its revision, a glance reads afresh, and a header takes
 *  the frame's status with no read. A run held at no revision is left to its frames. Coalesced with
 *  any read out, as `invalidateRun`. */
export function reviseRun(workflowID: string, rev?: number, status?: string): void {
  const held = heldRevs.get(workflowID);
  if (!runRevHeld(workflowID) || held === undefined || (rev !== undefined && rev <= held)) {
    return;
  }
  if (rev !== undefined && readShape(workflowID) === "header") {
    applyRevStatus(workflowID, rev, status);
    return;
  }
  if (inFlight.has(workflowID)) {
    stale.add(workflowID);
    return;
  }
  void fetchRun(workflowID);
}

/** A header's move to `rev`: the status, which is all a header reader draws of a revision. A wider
 *  body the frame does not move goes stale. */
function applyRevStatus(workflowID: string, rev: number, status: string | undefined): void {
  const held = cells.get(workflowID)?.peek();
  if (held === undefined) {
    return;
  }
  heldRevs.set(workflowID, rev);
  bodies.delete(workflowID);
  revs.delete(workflowID);
  const next = classifyRunStatus(status);
  if (next !== undefined && next !== held.status) {
    cell(workflowID).value = { ...held, status: next };
  }
}

/** Read a run's current state unless a read of it is already out, which is that ask; one out in a
 *  narrower shape than its readers now demand gets a trailing read. */
export function readRunUnlessOut(workflowID: string): void {
  if (inFlight.has(workflowID)) {
    widenRun(workflowID);
  } else {
    invalidateRun(workflowID);
  }
}

/** Read the run when the state held, or the read out, is narrower than its readers now demand (or
 *  the read out will be dropped): a reader calls it when it starts drawing more of a run. Nothing
 *  held and nothing out is left to the reader's first read (`drawRun`). */
export function widenRun(workflowID: string): void {
  const want = readShape(workflowID);
  const out = inFlight.get(workflowID);
  if (out !== undefined) {
    // A read out for a forgotten run drops its answer, so a returning reader needs its own.
    if (wider(want, out) || forgotten.has(workflowID)) {
      stale.add(workflowID);
    }
    return;
  }
  if (cells.get(workflowID)?.peek() !== undefined && wider(want, heldShape(workflowID))) {
    void fetchRun(workflowID);
  }
}

/** A reader starts drawing the run: read it when nothing is held or out, else as `widenRun`. A held
 *  state is kept current in its shape, so a reader that needs no wider one reads nothing. */
export function drawRun(workflowID: string): void {
  if (workflowID === "") {
    return;
  }
  if (!inFlight.has(workflowID) && cells.get(workflowID)?.peek() === undefined) {
    void fetchRun(workflowID);
    return;
  }
  widenRun(workflowID);
}

/** The gap repair for everything a run's state applies from frames (`run_progress`, `node_complete`,
 *  a step's recorded grade in `step_ends`). Every reader registers a demand, so a run nobody holds
 *  is read by its next reader. A held run whose read is out gets one trailing read, since that read
 *  may predate the gap. */
export function rereadHeldRuns(): void {
  for (const id of cells.keys()) {
    if (!runStateDemanded(id)) {
      continue;
    }
    // A revision the server no longer holds answers whole.
    if (runRevHeld(id)) {
      reviseRun(id);
    } else {
      invalidateRun(id);
    }
  }
}

/** The URL of a read in `shape`; a tree read with a `since` revision asks only what changed. */
function readURL(workflowID: string, shape: RunShape, since: number | undefined): string {
  const url = `/api/runs/${encodeURIComponent(workflowID)}`;
  switch (shape) {
    case "header":
      return `${url}?view=header`;
    case "glance":
      return `${url}?view=glance`;
    case "tree":
      return since === undefined ? url : `${url}?since=${String(since)}`;
  }
}

async function fetchRun(workflowID: string): Promise<void> {
  const owed = wholeOwed.delete(workflowID);
  const shape = readShape(workflowID);
  inFlight.set(workflowID, shape);
  let answered = false;
  let dropped: boolean;
  // The status of a read that produced nothing, which is what tells a SETTLED answer from the
  // absence of one: `handleRun` answers 404 only where the engine described this run and refused,
  // so no number of retries can change it. 0 (no request) and every other status are worth
  // re-asking.
  let failed = 0;
  const issued = executionsOf(workflowID).frames;
  const since = shape === "tree" && !owed ? sinceBase(workflowID) : undefined;
  try {
    const r = await apiGetOrError<RawRunInspect | RawRunSince>(readURL(workflowID, shape, since));
    const changes = r.data !== null && isSinceAnswer(r.data) ? r.data : undefined;
    const d = r.data === null || isSinceAnswer(r.data) ? undefined : r.data;
    if (forgotten.has(workflowID)) {
      // The last reader left while this read was out: writing the answer would recreate a cell nobody holds.
    } else if (
      (changes !== undefined || d?.state !== undefined) &&
      executionsOf(workflowID).frames !== issued
    ) {
      // A frame changed the tree while this read was out, so the answer may predate it. Only a
      // read issued after the frame can say which side of it KAS is on.
      stale.add(workflowID);
      answered = true;
    } else if (changes !== undefined) {
      answered = applySince(workflowID, changes);
    } else if (d?.state !== undefined && shape === "header") {
      landHeader(workflowID, d.state, d);
      answered = true;
    } else if (d?.state !== undefined && shape !== "header") {
      // The plan, the step ends and the verbs BEFORE the state, because the state assignment is what
      // wakes every reader: a subscriber that re-rendered between them would draw a repeat's bound
      // from the previous plan, or the previous status's verbs beside this one.
      if (d.nodePlan === undefined) {
        plans.delete(workflowID);
      } else {
        plans.set(workflowID, d.nodePlan);
      }
      const starts = new Map(Object.entries(d.step_starts ?? {}));
      const state = classifyRunState(d.state, starts);
      withReadExtras(state, d);
      if (state.root !== undefined) {
        stampStarts(workflowID, state.root, widenedGlance(workflowID, shape));
      }
      stepEnds.set(workflowID, new Map(Object.entries(d.step_ends ?? {})));
      stepStarts.set(workflowID, starts);
      seedStepTails(workflowID, starts);
      bodies.set(workflowID, shape);
      if (shape === "glance" && foldsPasses(d.state.root)) {
        folded.add(workflowID);
      } else {
        folded.delete(workflowID);
      }
      holdRev(workflowID, shape === "tree" ? d.rev : undefined, d.rev);
      landRead(workflowID, state, d);
      answered = true;
    } else {
      failed = r.status;
    }
  } finally {
    inFlight.delete(workflowID);
    dropped = forgotten.delete(workflowID);
    if (dropped) {
      // Nothing to retry for a run no reader holds; `forgetRun` already cleared its ladder.
    } else if (answered) {
      cancelRunRetry(workflowID);
    } else {
      if (failed === RUN_GONE_STATUS) {
        // CANCELLED rather than merely not armed: a rung an earlier transient armed is still due,
        // and the answer it would collect is this one.
        cancelRunRetry(workflowID);
        goneCell(workflowID).value = true;
      } else {
        scheduleRunRetry(workflowID, failed);
      }
    }
  }
  if (dropped && !runStateDemanded(workflowID)) {
    // `stale` may come from a lifecycle invalidation, which fires whether or not anyone reads the run,
    // so only a registered demand lets the trailing read recreate the cell.
    stale.delete(workflowID);
  }
  if (stale.delete(workflowID)) {
    await fetchRun(workflowID);
  }
}

/** The state keys a read writes whole (the tree and `withReadExtras`'s), which a `?since` head
 *  never carries. */
const READ_KEYS = ["root", "planUpdate", "readPosition", "autoResumeStoppedAt", "displayName"];

/** The state members a header answer leaves out (the server's `headBulk`), kept from the held state. */
const HEADER_LEAVES: ReadonlySet<string> = new Set([
  "root",
  "capturedOutputs",
  "originalUserMessages",
  "inputs",
  "artifacts",
]);

/** Land a header answer: its head over the held state's tree and bulk, which go stale. */
function landHeader(workflowID: string, raw: RawRunState, d: RawRunInspect): void {
  const held = cells.get(workflowID)?.peek();
  const kept =
    held === undefined
      ? {}
      : Object.fromEntries(Object.entries(held).filter(([k]) => HEADER_LEAVES.has(k)));
  const state: RunState = { ...kept, ...classifyRunState(raw, new Map()) };
  withReadExtras(state, d);
  bodies.delete(workflowID);
  holdRev(workflowID, undefined, d.rev);
  landRead(workflowID, state, d);
}

function isSinceAnswer(d: RawRunInspect | RawRunSince): d is RawRunSince {
  return "nodes" in d && Array.isArray(d.nodes);
}

/** A `?since` record, or undefined for an element of another shape. */
function sinceRecordOf(v: unknown): SinceRecord | undefined {
  if (typeof v !== "object" || v === null) {
    return undefined;
  }
  const { path, parent, index, node } = v as Record<string, unknown>;
  if (typeof path !== "string" || typeof node !== "object" || node === null) {
    return undefined;
  }
  return {
    path,
    parent: typeof parent === "string" ? parent : "",
    index:
      typeof index === "number" && Number.isSafeInteger(index) ? index : Number.MAX_SAFE_INTEGER,
    node: node as RawRunNode,
  };
}

/** marotte's keys of a read, written onto its state. */
function withReadExtras(state: RunState, d: RawRunInspect | RawRunSince): void {
  const planUpdate = planUpdateOf(d.plan_update);
  if (planUpdate !== undefined) {
    state.planUpdate = planUpdate;
  }
  state.readPosition = readPositionOf(d.read_position);
  if (typeof d.auto_resume_stopped_at === "string" && d.auto_resume_stopped_at !== "") {
    state.autoResumeStoppedAt = d.auto_resume_stopped_at;
  }
  if (typeof d.display_name === "string" && d.display_name !== "") {
    state.displayName = d.display_name;
  }
}

/** Hold an answer's revisions: `tree` is the since base it leaves (none for a narrower answer),
 *  `held` the revision it reflects. */
function holdRev(workflowID: string, tree: unknown, held: unknown): void {
  const valid = (rev: unknown): rev is number =>
    typeof rev === "number" && Number.isSafeInteger(rev) && rev > 0;
  if (valid(tree)) {
    revs.set(workflowID, tree);
  } else {
    revs.delete(workflowID);
  }
  if (valid(held)) {
    heldRevs.set(workflowID, held);
  } else {
    heldRevs.delete(workflowID);
  }
}

/** Write an answered read: the verbs and the gone flag BEFORE the state, whose assignment wakes
 *  every reader. */
function landRead(workflowID: string, state: RunState, d: RawRunInspect | RawRunSince): void {
  controlCell(workflowID).value = controlsOf(d.controls);
  const gone = goneCells.get(workflowID);
  if (gone !== undefined) {
    gone.value = false;
  }
  cell(workflowID).value = state;
}

/** Apply a `?since` answer onto the held state in one write; false when no state is held to apply
 *  it to, which owes the run a whole read. */
function applySince(workflowID: string, d: RawRunSince): boolean {
  const held = cells.get(workflowID)?.peek();
  if (held === undefined) {
    wholeOwed.add(workflowID);
    stale.add(workflowID);
    return true;
  }
  if ("nodePlan" in d) {
    if (d.nodePlan === null || d.nodePlan === undefined) {
      plans.delete(workflowID);
    } else {
      plans.set(workflowID, d.nodePlan);
    }
  }
  const starts = mergedRows(stepStarts.get(workflowID), d.step_starts, d.step_starts_removed);
  const ends = mergedRows(stepEnds.get(workflowID), d.step_ends, d.step_ends_removed);
  const drop = new Set<string>([...READ_KEYS, ...(d.head_removed ?? [])]);
  const head = {
    ...Object.fromEntries(Object.entries(held).filter(([k]) => !drop.has(k))),
    ...d.head,
  };
  const state = classifyRunState(head as RawRunState, starts);
  withReadExtras(state, d);
  if (held.root !== undefined) {
    const rebuilt: [RunNode, string][] = [];
    state.root = applyRecords(held.root, d.nodes, new Set(d.removed ?? []), starts, rebuilt);
    stampNodes(workflowID, rebuilt);
  }
  stepEnds.set(workflowID, ends);
  stepStarts.set(workflowID, starts);
  seedStepTails(workflowID, starts);
  holdRev(workflowID, d.rev, d.rev);
  landRead(workflowID, state, d);
  return true;
}

function mergedRows<T>(
  held: ReadonlyMap<string, T> | undefined,
  changed: Record<string, T> | undefined,
  removed: readonly string[] | undefined,
): Map<string, T> {
  const out = new Map(held);
  for (const k of removed ?? []) {
    out.delete(k);
  }
  for (const [k, v] of Object.entries(changed ?? {})) {
    out.set(k, v);
  }
  return out;
}

/** The held tree with each record's node rebuilt in place, its kids kept by identity unless a record
 *  or `removed` names them, and new kids seated at their index. Only the records' nodes are walked:
 *  the server sends every ancestor of a changed node. `rebuilt` collects each rebuilt node with its
 *  path key, for `stampNodes`. */
function applyRecords(
  root: RunNode,
  records: readonly unknown[],
  removed: ReadonlySet<string>,
  starts: StepStarts,
  rebuilt: [RunNode, string][],
): RunNode {
  const byPath = new Map<string, SinceRecord>();
  const byParent = new Map<string, SinceRecord[]>();
  for (const v of records) {
    const r = sinceRecordOf(v);
    if (r === undefined) {
      continue;
    }
    byPath.set(r.path, r);
    const siblings = byParent.get(r.parent) ?? [];
    siblings.push(r);
    byParent.set(r.parent, siblings);
  }
  const rebuild = (
    rec: SinceRecord,
    held: RunNode | undefined,
    parent: RunNode | undefined,
    trail: readonly string[],
  ): RunNode => {
    const ownRaw = { ...rec.node };
    delete ownRaw.children;
    const own = classifyRunNode(ownRaw);
    const path = [...trail, nodePathSegment(own, parent)];
    const kids: RunNode[] = [];
    const seen = new Set<string>();
    for (const k of held?.children ?? []) {
      const key = nodePathKey([...path, nodePathSegment(k, own)]);
      const r = byPath.get(key);
      if (removed.has(key)) {
        continue;
      }
      seen.add(key);
      kids.push(r === undefined ? k : rebuild(r, k, own, path));
    }
    const added = (byParent.get(rec.path) ?? []).filter((r) => !seen.has(r.path));
    for (const r of added.sort((a, b) => a.index - b.index)) {
      kids.splice(Math.min(Math.max(r.index, 0), kids.length), 0, rebuild(r, undefined, own, path));
    }
    if (held?.children !== undefined || kids.length > 0) {
      own.children = kids;
    }
    const log = starts.get(rec.path);
    let startedAt = attemptStart(own.startedAt, own.status, log);
    if (log === undefined && startedAt !== undefined) {
      for (const k of kids) {
        startedAt = earlier(startedAt, k.startedAt);
      }
    }
    if (startedAt === undefined) {
      delete own.startedAt;
    } else {
      own.startedAt = startedAt;
    }
    rebuilt.push([own, rec.path]);
    return own;
  };
  const top = byPath.get(nodePathKey([nodePathSegment(root, undefined)]));
  return top === undefined ? root : rebuild(top, root, undefined, []);
}

function scheduleRunRetry(workflowID: string, status: number): void {
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
    invalidateRun(workflowID);
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

/** Externally-owned reasons a run's state cell must be KEPT, registered by the module that owns
 *  the reader (the composition root for tabs, a surface for its own mounted notes) so this module
 *  stays a leaf — importing `tabs.ts` here would invert the dependency direction, which is why
 *  `store.ts`'s eviction exemptions take the same shape. Every reader of a cell must register one
 *  (tabs, mounted notes, transcript run cards, and detached run cards in subagent pages and run-tab step streams): `forgetRun` and a dropped late read treat an
 *  unregistered reader as absent. */
const stateDemands: {
  readonly holds: (workflowID: string) => boolean;
  readonly shape: RunShape;
}[] = [];

/** Whether any registered demand holds the run's state cell. */
export function runStateDemanded(workflowID: string): boolean {
  return stateDemands.some((d) => d.holds(workflowID));
}

/** The widest shape a registered demand holds the run in; undefined when none holds it. */
function demandedShape(workflowID: string): RunShape | undefined {
  let out: RunShape | undefined;
  for (const d of stateDemands) {
    if ((out === undefined || wider(d.shape, out)) && d.holds(workflowID)) {
      out = d.shape;
    }
  }
  return out;
}

/** Register one demand predicate, for a reader drawing the run in `shape`. Returns its unregister. */
export function registerRunStateDemand(
  holds: (workflowID: string) => boolean,
  shape: RunShape,
): () => void {
  const d = { holds, shape };
  stateDemands.push(d);
  return () => {
    const i = stateDemands.indexOf(d);
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
  if (runStateDemanded(workflowID)) {
    return;
  }
  if (inFlight.has(workflowID)) {
    forgotten.add(workflowID);
  }
  cells.delete(workflowID);
  stale.delete(workflowID);
  revs.delete(workflowID);
  heldRevs.delete(workflowID);
  bodies.delete(workflowID);
  folded.delete(workflowID);
  wholeOwed.delete(workflowID);
  cancelRunRetry(workflowID);
  plans.delete(workflowID);
  executions.delete(workflowID);
  stepEnds.delete(workflowID);
  stepStarts.delete(workflowID);
  controlCells.delete(workflowID);
  goneCells.delete(workflowID);
  launchedBy.delete(workflowID);
  frameLabels.delete(workflowID);
  resumedBy.delete(workflowID);
  runLogs.delete(workflowID);
  forgetStepTails(workflowID);
  logObserver?.forget(workflowID);
}

/** What this run is CALLED: the server's display name from the read, else the lifecycle frame's,
 *  else `""`. UNTRACKED, like `runPlan`. */
export function runLabelOf(workflowID: string): string {
  return peekRunState(workflowID)?.displayName ?? frameLabels.get(workflowID) ?? "";
}

// What may be done to a run: the run read's `controls`, written with the state it was decided from, so
// the verbs follow every state change through that read's coalescing and retry ladder.

const controlCells = new Map<string, Signal<RunControlsResponse | undefined>>();

function controlCell(workflowID: string): Signal<RunControlsResponse | undefined> {
  let c = controlCells.get(workflowID);
  if (c === undefined) {
    c = signal<RunControlsResponse | undefined>(undefined);
    controlCells.set(workflowID, c);
  }
  return c;
}

const goneCells = new Map<string, Signal<boolean>>();

function goneCell(workflowID: string): Signal<boolean> {
  let c = goneCells.get(workflowID);
  if (c === undefined) {
    c = signal(false);
    goneCells.set(workflowID, c);
  }
  return c;
}

/** Subscribe to whether the server answered this run's last read with 404, as it does for a run
 *  that was deleted. A later read that describes the run clears it. */
export function runGone(workflowID: string): boolean {
  return goneCell(workflowID).value;
}

/** Subscribe to what a run offers. */
export function runControls(workflowID: string): RunControlsResponse | undefined {
  return controlCell(workflowID).value;
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
interface LiveRunRow {
  readonly chat: string;
  readonly executing: boolean;
}

/** A row handed OUT, carrying the workflow id the map holds it under. The id is the map's KEY
 *  rather than a field of the row, so a reader given the row alone cannot name the run it
 *  describes. */
interface LiveRunEntry extends LiveRunRow {
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

/** Not an index: the single-run rule bounds live runs to a handful, and a second map keyed by chat
 *  would be one more thing the rebuild could leave inconsistent. */
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
export async function rebuildLiveRuns(signal?: AbortSignal): Promise<void> {
  const d = await apiGetTyped("/api/runs/live", decodeLiveRunsResponse, signal);
  if (d === null) {
    return;
  }
  adoptLiveRuns(d.runs, true);
  // AFTER the adoption: the `runs` digest stamp certifies the inventory now held.
  observeStamp(d.subject);
}

/** Adopt an inventory somebody else already read. `readHeld` reads each live run a reader holds that
 *  has no state yet; a run with state is kept current by its frames and by `rereadHeldRuns`. */
function adoptLiveRuns(rows: readonly LiveRun[], readHeld: boolean): void {
  liveRunChats.clear();
  for (const r of rows) {
    if (r.workflow_id !== "") {
      liveRunChats.set(r.workflow_id, { chat: r.chat_id, executing: r.executing });
      noteRunChat(r.workflow_id, r.chat_id);
      noteRunKnown?.(r.workflow_id);
      // After the `liveRunChats` write: a chat tab's demand reads it to hold this run.
      if (readHeld && !cells.has(r.workflow_id) && runStateDemanded(r.workflow_id)) {
        readRunUnlessOut(r.workflow_id);
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
    void rebuildLiveRuns();
    return;
  }
  adoptLiveRuns(p.live_runs ?? [], false);
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
  /** Each step path's turn ids in `order`'s order, so a step's reader never scans the run. */
  byPath: Map<string, string[]>;
  /** One version per step path, beside the state cells rather than inside them: a step's entries
   *  and the run's tree move on different clocks, and a frame repaints only its own step's readers. */
  pathVersions: Map<string, Signal<number>>;
}

const runLogs = new Map<string, RunLog>();

function log(workflowID: string): RunLog {
  let l = runLogs.get(workflowID);
  if (l === undefined) {
    l = {
      turns: new Map(),
      order: [],
      holes: new Set(),
      byPath: new Map(),
      pathVersions: new Map(),
    };
    runLogs.set(workflowID, l);
  }
  return l;
}

function pathCell(l: RunLog, nodePath: string): Signal<number> {
  let c = l.pathVersions.get(nodePath);
  if (c === undefined) {
    c = signal(0);
    l.pathVersions.set(nodePath, c);
  }
  return c;
}

/** Tell the readers of a held turn's step; a turn the log does not hold has none yet. */
function bumpLog(workflowID: string, turnID: string): void {
  const l = runLogs.get(workflowID);
  const state = l?.turns.get(turnID);
  const path = state === undefined ? undefined : turnOpenOf(state)?.node_path;
  if (l !== undefined && path !== undefined) {
    const c = pathCell(l, path);
    c.value = c.peek() + 1;
  }
}

/** Subscribe to one step's turns, in the order they opened: a frame of another step does not
 *  re-run the caller. */
export function runStepTurns(workflowID: string, nodePath: string): readonly [string, TurnState][] {
  const l = log(workflowID);
  touch(pathCell(l, nodePath));
  return (l.byPath.get(nodePath) ?? []).flatMap((id) => {
    const t = l.turns.get(id);
    return t === undefined ? [] : [[id, t] as [string, TurnState]];
  });
}

/** The run's whole log as a `TurnSource`, UNTRACKED and uncopied: the projection context for a
 *  step's turns, which its caller subscribes to through `runStepTurns`. */
export function runTurnSource(workflowID: string): TurnSource {
  const l = runLogs.get(workflowID);
  return l === undefined
    ? { turns: new Map(), turn_order: [] }
    : { turns: l.turns, turn_order: l.order };
}

/** The turns of one step this run's log is missing entries for, subscribing to that step only.
 *  The pane re-reads such a turn through the step GET, which serves the whole turn; the digest's
 *  own repair is the range read (`run-turn-range.ts`), and both clear the marker through
 *  `clearRunHole`. */
export function runStepHoles(workflowID: string, nodePath: string): readonly string[] {
  const l = log(workflowID);
  touch(pathCell(l, nodePath));
  return (l.byPath.get(nodePath) ?? []).filter((id) => l.holes.has(id));
}

/** The newest `seq` this client holds for one step turn, which is what a `run_turn` repair asks
 *  past. `entries[i].seq === i` is the log's invariant, so the length answers it; a turn the
 *  store does not hold answers `undefined`, which asks for the WHOLE turn. */
export function runTurnHeldSeq(workflowID: string, turnID: string): number | undefined {
  const state = runLogs.get(workflowID)?.turns.get(turnID);
  return state === undefined || state.entries.length === 0 ? undefined : state.entries.length - 1;
}

/** The held turns of a run's log with no `turn_close`, in log order. */
export function runOpenTurnIDs(workflowID: string): string[] {
  const l = runLogs.get(workflowID);
  return l === undefined ? [] : l.order.filter((id) => l.turns.get(id)?.closeAt === undefined);
}

/** Drop the `run_turn` stamps of a run whose frames stop reaching this page, so a revalidation
 *  re-reads no turn nobody draws. The next frame or read applied to a held turn stamps it again. */
export function forgetRunTurnStamps(workflowID: string): void {
  for (const id of runOpenTurnIDs(workflowID)) {
    forgetSubject("run_turn", runTurnRef(workflowID, id));
  }
}

/** Retire a hole the reader's own repair closed. The chat store's twin restores residency when
 *  no repair is left out; here the range read and the pane's step GET are the repairs, so the
 *  reader that adopts that answer is what says the turn is whole again. */
export function clearRunHole(workflowID: string, turnID: string): void {
  const l = runLogs.get(workflowID);
  if (!l?.holes.delete(turnID)) {
    return;
  }
  bumpLog(workflowID, turnID);
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

/** Who keeps state derived from a run's log: told of each `steer` entry the log takes and of the log
 *  being forgotten. Injected, because that owner (`run-step-steers.ts`) is a leaf this module must
 *  not import. */
interface RunLogObserver {
  steer(workflowID: string, nodePath: string, entry: Entry): void;
  forget(workflowID: string): void;
}

let logObserver: RunLogObserver | undefined;

export function registerRunLogObserver(o: RunLogObserver): void {
  logObserver = o;
}

/** Mark a turn this client cannot complete from the stream, and ask for the repair. */
function markRunHole(workflowID: string, turnID: string): void {
  log(workflowID).holes.add(turnID);
  repairRunTurn?.(workflowID, turnID, runTurnHeldSeq(workflowID, turnID));
}

/** Create the turn a run-scoped `turn_opened` announced, with its `turn_open` as `entries[0]`.
 *  Idempotent by turn id, like the chat store's. Seated by its `turn_open.n`, the run log's own
 *  ordinal, because a step read can adopt a turn older than one the stream already opened. */
export function openRunTurn(workflowID: string, entry: Entry): void {
  const l = log(workflowID);
  if (l.turns.has(entry.turn)) {
    return;
  }
  const state: TurnState = { entries: [entry], openEntries: new Map() };
  l.turns.set(entry.turn, state);
  const n = runTurnOrdinal(state);
  const at = l.order.findIndex((id) => {
    const other = l.turns.get(id);
    return other !== undefined && runTurnOrdinal(other) > n;
  });
  if (at < 0) {
    l.order.push(entry.turn);
  } else {
    l.order.splice(at, 0, entry.turn);
  }
  const path = turnOpenOf(state)?.node_path;
  if (path !== undefined) {
    const ids = l.byPath.get(path) ?? [];
    const near = ids.findIndex((id) => {
      const other = l.turns.get(id);
      return other !== undefined && runTurnOrdinal(other) > n;
    });
    ids.splice(near < 0 ? ids.length : near, 0, entry.turn);
    l.byPath.set(path, ids);
  }
  stampRunTurn(workflowID, entry.turn, state);
  bumpLog(workflowID, entry.turn);
}

/** A turn without a readable ordinal sorts last, where a newer turn belongs. */
function runTurnOrdinal(state: TurnState): number {
  return turnOpenOf(state)?.n ?? Number.MAX_SAFE_INTEGER;
}

/** Append one sealed entry of a run's log at the position its `seq` claims. Same three answers
 *  as the chat store: the next `seq` appends, a `seq` already held under the same id is a
 *  redelivery and is dropped, anything else is a hole. */
export function appendRunEntry(workflowID: string, entry: Entry): void {
  const l = log(workflowID);
  const state = l.turns.get(entry.turn);
  if (state === undefined) {
    markRunHole(workflowID, entry.turn);
    return;
  }
  if (entry.seq !== state.entries.length) {
    if (state.entries[entry.seq]?.id === entry.id) {
      return;
    }
    markRunHole(workflowID, entry.turn);
    bumpLog(workflowID, entry.turn);
    return;
  }
  state.entries.push(entry);
  if (entry.kind === "steer") {
    logObserver?.steer(workflowID, turnOpenOf(state)?.node_path ?? "", entry);
  }
  if (
    entry.kind === "steer" ||
    entry.kind === "steer_delivered" ||
    entry.kind === "steer_cleared"
  ) {
    settleWorkflowMessages(turnsInOrder(l.order, l.turns));
  }
  if (entry.kind === "turn_close") {
    state.closeAt = entry.seq;
    // The run_turn stamp names an open turn as the digest's subject; a step_graded filed after the
    // close does not reopen it (stampRunTurn), and the run re-read on that frame carries the grade.
    forgetSubject("run_turn", runTurnRef(workflowID, entry.turn));
  } else {
    stampRunTurn(workflowID, entry.turn, state);
  }
  bumpLog(workflowID, entry.turn);
}

/** Seat a lane's open entry of a run's turn. */
export function openRunEntry(workflowID: string, open: OpenEntry): void {
  const state = runLogs.get(workflowID)?.turns.get(open.turn);
  if (state === undefined) {
    markRunHole(workflowID, open.turn);
    return;
  }
  if (openIsRedelivery(state.entries, state.openEntries, open)) {
    return;
  }
  const lane = open.lane ?? "";
  state.openEntries.set(lane, { ...open, lane });
  bumpLog(workflowID, open.turn);
}

/** Merge a read's open tails for one turn into the held ones, after its sealed entries are seated
 *  (`open-tails.ts` `mergeOpenTails` owns the rule): a frame that landed while the read was out
 *  keeps its word, and a tail whose entry has sealed does not come back. */
export function mergeRunOpenEntries(
  workflowID: string,
  turnID: string,
  open: readonly OpenEntry[],
): void {
  const state = runLogs.get(workflowID)?.turns.get(turnID);
  if (state === undefined) {
    return;
  }
  state.openEntries = mergeOpenTails(
    state.entries,
    state.openEntries,
    open.filter((o) => o.turn === turnID),
  );
  bumpLog(workflowID, turnID);
}

/** Extend a lane's open entry by one delta. `n` is the running count AFTER the delta, so `open.n
 *  + 1` is the only admissible value once a delta the turn already holds is dropped. */
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
    return;
  }
  const open = state.openEntries.get(lane);
  if (deltaIsRedelivery(state.entries, open, entryID, n)) {
    return;
  }
  if (open?.id !== entryID || n !== open.n + 1) {
    markRunHole(workflowID, turnID);
    bumpLog(workflowID, turnID);
    return;
  }
  state.openEntries.set(lane, { ...open, text: open.text + delta, n });
  bumpLog(workflowID, turnID);
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
    return;
  }
  const open = state.openEntries.get(lane);
  if (sealIsRedelivery(state.entries, entryID, seq)) {
    return;
  }
  if (open?.id !== entryID || n !== open.n) {
    markRunHole(workflowID, turnID);
    bumpLog(workflowID, turnID);
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

/** Every node of `type` in the tree, depth-first in plan order, every pass included. */
export function nodesOfType(root: RunNode | undefined, type: string): RunNode[] {
  const out: RunNode[] = [];
  const walk = (n: RunNode): void => {
    if (n.type === type) {
      out.push(n);
    }
    (n.children ?? []).forEach(walk);
  };
  if (root !== undefined) {
    walk(root);
  }
  return out;
}

/** Whether a run is still this process's to finish. Drives the elapsed clock and the card's
 *  open-by-default state. `paused` counts as live: it is stopped waiting for something, not
 *  over. */
export function runIsLive(state: RunState | undefined): boolean {
  const status = state?.status;
  return status === undefined ? false : runStatusActive(status);
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
