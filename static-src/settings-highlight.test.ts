// What this module still owns after the scroll-and-mark primitive moved to flash-target.ts: the
// one-shot `?highlight=` read, `openSetting`'s single path (open the view on the tab, then mark the
// control), and `highlightControl`'s own `id === ""` guard — an empty id means the CALLER had no
// target, which `flashTarget` would spend its whole frame budget failing to find.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";

const mocks = vi.hoisted(() => ({
  // Opening a singleton is a round trip, so the open RESOLVES: `openSetting` sequences the
  // highlight in its continuation, because it addresses a panel the open has to produce first.
  openSettingsView: vi.fn(() => Promise.resolve()),
}));

vi.mock("./tabs.js", () => ({
  openSettingsView: mocks.openSettingsView,
}));

import {
  highlightControl,
  openSetting,
  flushURLHighlight,
  _setPendingTargetForTest,
} from "./settings-highlight.js";

async function frames(n = 3): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => {
      requestAnimationFrame(() => {
        r(null);
      });
    });
  }
}

let scrolled: string[] = [];

beforeEach(() => {
  vi.clearAllMocks();
  scrolled = [];
  _setPendingTargetForTest(null);
  document.body.innerHTML = "";
  Element.prototype.scrollIntoView = function (this: Element) {
    scrolled.push(this.id);
  };
});

afterEach(() => {
  _setPendingTargetForTest(null);
});

function control(id: string): void {
  const e = document.createElement("input");
  e.id = id;
  e.type = "checkbox";
  document.body.appendChild(e);
}

describe("highlightControl", { timeout: testTimeoutFor(framesBudgetMs(25)) }, () => {
  it("ignores an empty id without scheduling a retry", async () => {
    highlightControl("");
    await frames(25);
    expect(scrolled).toEqual([]);
  });
});

describe("openSetting", () => {
  // Opening the tab is a ROUND TRIP, and the open names the sub-tab, so the panel and the URL are
  // the open's; only the highlight, a DOM write against that panel, runs in its continuation.
  it("opens the Settings view on the tab, then marks the control", async () => {
    expect.assertions(3);
    control("security-profile-list");
    let release: () => void = () => undefined;
    mocks.openSettingsView.mockReturnValueOnce(
      new Promise<void>((r) => {
        release = r;
      }),
    );

    openSetting("permissions", "security-profile-list");

    expect(mocks.openSettingsView).toHaveBeenCalledWith("permissions");
    await frames(1);
    // Nothing has reached the panel yet: the open has not resolved.
    expect(scrolled).toEqual([]);

    release();
    await mocks.openSettingsView.mock.results[0]?.value;
    await frames(1);
    expect(scrolled).toEqual(["security-profile-list"]);
  });

  // ONE path, and this is what pins it: the function reads no route and takes no already-there
  // branch, so a jump made while Settings is the active view runs the whole sequence again rather
  // than returning early.
  it("runs the same sequence on a second call", async () => {
    control("chat-retention-days");

    openSetting("general", "chat-retention-days");
    await mocks.openSettingsView.mock.results[0]?.value;
    await frames(1);
    expect(scrolled).toEqual(["chat-retention-days"]);

    openSetting("general", "chat-retention-days");
    await mocks.openSettingsView.mock.results[1]?.value;
    await frames(1);
    expect(mocks.openSettingsView).toHaveBeenCalledTimes(2);
    expect(scrolled).toEqual(["chat-retention-days", "chat-retention-days"]);
  });
});

describe("flushURLHighlight", () => {
  it("fires the captured target exactly once", async () => {
    control("flag-debug-logs");
    _setPendingTargetForTest("flag-debug-logs");

    flushURLHighlight();
    await frames(1);
    expect(scrolled).toEqual(["flag-debug-logs"]);

    // A later popstate back to the same URL must not re-flash a control the reader has already been
    // shown.
    flushURLHighlight();
    await frames(2);
    expect(scrolled).toEqual(["flag-debug-logs"]);
  });

  it("is a no-op when the page load carried no highlight", async () => {
    control("flag-debug-logs");
    flushURLHighlight();
    await frames(2);
    expect(scrolled).toEqual([]);
  });
});
