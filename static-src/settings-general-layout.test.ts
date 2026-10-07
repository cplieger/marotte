// The shipped markup, not a fixture: a fixture would keep passing after index.html regressed.
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

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
  "chat-retention-days",
  "memory-mode",
  "auto-compact-pct",
  "spec-planning",
  "output-style",
  "shell-command-timeout",
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
    for (const id of ["memory-mode", "spec-planning", "output-style", "shell-command-timeout"]) {
      const hint = control(id).closest(".section-field")?.querySelector(".section-hint");
      expect(hint, `#${id} has a hint`).not.toBeNull();
      expect(hint!.getBoundingClientRect().top).toBeGreaterThanOrEqual(
        control(id).getBoundingClientRect().bottom,
      );
    }
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
