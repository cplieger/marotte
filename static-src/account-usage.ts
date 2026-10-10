// Account usage (plan, credits, quota from KAS _kiro/account/getUsage) in the sidebar
// status popup, distinct from the per-chat context ring. Fetched lazily on popup open,
// throttled on top of the server's cache; a failure reads "Usage unavailable".

import { $ } from "./dom.js";
import { apiGetTyped } from "./api-client.js";
import { decodeAccountUsage } from "./wire/decoders.gen.js";
import type { AccountUsage, AccountUsageBreakdown } from "./types.js";

const CLIENT_TTL_MS = 30_000;
let lastFetch = 0;
let inflight = false;

/**
 * Fetch and render account usage, throttled to CLIENT_TTL_MS unless forced; safe on every
 * popup open.
 */
export function loadAccountUsage(force = false): void {
  const now = Date.now();
  if (inflight || (!force && now - lastFetch < CLIENT_TTL_MS)) {
    return;
  }
  inflight = true;
  lastFetch = now;
  void apiGetTyped<AccountUsage>("/api/account/usage", decodeAccountUsage)
    .then((u) => {
      renderAccountUsage(u);
    })
    .finally(() => {
      inflight = false;
    });
}

function primaryBreakdown(u: AccountUsage): AccountUsageBreakdown | undefined {
  const list = u.breakdowns;
  return list.find((b) => b.resource_type === "CREDIT") ?? list[0];
}

function fmtAmount(n: number): string {
  return n.toLocaleString(undefined, { maximumFractionDigits: 0 });
}

function unitLabel(b: AccountUsageBreakdown): string {
  // Credits render as "cr"; anything else uses its display name lowercased.
  if (b.resource_type === "CREDIT") {
    return "cr";
  }
  return (b.display_name ?? "").toLowerCase();
}

function renderAccountUsage(u: AccountUsage | null): void {
  const box = $.stAccount;
  const planEl = $.acctPlan;
  const meterEl = $.acctMeter;
  const overageEl = $.acctOverage;
  box.hidden = false;

  if (u === null) {
    planEl.textContent = "Usage unavailable";
    meterEl.textContent = "";
    meterEl.removeAttribute("data-tooltip");
    overageEl.textContent = "";
    return;
  }

  // Overage billing is read-only, so this line tells the reader the row's link can change
  // it. Written here because every arm below returns. Total because the wire field has no
  // `omitempty`; blank is reserved for the null arm above, where nothing is known.
  overageEl.textContent = u.overages_enabled ? "Overages on" : "Overages off";

  const plan =
    u.plan_name !== undefined && u.plan_name !== "" ? u.plan_name : (u.note ?? "Account");
  planEl.textContent = u.stale === true ? `${plan} (cached)` : plan;

  const b = primaryBreakdown(u);
  if (b === undefined) {
    // No usage line (e.g. admin-managed plan). The plan label already
    // carries the note, so leave the meter empty.
    meterEl.textContent = "";
    meterEl.removeAttribute("data-tooltip");
    return;
  }

  const used = fmtAmount(b.used);
  const unit = unitLabel(b);
  if (b.has_limit === true) {
    const limit = fmtAmount(b.limit);
    meterEl.textContent = `${used} / ${limit} ${unit} (${String(b.percentage)}%)`;
  } else {
    meterEl.textContent = `${used} ${unit}`;
  }
  if (u.billing_cycle_reset !== undefined && u.billing_cycle_reset !== "") {
    meterEl.dataset["tooltip"] = `Resets ${u.billing_cycle_reset}`;
  } else {
    meterEl.removeAttribute("data-tooltip");
  }
}
