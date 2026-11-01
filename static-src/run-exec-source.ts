// The WORKFLOW adapter: KAS's `inspect` reply folded into the exec view's model.

import { nodePathKey, nodePathSegment } from "./run-node-key.js";
import { truncate } from "./strings.js";
import {
  pauseDetailPhrase,
  pausePendingSentence,
  resumeSentence,
  type RunNode,
  type RunState,
  stopSentence,
} from "./run-store.js";
import { askedSteps, type AskedSteps, type RunAsks } from "./run-asks.js";
import { runStatusTerminal, type ClassifiedRunStatus } from "./run-status.js";
import type { RunPlanUpdate, RunStepMessageVerb } from "./wire/types.gen.js";
import { stateOf, withAsk, inFlight, settled, type ExecState } from "./exec-view/status.js";
import {
  currentMembers,
  failureOwner,
  flatten,
  isWork,
  passBudgetText,
  passLimitText,
  passText,
  type ExecFact,
  type ExecKind,
  type ExecNode,
  type ExecQuestion,
  type ExecRun,
} from "./exec-view/model.js";
import {
  lastTailLine,
  watchResult,
  watchSummary,
  watchTailFence,
  type WatchResult,
} from "./watch-result.js";
import { defaultFailureReason, isBroken } from "./turn-severity.js";
import type { RunStepEnd } from "./wire/types.gen.js";

/** The run log's step ends by node path (`run-store.ts` `runStepEnds`). */
export type StepEnds = ReadonlyMap<string, RunStepEnd>;

/** The per-node facts `nodePlan` carries that the state tree does not. Keyed by node id rather
 *  than by path, because the PLAN is the definition: it describes a node once, while the state
 *  tree describes every execution of it. */
interface PlanEntry {
  maxIterations?: number;
  onMaxIterations?: string;
  /** The stop condition in words, a file check naming its whole path; `stopBrief` names the file
   *  alone, for a one-line subtitle. */
  stopCondition?: string;
  stopBrief?: string;
  /** A watch's handler id. */
  watch?: string;
  /** On a sequence KAS expanded from an `include`: the included recipe's name and the path the
   *  include wrote. Only the plan carries it; the state tree's sequence drops it. */
  includedFrom?: { name: string; path?: string };
  /** A container's planned children, raw: what it will hold before KAS expands it. */
  body?: unknown[];
}

/** Every container spelling the engine uses, plus a generic `children` so a node type added
 *  upstream still has its descendants indexed. */
const PLAN_CHILD_KEYS = ["steps", "branches", "children", "body", "nodes"] as const;

/** A loop's stop condition in words: a string as written, or KAS's object form (`containsText`,
 *  `fileCheck { path, jsonPath, value }`, `completionSignal`; an array `value` is any-of), each
 *  part it sets. `brief` names a checked file by its base name. */
function stopPhrase(raw: unknown, brief: boolean): string | undefined {
  if (typeof raw === "string") {
    return raw === "" ? undefined : raw;
  }
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) {
    return undefined;
  }
  const o = raw as Record<string, unknown>;
  const literal = (v: unknown): string => (typeof v === "string" ? v : JSON.stringify(v));
  const parts: string[] = [];
  if (typeof o["containsText"] === "string" && o["containsText"] !== "") {
    parts.push(`output contains ${JSON.stringify(o["containsText"])}`);
  }
  const check = o["fileCheck"];
  if (check !== null && typeof check === "object" && !Array.isArray(check)) {
    const c = check as Record<string, unknown>;
    const path = typeof c["path"] === "string" ? c["path"] : "";
    if (path !== "" && "value" in c) {
      const file = brief ? (path.split("/").pop() ?? path) : path;
      const field =
        typeof c["jsonPath"] === "string" && c["jsonPath"] !== "" ? ` ${c["jsonPath"]}` : "";
      const value = c["value"];
      const want = Array.isArray(value)
        ? `one of ${value.map(literal).join(", ")}`
        : literal(value);
      parts.push(`${file}${field} is ${want}`);
    }
  }
  if (typeof o["completionSignal"] === "string" && o["completionSignal"] !== "") {
    parts.push(`the step signals ${o["completionSignal"]}`);
  }
  return parts.length === 0 ? undefined : parts.join("; ");
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
      const onMax = str(o["onMaxIterations"]);
      if (onMax !== undefined) {
        entry.onMaxIterations = onMax;
      }
      // `stopWhen` is the other spelling of a stop condition; the engine rejects a node declaring
      // both, so taking either into one field cannot lose one.
      const stop = o["stopCondition"] ?? o["stopWhen"];
      const full = stopPhrase(stop, false);
      if (full !== undefined) {
        entry.stopCondition = full;
        entry.stopBrief = stopPhrase(stop, true) ?? full;
      }
      // KAS's nodePlan carries a watch's handler id in `agentName`.
      if (o["type"] === "watch") {
        const handler = str(o["agentName"]);
        if (handler !== undefined) {
          entry.watch = handler;
        }
      }
      const from = o["includedFrom"];
      if (from !== null && typeof from === "object" && !Array.isArray(from)) {
        const f = from as Record<string, unknown>;
        const name = str(f["name"]);
        if (name !== undefined) {
          const path = str(f["path"]);
          entry.includedFrom = path === undefined ? { name } : { name, path };
        }
      }
      const body = PLAN_CHILD_KEYS.flatMap((key) => {
        const v = o[key];
        return Array.isArray(v) ? (v as unknown[]) : [];
      });
      if (body.length > 0) {
        entry.body = body;
      }
      if (Object.values(entry).some((v) => v !== undefined)) {
        out.set(id, entry);
      }
    }
    for (const key of PLAN_CHILD_KEYS) {
      walk(o[key]);
    }
  };
  walk(plan);
  return out;
}

/** A container's state, derived from its current members (`currentMembers`): a repeat is where
 *  its latest pass is. */
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

/** A repeat's pass budget as KAS's loop executor reads it, `extendedMaxIterations ??
 *  maxIterations` (an extension stores the new total), and how many passes the extension added. */
function passBudget(
  node: RunNode,
  plan: PlanEntry | undefined,
): { max: number; added: number } | undefined {
  const planned = plan?.maxIterations;
  const max = node.extendedMaxIterations ?? planned;
  if (max === undefined) {
    return undefined;
  }
  return { max, added: planned === undefined ? 0 : Math.max(0, max - planned) };
}

/** The identity facts for a step, in the order they answer questions: who ran it, on what, how
 *  it ended, and what it took to get there. */
function stepFacts(
  node: RunNode,
  plan: PlanEntry | undefined,
  watch: WatchResult | undefined,
  include: Include | undefined,
): ExecFact[] {
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
  add("Recipe", plan?.includedFrom?.name ?? include?.name);
  add("Recipe file", plan?.includedFrom?.path, true);
  if (node.watchTerminal === true) {
    add("Watch", "reached a terminal state");
  }
  const budget = passBudget(node, plan);
  add(
    "Max passes",
    budget === undefined
      ? undefined
      : budget.added > 0
        ? `${String(budget.max)} (${String(budget.max - budget.added)} planned, ${String(budget.added)} added)`
        : String(budget.max),
  );
  add("At the cap", plan?.onMaxIterations);
  add("Stops when", plan?.stopCondition, true);
  add("Handler", plan?.watch, true);
  if (watch?.kind === "process") {
    // "Signal" is KAS's completion signal above, so a kill is stated as the exit.
    if (watch.signal !== null && watch.signal !== "") {
      add("Exit", `killed by ${watch.signal}`);
    } else if (watch.exitCode !== null) {
      add("Exit", String(watch.exitCode));
    }
    add("Output file", watch.outputFile, true);
  }
  // Last, and monospace: it is the handle for the step's own session rather than something a reader
  // acts on, so it sits below the facts that describe the work.
  add("Session", node.sessionId, true);
  return facts;
}

function subtitleOf(
  node: RunNode,
  kind: ExecKind,
  plan: PlanEntry | undefined,
  watch: WatchResult | undefined,
): string {
  const bits: string[] = [];
  if (kind === "repeat") {
    const budget = passBudget(node, plan);
    bits.push(budget === undefined ? "repeats" : passBudgetText(budget.max, budget.added));
    if (plan?.stopBrief !== undefined) {
      bits.push(`until ${truncate(plan.stopBrief, 60)}`);
    }
  } else if (kind === "parallel") {
    bits.push("in parallel");
  } else if (kind === "sequence") {
    // Provenance, not a second name: shown even when the recipe is named like the include.
    if (plan?.includedFrom !== undefined) {
      bits.push(`from ${truncate(plan.includedFrom.name, 60)}`);
    }
  } else if (kind === "watch") {
    if (watch !== undefined) {
      bits.push(watchSummary(watch));
    } else {
      bits.push(plan?.watch === undefined ? "polls" : `polls ${truncate(plan.watch, 60)}`);
    }
  } else if (kind === "step") {
    if (node.agentName !== undefined && node.agentName !== "") {
      bits.push(node.agentName);
    }
    if (node.modelId !== undefined && node.modelId !== "" && node.modelId !== "auto") {
      bits.push(node.modelId);
    }
    // No pass number: the pass is the loop's, stated on its pass row.
    if (node.continuationAttempts !== undefined && node.continuationAttempts > 0) {
      const n = node.continuationAttempts;
      bits.push(`${String(n)} ${n === 1 ? "retry" : "retries"}`);
    }
  }
  // Kiro's step detail: a pause's reason, else a retry wait's, verbatim.
  if (node.status === "paused" && node.pauseReason !== undefined) {
    bits.push(`Paused: ${truncate(node.pauseReason, 80)}`);
  } else if (node.status === "running" && node.retryReason !== undefined) {
    bits.push(`Retrying: ${truncate(node.retryReason, 80)}`);
  }
  return bits.join(" \u00b7 ");
}

function latestEnd(nodes: readonly ExecNode[]): string | undefined {
  let best: string | undefined;
  let bestAt = Number.NEGATIVE_INFINITY;
  for (const n of nodes) {
    const at = n.end === undefined ? Number.NaN : Date.parse(n.end);
    if (!Number.isNaN(at) && at > bestAt) {
      best = n.end;
      bestAt = at;
    }
  }
  return best;
}

/** The include a node sits inside: its id (the prefix KAS gave every node in it) and its recipe. */
interface Include {
  id: string;
  name: string;
}

/** `path` goes through `nodePathSegment`, so a step's tree row is addressed the way KAS addresses
 *  its FRAMES, the key its live transcript is filed under. */
interface FoldContext {
  plans: Map<string, PlanEntry>;
  asked: AskedSteps;
  run: ClassifiedRunStatus;
  ends: StepEnds;
}

/** The server's `stepMessageVerb` (internal/agent/run_message.go), read off the raw statuses. */
function messageVerb(node: RunNode, ctx: FoldContext): RunStepMessageVerb | undefined {
  if (runStatusTerminal(ctx.run) || node.sessionId === undefined || node.sessionId === "") {
    return undefined;
  }
  if (node.status === "running") {
    return "steer";
  }
  if (node.status !== "paused") {
    return undefined;
  }
  if (ctx.asked.answerable.has(node)) {
    return "answer";
  }
  return ctx.run === "paused" ? "prompt" : undefined;
}

/** A raw plan child as a not-yet-started state node, or nothing when it names no node. Any type is
 *  kept, as the state tree keeps it, so `kindOf` decides both paths alike. */
function plannedNode(raw: unknown): RunNode[] {
  if (raw === null || typeof raw !== "object") {
    return [];
  }
  const o = raw as Record<string, unknown>;
  const id = o["nodeId"];
  const type = o["type"];
  if (typeof id !== "string" || id === "" || typeof type !== "string" || type === "") {
    return [];
  }
  const out: RunNode = { nodeId: id, type, status: "pending" };
  for (const key of ["agentName", "modelId", "effortLevel"] as const) {
    const v = o[key];
    if (typeof v === "string" && v !== "") {
      out[key] = v;
    }
  }
  return [out];
}

/** What a container KAS has not expanded yet will hold, from its plan: a parallel's branches
 *  before they start, a repeat's first pass before it opens. A repeat's pass is spelled the way
 *  KAS spells it (`<id>#0`, iteration 0), so the paths these get are the ones the real nodes take. */
function plannedChildren(node: RunNode, plan: PlanEntry | undefined): RunNode[] {
  const body = (plan?.body ?? []).flatMap(plannedNode);
  if (body.length === 0 || isWork({ kind: kindOf(node.type) })) {
    return [];
  }
  if (node.type === "repeat") {
    return [
      {
        nodeId: `${node.nodeId}#0`,
        type: "sequence",
        status: "pending",
        iteration: 0,
        children: body,
      },
    ];
  }
  return body;
}

function toNode(
  node: RunNode,
  trail: readonly string[],
  ctx: FoldContext,
  parent?: RunNode,
  include?: Include,
): ExecNode {
  const path = [...trail, nodePathSegment(node, parent)];
  const address = nodePathKey(path);
  const plan = ctx.plans.get(node.nodeId);
  const kind = kindOf(node.type);
  const actual = node.children ?? [];
  const kids = actual.length > 0 ? actual : plannedChildren(node, plan);
  // KAS refuses an include inside an included recipe, so one enclosing include is all there is.
  const inner =
    kind === "sequence" && plan?.includedFrom !== undefined
      ? { id: node.nodeId, name: plan.includedFrom.name }
      : include;
  const children = kids.map((k) => toNode(k, path, ctx, node, inner));
  const own = withAsk(stateOf(node.status), ctx.asked.waiting.has(node));
  const work = isWork({ kind });
  const end = work ? ctx.ends.get(address) : undefined;
  const state = work ? endedState(own, end) : rollUp(own, currentMembers({ kind, children }));
  // A repeat's child is a PASS; KAS names a pass container `<repeatId>#<n>`, which is its id
  // and not something a reader can read.
  const pass =
    parent?.type === "repeat"
      ? (node.iteration ?? (parent.children ?? []).indexOf(node)) + 1
      : undefined;
  const label = pass !== undefined && !work ? passText(pass) : node.nodeId;
  const out: ExecNode = { path: address, label, kind, state, children };
  // KAS prefixes every id inside an include with `<include id>/`; a hand-written id holding a `/`
  // is not one, so the prefix is matched against the include's id, never split out of the label.
  const qualifier = include === undefined ? "" : `${include.id}/`;
  if (qualifier !== "" && label.length > qualifier.length && label.startsWith(qualifier)) {
    out.qualifier = qualifier;
  }
  const verb = work ? messageVerb(node, ctx) : undefined;
  if (verb !== undefined) {
    out.verb = verb;
  }
  const ask = verb === "answer" ? ctx.asked.answerable.get(node) : undefined;
  if (ask !== undefined && ask !== "") {
    out.ask = ask;
  }
  if (pass !== undefined) {
    out.pass = pass;
  }
  const budget = kind === "repeat" ? passBudget(node, plan) : undefined;
  if (budget !== undefined) {
    out.maxPasses = budget.max;
  }
  if (node.startedAt !== undefined) {
    out.start = node.startedAt;
  }
  if (node.startGen !== undefined) {
    out.startGen = node.startGen;
  }
  if (node.endedAt !== undefined) {
    out.end = node.endedAt;
  } else if (!work && settled(out.state)) {
    // KAS stamps no end on a finished repeat, so its row would count on to now.
    const last = latestEnd(children);
    if (last !== undefined) {
      out.end = last;
    }
  }
  const watch =
    kind === "watch" && node.capturedOutput !== undefined
      ? watchResult(node.capturedOutput)
      : undefined;
  const sub = subtitleOf(node, kind, plan, watch);
  if (sub !== "") {
    out.subtitle = sub;
  }
  const facts = stepFacts(node, plan, watch, include);
  if (facts.length > 0) {
    out.facts = facts;
  }
  const kasReason = (node.failureReason ?? "").trim();
  if (kasReason !== "") {
    out.failure = kasReason;
  } else if (state !== own && end !== undefined) {
    const logReason = (end.failure_reason ?? "").trim();
    out.failure = logReason !== "" ? logReason : defaultFailureReason(end.outcome);
  }
  // An idle-timeout record says only what the subtitle already does, so it shows no Output box.
  if (watch?.kind === "process") {
    out.output = watchTailFence(watch.tail);
  } else if (watch === undefined && node.capturedOutput !== undefined) {
    out.output = node.capturedOutput;
  }
  if (node.capturedOutput !== undefined) {
    out.capture =
      watch === undefined
        ? node.capturedOutput
        : watch.kind === "process"
          ? lastTailLine(watch.tail)
          : "";
  }
  if (node.artifacts !== undefined && Object.keys(node.artifacts).length > 0) {
    out.artifacts = node.artifacts;
  }
  // A work node can host a transcript; a container cannot, and saying so is what lets the detail
  // pane distinguish "nothing streams here" from "nothing has arrived".
  if (work) {
    out.transcript = true;
  }
  return out;
}

/** The run's alert, and the five things that put it in front of a person. */
function alertOf(state: RunState, asks: RunAsks, nodes: readonly ExecNode[]): ExecRun["alert"] {
  if (asks.count > 0) {
    const head =
      asks.label === "" ? "Waiting for answer" : `Waiting for answer: ${truncate(asks.label, 160)}`;
    return {
      kind: "input",
      text: asks.count > 1 ? `${head} (${String(asks.count)} asks waiting)` : head,
    };
  }
  const stopped = stopSentence(state);
  if (state.status === "paused") {
    return { kind: "paused", text: pausedText(state, stopped) };
  }
  if (stopped !== undefined) {
    return { kind: "stopped", text: stopped };
  }
  const pending = pausePendingSentence(state);
  if (pending !== undefined) {
    return { kind: "paused", text: pending };
  }
  const resumed = resumeSentence(state);
  if (resumed !== undefined) {
    return { kind: "stopped", text: resumed };
  }
  if (state.status === "failed") {
    const failed = failureOwner(nodes);
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

/** A paused run's one line: the loop a cap stopped (in place of KAS's cap sentence), then who
 *  paused it (`stopSentence`), else on an uncapped pause KAS's reason, then `pauseDetailPhrase`. */
function pausedText(state: RunState, attributed: string | undefined): string {
  const cap = state.pause;
  const limit =
    cap?.kind === "maxIterations" &&
    typeof cap.iterationsRun === "number" &&
    typeof cap.maxIterations === "number"
      ? passLimitText(cap.nodeId ?? "A loop", cap.iterationsRun, cap.maxIterations)
      : undefined;
  const bits = limit === undefined ? [] : [limit];
  const reason = state.pauseReason ?? "";
  if (attributed !== undefined) {
    bits.push(attributed);
  } else if (limit === undefined) {
    bits.push(reason === "" ? "Waiting" : `Waiting: ${reason}`);
  }
  const detail = pauseDetailPhrase(state.pauseDetail);
  if (detail !== undefined) {
    bits.push(detail);
  }
  return bits.join(" \u00b7 ");
}

/** The run's step questions whole (`decision-dock.ts` `runPendingAsks` owns the text), each placed
 *  at its step where the server's match places one. A request decision is left out: its own card
 *  in the bottom bar asks it. */
function questionsOf(
  asks: RunAsks,
  paths: readonly (string | undefined)[],
  nodes: readonly ExecNode[],
): ExecQuestion[] {
  const all = flatten(nodes);
  return asks.asked.flatMap((a, i) => {
    if (!a.answer || a.text === undefined) {
      return [];
    }
    const path = paths[i];
    const step = path === undefined ? undefined : all.find((n) => n.path === path)?.label;
    return [
      {
        text: a.text,
        ...(path === undefined ? {} : { path }),
        ...(step === undefined ? {} : { step }),
      },
    ];
  });
}

/** What became of a queued plan revision, each outcome KAS names today in Kiro's words. The
 *  vocabulary is open: a word missing here is shown as KAS sent it. */
const PLAN_OUTCOMES = new Map<string, (reason: string) => string>([
  ["queued", () => ""],
  ["applied", () => "applied"],
  ["rejected", (reason) => (reason === "" ? "rejected" : `rejected: ${truncate(reason, 200)}`)],
  ["dropped", () => "dropped, the run ended first"],
]);

/** The run tab's one line for a plan revision: `Plan revised: N steps queued after <step>`, then
 *  its outcome. */
function planNotice(u: RunPlanUpdate): { text: string; failed: boolean } {
  const steps = u.pending === 1 ? "1 step" : `${String(u.pending)} steps`;
  const after = u.after === undefined || u.after === "" ? "" : ` after ${u.after}`;
  const head = `Plan revised: ${steps} queued${after}`;
  const outcome = PLAN_OUTCOMES.get(u.outcome)?.(u.reason ?? "") ?? u.outcome;
  return {
    text: outcome === "" ? head : `${head} \u00b7 ${outcome}`,
    failed: u.outcome === "rejected",
  };
}

/** Fold KAS's `inspect` reply into the exec view's model. */
export function runToExec(
  state: RunState,
  plan: unknown,
  asks: RunAsks,
  focus = "",
  ends: StepEnds = new Map(),
): ExecRun {
  // A sequence root is a container KAS names after the workflow itself, so its children are the
  // run's real top level (one group around everything is an indent for no information). The plan's
  // top level is the root's body, which no plan entry names.
  const root = state.root;
  const ctx: FoldContext = {
    plans: indexPlan(plan),
    asked: askedSteps(root, asks),
    run: state.status ?? "unknown",
    ends,
  };
  let nodes: ExecNode[] = [];
  if (root?.type === "sequence") {
    const planned = Array.isArray(plan) ? (plan as unknown[]).flatMap(plannedNode) : [];
    const top = root.children !== undefined && root.children.length > 0 ? root.children : planned;
    const trail = [nodePathSegment(root, undefined)];
    nodes = top.map((k) => toNode(k, trail, ctx, root));
  } else if (root !== undefined) {
    nodes = [toNode(root, [], ctx)];
  }

  const runState = stateOf(state.status);
  const out: ExecRun = {
    // An empty label names nothing, so it falls through like an absent one.
    label:
      [state.runLabel, state.workflowName].find((s) => s !== undefined && s !== "") ??
      "Workflow run",
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
  const questions = questionsOf(asks, ctx.asked.paths, nodes);
  if (questions.length > 0) {
    out.questions = questions;
  }
  if (state.planUpdate !== undefined) {
    out.notice = planNotice(state.planUpdate);
  }
  if (focus !== "") {
    out.focus = focus;
  }
  return out;
}
