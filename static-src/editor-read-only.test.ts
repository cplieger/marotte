// A file the server marks read-only (a KAS tool output under the sessions tree)
// opens in the editor with no way into edit mode: no Edit control, and the Edit
// verb does nothing if reached anyway.
import { describe, it, expect, beforeEach, vi } from "vitest";
import type * as Dom from "./dom.js";
import type * as EditorOpeners from "./editor-openers.js";
import type * as Confirm from "./confirm.js";
import type * as Git from "./git.js";
import type * as WebOpen from "./web-open.js";

const { els } = vi.hoisted(() => ({ els: new Map<string, HTMLElement>() }));

vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Dom>()),
  byId: (id: string): HTMLElement => {
    let el = els.get(`#${id}`);
    if (el === undefined) {
      el = document.createElement("div");
      els.set(`#${id}`, el);
    }
    return el;
  },
  maybeEl: () => null,
  forceReflow: () => 0,
  setBusy: vi.fn(),
  setControlBusy: vi.fn(),
  $: new Proxy(
    {},
    {
      get: (_t, key: string): HTMLElement => {
        let el = els.get(key);
        if (el === undefined) {
          el = document.createElement(key.endsWith("Btn") ? "button" : "div");
          els.set(key, el);
        }
        return el;
      },
    },
  ),
}));
vi.mock("./editor-openers.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorOpeners>()),
  fetchGitDiffSources: vi.fn(),
  openFileGitDiff: vi.fn(),
  openFile: vi.fn(),
  openFileDiff: vi.fn(),
  activateFile: vi.fn(),
  closeEditorFile: vi.fn(),
}));
vi.mock("./confirm.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Confirm>()),
  confirm: vi.fn(() => Promise.resolve(true)),
}));
vi.mock("./git.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Git>()),
  markGitDirty: vi.fn(),
}));
vi.mock("./web-open.js", async (importOriginal) => ({
  ...(await importOriginal<typeof WebOpen>()),
  openWebPreview: vi.fn(),
}));

const { initEditor } = await import("./editor-core.js");
const { renderEditModeUI } = await import("./editor-ui.js");
const { fileStates, freshState, setActiveFilePath } = await import("./editor-types.js");
const { $ } = await import("./dom.js");

const PATH = "/config/.kiro/sessions/cli/sess_1/tool-outputs/shell-0a1b2c3d.txt";

function stage(readOnly: boolean): ReturnType<typeof freshState> {
  const state = freshState(PATH);
  state.loaded = true;
  state.readOnly = readOnly;
  state.original.value = "output";
  state.current.value = "output";
  fileStates.set(PATH, state);
  setActiveFilePath(PATH);
  return state;
}

let wired = false;

beforeEach(() => {
  if (!wired) {
    wired = true;
    initEditor();
  }
  fileStates.clear();
  setActiveFilePath("");
});

describe("a read-only file in the editor", () => {
  it("hides the Edit control", () => {
    renderEditModeUI(stage(true));
    expect($.editorEditBtn.classList.contains("hidden")).toBe(true);
  });

  it("an ordinary file shows it", () => {
    renderEditModeUI(stage(false));
    expect($.editorEditBtn.classList.contains("hidden")).toBe(false);
  });

  it("does not enter edit mode when the Edit verb is reached anyway", () => {
    const state = stage(true);
    $.editorEditBtn.click();
    expect(state.mode.value).toEqual({ kind: "edit", editing: false });
  });

  it("an ordinary file enters edit mode on Edit", () => {
    const state = stage(false);
    $.editorEditBtn.click();
    expect(state.mode.value).toEqual({ kind: "edit", editing: true });
  });
});
