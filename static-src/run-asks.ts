// The one store of workflow steps waiting on a person: each run's newest `run_asks` snapshot,
// replaced whole. Every surface that shows a step's question (the run tab, the run bar, the run
// dots, the notifications) reads it, and nothing else says a step is waiting.

import { join } from "@cplieger/keyenc";
import { signal, touch } from "@cplieger/reactive";
import { nodePathKey, nodePathSegment } from "./run-node-key.js";
import type { RunNode } from "./run-store.js";
import type { RunAsk, RunAsksPayload } from "./wire/types.gen.js";

/** By workflow id. A run is absent once its final frame (no asks, reachable) lands, or once a
 *  `pending_snapshot` no longer names it: either way the server answers for it no more. */
const snapshots = new Map<string, RunAsksPayload>();

const version = signal(0);

function bump(): void {
  version.value = version.peek() + 1;
}

/**
 * Take one run's snapshot unless the one held is newer: a different epoch always replaces, and
 * within one epoch only a larger `seq` does. False when the frame was stale and changed nothing.
 * One run's frames reach a stream in publish order and `seq` grows across a run's cells, so an
 * absent run admits any frame.
 */
export function applyRunAsks(p: RunAsksPayload): boolean {
  const held = snapshots.get(p.workflow_id);
  if (held?.epoch === p.epoch && p.seq <= held.seq) {
    return false;
  }
  if (p.asks.length === 0 && p.reachable) {
    if (snapshots.delete(p.workflow_id)) {
      bump();
    }
    return true;
  }
  snapshots.set(p.workflow_id, p);
  bump();
  return true;
}

/** Forget every run, for a `pending_snapshot` about to re-offer the whole set. */
export function dropAllRunAsks(): void {
  if (snapshots.size === 0) {
    return;
  }
  snapshots.clear();
  bump();
}

/** One run's snapshot, subscribing the caller. */
export function runAsksOf(workflowID: string): RunAsksPayload | undefined {
  touch(version);
  return snapshots.get(workflowID);
}

/** Every run's snapshot in arrival order, subscribing the caller. */
export function allRunAsks(): readonly RunAsksPayload[] {
  touch(version);
  return [...snapshots.values()];
}

/** An ask's wait position, the server's `workflow.AskPosition`: its id without the turn marker, so
 *  a re-park of the same wait keeps it. Notification tags and drafts key on it. */
export function runAskPosition(workflowID: string, ask: RunAsk): string {
  return join(workflowID, nodePathKey(ask.node_path), ask.step_session_id);
}

/** What a run is waiting on a person for, folded for the run's surfaces: its steps' questions from
 *  this store plus its steps' request-shaped asks from the dock (`decision-dock.ts`
 *  `runPendingAsks`). */
export interface RunAsks {
  /** How many of this run's asks are unanswered. */
  count: number;
  /** One address per ask, which `askedSteps` resolves. Separate from `count` because a
   *  request-shaped ask cannot always be attributed (the step-session registry has to have seen
   *  the sub-session), and a run with an unattributable ask is still blocked. */
  asked: readonly RunAskAddress[];
  /** The head ask as one line, for the alert. "" when there is none to name. */
  label: string;
}

/** What an ask says about the step it came from; any field may be empty. */
export interface RunAskAddress {
  nodeID: string;
  sessionID: string;
  /** A step's question from this store, which the run tab's composer answers. */
  answer: boolean;
  /** A step question's node path key (`nodePathKey`). */
  path?: string;
  /** A step question's ask id, which words sent to that step name. */
  askID?: string;
  /** What the ask says, whole; absent where the source has no text. */
  text?: string;
}

export interface AskedSteps {
  /** The executions waiting on a person. */
  waiting: ReadonlySet<RunNode>;
  /** The paused executions whose question the run tab's words answer, each with its ask id. */
  answerable: ReadonlyMap<RunNode, string>;
  /** Each address's step as a node path key (`nodePathKey`), index-aligned with `RunAsks.asked`;
   *  undefined where the server's match places none. */
  paths: readonly (string | undefined)[];
}

interface PlacedStep {
  readonly node: RunNode;
  readonly key: string;
}

/** Every step with its path key, spelled as the server's `workflow.Steps` walk spells it. */
function placedSteps(root: RunNode | undefined): PlacedStep[] {
  const out: PlacedStep[] = [];
  const walk = (n: RunNode, parent: RunNode | undefined, trail: readonly string[]): void => {
    const path = [...trail, nodePathSegment(n, parent)];
    if (n.type === "step") {
      out.push({ node: n, key: nodePathKey(path) });
    }
    for (const child of n.children ?? []) {
      walk(child, n, path);
    }
  };
  if (root !== undefined) {
    walk(root, undefined, []);
  }
  return out;
}

/** The server's match, so a step shows a question exactly where the server would answer one: by
 *  step session, else by a path or node id only one candidate carries (a repeat's iterations share
 *  a node id, a fan-out's executions a path). */
export function askedSteps(root: RunNode | undefined, asks: RunAsks): AskedSteps {
  const steps = placedSteps(root);
  const waiting = new Set<RunNode>();
  const answerable = new Map<RunNode, string>();
  const paths = asks.asked.map((a) => {
    const hit = askedStep(steps, a);
    if (hit === undefined) {
      return undefined;
    }
    waiting.add(hit.node);
    if (a.answer && hit.node.status === "paused") {
      answerable.set(hit.node, a.askID ?? "");
    }
    return hit.key;
  });
  return { waiting, answerable, paths };
}

function askedStep(steps: readonly PlacedStep[], a: RunAskAddress): PlacedStep | undefined {
  if (a.sessionID !== "") {
    const own = steps.find((s) => s.node.sessionId === a.sessionID);
    if (own !== undefined) {
      return own;
    }
  }
  const candidates = a.answer
    ? steps.filter((s) => a.path !== undefined && s.key === a.path)
    : steps.filter(
        (s) =>
          a.nodeID !== "" &&
          s.node.nodeId === a.nodeID &&
          (s.node.status === "paused" || s.node.status === "running"),
      );
  return candidates.length === 1 ? candidates[0] : undefined;
}

/** Reset the store between tests. */
// deadset:ignore DS1004 -- test seam: resets the snapshots
export function _resetRunAsksForTest(): void {
  snapshots.clear();
  version.value = 0;
}
