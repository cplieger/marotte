// ---------------------------------------------------------------------------
// Position cues inside the editor pane: take a line into view, flash it, and mark a span; and the
// per-file viewport the shared pane is put back to on every activation. The read state's
// geometry is the viewer's; the edit state's is its EditSurface's.
// ---------------------------------------------------------------------------

import { join } from "@cplieger/keyenc";

import { $ } from "./dom.js";
import { fileStates, getActiveFilePath, isShown } from "./editor-types.js";
import type { EditorView, FileState } from "./editor-types.js";
import { editDecor, editSurface, editing, paneBody, viewer } from "./editor-pane.js";
import type { LineLocation } from "./viewer-rows.js";

/** A box with no layout reads every offset as 0, so a scroll recorded from a
 *  hidden view would overwrite the position it is supposed to keep. */
function laidOut(box: Element): boolean {
  return box.getClientRects().length > 0;
}

function activeView(): EditorView | null {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return null;
  }
  state.view ??= {
    topLine: 1,
    topRowInLine: 0,
    left: 0,
    areaLeft: 0,
    selection: null,
    diff: null,
  };
  return state.view;
}

/** The text offset where 1-based `line` starts, or the end when the text is shorter. */
function lineStart(text: string, line: number): number {
  let at = 0;
  for (let n = 1; n < line; n++) {
    const lf = text.indexOf("\n", at);
    if (lf === -1) {
      return text.length;
    }
    at = lf + 1;
  }
  return at;
}

/** Record the active file's position as the reader moves it. Continuous rather than at
 *  departure: leaving the editor tab runs no editor code. */
export function trackEditorView(): void {
  const body = paneBody();
  body.addEventListener(
    "scroll",
    () => {
      const view =
        laidOut(body) && !$.editorViewer.classList.contains("hidden") ? activeView() : null;
      const rows = viewer().rows;
      if (view !== null && rows !== null) {
        const row = viewer().topRow();
        view.topLine = rows.lines[row] ?? 1;
        view.topRowInLine = rows.offsetInLine(row);
        view.left = body.scrollLeft;
      }
    },
    { passive: true },
  );
  const area = $.editorContent;
  area.addEventListener(
    "scroll",
    () => {
      const view = laidOut(area) ? activeView() : null;
      if (view !== null) {
        // The textarea does not cut lines, so a scroll that keeps the line keeps the read state's
        // row inside it.
        const line = editSurface().topLine();
        if (line !== view.topLine) {
          view.topLine = line;
          view.topRowInLine = 0;
        }
        view.areaLeft = area.scrollLeft;
      }
    },
    { passive: true },
  );
}

/** Take the textarea's selection into the file's view. Must run before anything assigns
 *  `.value`, which resets the selection; there is no event for it. */
export function captureSelection(state: FileState): void {
  const m = state.mode.value;
  const editingNow = (m.kind === "text" && m.editing) || m.kind === "conflict";
  if (!editingNow || !isShown(state)) {
    return;
  }
  const area = $.editorContent;
  const view = activeView();
  if (view !== null) {
    view.selection = {
      start: area.selectionStart,
      end: area.selectionEnd,
      direction: area.selectionDirection,
    };
  }
}

/** Put the shared pane back where this file was left, by logical line and the cut row inside it.
 *  Never focuses. */
export function restoreEditorView(state: FileState): void {
  const view = state.view;
  if (view === null) {
    return;
  }
  if (editing()) {
    const area = $.editorContent;
    // A first edit has no selection yet; the caret starts on the line in view, not at the end the
    // `.value` write left it, where the first keystroke would scroll to.
    if (view.selection === null) {
      const at = lineStart(area.value, view.topLine);
      area.setSelectionRange(at, at);
    } else {
      area.setSelectionRange(view.selection.start, view.selection.end, view.selection.direction);
    }
    editSurface().scrollToLine(view.topLine, "top");
    area.scrollLeft = view.areaLeft;
    return;
  }
  if (!$.editorViewer.classList.contains("hidden")) {
    const rows = viewer().rows;
    if (rows !== null) {
      viewer().scrollToRow(rows.rowInLine(view.topLine, view.topRowInLine), "top");
    }
    paneBody().scrollLeft = view.left;
  }
}

/** Restore and track a freshly built diff pane. Its body is the vertical scroller and its
 *  columns the horizontal ones (`diff-pane.ts`); the pane is rebuilt on every repaint, so the
 *  position lives on the file, keyed by the comparison. */
export function bindDiffView(
  state: FileState,
  pane: HTMLElement,
  src: { oldLabel: string; newLabel: string; kind: string },
): void {
  const key = join(src.kind, src.oldLabel, src.newLabel);
  const split = (): HTMLElement | null => pane.querySelector<HTMLElement>(".diff-pane-split");
  const cols = (): HTMLElement[] => [
    ...pane.querySelectorAll<HTMLElement>(".diff-pane-split > .diff-col"),
  ];
  const saved = state.view?.diff;
  const body = split();
  if (saved?.key === key && body !== null) {
    body.scrollTop = saved.top;
    for (const col of cols()) {
      col.scrollLeft = saved.left;
    }
  }
  pane.addEventListener(
    "scroll",
    () => {
      const now = split();
      if (now === null || !laidOut(now) || getActiveFilePath() !== state.path) {
        return;
      }
      const view = activeView();
      if (view !== null) {
        view.diff = {
          key,
          top: now.scrollTop,
          left: Math.max(0, ...cols().map((c) => c.scrollLeft)),
        };
      }
    },
    { capture: true, passive: true },
  );
}

/** Go to line: 1-based `line`'s first row lands at the top; a line past the end lands on the last
 *  one, and the answer says which line was shown. */
export function scrollToEditorLine(line: number): LineLocation | null {
  if (editing()) {
    return editSurface().scrollToLine(line, "top");
  }
  const rows = viewer().rows;
  if (rows === null || $.editorViewer.classList.contains("hidden")) {
    return null;
  }
  const loc = rows.lineToRow(line);
  viewer().scrollToRow(loc.row, "top");
  return loc;
}

/** Flash 1-based `line` for 1.2 s. */
export function flashEditorLine(line: number): void {
  if (editing()) {
    editDecor().flash(line);
    return;
  }
  const rows = viewer().rows;
  if (rows !== null) {
    viewer().flashRow(rows.lineToRow(line).row);
  }
}

/** Bring the buffer span `[offset, offset + length)` on 1-based `line` on screen, flash its row
 *  and mark it. In edit state the textarea's selection lands on it too, so leaving the find box
 *  puts the caret there. */
export function revealBufferHit(text: string, offset: number, length: number, line: number): void {
  if (editing()) {
    $.editorContent.setSelectionRange(offset, offset + length);
    editSurface().scrollToLine(line, "third");
    editDecor().flash(line);
    const lineStart = offset === 0 ? 0 : text.lastIndexOf("\n", offset - 1) + 1;
    editDecor().mark(line, text.slice(lineStart, offset), text.slice(offset, offset + length));
    return;
  }
  const rows = viewer().rows;
  if (rows === null) {
    return;
  }
  const row = rows.rowOfOffset(offset);
  viewer().scrollToRow(row, "third");
  viewer().flashRow(row);
  viewer().markRange(offset, offset + length);
}

export function clearEditorMark(): void {
  viewer().clearMark();
  editDecor().clearMark();
}
