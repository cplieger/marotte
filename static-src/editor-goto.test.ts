// Go to line, and the `#L<n>` deep link that lands through it: a line past the end shows the
// last line and says which one, rather than landing silently somewhere else.
import { describe, it, expect, beforeAll, afterAll, beforeEach, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type * as Tabs from "./tabs.js";

vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  getActiveTabKind: () => "editor",
}));

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { mountEditorView } = await import("./__test-helpers__/editor-dom.js");
const { applyPendingLine, paintCommonControls } = await import("./editor-ui.js");
const { goToLine, initGoto } = await import("./editor-goto.js");
const { viewer } = await import("./editor-pane.js");
const { WholeRows } = await import("./viewer-rows.js");
const { fileStates, freshState, setActiveFilePath } = await import("./editor-types.js");

let sheet: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  sheet = mountAppCSS();
  host = mountEditorView();
  initGoto();
});

afterAll(() => {
  host.remove();
  sheet.remove();
});

beforeEach(() => {
  fileStates.clear();
  setActiveFilePath("");
});

const byId = (id: string): HTMLElement => document.getElementById(id) as HTMLElement;
const panelOpen = (): boolean => !byId("editor-goto").classList.contains("hidden");

function showRead(text: string): void {
  byId("editor-viewer").classList.remove("hidden");
  byId("editor-goto").classList.add("hidden");
  byId("editor-goto-status").textContent = "";
  viewer().show({ rows: new WholeRows(text), runs: null, agentLines: new Set() });
}

/** A loaded, readable text file, active in the pane. */
function activeText(path: string): ReturnType<typeof freshState> {
  const state = freshState(path);
  state.loaded = true;
  state.facts.value = {
    kind: "small",
    binary: false,
    utf8: true,
    conflict: false,
    readOnly: false,
    size: 5,
  };
  fileStates.set(path, state);
  setActiveFilePath(path);
  return state;
}

const ctrlG = (): void => {
  document.dispatchEvent(
    new KeyboardEvent("keydown", { key: "g", ctrlKey: true, bubbles: true, cancelable: true }),
  );
};

const frames = (): Promise<void> =>
  new Promise((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });

describe("a #L<n> deep link", () => {
  it("past the end shows the last line and says so", async () => {
    const state = activeText("/workspace/x.txt");
    showRead("a\nb\nc");
    state.pendingLine = 40;
    applyPendingLine(state);
    await frames();
    expect(panelOpen()).toBe(true);
    expect(byId("editor-goto-status").textContent).toBe("Line 40 is past the end; showing line 3.");
  });

  it("inside the file says nothing", async () => {
    const state = activeText("/workspace/x.txt");
    showRead("a\nb\nc");
    state.pendingLine = 2;
    applyPendingLine(state);
    await frames();
    expect(panelOpen()).toBe(false);
    expect(byId("editor-goto-status").textContent).toBe("");
  });
});

describe("goToLine", () => {
  it("past the end opens the control with the sentence", () => {
    showRead("one\ntwo");
    goToLine(9);
    expect(byId("editor-goto-status").textContent).toBe("Line 9 is past the end; showing line 2.");
  });
});

describe("the Go to line control", () => {
  it("goes to the typed line on Enter", async () => {
    showRead("a\nb\nc");
    byId("editor-goto").classList.remove("hidden");
    const input = byId("editor-goto-input") as HTMLInputElement;
    input.value = "";
    await userEvent.type(input, "7{Enter}");
    expect(byId("editor-goto-status").textContent).toBe("Line 7 is past the end; showing line 3.");
  });

  it("closes when another file becomes active", () => {
    activeText("/workspace/a.txt");
    showRead("a\nb\nc");
    ctrlG();
    expect(panelOpen()).toBe(true);
    activeText("/workspace/b.txt");
    expect(panelOpen()).toBe(false);
  });

  it("closes when the open file turns too large to view", () => {
    const state = activeText("/workspace/a.log");
    showRead("a\nb\nc");
    ctrlG();
    expect(panelOpen()).toBe(true);
    state.facts.value = { kind: "large", size: 3 << 20 };
    state.mode.value = { kind: "large" };
    paintCommonControls(state);
    expect(panelOpen()).toBe(false);
    expect(byId("editor-goto-btn").classList.contains("hidden")).toBe(true);
  });

  it("does not open on Ctrl+G before the file has loaded", () => {
    const state = activeText("/workspace/slow.txt");
    state.loaded = false;
    ctrlG();
    expect(panelOpen()).toBe(false);
  });
});
