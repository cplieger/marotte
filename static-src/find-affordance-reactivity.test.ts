// The toolbar's find affordance must re-derive when a page registers and when the tab changes: `activateTab`
// announces the switch before `onShow`, and pages register in `onShow`.

import { describe, it, expect, beforeEach, vi } from "vitest";
import { effect, signal } from "@cplieger/reactive";
import type { TabKind } from "./tabs.js";
import type { PageFind } from "./find-registry.js";

/** Mocked so the test states its own dependency. */
let activeKind: TabKind | null = "chat";
const tabSignal = signal(0);

vi.mock("./tabs.js", () => ({
  getActiveTabKind: () => {
    // Mirrors the production getter: subscribe, then read.
    void tabSignal.value;
    return activeKind;
  },
}));
vi.mock("./find-in-chat.js", () => ({
  handleFindHotkey: () => false,
  toggleChatFind: () => undefined,
}));
vi.mock("./files-search.js", () => ({
  handleFindInFilesHotkey: () => false,
  toggleFilesSearch: () => undefined,
}));
vi.mock("./editor-find.js", () => ({
  handleEditorFindHotkey: () => false,
  toggleEditorFind: () => undefined,
  editorFindAvailable: () => false,
}));

const { findAffordanceForActiveTab } = await import("./find-dispatch.js");
const { registerFind, _resetFindRegistry } = await import("./find-registry.js");

function setActive(kind: TabKind | null): void {
  activeKind = kind;
  tabSignal.value = tabSignal.value + 1;
}

function fakeFind(available: boolean): PageFind {
  return {
    open: () => true,
    toggle: () => undefined,
    focused: () => false,
    kind: () => "filter",
    available: () => available,
  };
}

function trackAffordance(): { runs: { available: boolean; kind: string }[]; stop: () => void } {
  const runs: { available: boolean; kind: string }[] = [];
  const stop = effect(() => {
    runs.push(findAffordanceForActiveTab());
  });
  return { runs, stop };
}

describe("the find affordance re-derives when a page registers", () => {
  beforeEach(() => {
    _resetFindRegistry();
    activeKind = "chat";
  });

  it("repaints when a page registers AFTER the first paint, from a chat tab", () => {
    // A chat is active at boot, so the `page` branch never runs; a registry read inside it subscribes to nothing.
    const { runs, stop } = trackAffordance();
    expect(runs).toHaveLength(1);

    setActive("docs");
    registerFind("docs", fakeFind(true));

    expect(runs.at(-1)?.available).toBe(true);
    stop();
  });

  it("repaints when the page registers while it is ALREADY the active tab", () => {
    // activateTab announces before onShow, the real ordering.
    setActive("docs");
    const { runs, stop } = trackAffordance();
    expect(runs.at(-1)?.available).toBe(false);

    registerFind("docs", fakeFind(true));
    expect(runs.at(-1)?.available).toBe(true);
    stop();
  });

  it("re-registering the SAME find object does not churn the effect", () => {
    // A page may register on every mount without repainting the toolbar.
    const find = fakeFind(true);
    setActive("docs");
    registerFind("docs", find);
    const { runs, stop } = trackAffordance();
    const before = runs.length;

    registerFind("docs", find);
    registerFind("docs", find);
    expect(runs).toHaveLength(before);
    stop();
  });
});

describe("the find affordance re-derives when the active tab changes", () => {
  beforeEach(() => {
    _resetFindRegistry();
    activeKind = "chat";
  });

  it("follows a tab switch without any bus event", () => {
    registerFind("docs", fakeFind(true));
    const { runs, stop } = trackAffordance();

    setActive("docs");
    expect(runs.at(-1)?.available).toBe(true);

    setActive("settings");
    expect(runs.at(-1)?.available).toBe(false);
    stop();
  });

  it("follows an `available` predicate that flips under one tab id", () => {
    // One git tab whose sub-tab decides the answer, so no tab id changes; the tab signal stands in for readGitTab.
    let sourcesActive = false;
    registerFind("git", {
      open: () => true,
      toggle: () => undefined,
      focused: () => false,
      kind: () => "filter",
      available: () => {
        void tabSignal.value;
        return !sourcesActive;
      },
    });
    setActive("git");
    const { runs, stop } = trackAffordance();
    expect(runs.at(-1)?.available).toBe(true);

    sourcesActive = true;
    tabSignal.value = tabSignal.value + 1;
    expect(runs.at(-1)?.available).toBe(false);

    sourcesActive = false;
    tabSignal.value = tabSignal.value + 1;
    expect(runs.at(-1)?.available).toBe(true);
    stop();
  });
});
