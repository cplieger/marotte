// The exec view's model: delegated work as a tree of nodes over time. Workflow runs, subagents and
// pipelines each fold into `ExecRun` through an adapter (`run-exec-source.ts`); derived on read,
// held by nobody, and the wire shape of nothing.

import { join } from "@cplieger/keyenc";
import { STATE_WORD, inFlight, type ExecState } from "./status.js";
import type { RunStepMessageVerb } from "../wire/types.gen.js";

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
  /** A strict leading part of `label` naming the scope the node sits in, drawn dimmer than the
   *  rest (`label.ts`); `label` stays the whole verbatim name. */
  qualifier?: string;
  kind: ExecKind;
  state: ExecState;
  children: ExecNode[];
  /** ISO timestamps, when the source has them. For display: two sources can time one start
   *  differently, so `startGen` orders starts. */
  start?: string;
  end?: string;
  /** Orders this node's current start against the run's others: larger is later, and one start
   *  keeps its value however its time is re-read. Absent before it starts. */
  startGen?: number;
  /** The one-line summary under the label, pre-joined by the adapter. */
  subtitle?: string;
  /** The newest line the node printed, for a one-line glance; absent where the source has none. */
  tail?: string;
  /** The identity facts, for the detail pane. */
  facts?: ExecFact[];
  /** Why this node ended badly, never blank: the source's reason trimmed, or its outcome's default sentence. */
  failure?: string;
  /** What the node produced, as markdown. */
  output?: string;
  /** The card's result text: the capture verbatim, or a decoded watch's last output line (its
   *  subtitle says how it ended). */
  capture?: string;
  /** Named artifacts the node published. */
  artifacts?: Record<string, string>;
  /** What words sent to this execution do now, as the server decides it from the raw state that
   *  `state` folds an ask into; absent where the server would refuse them. */
  verb?: RunStepMessageVerb;
  /** The id of the question words sent here answer, beside an `answer` verb, so a newer question
   *  refuses them as stale. */
  ask?: string;
  /** Whether this node can host a live transcript, so the pane can say "none here" rather than
   *  "none yet". */
  transcript?: boolean;
  /** On a `repeat`'s child: which pass it is, 1-based. */
  pass?: number;
  /** On a `repeat`: the most passes it may run, an extension included, when the plan bounds it. */
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

/** One question a person is asked, whole. */
export interface ExecQuestion {
  text: string;
  /** The asking step's node path, when the source can place it. */
  path?: string;
  /** The asking step's label, for the region's name. */
  step?: string;
}

/** The whole execution, plus the facts its run dock states. */
export interface ExecRun {
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
  /** Every question waiting on a person, in the order the source lists them. */
  questions?: readonly ExecQuestion[];
  /** A standing line about the execution's plan, beside the alert. */
  notice?: { text: string; failed: boolean };
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
 *  only, so the step count covers one pass and the pass number stays the loop's. */
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

interface ExecCounters {
  total: number;
  done: number;
  failed: number;
  /** The 1-based position of the run's ONE in-flight work node (a skipped one or a parallel node
   *  breaks `done + 1`); 0 with none in flight, and with several, since parallel branches each stand
   *  somewhere else and the first one's position reads as barely started: the count is then `done`. */
  current: number;
}

/** The step counter over the current pass (`currentWork`), for every run surface. */
export function counters(nodes: readonly ExecNode[]): ExecCounters {
  const ls = currentWork(nodes);
  let done = 0;
  let failed = 0;
  const flying: number[] = [];
  ls.forEach((n, i) => {
    if (n.state === "ok" || n.state === "skipped") {
      done++;
    }
    if (n.state === "fail") {
      failed++;
    }
    // A paused or unknown step is where the run is, as much as a running one.
    if (inFlight(n.state)) {
      flying.push(i + 1);
    }
  });
  return { total: ls.length, done, failed, current: flying.length === 1 ? (flying[0] ?? 0) : 0 };
}

/** The work node with the largest `startGen`; one carrying none has not started. */
export function newestStarted(nodes: readonly ExecNode[]): ExecNode | undefined {
  let best: ExecNode | undefined;
  for (const n of workNodes(nodes)) {
    if (n.startGen !== undefined && (best?.startGen === undefined || n.startGen > best.startGen)) {
      best = n;
    }
  }
  return best;
}

/** A pass as every surface words it: "pass 3 of 8", or "pass 3" for a loop with no bound. */
export function passText(pass: number, max?: number): string {
  return max === undefined ? `pass ${String(pass)}` : `pass ${String(pass)} of ${String(max)}`;
}

/** A loop paused at its cap, "review reached its pass limit (3/3)". */
export function passLimitText(loop: string, ran: number, max: number): string {
  return `${loop} reached its pass limit (${String(ran)}/${String(max)})`;
}

/** A loop's budget, "up to 8 passes", with "(5 added)" when an extension grew it. */
export function passBudgetText(max: number, added: number): string {
  const bound = `up to ${String(max)} passes`;
  return added > 0 ? `${bound} (${String(added)} added)` : bound;
}

/** One repeat around a step: its node id, the pass the step is in, and its budget. */
interface LoopLevel {
  id: string;
  pass: number;
  max?: number;
  repeat: ExecNode;
}

/** The repeats enclosing `target`, outermost first; empty when none does. */
function loopsAround(nodes: readonly ExecNode[], target: ExecNode): readonly LoopLevel[] {
  let found: readonly LoopLevel[] = [];
  const walk = (n: ExecNode, around: readonly LoopLevel[], parent?: ExecNode): boolean => {
    const here =
      parent?.kind === "repeat" && n.pass !== undefined
        ? [
            ...around,
            parent.maxPasses === undefined
              ? { id: parent.label, pass: n.pass, repeat: parent }
              : { id: parent.label, pass: n.pass, max: parent.maxPasses, repeat: parent },
          ]
        : around;
    if (n === target) {
      found = here;
      return true;
    }
    return n.children.some((k) => walk(k, here, n));
  };
  nodes.some((n) => walk(n, []));
  return found;
}

/** What the progress phrase reads of a run. */
export type ProgressSource = Pick<ExecRun, "nodes" | "state">;

/** Where the run stands: every in-flight step of the plan as it stands, else the newest started
 *  one while work is still to come. A run no longer in flight stands nowhere, though a failure or a
 *  cancel leaves later steps pending. */
function standingSteps({ nodes, state }: ProgressSource): ExecNode[] {
  if (!inFlight(state)) {
    return [];
  }
  const now = currentWork(nodes);
  const flying = now.filter((n) => inFlight(n.state));
  if (flying.length > 0) {
    return flying;
  }
  const newest = now.some((n) => n.state === "pending") ? newestStarted(nodes) : undefined;
  return newest === undefined ? [] : [newest];
}

/** The loop levels every standing step shares, outermost first: the common prefix of their chains,
 *  a level shared when it is the same repeat at the same pass. Only a pass the whole run stands in
 *  is the run's, so a step in no loop shares none. */
function sharedLoops(nodes: readonly ExecNode[], at: readonly ExecNode[]): readonly LoopLevel[] {
  const chains = at.map((n) => loopsAround(nodes, n));
  const first = chains[0] ?? [];
  const sharedAt = (i: number): boolean =>
    chains.every((c) => c[i]?.repeat === first[i]?.repeat && c[i]?.pass === first[i]?.pass);
  let depth = 0;
  while (depth < first.length && sharedAt(depth)) {
    depth++;
  }
  return first.slice(0, depth);
}

/** The parallel branch a node sits in, named by the branch's own label: the child of its innermost
 *  enclosing `parallel` that holds it. */
function branchOf(nodes: readonly ExecNode[], target: ExecNode): string | undefined {
  let found: string | undefined;
  const walk = (n: ExecNode, branch: string | undefined, parent?: ExecNode): boolean => {
    const here = parent?.kind === "parallel" ? n.label : branch;
    if (n === target) {
      found = here;
      return true;
    }
    return n.children.some((k) => walk(k, here, n));
  };
  nodes.some((n) => walk(n, undefined));
  return found;
}

/** The levels the phrase names: the outermost repeat, then the innermost one past its first pass
 *  (a first pass is the normal case and adds nothing). */
function shownLoops(levels: readonly LoopLevel[]): { head?: LoopLevel; inner?: LoopLevel } {
  const head = levels[0];
  const inner = levels
    .slice(1)
    .reverse()
    .find((l) => l.pass > 1);
  return { ...(head === undefined ? {} : { head }), ...(inner === undefined ? {} : { inner }) };
}

const fraction = (n: number, max?: number): string =>
  max === undefined ? String(n) : `${String(n)}/${String(max)}`;

/** Where the run stands, in parts, for a surface that lays them out itself. */
export interface ProgressPhrase {
  /** The outermost loop's pass, "15/50" or "15" with no budget; "" outside a loop. */
  pass: string;
  /** The innermost inner loop past its first pass, "2/4"; "" when there is none. */
  inner: string;
  /** The step count, "9/27"; "" when withheld. */
  steps: string;
  /** Every level in words, with the loop ids: "tasks pass 15 of 50, build pass 1 of 4, step 1 of 4". */
  words: string;
  /** The tooltip: the words, or with branches in different passes each live loop with its branch,
   *  "track-a ↻2/3 · 8/14". */
  tip: string;
}

const NO_PROGRESS: ProgressPhrase = { pass: "", inner: "", steps: "", words: "", tip: "" };

function progressOf(run: ProgressSource, withSteps: boolean): ProgressPhrase {
  const c = counters(run.nodes);
  if (c.total === 0) {
    return NO_PROGRESS;
  }
  const at = c.current > 0 ? c.current : c.done;
  const steps = withSteps ? fraction(at, c.total) : "";
  const stepWords = !withSteps
    ? ""
    : c.current > 0
      ? `step ${String(c.current)} of ${String(c.total)}`
      : `${String(c.done)} of ${String(c.total)} steps done`;
  const standing = standingSteps(run);
  const levels = sharedLoops(run.nodes, standing);
  const { head, inner } = shownLoops(levels);
  const pass = head === undefined ? "" : fraction(head.pass, head.max);
  const innerText = inner === undefined ? "" : fraction(inner.pass, inner.max);
  if (levels.length > 0 || standing.length < 2) {
    const words = [...levels.map((l) => `${l.id} ${passText(l.pass, l.max)}`), stepWords]
      .filter((s) => s !== "")
      .join(", ");
    return { pass, inner: innerText, steps, words, tip: words };
  }
  // Branches in different passes: the headline names no pass, and the tooltip names each live loop
  // with its branch.
  const live = new Map<string, { words: string; tip: string }>();
  for (const n of standing) {
    const top = loopsAround(run.nodes, n)[0];
    if (top === undefined) {
      continue;
    }
    const branch = branchOf(run.nodes, n) ?? top.id;
    const key = join(branch, top.id, String(top.pass));
    live.set(key, {
      words: `${branch} ${top.id} ${passText(top.pass, top.max)}`,
      tip: `${branch} ${REPEAT_MARK}${fraction(top.pass, top.max)}`,
    });
  }
  const loops = [...live.values()];
  const words = [...loops.map((l) => l.words), stepWords].filter((s) => s !== "").join(", ");
  const tip = [loops.map((l) => l.tip).join(", "), steps].filter((s) => s !== "").join(" \u00b7 ");
  return { pass: "", inner: "", steps, words, tip };
}

/** The repeat glyph's text stand-in, for a surface that compares or prints the phrase as text. */
const REPEAT_MARK = "\u21bb";

/** The phrase as one compact string, "↻15/50 › 2/4 · 1/4"; "" for a plan with no work. */
export function compactText(p: ProgressPhrase): string {
  const loop =
    p.pass === "" ? "" : `${REPEAT_MARK}${p.pass}${p.inner === "" ? "" : ` \u203a ${p.inner}`}`;
  return [loop, p.steps].filter((s) => s !== "").join(" \u00b7 ");
}

/** Where the run stands, as every run surface words it, in parts: the outermost loop's pass and an
 *  inner loop past its first pass when the whole run stands in them, then the step count
 *  (`counters` decides between the in-flight position and how many are done). */
export function progressPhrase(run: ProgressSource): ProgressPhrase {
  return progressOf(run, true);
}

/** `progressPhrase` for the run summary, which withholds a one-step plan's count (1/1 cannot
 *  progress, and the state word says how it ended) and keeps the pass. */
export function summaryProgress(run: ProgressSource): ProgressPhrase {
  return progressOf(run, counters(run.nodes).total > 1);
}

/** One segment of the mini bar, one step; `branch` starts a sibling parallel branch. */
export interface MiniSeg {
  key: string;
  state: ExecState;
  tip: string;
  branch: boolean;
}

/** The mini bar under the phrase: one segment per step of the plan as it stands (`currentWork`, the
 *  set the step count counts) in plan order, inside a loop of any length; the pass is the phrase's. */
export function miniBar(run: Pick<ExecRun, "nodes">): MiniSeg[] {
  const splits = branchStarts(run.nodes);
  return currentWork(run.nodes).map((n) => ({
    key: n.path,
    state: n.state,
    tip: `${n.label}, ${STATE_WORD[n.state]}`,
    branch: splits.has(n),
  }));
}

/** The work nodes of the plan as it stands that open a sibling branch of a `parallel`: the first
 *  work of each branch after the first that holds any. */
function branchStarts(nodes: readonly ExecNode[]): Set<ExecNode> {
  const out = new Set<ExecNode>();
  const walk = (n: ExecNode): void => {
    if (isWork(n)) {
      return;
    }
    if (n.kind === "parallel") {
      let seen = false;
      for (const branch of n.children) {
        const first = currentWork([branch])[0];
        if (first !== undefined && seen) {
          out.add(first);
        }
        seen ||= first !== undefined;
      }
    }
    currentMembers(n).forEach(walk);
  };
  nodes.forEach(walk);
  return out;
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
interface ExecWindow {
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
