// ---------------------------------------------------------------------------
// Task list pill: shows the agent's running plan as a checklist in the
// prompt bar. Badge shows pending + in-progress count. Popover shows
// all plan entries with status indicators.
// ---------------------------------------------------------------------------

import type { PlanEntry, Session } from "./types.js";
import { getActive, watchActiveId, messagesVersionOf } from "./store.js";
import { payloadOf } from "./turns.js";
import { effect, el } from "@cplieger/reactive";
import { reconcile } from "./reconcile.js";
import { paintStatus } from "./fundamentals/work-status.js";

export function initTaskListPill(): void {
  effect(() => {
    // The transcript effect's two inputs, for the same reason it has two: the
    // body below reads `getActive()`, which is a `peek` and subscribes to
    // nothing, so the version alone left the pill showing the PREVIOUS chat's
    // plan until some chat happened to bump it. Versions are per chat now, so
    // the pill tracks the ACTIVE chat's — and the switch itself via the tracked
    // active id.
    const id = watchActiveId();
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    messagesVersionOf(id).value;
    refreshTaskList();
  });
}

/** The plan in force: the newest `plan` entry of the newest turn holding one.
 *  A later plan frame is its own entry, so the last one appended wins. */
function latestPlan(s: Session): PlanEntry[] {
  for (let i = s.turn_order.length - 1; i >= 0; i--) {
    const turnID = s.turn_order[i];
    const t = turnID === undefined ? undefined : s.turns.get(turnID);
    if (t === undefined) {
      continue;
    }
    for (let j = t.entries.length - 1; j >= 0; j--) {
      const e = t.entries[j];
      const entries = e === undefined ? undefined : payloadOf(e, "plan")?.entries;
      if (entries !== undefined && entries.length > 0) {
        return entries;
      }
    }
  }
  return [];
}

function refreshTaskList(): void {
  const pill = document.getElementById("task-list-pill");
  const badge = document.getElementById("task-list-badge");
  const items = document.getElementById("task-list-items");
  if (pill === null || badge === null || items === null) {
    return;
  }

  const session = getActive();
  if (session === undefined) {
    pill.classList.add("hidden");
    return;
  }

  const plan = latestPlan(session);

  if (plan.length === 0) {
    pill.classList.add("hidden");
    return;
  }

  pill.classList.remove("hidden");

  // Badge: count of pending + in_progress items.
  const active = plan.filter((e) => e.status !== "completed").length;
  if (active > 0) {
    badge.textContent = String(active);
    badge.classList.remove("hidden");
  } else {
    badge.classList.add("hidden");
  }

  // Popover items. Key by content (status changes preserve identity);
  // status flips into the row's class via the spec's update.
  reconcile(items, plan, {
    key: (e) => e.content,
    mount: (e) => buildTaskRow(e),
    update: (row, e) => {
      row.className = `task-item task-${e.status}`;
      const icon = row.querySelector<HTMLElement>(".task-icon");
      if (icon !== null) {
        paintStatus(icon, e.status);
      }
    },
  });
}

function buildTaskRow(entry: PlanEntry): HTMLElement {
  const icon = el("span", { className: "task-icon" });
  paintStatus(icon, entry.status);
  return el(
    "div",
    { className: `task-item task-${entry.status}` },
    icon,
    el("span", { className: "task-text" }, entry.content),
  );
}
