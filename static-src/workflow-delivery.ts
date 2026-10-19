import { el } from "@cplieger/reactive";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";
import { chevronEl } from "./chevron.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { iconEl } from "./icon-el.js";
import { ICON_TAB_RUN } from "./icons.js";
import { payloadOf } from "./turns.js";
import type { EntrySteer, EntrySteerDelivered, TurnState } from "./types.js";

/** The id prefix of a workflow message marotte logs at its send, which waits for its settlement (twin
 *  of `marotte.WorkflowMessageIDPrefix`). Any other step notice is logged when KAS reads it. */
export const WORKFLOW_MESSAGE_ID_PREFIX = "wfmsg-";

/** The row as its settlement leaves it: the server's own rule (`WorkflowMessageFold.Settle`). */
function settledSteer(steer: EntrySteer, d: EntrySteerDelivered): EntrySteer {
  const waiting = waitingSteer(steer);
  return d.cleared === true
    ? { ...waiting, cleared: true }
    : { ...waiting, state: "read", read_ts: d.read_ts };
}

/** A row as it stood before any settlement: waiting, with no state. */
function waitingSteer(steer: EntrySteer): EntrySteer {
  const next: EntrySteer = { ...steer };
  delete next.state;
  delete next.reason;
  delete next.read_ts;
  delete next.cleared;
  return next;
}

/** `turns` in `order`, then any held turn `order` does not name: the file order the folds read. */
export function turnsInOrder(
  order: readonly string[],
  turns: ReadonlyMap<string, TurnState>,
): TurnState[] {
  const out: TurnState[] = [];
  const seen = new Set<string>();
  for (const id of order) {
    const t = turns.get(id);
    if (t !== undefined && !seen.has(id)) {
      seen.add(id);
      out.push(t);
    }
  }
  for (const [id, t] of turns) {
    if (!seen.has(id)) {
      out.push(t);
    }
  }
  return out;
}

/** Return every held row a released turn's settlement or clear settled to waiting, then fold the
 *  held turns (in file order) again: a rewind takes the released entries out of the history the
 *  rows are read against, as the server's read does, and an earlier clear still settles its rows. */
export function releaseWorkflowTakeUps(
  released: Iterable<TurnState>,
  held: Iterable<TurnState>,
): void {
  const named = new Set<string>();
  const sessions = new Set<string>();
  for (const t of released) {
    for (const e of t.entries) {
      const d = payloadOf(e, "steer_delivered");
      if (d !== undefined) {
        named.add(d.steer_id);
      }
      const c = payloadOf(e, "steer_cleared");
      if (c !== undefined) {
        sessions.add(c.session);
      }
    }
  }
  if (named.size === 0 && sessions.size === 0) {
    return;
  }
  const kept = [...held];
  for (const t of kept) {
    for (const e of t.entries) {
      const steer = payloadOf(e, "steer");
      if (
        steer !== undefined &&
        (named.has(e.id) || (steer.cleared === true && sessions.has(steer.to_session ?? "")))
      ) {
        e.payload = waitingSteer(steer);
      }
    }
  }
  settleWorkflowMessages(kept);
}

/** Every settlement `turns` hold, by the row it names, folded in file order by the server's rule
 *  (`marotte.FoldWorkflowMessages`): a clear settles cleared each row sent to its session before it
 *  that nothing settled, and a later take-up replaces that. A row the server already settled is
 *  left as it reads. */
function settlementsOf(turns: Iterable<TurnState>): Map<string, EntrySteerDelivered> {
  const out = new Map<string, EntrySteerDelivered>();
  const rows: { id: string; steer: EntrySteer }[] = [];
  for (const t of turns) {
    for (const e of t.entries) {
      const steer = payloadOf(e, "steer");
      if (steer !== undefined && e.id.startsWith(WORKFLOW_MESSAGE_ID_PREFIX)) {
        rows.push({ id: e.id, steer });
      }
      const d = payloadOf(e, "steer_delivered");
      if (d !== undefined) {
        out.set(d.steer_id, d);
      }
      const c = payloadOf(e, "steer_cleared");
      if (c === undefined) {
        continue;
      }
      for (const r of rows) {
        const to = r.steer.to_session ?? "";
        const settled = out.has(r.id) || r.steer.state !== undefined || r.steer.cleared === true;
        if (to !== "" && to === c.session && !settled) {
          out.set(r.id, { steer_id: r.id, read_ts: e.ts, cleared: true });
        }
      }
    }
  }
  return out;
}

/** Fold every settlement and clear the turns hold onto the row it names, both resident, so a
 *  remount reads the settled row. `turns` come in file order (`turnsInOrder`), since a clear settles
 *  only the rows before it. Idempotent, so any arrival may run it: an appended row, take-up or
 *  clear, an installed page. */
export function settleWorkflowMessages(turns: Iterable<TurnState>): void {
  const held = [...turns];
  const takeUps = settlementsOf(held);
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
      if (
        next.state !== steer.state ||
        next.read_ts !== steer.read_ts ||
        next.cleared !== steer.cleared
      ) {
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

/** The box's header: the one run named when every row comes from it, else the runs counted, and only
 *  when every row names its own, so the count never undercounts. */
function groupLabel(rows: readonly HTMLElement[]): string {
  const runs = rows.map((r) => r.dataset["run"] ?? "");
  if (runs.includes("")) {
    return `${String(rows.length)} workflow updates`;
  }
  const n = new Set(runs).size;
  const name = rows[0]?.dataset["runLabel"] ?? "";
  if (n === 1 && name !== "") {
    return `${String(rows.length)} updates from ${name}`;
  }
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
