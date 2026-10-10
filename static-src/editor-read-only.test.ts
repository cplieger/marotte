// A file the server marks read-only (a KAS tool output under the sessions tree), or one that is not
// UTF-8 text, opens in the editor with no way into edit mode: no Edit control, a header label
// saying why, and the Edit verb does nothing if reached anyway.
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
const { renderTextModeUI } = await import("./editor-ui.js");
const { fileStates, freshState, setActiveFilePath } = await import("./editor-types.js");
const { $ } = await import("./dom.js");

const PATH = "/config/.kiro/sessions/cli/sess_1/tool-outputs/shell-0a1b2c3d.txt";

function stage(readOnly: boolean, utf8 = true): ReturnType<typeof freshState> {
  const state = freshState(PATH);
  state.loaded = true;
  state.facts.value = { kind: "small", binary: false, utf8, conflict: false, readOnly, size: 6 };
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
  it("hides the Edit control and labels the header", () => {
    renderTextModeUI(stage(true));
    expect($.editorEditBtn.classList.contains("hidden")).toBe(true);
    expect($.editorReadonlyLabel.textContent).toBe("KAS tool output");
    expect($.editorReadonlyLabel.classList.contains("hidden")).toBe(false);
  });

  // JSON replaced the invalid bytes, so saving would write U+FFFD over them.
  it("keeps a file that is not UTF-8 text read-only, saying so", () => {
    const state = stage(false, false);
    renderTextModeUI(state);
    expect($.editorEditBtn.classList.contains("hidden")).toBe(true);
    expect($.editorReadonlyLabel.textContent).toBe("Not UTF-8 text");
    $.editorEditBtn.click();
    expect(state.mode.value).toEqual({ kind: "text", editing: false });
  });

  it("an ordinary file shows it, with no label", () => {
    renderTextModeUI(stage(false));
    expect($.editorEditBtn.classList.contains("hidden")).toBe(false);
    expect($.editorReadonlyLabel.classList.contains("hidden")).toBe(true);
  });

  it("does not enter edit mode when the Edit verb is reached anyway", () => {
    const state = stage(true);
    $.editorEditBtn.click();
    expect(state.mode.value).toEqual({ kind: "text", editing: false });
  });

  it("an ordinary file enters edit mode on Edit", () => {
    const state = stage(false);
    $.editorEditBtn.click();
    expect(state.mode.value).toEqual({ kind: "text", editing: true });
  });
});
