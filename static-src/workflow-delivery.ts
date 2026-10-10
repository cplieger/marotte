import { payloadOf } from "./turns.js";
import type { EntrySteer, EntrySteerDelivered, TurnState } from "./types.js";

/** The row as its take-up settles it: the server's own rule (`WorkflowMessageFold.Settle`). */
function settledSteer(steer: EntrySteer, d: EntrySteerDelivered): EntrySteer {
  return d.dropped === true
    ? { ...steer, state: "dropped", reason: "boundary" }
    : { ...steer, state: "read", read_ts: d.read_ts };
}

/** A row as it stood before any take-up: the server settles only a row still waiting. The wire's
 *  waiting state is the empty string, which the generated `SteerState` vocabulary leaves out. */
function waitingSteer(steer: EntrySteer): EntrySteer {
  const next: EntrySteer = { ...steer, state: "" as EntrySteer["state"] };
  delete next.read_ts;
  delete next.reason;
  return next;
}

/** Return every held row a released turn's take-up settled to waiting: a rewind takes the take-up
 *  out of the history the row is read against, as the server's read does. */
export function releaseWorkflowTakeUps(
  released: Iterable<TurnState>,
  held: Iterable<TurnState>,
): void {
  const named = new Set<string>();
  for (const t of released) {
    for (const e of t.entries) {
      const d = payloadOf(e, "steer_delivered");
      if (d !== undefined) {
        named.add(d.steer_id);
      }
    }
  }
  if (named.size === 0) {
    return;
  }
  for (const t of held) {
    for (const e of t.entries) {
      const steer = payloadOf(e, "steer");
      if (steer !== undefined && named.has(e.id)) {
        e.payload = waitingSteer(steer);
      }
    }
  }
}

/** Fold every take-up the turns hold onto the row it names, both resident, so a remount reads the
 *  settled row. Idempotent, so any arrival may run it: an appended row, an appended take-up, an
 *  installed page. */
export function settleWorkflowMessages(turns: Iterable<TurnState>): void {
  const held = [...turns];
  const takeUps = new Map<string, EntrySteerDelivered>();
  for (const t of held) {
    for (const e of t.entries) {
      const d = payloadOf(e, "steer_delivered");
      if (d !== undefined) {
        takeUps.set(d.steer_id, d);
      }
    }
  }
  if (takeUps.size === 0) {
    return;
  }
  for (const t of held) {
    for (const e of t.entries) {
      const steer = payloadOf(e, "steer");
      const d = takeUps.get(e.id);
      if (steer === undefined || d === undefined) {
        continue;
      }
      const next = settledSteer(steer, d);
      if (next.state !== steer.state || next.read_ts !== steer.read_ts) {
        e.payload = next;
      }
    }
  }
}

/** Group a container's consecutive workflow updates as Kiro does: a run of two or more step
 *  messages gets one toggle reading "N updates from M workflows". The run at the container's tail
 *  starts open, every other one closed, until the reader picks. */
export function groupWorkflowUpdates(container: HTMLElement): void {
  for (const stale of container.querySelectorAll(":scope > .steer-group-toggle")) {
    stale.remove();
  }
  const kids = Array.from(container.children) as HTMLElement[];
  let run: HTMLElement[] = [];
  const flush = (atTail: boolean): void => {
    if (run.length >= 2) {
      mountGroup(container, run, atTail);
    } else {
      for (const n of run) {
        n.hidden = false;
      }
    }
    run = [];
  };
  for (const kid of kids) {
    if (isWorkflowUpdate(kid)) {
      run.push(kid);
    } else {
      flush(false);
    }
  }
  flush(true);
}

/** Selects what `isWorkflowUpdate` accepts, for finding the containers to regroup. */
export const WORKFLOW_UPDATE_SELECTOR = '.steer-note[data-origin="step"][data-step]';

function isWorkflowUpdate(el: HTMLElement): boolean {
  return el.matches(WORKFLOW_UPDATE_SELECTOR);
}

function mountGroup(container: HTMLElement, rows: HTMLElement[], atTail: boolean): void {
  const lead = rows[0];
  if (lead === undefined) {
    return;
  }
  const picked = lead.dataset["groupOpen"];
  const open = picked === undefined ? atTail : picked === "true";
  const runs = new Set(rows.map((r) => r.dataset["run"] ?? ""));
  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "steer-group-toggle";
  toggle.textContent = `${String(rows.length)} updates from ${String(runs.size)} ${runs.size === 1 ? "workflow" : "workflows"}`;
  toggle.setAttribute("aria-expanded", String(open));
  toggle.addEventListener("click", () => {
    lead.dataset["groupOpen"] = String(toggle.getAttribute("aria-expanded") !== "true");
    groupWorkflowUpdates(container);
  });
  container.insertBefore(toggle, lead);
  for (const r of rows) {
    r.hidden = !open;
  }
}
