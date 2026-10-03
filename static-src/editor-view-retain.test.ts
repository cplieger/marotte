// The editor pane is ONE set of elements serving every editor tab, so a file's
// reading position has to live on its own state. Measured in a real browser over
// the shipped stylesheet: activating a file again (the tab's onShow) must land
// where the reader left it, whether they went to another view or another file.
import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { initEditor } from "./editor-core.js";
import { activateFile, closeEditorFile } from "./editor-openers.js";
import { fileStates, freshState, setActiveFilePath, type FileMode } from "./editor-types.js";

let sheet: HTMLStyleElement;
let sizing: HTMLStyleElement;

beforeAll(() => {
  sheet = mountAppCSS();
  sizing = document.createElement("style");
  sizing.textContent = `[id="editor-view"] { width: 480px; height: 240px; display: flex; flex-direction: column; }
    [id="editor-view"] > .editor-page { flex: 1; min-height: 0; }`;
  document.head.appendChild(sizing);
  document.body.innerHTML = `
    <div id="other-view" data-tab-view class="hidden">chat</div>
    <div id="editor-view" data-tab-view>
      <div class="editor-page">
        <div id="editor-error" class="editor-error hidden"></div>
        <div id="editor-conflict-overlay" class="editor-conflict-overlay hidden"></div>
        <div class="editor-body">
          <pre id="editor-gutter" class="editor-gutter" aria-hidden="true"></pre>
          <pre id="editor-highlight" class="editor-highlight"><code id="editor-code"></code></pre>
          <textarea id="editor-content" class="editor-content hidden" aria-label="File editor"></textarea>
          <div id="editor-markdown" class="editor-markdown hidden"></div>
          <div id="editor-image" class="editor-image hidden"></div>
          <div id="editor-diff-pane" class="editor-diff-pane hidden"></div>
        </div>
        <div class="editor-toolbar bottom-bar">
          <span id="editor-filename"></span>
          <button type="button" id="editor-preview-btn" class="hidden"></button>
          <button type="button" id="editor-git-diff-btn" class="hidden"></button>
          <button type="button" id="editor-diff-btn"></button>
          <button type="button" id="editor-edit-btn"></button>
          <button type="button" id="editor-save-btn" class="hidden"></button>
          <button type="button" id="editor-cancel-btn" class="hidden"></button>
        </div>
      </div>
    </div>`;
  initEditor();
});

afterAll(() => {
  sheet.remove();
  sizing.remove();
  document.body.replaceChildren();
});

const A = "/workspace/a.go";
const B = "/workspace/b.go";

function lines(tag: string, n: number, width = 10): string {
  return Array.from({ length: n }, (_, i) => `${tag} line ${String(i)} ${"x".repeat(width)}`).join(
    "\n",
  );
}

function seed(path: string, content: string, mode: FileMode = { kind: "edit", editing: false }) {
  const state = freshState(path);
  state.original.value = content;
  state.current.value = content;
  state.loaded = true;
  state.mode.value = mode;
  fileStates.set(path, state);
  return state;
}

function body(): HTMLElement {
  return document.querySelector<HTMLElement>(".editor-body")!;
}

function textarea(): HTMLTextAreaElement {
  return document.getElementById("editor-content") as HTMLTextAreaElement;
}

/** A scroll event is dispatched at the next frame, which is when the capture runs. */
async function frames(n = 2): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise<void>((r) => requestAnimationFrame(() => r()));
  }
}

function leaveEditorView(): void {
  document.getElementById("editor-view")!.classList.add("hidden");
  document.getElementById("other-view")!.classList.remove("hidden");
}

function returnToEditorView(path: string): void {
  document.getElementById("other-view")!.classList.add("hidden");
  document.getElementById("editor-view")!.classList.remove("hidden");
  activateFile(path);
}

beforeEach(() => {
  document.getElementById("editor-view")!.classList.remove("hidden");
  document.getElementById("other-view")!.classList.add("hidden");
});

afterEach(() => {
  closeEditorFile(A);
  closeEditorFile(B);
  setActiveFilePath("");
});

describe("the editor keeps each file's reading position", () => {
  it("survives a round trip through another view", async () => {
    seed(A, lines("a", 400));
    activateFile(A);
    body().scrollTop = 1200;
    await frames();
    leaveEditorView();
    await frames();
    returnToEditorView(A);
    expect(body().scrollTop).toBe(1200);
  });

  it("is per file: A, then B, then A lands each where it was left", async () => {
    seed(A, lines("a", 400));
    seed(B, lines("b", 400));
    activateFile(A);
    body().scrollTop = 1200;
    await frames();
    activateFile(B);
    // A first open lands at the top rather than at the previous file's offset.
    expect(body().scrollTop).toBe(0);
    body().scrollTop = 300;
    await frames();
    activateFile(A);
    expect(body().scrollTop).toBe(1200);
    await frames();
    activateFile(B);
    expect(body().scrollTop).toBe(300);
  });

  it("keeps the selection and the textarea's own pan in edit mode", async () => {
    seed(A, lines("a", 40, 300), { kind: "edit", editing: true });
    seed(B, lines("b", 40, 300), { kind: "edit", editing: true });
    activateFile(A);
    const ta = textarea();
    ta.setSelectionRange(10, 25, "backward");
    ta.scrollLeft = 200;
    body().scrollTop = 150;
    await frames();
    activateFile(B);
    activateFile(A);
    expect(ta.selectionStart).toBe(10);
    expect(ta.selectionEnd).toBe(25);
    expect(ta.selectionDirection).toBe("backward");
    expect(ta.scrollLeft).toBe(200);
    expect(body().scrollTop).toBe(150);
    expect(document.activeElement).not.toBe(ta);
  });

  // Distinct selections, because the textarea is SHARED: an activation that read it
  // back would file the outgoing file's selection under the incoming one, and two
  // equal selections cannot show that.
  it("restores each file's own selection when two files differ", async () => {
    seed(A, lines("a", 40, 300), { kind: "edit", editing: true });
    seed(B, lines("b", 40, 300), { kind: "edit", editing: true });
    const ta = textarea();
    activateFile(A);
    ta.setSelectionRange(10, 25, "backward");
    await frames();
    activateFile(B);
    ta.setSelectionRange(300, 340, "forward");
    await frames();

    activateFile(A);
    expect([ta.selectionStart, ta.selectionEnd, ta.selectionDirection]).toEqual([
      10,
      25,
      "backward",
    ]);
    activateFile(B);
    expect([ta.selectionStart, ta.selectionEnd, ta.selectionDirection]).toEqual([
      300,
      340,
      "forward",
    ]);
  });

  it("keeps a diff's scroll position, on both axes, across a rebuild of the pane", async () => {
    const old = lines("a", 300, 200);
    const next = old.replace("a line 150", "A LINE 150").replace("a line 7 ", "A LINE 7 ");
    seed(A, old, {
      kind: "diff",
      diffSource: {
        oldContent: old,
        newContent: next,
        oldLabel: "saved",
        newLabel: "unsaved",
        fromGit: false,
      },
    });
    seed(B, lines("b", 400));
    activateFile(A);
    await frames(3);
    const split = (): HTMLElement => document.querySelector<HTMLElement>(".diff-pane-split")!;
    const col = (): HTMLElement => document.querySelector<HTMLElement>(".diff-col-old")!;
    const bar = (): HTMLElement => document.querySelector<HTMLElement>(".diff-pane-hbar")!;
    expect(split().scrollHeight).toBeGreaterThan(split().clientHeight);
    split().scrollTop = 800;
    col().scrollLeft = 120;
    await frames();
    const pane = split();
    activateFile(B);
    activateFile(A);
    expect(split()).not.toBe(pane);
    expect(split().scrollTop).toBe(800);
    expect(col().scrollLeft).toBe(120);
    await frames(4);
    expect(bar().scrollLeft).toBe(120);
  });
});
