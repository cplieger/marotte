// The settings switcher is a segmented bar, never a `<select>`, and every tab carries
// an icon before its label: when one label would truncate the whole bar collapses to
// icon-only (tab-bar-fit.test.ts), so a tab without an icon would become a blank
// button. Read off the shipped page through the browser's own HTML parser.
import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";

const bar = new DOMParser()
  .parseFromString(indexHtml, "text/html")
  .getElementById("settings-tab-bar");
const tabs = [...(bar?.querySelectorAll<HTMLElement>(".seg") ?? [])];

describe("the settings tab bar markup", () => {
  it("exists and holds tabs", () => {
    expect.assertions(2);
    expect(bar).not.toBeNull();
    expect(tabs.length).toBeGreaterThan(1);
  });

  it("holds no select", () => {
    expect.assertions(1);
    expect(bar?.querySelector("select")).toBeNull();
  });

  it.each(tabs.map((t) => [t.dataset["settingsTab"] ?? "?", t] as const))(
    "puts an icon before the label on the %s tab",
    (_name, tab) => {
      expect.assertions(1);
      const icon = tab.querySelector(".seg-icon");
      const label = tab.querySelector(".seg-label");
      const iconFirst =
        icon !== null &&
        label !== null &&
        (icon.compareDocumentPosition(label) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
      expect(iconFirst).toBe(true);
    },
  );
});
