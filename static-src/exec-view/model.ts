// The exec view's model: delegated work as a tree of nodes over time. Workflow runs, subagents and
// pipelines each fold into `ExecRun` through an adapter (`run-exec-source.ts`); derived on read,
// held by nobody, and the wire shape of nothing.

import { inFlight, type ExecState } from "./status.js";

/** What KIND of node this is. `group` is the catch-all, so an unseen source has a legal value. */
export type ExecKind = "step" | "sequence" | "repeat" | "parallel" | "watch" | "group";

/** One labelled fact for the identity list; a list because the facts differ per source. */
export interface ExecFact {
  label: string;
  value: string;
  /** Rendered monospace: an id, a path, a model name. */
  mono?: boolean;
}

/** A node of the execution tree. */
export interface ExecNode {
  /** Stable, instance-unique address: react key, selection key and transcript key (a repeat's
   *  iterations must differ, which a node ID cannot). */
  path: string;
  /** What the row says: the step's own name, not its path. */
  label: string;
  kind: ExecKind;
  state: ExecState;
  children: ExecNode[];
  /** ISO timestamps, when the source has them. */
  start?: string;
  end?: string;
  /** The one-line summary under the label, pre-joined by the adapter. */
  subtitle?: string;
  /** The identity facts, for the detail pane. */
  facts?: ExecFact[];
  /** Why this node ended badly, never blank: the source's reason trimmed, or its outcome's default sentence. */
  failure?: string;
  /** What the node produced, as markdown. */
  output?: string;
  /** Named artifacts the node published. */
  artifacts?: Record<string, string>;
  /** Whether this node can host a live transcript, so the pane can say "none here" rather than
   *  "none yet". */
  transcript?: boolean;
  /** On a `repeat`'s child: which pass it is, 1-based. */
  pass?: number;
  /** On a `repeat`: the most passes its plan allows, when the plan bounds it. */
  maxPasses?: number;
}

/** Whether a node does WORK. Decided by kind, never by shape: a container KAS has not expanded
 *  yet has no children and is still a container, not work. */
export function isWork(n: { readonly kind: ExecKind }): boolean {
  return n.kind === "step" || n.kind === "watch";
}

/** A `repeat`'s current pass: its last child, which is where the loop is. */
export function latestPass(repeat: Pick<ExecNode, "children">): ExecNode | undefined {
  return repeat.children[repeat.children.length - 1];
}

/** The whole execution, plus the facts a header states. */
export interface ExecRun {
  /** The id the route and the store key on. */
  id: string;
  label: string;
  /** The overall state, NOT derivable from the nodes: a run can be `paused` with
   *  every node settled, and only the source knows its own answer. */
  state: ExecState;
  /** The tree's roots, a list so several top-level nodes need no synthetic parent. */
  nodes: ExecNode[];
  /** What this execution was asked to do. */
  inputs?: Record<string, string>;
  /** One line about why the execution wants a person, pre-composed by the adapter. */
  alert?: { kind: "input" | "paused" | "stopped" | "failed"; text: string };
  /** The node to open on before a click. A subagent expansion has one door PER delegate, so a
   *  stage's link opens THAT stage; naming a node stops the auto-follow like a click. Absent for
   *  a workflow run. */
  focus?: string;
  /** Whether anything is still moving, so the page knows to run its clock. */
  live: boolean;
}

/** Every node, depth-first, in plan order. */
export function flatten(nodes: readonly ExecNode[]): ExecNode[] {
  const out: ExecNode[] = [];
  const walk = (n: ExecNode): void => {
    out.push(n);
    for (const k of n.children) {
      walk(k);
    }
  };
  for (const n of nodes) {
    walk(n);
  }
  return out;
}

/** The nodes that DO work, every pass included: a container's duration is its children's span,
 *  so counting it would double-count time and inflate the step total. */
export function workNodes(nodes: readonly ExecNode[]): ExecNode[] {
  return flatten(nodes).filter(isWork);
}

/** A container's members as the plan stands NOW: a repeat's latest pass, anything else its
 *  children. */
export function currentMembers(n: Pick<ExecNode, "kind" | "children">): readonly ExecNode[] {
  if (n.kind !== "repeat") {
    return n.children;
  }
  const pass = latestPass(n);
  return pass === undefined ? [] : [pass];
}

/** Every node of the plan as it stands NOW, depth-first: a repeat through its latest pass only. */
function currentNodes(nodes: readonly ExecNode[]): ExecNode[] {
  const out: ExecNode[] = [];
  const walk = (n: ExecNode): void => {
    out.push(n);
    if (!isWork(n)) {
      currentMembers(n).forEach(walk);
    }
  };
  for (const n of nodes) {
    walk(n);
  }
  return out;
}

/** The work of the plan as it stands NOW: every work node once, a repeat through its latest pass
 *  only, so "step N of M" counts one pass and the pass number stays the loop's. */
export function currentWork(nodes: readonly ExecNode[]): ExecNode[] {
  return currentNodes(nodes).filter(isWork);
}

/** The node a failed run's alert names: the first failed work node of the plan as it stands now,
 *  else the first failed container in it. A failure an earlier pass moved past is that pass's, not
 *  the run's, so it is never named. */
export function failureOwner(nodes: readonly ExecNode[]): ExecNode | undefined {
  const named = (n: ExecNode): boolean => n.state === "fail" && n.failure !== undefined;
  const now = currentNodes(nodes);
  return now.find((n) => isWork(n) && named(n)) ?? now.find(named);
}

export interface ExecCounters {
  total: number;
  done: number;
  failed: number;
  /** The 1-based position of the first IN-FLIGHT work node, or 0 (a skipped one or a parallel node
   *  breaks `done + 1`). */
  current: number;
}

/** The step counter over the current pass (`currentWork`). */
export function counters(nodes: readonly ExecNode[]): ExecCounters {
  const ls = currentWork(nodes);
  let done = 0;
  let failed = 0;
  let current = 0;
  ls.forEach((n, i) => {
    if (n.state === "ok" || n.state === "skipped") {
      done++;
    }
    if (n.state === "fail") {
      failed++;
    }
    // A paused or unknown step is where the run is, as much as a running one.
    if (current === 0 && inFlight(n.state)) {
      current = i + 1;
    }
  });
  return { total: ls.length, done, failed, current };
}

/** Elapsed ms between two ISO stamps, to NOW when only the end is absent; 0 when unusable. */
export function elapsed(start: string | undefined, end: string | undefined): number {
  if (start === undefined || start === "") {
    return 0;
  }
  const from = Date.parse(start);
  if (Number.isNaN(from)) {
    return 0;
  }
  const to = end === undefined || end === "" ? Date.now() : Date.parse(end);
  if (Number.isNaN(to)) {
    return 0;
  }
  return Math.max(0, to - from);
}

/** The execution's window, earliest start to latest end (or now). From the WORK nodes: a source
 *  may not stamp its containers. */
export interface ExecWindow {
  from: number;
  to: number;
  span: number;
}

export function window(nodes: readonly ExecNode[], live: boolean): ExecWindow | undefined {
  let from = Number.POSITIVE_INFINITY;
  let to = 0;
  for (const n of workNodes(nodes)) {
    if (n.start === undefined) {
      continue;
    }
    const s = Date.parse(n.start);
    if (Number.isNaN(s)) {
      continue;
    }
    from = Math.min(from, s);
    const e = n.end === undefined ? Date.now() : Date.parse(n.end);
    to = Math.max(to, Number.isNaN(e) ? s : e);
  }
  if (!Number.isFinite(from)) {
    return undefined;
  }
  if (live) {
    to = Math.max(to, Date.now());
  }
  // A 1ms floor so a one-tick run still divides and its bars have a width.
  return { from, to, span: Math.max(1, to - from) };
}
