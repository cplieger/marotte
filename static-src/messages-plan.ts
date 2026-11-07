import type { PlanEntry } from "./types.js";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { el } from "@cplieger/ui";
import { CHROME_ATTR } from "./chrome-attr.js";

/** Build the plan card shell and reconcile entries into it. */
export function planElement(entries: readonly PlanEntry[]): HTMLDivElement {
  const card = el("div", { className: "plan-message" }) as HTMLDivElement;

  // The card's wording is chrome: find searches each entry's content, the server's plan segments.
  const header = el("div", { className: "plan-header", [CHROME_ATTR]: "" }, "Plan");
  card.appendChild(header);

  const list = el("div", { className: "plan-entries" }) as HTMLDivElement;
  card.appendChild(list);
  reconcilePlanEntries(list, entries);

  return card;
}

/** Update an existing plan element's entries in place. */
export function updatePlanElement(el: HTMLDivElement, entries: readonly PlanEntry[]): void {
  const list = el.querySelector<HTMLDivElement>(":scope > .plan-entries");
  if (list !== null) {
    reconcilePlanEntries(list, entries);
  }
}

const planEntrySpec: ReconcileSpec<PlanEntry> = {
  key: (e) => e.content,
  mount: (e) => buildPlanRow(e),
  update: (el, e) => {
    updatePlanRow(el as HTMLDivElement, e);
  },
};

function reconcilePlanEntries(list: HTMLDivElement, entries: readonly PlanEntry[]): void {
  reconcile(list, entries, planEntrySpec);
}

export function buildPlanRow(e: PlanEntry): HTMLDivElement {
  const row = el("div", { className: "plan-entry" }) as HTMLDivElement;
  updatePlanRow(row, e);
  return row;
}

function updatePlanRow(row: HTMLDivElement, e: PlanEntry): void {
  const icon = e.status === "completed" ? "✅" : e.status === "in_progress" ? "🔄" : "⬜";
  row.replaceChildren(
    el("span", { [CHROME_ATTR]: "" }, `${icon} `),
    e.content,
    ...(e.priority === "high"
      ? [el("span", { [CHROME_ATTR]: "" }, " ", el("span", { className: "plan-hi" }, "[high]"))]
      : []),
  );
  row.dataset["status"] = e.status;
}
