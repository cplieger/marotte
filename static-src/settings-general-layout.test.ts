// The shipped markup, not a fixture: a fixture would keep passing after index.html regressed.
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { settingsPayload } from "./__test-helpers__/settings.js";
import { patchSettings } from "./persist.js";
import { applyGeneralPanel, initGeneralPanelControls } from "./settings.js";
import type * as Persist from "./persist.js";

vi.mock("./persist.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Persist>()),
  patchSettings: vi.fn(() => Promise.resolve({ outcome: "saved" })),
}));

const page = new DOMParser().parseFromString(indexHtml, "text/html");
const host = document.createElement("div");
host.style.cssText = "position:absolute;top:0;left:0;inline-size:960px;";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.documentElement.dataset["pointer"] = "fine";
  const general = page.querySelector('[data-settings-panel="general"]');
  if (general === null) {
    throw new Error("index.html has no General panel");
  }
  host.appendChild(document.importNode(general, true));
  document.body.appendChild(host);
});

afterAll(() => {
  host.remove();
  style.remove();
  document.documentElement.removeAttribute("data-pointer");
});

const VALUE_CONTROLS = [
  "clock-choice",
  "chat-retention-days",
  "memory-mode",
  "auto-compact-pct",
  "spec-planning",
  "output-style",
  "workflow-launch",
  "workflow-creator-effort",
  "chat-message-delivery-limit",
  "knowledge-index-type",
  "knowledge-include-patterns",
  "knowledge-exclude-patterns",
  "knowledge-max-files",
  "knowledge-chunk-size",
  "shell-command-timeout",
  "trigger-max-live-runs",
  "trigger-max-launches-per-hour",
] as const;

function control(id: string): HTMLElement {
  const el = host.querySelector<HTMLElement>(`[id="${id}"]`);
  if (el === null) {
    throw new Error(`no #${id} in the General panel`);
  }
  return el;
}

function leadOf(id: string): HTMLElement {
  const lead = control(id).closest(".section-field")?.firstElementChild;
  if (!(lead instanceof HTMLElement)) {
    throw new Error(`#${id} is not in a .section-field`);
  }
  return lead;
}

describe("Settings > General value rows at desktop width", () => {
  it.each(VALUE_CONTROLS)("%s sits on its label's line, to the right of it", (id) => {
    const label = host.querySelector(`label[for="${id}"]`)?.getBoundingClientRect();
    const box = control(id).getBoundingClientRect();
    expect(label, `a label for #${id}`).toBeDefined();
    expect(box.left, "control starts after the label ends").toBeGreaterThan(label!.right);
    expect(box.top, "control overlaps the label's line").toBeLessThan(label!.bottom);
    expect(box.bottom, "control overlaps the label's line").toBeGreaterThan(label!.top);
  });

  it("keeps one gap between every label and its control", () => {
    const gaps = VALUE_CONTROLS.map(
      (id) => control(id).getBoundingClientRect().left - leadOf(id).getBoundingClientRect().right,
    );
    expect(new Set(gaps.map((g) => Math.round(g))).size, `gaps ${gaps.join(", ")}`).toBe(1);
    expect(gaps[0]).toBeGreaterThan(0);
  });

  it("puts each row's hint on its own line below the control", () => {
    for (const id of [
      "clock-choice",
      "memory-mode",
      "spec-planning",
      "output-style",
      "shell-command-timeout",
      "knowledge-include-patterns",
      "knowledge-chunk-overlap",
    ]) {
      const hint = control(id).closest(".section-field")?.querySelector(".section-hint");
      expect(hint, `#${id} has a hint`).not.toBeNull();
      expect(hint!.getBoundingClientRect().top).toBeGreaterThanOrEqual(
        control(id).getBoundingClientRect().bottom,
      );
    }
  });
});

describe("the knowledge passage size row", () => {
  it("puts the overlap box on the size box's line, after its own label", () => {
    const size = control("knowledge-chunk-size").getBoundingClientRect();
    const label = host
      .querySelector('label[for="knowledge-chunk-overlap"]')
      ?.getBoundingClientRect();
    const overlap = control("knowledge-chunk-overlap").getBoundingClientRect();
    expect(label, "the overlap box has a label").toBeDefined();
    expect(label!.left).toBeGreaterThan(size.right);
    expect(overlap.left).toBeGreaterThan(label!.right);
    expect(overlap.top).toBeLessThan(size.bottom);
    expect(overlap.bottom).toBeGreaterThan(size.top);
  });
});

describe("the config.json door", () => {
  it("is the last row of Settings > General", () => {
    const general = host.firstElementChild;
    const door = general?.lastElementChild?.querySelector("[id='settings-open-config']");
    expect(door?.textContent).toBe("config.json");
  });

  it("is gone from Settings > Tools, which keeps only tools.json", () => {
    const tools = page.querySelector('[data-settings-panel="tools"]');
    expect(tools?.querySelector("[id='tool-open-manifest']")).not.toBeNull();
    expect(tools?.querySelector("[aria-label='Open config.json in the editor']")).toBeNull();
    expect(tools?.textContent).not.toContain("Apply");
  });
});

describe("the Settings booleans", () => {
  it.each([
    "chat-retention-forever",
    "supervised-default-checkbox",
    "scheduled-auto-approve-checkbox",
  ])("%s is a standard toggle row", (id) => {
    const input = page.querySelector(`input[type="checkbox"][id="${id}"]`);
    expect(input, `#${id} in index.html`).not.toBeNull();
    expect(input!.parentElement?.matches("label.toggle")).toBe(true);
    expect(input!.nextElementSibling?.matches(".toggle-slider")).toBe(true);
    expect(input!.closest(".section-option")).not.toBeNull();
  });
});

describe("the unattended runs section", () => {
  it("names scheduled and triggered runs on the auto-approve toggle", () => {
    const input = page.querySelector<HTMLInputElement>("#scheduled-auto-approve-checkbox");
    const section = input?.closest(".page-section");
    const label = input?.closest(".section-option")?.querySelector(".section-option-label");
    const want = "Approve tool requests in scheduled and triggered runs instead of refusing them";
    expect(section?.querySelector(".section-title")?.textContent).toBe("Unattended runs");
    expect(label?.textContent).toBe(want);
    expect(input?.getAttribute("aria-label")).toBe(want);
    const hint = section?.querySelector(".section-hint")?.textContent ?? "";
    expect(hint).toContain("Scheduled and triggered workflows run with nobody watching");
    expect(hint).toContain(
      "A triggered run can read text other people wrote, such as an issue body",
    );
  });
});

describe("the count rows", () => {
  it("edits the trigger caps and the chat message delivery limit within their bounds", () => {
    initGeneralPanelControls();
    applyGeneralPanel(
      settingsPayload({
        trigger_max_live_runs: 4,
        trigger_max_launches_per_hour: 9,
        chat_message_delivery_limit: 70,
      }),
    );
    // Bounds: settings.validTriggerCap (1 to 100) and kiro-cli's sendMessageDeliveryLimit (1 to 500).
    const cases = [
      ["trigger-max-live-runs", "trigger_max_live_runs", "4", 100],
      ["trigger-max-launches-per-hour", "trigger_max_launches_per_hour", "9", 100],
      ["chat-message-delivery-limit", "chat_message_delivery_limit", "70", 500],
    ] as const;
    for (const [id, key, stored, max] of cases) {
      const input = control(id) as HTMLInputElement;
      expect(input.type).toBe("number");
      expect([input.min, input.max]).toEqual(["1", String(max)]);
      expect(input.value, `#${id} paints the stored value`).toBe(stored);
      input.value = "7";
      input.dispatchEvent(new Event("change"));
      expect(patchSettings).toHaveBeenLastCalledWith({ [key]: 7 });
      input.value = "9000";
      input.dispatchEvent(new Event("change"));
      expect(patchSettings).toHaveBeenLastCalledWith({ [key]: max });
      expect(input.value).toBe(String(max));
      input.value = "0";
      input.dispatchEvent(new Event("change"));
      expect(patchSettings).toHaveBeenLastCalledWith({ [key]: 1 });
      vi.mocked(patchSettings).mockClear();
      input.value = "";
      input.dispatchEvent(new Event("change"));
      expect(patchSettings, "an emptied box sends nothing").not.toHaveBeenCalled();
    }
  });

  it("edits the knowledge file limit from 1 up, with no upper bound", () => {
    initGeneralPanelControls();
    applyGeneralPanel(settingsPayload({ knowledge_max_files: 2500 }));
    const input = control("knowledge-max-files") as HTMLInputElement;
    expect([input.type, input.min, input.max]).toEqual(["number", "1", ""]);
    expect(input.value).toBe("2500");
    input.value = "250000";
    input.dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenLastCalledWith({ knowledge_max_files: 250000 });
    input.value = "0";
    input.dispatchEvent(new Event("change"));
    expect(patchSettings).toHaveBeenLastCalledWith({ knowledge_max_files: 1 });
  });
});
