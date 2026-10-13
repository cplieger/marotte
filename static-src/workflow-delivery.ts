import { el } from "@cplieger/reactive";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";
import { chevronEl } from "./chevron.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { iconEl } from "./icon-el.js";
import { ICON_TAB_RUN } from "./icons.js";
import { payloadOf } from "./turns.js";
import type { EntrySteer, EntrySteerDelivered, TurnState } from "./types.js";

/** The id prefix of a workflow message marotte logs at its send, which waits for a take-up (twin of
 *  `marotte.WorkflowMessageIDPrefix`). Any other step notice is logged when KAS reads it. */
export const WORKFLOW_MESSAGE_ID_PREFIX = "wfmsg-";

/** The row as its take-up settles it: the server's own rule (`WorkflowMessageFold.Settle`). */
function settledSteer(steer: EntrySteer, d: EntrySteerDelivered): EntrySteer {
  return d.dropped === true
    ? { ...steer, state: "dropped", reason: "boundary" }
    : { ...steer, state: "read", read_ts: d.read_ts };
}

/** A row as it stood before any take-up: the server settles only a row still waiting, which
 *  carries no state. */
function waitingSteer(steer: EntrySteer): EntrySteer {
  const next: EntrySteer = { ...steer };
  delete next.state;
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
 *  messages or workflow results moves into one box whose header counts them, the tool group's
 *  box-in-box shape. The run at the container's tail starts open, every other one closed, until the
 *  reader picks. Idempotent: a row already seated in its box is never re-seated, so its focus and
 *  its entry animation survive the next pass. */
export function groupWorkflowUpdates(container: HTMLElement): void {
  let run: HTMLElement[] = [];
  let boxes: HTMLElement[] = [];
  const flush = (atTail: boolean): void => {
    if (run.length >= 2) {
      seatGroup(container, run, boxes, atTail);
    } else {
      for (const box of boxes) {
        unseatGroup(box);
      }
    }
    run = [];
    boxes = [];
  };
  for (const kid of Array.from(container.children)) {
    if (kid instanceof HTMLElement && kid.classList.contains(GROUP_CLASS)) {
      boxes.push(kid);
      run.push(...groupRows(kid));
    } else if (kid instanceof HTMLElement && isWorkflowUpdate(kid)) {
      run.push(kid);
    } else {
      flush(false);
    }
  }
  flush(true);
}

/** Selects what `isWorkflowUpdate` accepts, for finding the containers to regroup: a step's message
 *  and every agent note, which the transcript labels a workflow result. */
export const WORKFLOW_UPDATE_SELECTOR =
  '.steer-note[data-origin="step"][data-step], .steer-note[data-origin="agent"]';

const GROUP_CLASS = "steer-group";

/** The container a workflow-update row is grouped in: its box's host when a box holds it. */
export function workflowUpdateHost(row: HTMLElement): HTMLElement | null {
  const parent = row.parentElement;
  return parent?.classList.contains("steer-group-body") === true
    ? (parent.parentElement?.parentElement ?? null)
    : parent;
}

function isWorkflowUpdate(el: HTMLElement): boolean {
  return el.matches(WORKFLOW_UPDATE_SELECTOR);
}

interface GroupView {
  count: HTMLElement;
  body: HTMLElement;
  ctl: DisclosureController;
  /** The reader's pick rides the lead row, so a box rebuilt around it keeps the choice. */
  lead: () => HTMLElement | undefined;
}

const groups = new WeakMap<HTMLElement, GroupView>();

function groupBodyOf(box: HTMLElement): HTMLElement | null {
  return box.querySelector<HTMLElement>(":scope > .steer-group-body");
}

function groupRows(box: HTMLElement): HTMLElement[] {
  const body = groupBodyOf(box);
  return body === null
    ? []
    : Array.from(body.children).filter(
        (r): r is HTMLElement => r instanceof HTMLElement && isWorkflowUpdate(r),
      );
}

function seatGroup(
  container: HTMLElement,
  rows: HTMLElement[],
  boxes: HTMLElement[],
  atTail: boolean,
): void {
  const lead = rows[0];
  if (lead === undefined) {
    return;
  }
  const picked = lead.dataset["groupOpen"];
  const open = picked === undefined ? atTail : picked === "true";
  const [keep, ...extra] = boxes;
  const box = keep ?? buildGroup(open);
  const view = groups.get(box);
  if (view === undefined) {
    return;
  }
  if (keep === undefined) {
    container.insertBefore(box, lead);
  }
  rows.forEach((row, i) => {
    if (view.body.children[i] !== row) {
      view.body.insertBefore(row, view.body.children[i] ?? null);
    }
  });
  for (const stale of extra) {
    stale.remove();
  }
  view.count.textContent = groupLabel(rows);
  view.lead = () => lead;
  if (open !== view.ctl.isOpen) {
    if (open) {
      view.ctl.open();
    } else {
      view.ctl.close();
    }
  }
}

/** The box's header: the runs counted only when every row names its own, so the count never undercounts. */
function groupLabel(rows: readonly HTMLElement[]): string {
  const runs = rows.map((r) => r.dataset["run"] ?? "");
  if (runs.includes("")) {
    return `${String(rows.length)} workflow updates`;
  }
  const n = new Set(runs).size;
  return `${String(rows.length)} updates from ${String(n)} ${n === 1 ? "workflow" : "workflows"}`;
}

/** A box whose run fell under two rows hands its rows back to the container, in place. */
function unseatGroup(box: HTMLElement): void {
  for (const row of groupRows(box)) {
    box.before(row);
  }
  box.remove();
}

function buildGroup(open: boolean): HTMLElement {
  const count = el("span", { className: "steer-group-count" });
  const head = el(
    "div",
    { className: "steer-group-header", [CHROME_ATTR]: "" },
    chevronEl(),
    el("span", { className: "steer-group-icon tool-icon" }, iconEl(ICON_TAB_RUN)),
    count,
  );
  const body = el("div", { className: "steer-group-body" });
  const box = el("div", { className: GROUP_CLASS }, head, body);
  const view: GroupView = {
    count,
    body,
    lead: () => undefined,
    ctl: createDisclosure(head, body, {
      open,
      onToggle: (open, source) => {
        const lead = view.lead();
        if (source === "user" && lead !== undefined) {
          lead.dataset["groupOpen"] = String(open);
        }
      },
    }),
  };
  groups.set(box, view);
  return box;
}
