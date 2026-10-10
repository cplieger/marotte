import { nodesOfType, type RunNode } from "./run-store.js";

/** What a run is waiting on a PERSON for. KAS leaves an asking run `running`, so `inspect` cannot
 *  show it; injected from `decision-dock.ts` (a feature module). */
export interface RunAsks {
  /** How many of this run's asks are unanswered. */
  count: number;
  /** One address per ask, which `askedSteps` resolves. Separate from `count` because the wire cannot
   *  always attribute one (the step-session registry has to have seen the sub-session), and a run
   *  with an unattributable ask is still blocked. */
  asked: readonly RunAskAddress[];
  /** The head ask as one line, for the alert. "" when there is none to name. */
  label: string;
}

/** What an ask says about the step it came from; either id may be "". */
export interface RunAskAddress {
  nodeID: string;
  sessionID: string;
  /** A step's question (`run_input`), which the run tab's composer answers. */
  answer: boolean;
}

export interface AskedSteps {
  /** The executions waiting on a person. */
  waiting: ReadonlySet<RunNode>;
  /** The paused executions whose question the run tab's words answer. */
  answerable: ReadonlySet<RunNode>;
}

/** The server's match (`runNow.askedStep` in internal/agent/run_message.go), so a step shows a question
 *  exactly where the server would answer one. A repeat's iterations share a node id, so a node id that
 *  several candidates carry names none. */
export function askedSteps(root: RunNode | undefined, asks: RunAsks): AskedSteps {
  const steps = nodesOfType(root, "step");
  const waiting = new Set<RunNode>();
  const answerable = new Set<RunNode>();
  for (const a of asks.asked) {
    const hit = askedStep(steps, a);
    if (hit === undefined) {
      continue;
    }
    waiting.add(hit);
    if (a.answer && hit.status === "paused") {
      answerable.add(hit);
    }
  }
  return { waiting, answerable };
}

/** A question naming no node is the run's only paused step's; any other ask names its node. */
function askedStep(steps: readonly RunNode[], a: RunAskAddress): RunNode | undefined {
  if (a.sessionID !== "") {
    const own = steps.find((n) => n.sessionId === a.sessionID);
    if (own !== undefined) {
      return own;
    }
  }
  if (!a.answer && a.nodeID === "") {
    return undefined;
  }
  const candidates = steps.filter(
    (n) =>
      (a.nodeID === "" || n.nodeId === a.nodeID) &&
      (n.status === "paused" || (!a.answer && n.status === "running")),
  );
  return candidates.length === 1 ? candidates[0] : undefined;
}
