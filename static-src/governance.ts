// The one client owner of the governance snapshot (GET /api/governance plus the governance_state SSE), the
// org-policy disclosure and the setting locks.

import { el } from "@cplieger/reactive";
import { onSSE } from "./bus.js";
import { apiGetTyped } from "./api-client.js";
import { decodeGovernanceStatePayload } from "./wire/decoders.gen.js";
import type { GovernanceStatePayload, GovernanceFeatures, GovernanceLock } from "./types.js";

let current: GovernanceStatePayload | null = null;
const listeners = new Set<(g: GovernanceStatePayload) => void>();

/** The latest known governance state, or null before the first read. */
// deadset:ignore DS1004 -- test seam: observes the held governance snapshot
export function currentGovernance(): GovernanceStatePayload | null {
  return current;
}

/** True only when governance is known and the feature is off. The all-false zero value means unknown, not off. */
export function featureDisabled(key: keyof GovernanceFeatures): boolean {
  return current !== null && current.known && !current.features[key];
}

/**
 * The lock an administrator places on a setting, by the server's Lock* constants, or undefined. Not gated on
 * `known`: the machine administrator's rules lock settings on any account.
 */
function settingLock(key: string): GovernanceLock | undefined {
  return current?.locks?.[key];
}

/**
 * Why KAS's `disabledReason` says a feature is off. `admin`: the organization turned it off, or the reason is absent
 * or unknown. `unavailable`: KAS failed closed reading the organization's settings, which must not read as an
 * administrator decision. The raw token is never shown.
 */
type GovernanceReasonKind = "admin" | "unavailable";

export function governanceReasonKind(reason: string | undefined): GovernanceReasonKind {
  return reason === "api_failure" || reason === "no_endpoint" ? "unavailable" : "admin";
}

/** The sentence an `unavailable` reason reads as. */
export const GOVERNANCE_UNAVAILABLE = "Couldn't load your organization's settings.";

/**
 * Subscribe to governance changes. Fires immediately when a state is known, then on every update. No unsubscribe:
 * the consumers live for the app's lifetime.
 */
export function onGovernanceChange(fn: (g: GovernanceStatePayload) => void): void {
  listeners.add(fn);
  if (current !== null) {
    fn(current);
  }
}

function set(g: GovernanceStatePayload): void {
  current = g;
  renderOrgPolicy(g);
  paintSettingLocks();
  document
    .getElementById("security-profile-admin")
    ?.classList.toggle("hidden", g.admin_restricted !== true);
  for (const fn of listeners) {
    fn(g);
  }
}

/**
 * Fetch the snapshot and subscribe to live updates; call once from app.ts. The snapshot may be Known=false until a
 * session pushes the policy over SSE.
 */
export function initGovernance(): void {
  onSSE("governance_state", (_chatID, p) => {
    set(p);
  });
  void apiGetTyped<GovernanceStatePayload>("/api/governance", decodeGovernanceStatePayload).then(
    (g) => {
      // A cold Known=false snapshot still carries the locks; consumers gate on `known` themselves. An SSE frame that landed
      // first is newer than this response.
      if (g !== null && current === null) {
        set(g);
      }
    },
  );
}

/** A locked switch shows the required value, cannot be flipped, and names who set it. */
const lockableSwitches: readonly { key: string; inputID: string }[] = [
  { key: "workflows_enabled", inputID: "flag-workflows" },
  { key: "inline_agents_enabled", inputID: "flag-inline-agents" },
  { key: "content_collection_enabled", inputID: "flag-content-collection" },
  { key: "telemetry.enabled", inputID: "flag-telemetry" },
];

/**
 * Write a switch's stored value: the only writer besides the user's click. While a lock pins the switch the value is
 * held on the element, and `paintSettingLocks` restores it on unlock; the attribute marks a switch painted locked.
 */
export function writeSwitch(input: HTMLInputElement, value: boolean): void {
  if (input.dataset["heldValue"] !== undefined) {
    input.dataset["heldValue"] = String(value);
    return;
  }
  input.checked = value;
}

/**
 * Paint every lockable switch from the current governance, on every change and after each load, so a lock always
 * wins over a stored value.
 */
export function paintSettingLocks(): void {
  for (const row of lockableSwitches) {
    const input = document.getElementById(row.inputID) as HTMLInputElement | null;
    const text = input?.closest(".section-option")?.querySelector<HTMLElement>(":scope > div");
    if (input === null || text === null || text === undefined) {
      continue;
    }
    const lock = settingLock(row.key);
    const noteID = `${row.inputID}-lock`;
    let note = document.getElementById(noteID);
    input.disabled = lock !== undefined;
    if (lock === undefined) {
      const held = input.dataset["heldValue"];
      if (held !== undefined) {
        input.checked = held === "true";
        delete input.dataset["heldValue"];
      }
      note?.remove();
      input.removeAttribute("aria-describedby");
      continue;
    }
    input.dataset["heldValue"] ??= String(input.checked);
    input.checked = lock.value;
    if (note === null) {
      note = el("p", { className: "section-hint setting-lock", id: noteID });
      text.appendChild(note);
    }
    note.textContent = lock.reason;
    input.setAttribute("aria-describedby", noteID);
  }
}

/** mcp_enabled has its own affordance in Settings → Tools. Privacy flags first, then capability flags with no surface. */
const POLICY_ROWS: readonly { key: keyof GovernanceFeatures; label: string }[] = [
  { key: "prompt_logging", label: "Prompt logging" },
  { key: "usage_analytics", label: "Usage analytics" },
  { key: "content_collection", label: "Content collection" },
  { key: "web_tools_enabled", label: "Built-in web tools" },
  { key: "autonomous_agents", label: "Autonomous agents" },
  { key: "code_reference_tracker", label: "Code-reference tracking" },
];

/** Shown only for a known enterprise policy: on a personal account no organization controls these flags. */
function renderOrgPolicy(g: GovernanceStatePayload): void {
  const section = document.getElementById("general-governance-section");
  if (section === null) {
    return;
  }
  if (!g.known || g.is_enterprise !== true) {
    section.hidden = true;
    section.replaceChildren();
    return;
  }
  section.hidden = false;

  const grid = el("dl", { className: "about-grid governance-grid" });
  for (const row of POLICY_ROWS) {
    grid.appendChild(el("dt", {}, row.label));
    grid.appendChild(policyValue(g.features[row.key]));
  }

  const children: HTMLElement[] = [
    el("h2", { className: "section-title" }, "Organization policy"),
    el(
      "p",
      { className: "section-hint" },
      "Controlled by your organization or account. Shown here for transparency, and Marotte cannot change it.",
    ),
    grid,
  ];
  if (governanceReasonKind(g.disabled_reason) === "unavailable") {
    children.push(el("p", { className: "governance-reason" }, GOVERNANCE_UNAVAILABLE));
  }
  section.replaceChildren(...children);
}

function policyValue(on: boolean): HTMLElement {
  return el("dd", { className: on ? "governance-on" : "governance-off" }, on ? "On" : "Off");
}
