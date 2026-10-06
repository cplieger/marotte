// The exec view's model: delegated work as a tree of nodes over time. Workflow runs, subagents and
// pipelines each fold into `ExecRun` through an adapter (`run-exec-source.ts`); derived on read,
// held by nobody, and the wire shape of nothing.

import type { ExecState } from "./status.js";

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
  /** Why this node ended badly, verbatim. */
  failure?: string;
  /** What the node produced, as markdown. */
  output?: string;
  /** Named artifacts the node published. */
  artifacts?: Record<string, string>;
  /** Whether this node can host a live transcript, so the pane can say "none here" rather than
   *  "none yet". */
  transcript?: boolean;
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

/** The nodes that DO work: a container's duration is its children's span, so
 *  counting it would double-count time and inflate the step total. */
export function leaves(nodes: readonly ExecNode[]): ExecNode[] {
  return flatten(nodes).filter((n) => n.children.length === 0);
}

export interface ExecCounters {
  total: number;
  done: number;
  failed: number;
  /** The 1-based position of the RUNNING leaf, or 0 (a skipped leaf or a parallel node breaks
   *  `done + 1`). */
  current: number;
}

export function counters(nodes: readonly ExecNode[]): ExecCounters {
  const ls = leaves(nodes);
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
    if (current === 0 && (n.state === "running" || n.state === "input")) {
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

/** The execution's window, earliest start to latest end (or now). From the LEAVES: a source may
 *  not stamp its containers. */
export interface ExecWindow {
  from: number;
  to: number;
  span: number;
}

export function window(nodes: readonly ExecNode[], live: boolean): ExecWindow | undefined {
  let from = Number.POSITIVE_INFINITY;
  let to = 0;
  for (const n of leaves(nodes)) {
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
