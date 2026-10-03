import { describe, it, expect, afterEach } from "vitest";

import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { applyStoredSidebarWidth, writeSidebarPref } from "./sidebar-width.js";

const pref = (): string => document.documentElement.style.getPropertyValue("--sidebar-w-pref");

afterEach(() => {
  document.documentElement.style.removeProperty("--sidebar-w-pref");
  localStorage.clear();
});

describe("applyStoredSidebarWidth", () => {
  it("writes a stored width as the preferred px", () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ sidebar_w: 480 }));
    applyStoredSidebarWidth();
    expect(pref()).toBe("480px");
  });

  it.each([
    ["a stored 0 (the minimum)", JSON.stringify({ sidebar_w: 0 })],
    ["no blob", null],
    ["a corrupt blob", "{not json"],
    ["a negative width", JSON.stringify({ sidebar_w: -5 })],
    ["a string width", JSON.stringify({ sidebar_w: "wide" })],
  ])("writes nothing for %s", (_name, raw) => {
    if (raw !== null) {
      localStorage.setItem(LS_UI_STATE_KEY, raw);
    }
    applyStoredSidebarWidth();
    expect(pref()).toBe("");
  });
});

describe("writeSidebarPref", () => {
  it("removes the preference on null", () => {
    writeSidebarPref(400);
    expect(pref()).toBe("400px");
    writeSidebarPref(null);
    expect(pref()).toBe("");
  });
});
