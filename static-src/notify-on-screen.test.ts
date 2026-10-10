// A notification's target is on screen only while the page is visible AND its own tab is active.

import { describe, it, expect, beforeEach, vi } from "vitest";
import { chatTarget, runTarget } from "./push-subject.js";
import type * as Tabs from "./tabs.js";

const tabs = vi.hoisted(() => ({ active: "", ids: new Map<string, string>() }));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  getActiveTabId: () => tabs.active,
  tabIdFor: (kind: string, ref: string) => tabs.ids.get(`${kind}/${ref}`) ?? "",
}));

const { targetOnScreen } = await import("./notify-on-screen.js");

beforeEach(() => {
  tabs.ids = new Map([
    ["chat/c1", "tab-c1"],
    ["run/wf1", "tab-wf1"],
  ]);
  tabs.active = "tab-c1";
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
});

describe("targetOnScreen", () => {
  it("is true for the active chat's tab on a visible page", () => {
    expect(targetOnScreen(chatTarget("c1"))).toBe(true);
  });

  it("is false for another chat's tab", () => {
    expect(targetOnScreen(runTarget("wf1"))).toBe(false);
  });

  it("is true for the active run's tab", () => {
    tabs.active = "tab-wf1";
    expect(targetOnScreen(runTarget("wf1"))).toBe(true);
  });

  it("is false while the page is hidden, whatever tab is active", () => {
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    expect(targetOnScreen(chatTarget("c1"))).toBe(false);
  });

  it("is false for a chat with no open tab", () => {
    tabs.active = "";
    expect(targetOnScreen(chatTarget("gone"))).toBe(false);
  });
});
