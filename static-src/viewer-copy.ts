// Copy and select-all in the read state. The rows are windowed and each is its own block, so the
// browser's copy would drop rows that are not seated and break a cut line in two; every copy is
// written from the text and its line-ending record instead, byte-exact.

import { $ } from "./dom.js";
import * as toast from "./toast.js";
import { fileStates, getActiveFilePath, recordFor, type FileState } from "./editor-types.js";
import { serialize, serializeRange } from "./viewer-eol.js";
import { viewer } from "./editor-pane.js";

const SELECT_LESS = "Select less, or download the file.";

/** The exact text the current read-state selection names, or null when an endpoint sits in a
 *  row that is no longer seated. */
function selectedText(state: FileState): string | null {
  const v = viewer();
  const rows = v.rows;
  const record = rows === null ? null : recordFor(state, rows.text);
  if (rows === null || record === null) {
    return null;
  }
  if (v.selectsAll) {
    return serialize(rows.text, record);
  }
  const sel = document.getSelection();
  if (sel === null || sel.isCollapsed || sel.anchorNode === null || sel.focusNode === null) {
    return "";
  }
  const a = v.offsetAt(sel.anchorNode, sel.anchorOffset);
  const b = v.offsetAt(sel.focusNode, sel.focusOffset);
  if (a === null || b === null) {
    return null;
  }
  return serializeRange(rows.text, record, Math.min(a, b), Math.max(a, b));
}

export function initViewerCopy(): void {
  const root = $.editorViewer;
  root.addEventListener("copy", (e) => {
    const state = fileStates.get(getActiveFilePath());
    if (state === undefined) {
      return;
    }
    const text = selectedText(state);
    e.preventDefault();
    if (text === null) {
      toast.info(SELECT_LESS);
      return;
    }
    e.clipboardData?.setData("text/plain", text);
  });
  root.addEventListener("keydown", (e) => {
    if (e.key.toLowerCase() === "a" && (e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey) {
      e.preventDefault();
      document.getSelection()?.removeAllRanges();
      viewer().setAllSelected(true);
      return;
    }
    if (!e.ctrlKey && !e.metaKey) {
      viewer().setAllSelected(false);
    }
  });
  root.addEventListener("pointerdown", () => {
    viewer().setAllSelected(false);
  });
  document.addEventListener("selectionchange", () => {
    const sel = document.getSelection();
    const anchor = sel?.anchorNode ?? null;
    const focus = sel?.focusNode ?? null;
    if (sel === null || anchor === null || focus === null || !root.contains(anchor)) {
      return;
    }
    const v = viewer();
    const offsets = [v.offsetAt(anchor, sel.anchorOffset), v.offsetAt(focus, sel.focusOffset)];
    v.pinOffsets(offsets.filter((o): o is number => o !== null));
  });
}
