// Copy from the read state. Rows are windowed, so the browser's own copy would drop rows that are
// not seated; the copy is written from the buffer and its line-ending record instead.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { mountEditorView } from "./__test-helpers__/editor-dom.js";
import { initViewerCopy } from "./viewer-copy.js";
import { viewer } from "./editor-pane.js";
import { WholeRows } from "./viewer-rows.js";
import { EolTracker, normalize } from "./viewer-eol.js";
import { fileStates, freshState, setActiveFilePath } from "./editor-types.js";

const PATH = "/workspace/crlf.txt";
let host: HTMLElement;

beforeAll(() => {
  host = mountEditorView();
  initViewerCopy();
});

afterAll(() => {
  host.remove();
});

afterEach(() => {
  document.getSelection()?.removeAllRanges();
  fileStates.clear();
  setActiveFilePath("");
});

const root = (): HTMLElement => document.getElementById("editor-viewer") as HTMLElement;

/** Open `raw` (its line endings intact) in the read state. */
function showFile(raw: string): void {
  const { text, record } = normalize(raw);
  const state = freshState(PATH);
  state.loaded = true;
  state.original.value = text;
  state.current.value = text;
  state.eol = { saved: record, tracker: new EolTracker(text, record) };
  fileStates.set(PATH, state);
  setActiveFilePath(PATH);
  root().classList.remove("hidden");
  viewer().show({ rows: new WholeRows(text), runs: null, agentLines: new Set() });
}

function rowText(row: number): Text {
  const node = root().querySelector(
    `.viewer-row[data-row="${String(row)}"] .viewer-text`,
  )?.firstChild;
  if (!(node instanceof Text)) {
    throw new Error(`row ${String(row)} is not seated`);
  }
  return node;
}

/** Fire a copy at the viewer and return what it put on the clipboard. */
function copy(): string {
  const data = new DataTransfer();
  root().dispatchEvent(
    new ClipboardEvent("copy", { clipboardData: data, bubbles: true, cancelable: true }),
  );
  return data.getData("text/plain");
}

describe("copy from the read state", () => {
  it("writes a selection across rows with the file's own CRLF endings", () => {
    showFile("ab\r\ncd\r\n");
    const range = document.createRange();
    range.setStart(rowText(0), 1);
    range.setEnd(rowText(1), 1);
    document.getSelection()?.addRange(range);
    expect(copy()).toBe("b\r\nc");
  });

  it("copies the whole file byte for byte after Ctrl+A, rows not seated included", () => {
    const raw = Array.from({ length: 5000 }, (_, i) => `line ${String(i)}\r\n`).join("");
    showFile(raw);
    root().dispatchEvent(
      new KeyboardEvent("keydown", { key: "a", ctrlKey: true, bubbles: true, cancelable: true }),
    );
    expect(copy()).toBe(raw);
  });
});
