// Structural guard for the per-setting save-status slots (static/index.html).

import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";
import settingsSrc from "./settings.ts?raw";
import { settingsPayload } from "./__test-helpers__/settings.js";

/** Every field of the settings payload the client reads. */
const SETTINGS_FIELDS = new Set(Object.keys(settingsPayload()));

/** Every setting that owns a slot, with the key its own write carries. */
const EXPECTED_KEYS = [
  // Notifications (settings-notifications.ts)
  "notifications_enabled",
  "notify_agent_finished",
  "notify_pr_status",
  "notify_run_outcome",
  // Chat retention — one value, two controls, so one slot on the section title
  "chat_retention_days",
  // Links (settings.ts panelSwitches)
  "guard_payload_links",
  "mcp_wait_for_ready",
  // Automatic compaction (settings.ts initAutoCompactionControls): switch and threshold
  "auto_compaction_enabled",
  "auto_compact_pct",
  // Agent capabilities (settings.ts agentCapabilities)
  "knowledge_enabled",
  "tool_search_enabled",
  "memory_mode",
  "spec_planning",
  "spec_planning_ask_first",
  "inline_agents_enabled",
  "steering_reminders_enabled",
  "workflows_enabled",
  "work_validation",
  "cloudformation_safety_check",
  "output_style",
  "terminal_command_timeout_ms",
  // Data sharing: marotte's content-collection switch and kiro-cli's telemetry
  "content_collection_enabled",
  "telemetry.enabled",
  // Experimental features: two kiro-cli keys plus marotte's own debug_logs
  "hooks.showStatus",
  "chat.disableInheritingDefaultResources",
  "debug_logs",
  // Permissions
  "supervised_default",
  "scheduled_auto_approve",
  "agent_ignore_files",
  // Custom instructions
  "steering",
] as const;

/** Written by `patchSettings` but deliberately slotless: each is set from outside Settings,
 *  where the control the user moved is the confirmation. */
const NO_SLOT_KEYS = ["theme", "fb_path", "last_model", "last_effort_by_model"] as const;

function slots(): HTMLElement[] {
  const host = document.createElement("div");
  host.innerHTML = indexHtml;
  return [...host.querySelectorAll<HTMLElement>("[data-save-status]")];
}

describe("per-setting save slots (static/index.html)", () => {
  it("declares exactly the reviewed set of keys, each once", () => {
    const found = slots().flatMap((s) => (s.dataset["saveStatus"] ?? "").split(/\s+/));
    expect([...found].sort()).toEqual([...EXPECTED_KEYS].sort());
  });

  it("gives every slot the class, the live region and a title row", () => {
    for (const slot of slots()) {
      const key = slot.dataset["saveStatus"];
      expect(slot.classList.contains("settings-save-status"), `${key}: class`).toBe(true);
      // Hidden until a write starts, or every settings panel opens with a row of empty boxes
      // reserving space for a state nothing is in.
      expect(slot.classList.contains("hidden"), `${key}: starts hidden`).toBe(true);
      expect(slot.getAttribute("aria-live"), `${key}: aria-live`).toBe("polite");
      expect(slot.closest(".section-title-status-row"), `${key}: title row`).not.toBeNull();
    }
  });

  it("puts each slot after the label it belongs to, inside that row", () => {
    for (const slot of slots()) {
      const row = slot.closest(".section-title-status-row");
      const label = row?.firstElementChild;
      expect(label, `${slot.dataset["saveStatus"] ?? ""}: row has a label first`).not.toBe(slot);
      expect(
        label?.matches("h2.section-title, label.section-title, .section-option-label"),
        `${slot.dataset["saveStatus"] ?? ""}: row leads with a setting title`,
      ).toBe(true);
    }
  });

  it("names a settings field for every marotte-owned key", () => {
    const dotted = (k: string): boolean => k.includes(".");
    for (const key of EXPECTED_KEYS) {
      if (dotted(key) || key === "steering") {
        continue; // a kiro-cli key, or the steering textarea's own token
      }
      expect(SETTINGS_FIELDS.has(key), `${key} must be an EffectiveSettings field`).toBe(true);
    }
  });

  it("names a kiro-cli flag for every dotted key", () => {
    for (const key of EXPECTED_KEYS) {
      if (!key.includes(".")) {
        continue;
      }
      expect(settingsSrc, `${key} must be in experimentalFlags`).toContain(`key: "${key}"`);
    }
  });

  it("keeps the slotless keys slotless", () => {
    const found = new Set(slots().flatMap((s) => (s.dataset["saveStatus"] ?? "").split(/\s+/)));
    for (const key of NO_SLOT_KEYS) {
      expect(found.has(key), `${key} is written from outside Settings`).toBe(false);
      expect(SETTINGS_FIELDS.has(key), `${key} must still be a settings field`).toBe(true);
    }
  });
});
