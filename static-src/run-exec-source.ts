// The WORKFLOW adapter: KAS's `inspect` reply folded into the exec view's model.

import { truncate } from "./strings.js";
import {
  isNeedInputPark,
  nodePathSegment,
  pauseDetailPhrase,
  pausePendingSentence,
  type RunNode,
  type RunState,
  userStopSentence,
} from "./run-store.js";
import type { RunAsks } from "./fundamentals/run-card.js";
import { stateOf, withAsk, inFlight, type ExecState } from "./exec-view/status.js";
import type { ExecFact, ExecKind, ExecNode, ExecRun } from "./exec-view/model.js";
import { defaultFailureReason, isBroken } from "./turn-severity.js";
import type { RunStepEnd } from "./wire/types.gen.js";

/** The run log's step ends by node path (`run-store.ts` `runStepEnds`). */
type StepEnds = ReadonlyMap<string, RunStepEnd>;

/** The per-node facts `nodePlan` carries that the state tree does not. Keyed by node id rather
 *  than by path, because the PLAN is the definition: it describes a node once, while the state
 *  tree describes every execution of it. */
interface PlanEntry {
  maxIterations?: number;
  onMaxIterations?: string;
  stopCondition?: string;
  /** A parallel's join policy: `all` / `allSettled` / `any`. */
  join?: string;
  /** A watch node's handler and what it waits for. */
  watch?: string;
}

/** Walk the raw plan and index every node id it names. */
export function indexPlan(plan: unknown): Map<string, PlanEntry> {
  const out = new Map<string, PlanEntry>();
  const str = (v: unknown): string | undefined =>
    typeof v === "string" && v !== "" ? v : undefined;
  const num = (v: unknown): number | undefined =>
    typeof v === "number" && Number.isFinite(v) ? v : undefined;

  const walk = (raw: unknown): void => {
    if (Array.isArray(raw)) {
      for (const item of raw) {
        walk(item);
      }
      return;
    }
    if (raw === null || typeof raw !== "object") {
      return;
    }
    const o = raw as Record<string, unknown>;
    const id = str(o["nodeId"]);
    if (id !== undefined) {
      const entry: PlanEntry = {};
      const max = num(o["maxIterations"]);
      if (max !== undefined) {
        entry.maxIterations = max;
      }
      for (const [key, field] of [
        ["onMaxIterations", "onMaxIterations"],
        ["stopCondition", "stopCondition"],
        ["join", "join"],
      ] as const) {
        const v = str(o[key]);
        if (v !== undefined) {
          entry[field] = v;
        }
      }
      // `stopWhen` is the other spelling of a stop condition; the engine rejects a node declaring
      // both, so taking either into one field cannot lose one.
      if (entry.stopCondition === undefined) {
        const when = str(o["stopWhen"]);
        if (when !== undefined) {
          entry.stopCondition = when;
        }
      }
      const handler = str(o["watchHandler"]) ?? str(o["handler"]);
      const until = str(o["until"]) ?? str(o["waitFor"]);
      if (handler !== undefined || until !== undefined) {
        entry.watch = [handler, until].filter((v) => v !== undefined).join(" \u2192 ");
      }
      if (Object.values(entry).some((v) => v !== undefined)) {
        out.set(id, entry);
      }
    }
    // Every container spelling the engine uses, plus a generic `children` so a node type added
    // upstream still has its descendants indexed.
    for (const key of ["steps", "branches", "children", "body", "nodes"]) {
      walk(o[key]);
    }
  };
  walk(plan);
  return out;
}

/** A container's state, derived from its children. */
function rollUp(own: ExecState, kids: readonly ExecNode[]): ExecState {
  if (kids.length === 0) {
    return own;
  }
  const states = new Set(kids.map((k) => k.state));
  for (const s of ["input", "fail", "warn", "running", "waiting"] as const) {
    if (states.has(s)) {
      return s;
    }
  }
  if (states.has("ok")) {
    return states.has("pending") ? "running" : "ok";
  }
  return own;
}

/** A step KAS graded `completed` reads failed when its newest turn closed broken: the model refused, or kiro-cli
 *  stopped it at its model-call limit with its work unfinished. Any other KAS grade is KAS's own account. */
function endedState(own: ExecState, end: RunStepEnd | undefined): ExecState {
  return own === "ok" && isBroken(end?.outcome) ? "fail" : own;
}

/** The kind, mapped rather than passed through, so an upstream addition lands on `group` (which
 *  the CSS has a rule for) instead of on nothing. */
function kindOf(type: string): ExecKind {
  switch (type) {
    case "step":
    case "sequence":
    case "repeat":
    case "parallel":
    case "watch":
      return type;
    default:
      return "group";
  }
}

/** The identity facts for a step, in the order they answer questions: who ran it, on what, how
 *  it ended, and what it took to get there. */
function stepFacts(node: RunNode, plan: PlanEntry | undefined): ExecFact[] {
  const facts: ExecFact[] = [];
  const add = (label: string, value: string | undefined, mono = false): void => {
    if (value !== undefined && value !== "") {
      facts.push(mono ? { label, value, mono } : { label, value });
    }
  };
  add("Agent", node.agentName);
  // `auto` is not a model, it is the absence of a choice, and naming it as one implies a pin that
  // never happened.
  add("Model", node.modelId === "auto" ? undefined : node.modelId);
  add("Effort", node.effortLevel);
  // KAS's own statement of WHY the node ended, which is different information from its status: a
  // step can complete having asked for input.
  add("Signal", node.completionSignal);
  if (node.continuationAttempts !== undefined && node.continuationAttempts > 0) {
    add("Retries", String(node.continuationAttempts));
  }
  add("Pass", node.iteration === undefined ? undefined : String(node.iteration + 1));
  add("Branch", node.branchId);
  if (node.watchTerminal === true) {
    add("Watch", "reached a terminal state");
  }
  add("Max passes", plan?.maxIterations === undefined ? undefined : String(plan.maxIterations));
  add("At the cap", plan?.onMaxIterations);
  add("Stops when", plan?.stopCondition, true);
  add("Join", plan?.join);
  add("Waits for", plan?.watch, true);
  // Last, and monospace: it is the handle for the step's own session rather than something a reader
  // acts on, so it sits below the facts that describe the work.
  add("Session", node.sessionId, true);
  return facts;
}

/** The one-line summary under a row's label. */
function subtitleOf(node: RunNode, kind: ExecKind, plan: PlanEntry | undefined): string {
  const bits: string[] = [];
  if (kind === "repeat") {
    bits.push(
      plan?.maxIterations === undefined ? "repeats" : `up to ${String(plan.maxIterations)} passes`,
    );
    if (plan?.stopCondition !== undefined) {
      bits.push(`until ${truncate(plan.stopCondition, 60)}`);
    }
  } else if (kind === "parallel") {
    bits.push(plan?.join === undefined ? "in parallel" : `in parallel, join ${plan.join}`);
  } else if (kind === "watch") {
    bits.push(plan?.watch === undefined ? "polls" : `polls ${truncate(plan.watch, 60)}`);
  } else if (kind === "step") {
    if (node.agentName !== undefined && node.agentName !== "") {
      bits.push(node.agentName);
    }
    if (node.modelId !== undefined && node.modelId !== "" && node.modelId !== "auto") {
      bits.push(node.modelId);
    }
    if (node.iteration !== undefined) {
      bits.push(`pass ${String(node.iteration + 1)}`);
    }
  }
  return bits.join(" \u00b7 ");
}

/** Fold one state node and its subtree. `path` goes through `nodePathSegment`, so a step's tree
 *  row is addressed the way KAS addresses its FRAMES — otherwise a step inside a loop had its
 *  live transcript filed under a path no tree node ever selected, which is the other symptom of
 *  the same key defect that duplicated the transcript's step rows. */
function toNode(
  node: RunNode,
  trail: readonly string[],
  plans: Map<string, PlanEntry>,
  asks: RunAsks,
  ends: StepEnds,
  parent?: RunNode,
): ExecNode {
  const path = [...trail, nodePathSegment(node, parent)];
  const address = path.join("/");
  const plan = plans.get(node.nodeId);
  const kind = kindOf(node.type);
  const children = (node.children ?? []).map((k) => toNode(k, path, plans, asks, ends, node));
  const own = withAsk(stateOf(node.status), asks.nodes.has(node.nodeId));
  const end = children.length === 0 ? ends.get(address) : undefined;
  const state = children.length === 0 ? endedState(own, end) : rollUp(own, children);
  const out: ExecNode = {
    path: address,
    label: node.nodeId,
    kind,
    state,
    children,
  };
  if (node.startedAt !== undefined) {
    out.start = node.startedAt;
  }
  if (node.endedAt !== undefined) {
    out.end = node.endedAt;
  }
  const sub = subtitleOf(node, kind, plan);
  if (sub !== "") {
    out.subtitle = sub;
  }
  const facts = stepFacts(node, plan);
  if (facts.length > 0) {
    out.facts = facts;
  }
  if (node.failureReason !== undefined && node.failureReason !== "") {
    out.failure = node.failureReason;
  } else if (state !== own && end !== undefined) {
    out.failure = end.failure_reason ?? defaultFailureReason(end.outcome);
  }
  if (node.capturedOutput !== undefined) {
    out.output = node.capturedOutput;
  }
  if (node.artifacts !== undefined && Object.keys(node.artifacts).length > 0) {
    out.artifacts = node.artifacts;
  }
  // A LEAF can host a transcript; a container cannot, and saying so is what lets the detail pane
  // distinguish "nothing streams here" from "nothing has arrived".
  if (children.length === 0) {
    out.transcript = true;
  }
  return out;
}

/** The run's alert, and the five things that put it in front of a person. */
function alertOf(state: RunState, asks: RunAsks, nodes: readonly ExecNode[]): ExecRun["alert"] {
  if (asks.count > 0) {
    const head =
      asks.label === ""
        ? "Waiting for your answer"
        : `Waiting for your answer: ${truncate(asks.label, 160)}`;
    return {
      kind: "input",
      text: asks.count > 1 ? `${head} (${String(asks.count)} asks waiting)` : head,
    };
  }
  const stopped = userStopSentence(state);
  if (stopped !== undefined) {
    return { kind: "stopped", text: stopped };
  }
  const pending = pausePendingSentence(state);
  if (pending !== undefined) {
    return { kind: "paused", text: pending };
  }
  if (state.status === "paused") {
    // The two `need_input` literals are REPLACED rather than quoted.
    const bits = [
      isNeedInputPark(state)
        ? "A step is waiting for your answer. Resume alone will park it again, " +
          "so answer or waive it in the dock"
        : state.pauseReason === undefined || state.pauseReason === ""
          ? "Waiting"
          : `Waiting: ${state.pauseReason}`,
    ];
    const detail = pauseDetailPhrase(state.pauseDetail);
    if (detail !== undefined) {
      bits.push(detail);
    }
    return { kind: "paused", text: bits.join(" \u00b7 ") };
  }
  if (state.status === "failed") {
    const failed = nodes
      .flatMap(function all(n: ExecNode): ExecNode[] {
        return [n, ...n.children.flatMap(all)];
      })
      .find((n) => n.state === "fail" && n.failure !== undefined);
    return {
      kind: "failed",
      text:
        failed === undefined
          ? "The run failed"
          : `${failed.label} failed: ${truncate(failed.failure ?? "", 200)}`,
    };
  }
  return undefined;
}

/** Fold KAS's `inspect` reply into the exec view's model. */
export function runToExec(
  workflowID: string,
  state: RunState,
  plan: unknown,
  asks: RunAsks,
  focus = "",
  ends: StepEnds = new Map(),
): ExecRun {
  const plans = indexPlan(plan);
  // The root is a container KAS names after the workflow itself, so its children are the run's real
  // top level. Kept as a root only when it carries siblings worth showing — otherwise it would be
  // one group wrapping everything, which is an indent for no information.
  const root = state.root;
  const nodes =
    root === undefined
      ? []
      : root.type === "sequence" && (root.children?.length ?? 0) > 0
        ? (root.children ?? []).map((k) =>
            toNode(k, [nodePathSegment(root, undefined)], plans, asks, ends, root),
          )
        : [toNode(root, [], plans, asks, ends)];

  const runState = stateOf(state.status);
  const out: ExecRun = {
    id: workflowID,
    label: state.runLabel ?? state.workflowName ?? "Workflow run",
    state: asks.count > 0 ? "input" : runState,
    nodes,
    live: inFlight(runState) || asks.count > 0,
  };
  if (state.inputs !== undefined && Object.keys(state.inputs).length > 0) {
    out.inputs = state.inputs;
  }
  const alert = alertOf(state, asks, nodes);
  if (alert !== undefined) {
    out.alert = alert;
  }
  if (focus !== "") {
    out.focus = focus;
  }
  return out;
}
