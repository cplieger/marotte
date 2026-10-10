// ---------------------------------------------------------------------------
// Editor core: init, mode switches, save. Types and state live in editor-types.ts, openers in
// editor-openers.ts, rendering in editor-ui.ts and the windowed renderer in viewer-render.ts.
// ---------------------------------------------------------------------------

import { effect, el } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { confirm as confirmDialog } from "./confirm.js";
import { parseConflicts } from "./conflict.js";
import { saveFile as saveFileAction, type SaveOutcome } from "./actions/editor.js";
import { isPending, registerCleanup } from "./actions/index.js";
import { renderConflictOverlay } from "./editor-conflict.js";
import { getAgentLines, identifiedDownloadURL } from "./editor-ui.js";
import { restoreUI } from "./editor-modes.js";
import { restoreEditorView, trackEditorView } from "./editor-scroll.js";
import { activateFile, openFileGitDiff, startGitDiffLoad } from "./editor-openers.js";
import {
  baselineHeld,
  commitBaseline,
  enterView,
  fileStates,
  getActiveFilePath,
  activeDirty,
  bufferDiffSource,
  gitDiffSource,
  isShown,
  recordFor,
} from "./editor-types.js";
import { staleDiskText } from "./file-identity.js";
import type { FileMode, FileState } from "./editor-types.js";
import { rulesFor } from "./viewer-rules.js";
import { markGitDirty } from "./git.js";
import { onWorkspaceRoot, relToWorkspace } from "./workspace.js";
import { isPreviewablePage } from "./preview-page.js";
import { openWebPreview } from "./web-open.js";
import { iconEl } from "./icon-el.js";
import { ICON_DOWNLOAD, ICON_GIT_COMMIT, ICON_TAB_WEB } from "./icons.js";
import { onGitStatusChange, statusForPath } from "./git-status-store.js";
import { isViewableImage } from "./file-extensions.js";
import { normalize, serialize } from "./viewer-eol.js";
import { editSurface } from "./editor-pane.js";
import { countLines } from "./viewer-rows.js";
import { initLive, liveHold, liveSaved } from "./viewer-live.js";
import { initViewerCopy } from "./viewer-copy.js";
import { initGoto } from "./editor-goto.js";
import type { FileRefusal } from "./wire/types.gen.js";

export function initEditor(): void {
  $.editorEditBtn.addEventListener("click", startEditing);
  $.editorCancelBtn.addEventListener("click", confirmStopEditing);
  $.editorSaveBtn.addEventListener("click", saveFile);
  $.editorDiffBtn.addEventListener("click", toggleDiffMode);
  $.editorDownloadBtn.replaceChildren(iconEl(ICON_DOWNLOAD));
  $.editorDownloadBtn.addEventListener("click", downloadActive);
  $.editorError.addEventListener("click", onSaveWay);
  wireTextarea();
  trackEditorView();
  initLive();
  initViewerCopy();
  initGoto();

  // Sole owner of the save button's disabled state.
  effect(() => {
    $.editorSaveBtn.disabled =
      !activeDirty.value ||
      isPending("editor.save_file") ||
      fileStates.get(getActiveFilePath())?.alert.value?.kind === "refused";
  });

  // The glyph is injected rather than drawn in static/index.html, because a concept with an
  // icons.ts entry may not be redrawn there.
  $.editorGitDiffBtn.replaceChildren(iconEl(ICON_GIT_COMMIT));
  $.editorGitDiffBtn.addEventListener("click", toggleGitDiffMode);

  // Sole owner of the git-diff button's visibility and its toggle attributes. Two triggers: this
  // effect for the active path, error and mode signals, and the git-status subscription armed
  // below for a letter arriving after the file is open (statusForPath reads a plain Map).
  effect(() => {
    if (getActiveFilePath() !== "") {
      armGitStatusWatch();
    }
    paintGitDiffBtn();
  });

  $.editorPreviewBtn.replaceChildren(iconEl(ICON_TAB_WEB));
  $.editorPreviewBtn.addEventListener("click", () => {
    openWebPreview(previewPath());
  });
  effect(() => {
    paintPreviewBtn();
  });
  registerCleanup(onWorkspaceRoot(paintPreviewBtn));
}

/** Shadow every native edit for the line-ending record and hold live refresh around it: on a
 *  mutating `beforeinput` and through an IME composition, before the buffer turns dirty. */
function wireTextarea(): void {
  const area = $.editorContent;
  let conflictReparseQueued = false;
  // Each release is bound to the record its hold began on (liveHold), never to the path on screen
  // when the matching end event arrives.
  let releaseTyping = (): void => undefined;
  let releaseComposing = (): void => undefined;
  area.addEventListener("beforeinput", (e) => {
    const state = fileStates.get(getActiveFilePath());
    if (state === undefined) {
      return;
    }
    state.eol?.tracker.noteBeforeInput(area.selectionStart, area.selectionEnd, e.inputType);
    releaseTyping();
    releaseTyping = liveHold(state, "typing");
  });
  area.addEventListener("compositionstart", () => {
    const state = fileStates.get(getActiveFilePath());
    releaseComposing();
    releaseComposing = state === undefined ? () => undefined : liveHold(state, "composing");
  });
  const endComposition = (): void => {
    releaseComposing();
    releaseComposing = () => undefined;
  };
  area.addEventListener("compositionend", endComposition);
  // Some IMEs drop compositionend when the field loses focus mid-candidate.
  area.addEventListener("blur", endComposition);
  area.addEventListener("input", () => {
    const state = fileStates.get(getActiveFilePath());
    if (state === undefined) {
      return;
    }
    state.eol?.tracker.noteInput(area.value);
    state.current.value = area.value;
    releaseTyping();
    releaseTyping = () => undefined;
    editSurface().setLines(countLines(area.value), getAgentLines(state.path));
    if (state.mode.value.kind === "conflict" && !conflictReparseQueued) {
      // One re-parse per frame: a synchronous one per keystroke janked typing in large conflicts.
      conflictReparseQueued = true;
      requestAnimationFrame(() => {
        conflictReparseQueued = false;
        const st = fileStates.get(getActiveFilePath());
        if (st?.mode.value.kind !== "conflict") {
          return;
        }
        st.mode.value = {
          kind: "conflict",
          conflict: parseConflicts(st.current.value),
          editing: true,
        };
        renderConflictOverlay(st);
      });
    }
  });
}

function paintPreviewBtn(): void {
  const state = fileStates.get(getActiveFilePath());
  const large = state?.mode.value.kind === "large";
  $.editorPreviewBtn.classList.toggle("hidden", large || !isPreviewablePage(previewPath()));
}

/** The server's ServeMux collapses `/file//workspace/x` to `/file/workspace/x` on a cold load, so
 *  an editor path can lack its leading slash. */
function previewPath(): string {
  const path = getActiveFilePath();
  return path === "" || path.startsWith("/") ? path : `/${path}`;
}

let gitStatusWatched = false;

function armGitStatusWatch(): void {
  if (gitStatusWatched) {
    return;
  }
  gitStatusWatched = true;
  registerCleanup(onGitStatusChange(paintGitDiffBtn));
}

/** Paint #editor-git-diff-btn: shown for a text file with no error, read or in a git diff, and
 *  differing from the ref (or already in the diff, since it is also the way out). */
function paintGitDiffBtn(): void {
  const btn = $.editorGitDiffBtn;
  const path = getActiveFilePath();
  const state = path === "" ? undefined : fileStates.get(path);
  const m = state?.mode.value;
  const inGitDiff = m?.kind === "diff" && m.diffSource.kind === "git";
  const readingText = (m?.kind === "text" && !m.editing) || m?.kind === "markdown";
  // The extension clause covers an image opened straight into a git diff, before its view is set.
  const show =
    state?.error.value === "" &&
    !isViewableImage(path) &&
    (readingText || inGitDiff) &&
    (statusForPath(path) !== "" || inGitDiff);
  btn.classList.toggle("hidden", !show);
  btn.setAttribute("aria-pressed", inGitDiff ? "true" : "false");
  // The accessible NAME is stable across both states and the state travels on aria-pressed alone.
  btn.setAttribute("aria-label", "View diff vs HEAD");
  btn.setAttribute(
    "data-tooltip",
    inGitDiff ? "Diff vs HEAD. Exit diff view" : "View diff vs HEAD",
  );
}

/** Enter or leave the diff against HEAD; the exit is the same path as toggleDiffMode's. */
function toggleGitDiffMode(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return;
  }
  const m = state.mode.value;
  if (m.kind === "diff" && m.diffSource.kind === "git") {
    leaveDiff(state);
    return;
  }
  openFileGitDiff(state.path, "HEAD");
}

/** The read state the rules pick for the file. */
function readView(state: FileState): FileMode {
  return rulesFor(state.facts.value, state.path, "read").view === "markdown"
    ? { kind: "markdown" }
    : { kind: "text", editing: false };
}

function leaveDiff(state: FileState): void {
  enterView(state, readView(state));
  showView(state);
}

/** Paint a view change and, when the file is on screen, land it on the file's line. */
function showView(state: FileState): void {
  restoreUI(state);
  if (isShown(state)) {
    restoreEditorView(state);
  }
}

// --- Mode switches ---

function toggleDiffMode(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return;
  }
  if (state.mode.value.kind === "diff") {
    leaveDiff(state);
    return;
  }
  if (state.current.value === state.original.value) {
    return;
  }
  enterView(state, { kind: "diff", diffSource: bufferDiffSource() });
  restoreUI(state);
}

function startEditing(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state?.loaded !== true || !rulesFor(state.facts.value, state.path, "edit").edit) {
    return;
  }
  const m = state.mode.value;
  state.returnToGitDiff =
    m.kind === "diff" && m.diffSource.kind === "git"
      ? { ref: m.diffSource.ref, repo: state.repo }
      : null;
  enterView(state, { kind: "text", editing: true });
  showView(state);
  // showView put the textarea on the file's line; a scrolling focus would move it to the caret.
  $.editorContent.focus({ preventScroll: true });
}

function confirmStopEditing(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined) {
    return;
  }
  if (state.current.value !== state.original.value) {
    void (async () => {
      const ok = await confirmDialog("Discard unsaved changes?", "Discard", "destructive");
      if (ok) {
        stopEditing(state);
      }
    })();
  } else {
    stopEditing(state);
  }
}

/** Drop the edit: the buffer, and the line-ending record with its logs, go back to what was
 *  saved, and a save outcome about the dropped edit goes with them. */
function discardEdits(state: FileState): void {
  state.current.value = state.original.value;
  if (state.eol !== null) {
    state.eol.tracker.reset(state.original.value, state.eol.saved);
  }
  state.alert.value = null;
}

/** Leave the edit for the view it came from, a git diff waiting on a fresh load. */
function leaveEdit(state: FileState): void {
  const back = state.returnToGitDiff;
  state.returnToGitDiff = null;
  if (back === null) {
    enterView(state, readView(state));
  } else {
    state.repo = back.repo;
    enterView(state, { kind: "diff", diffSource: gitDiffSource(back.ref) });
  }
  showView(state);
  startGitDiffLoad(state);
}

function stopEditing(state: FileState): void {
  discardEdits(state);
  leaveEdit(state);
}

// --- Save ---

function saveFile(): void {
  const state = fileStates.get(getActiveFilePath());
  const eol = state?.eol;
  if (
    state === undefined ||
    eol === undefined ||
    eol === null ||
    state.alert.value?.kind === "refused"
  ) {
    return;
  }
  const text = $.editorContent.value;
  const unchanged = text === state.original.value;
  if (!unchanged && eol.tracker.uncertain) {
    showSaveRefusal(state, "uncertain");
    return;
  }
  dispatchSave(state, text, state.fileId === "" ? undefined : state.fileId);
}

/** PUT the serialized buffer; `fileId` absent writes over whatever is on disk (Overwrite). */
function dispatchSave(state: FileState, text: string, fileId: string | undefined): void {
  const eol = state.eol;
  const record = recordFor(state, text);
  if (eol === null || record === null) {
    return;
  }
  const args: Parameters<typeof saveFileAction.dispatch>[0] = {
    path: state.path,
    content: serialize(text, record),
  };
  if (fileId !== undefined) {
    args.fileId = fileId;
  }
  // A save's answer lands on its file's record whichever file is on screen by then. One whose tab
  // closed, or whose baseline a read replaced meanwhile, is recorded nowhere and paints nothing.
  const settles = baselineHeld(state);
  void saveFileAction.dispatch(args, {
    onError: (e) => {
      if (!settles()) {
        return;
      }
      state.alert.value = { kind: "failed", sentence: e.message || "Save failed" };
      restoreUI(state);
    },
    onSuccess: (outcome: SaveOutcome) => {
      if (outcome.kind === "saved") {
        // The user's own editor is a second writer with no announcement, so the git surfaces are
        // told of the write even when its answer settles nowhere; scoped to the one file.
        markGitDirty([relToWorkspace(state.path)]);
      }
      if (!settles()) {
        return;
      }
      if (outcome.kind === "stale") {
        onStaleSave(state, outcome.refusal);
        return;
      }
      // `current` is not touched: the reader may have typed while the save was in flight.
      commitBaseline(state, {
        fileId: outcome.result.file_id,
        text,
        eol: { saved: record, tracker: eol.tracker },
      });
      state.alert.value = null;
      liveSaved(state.path);
      const m = state.mode.value;
      if (state.returnToGitDiff !== null) {
        leaveEdit(state);
        return;
      }
      if (m.kind === "conflict" && m.conflict.hunks.length === 0) {
        enterView(state, { kind: "text", editing: false });
      }
      // A diff of the buffer opened while the save was in flight now compares against what it
      // wrote; with nothing left unsaved there is no diff to show.
      if (m.kind === "diff" && m.diffSource.kind === "buffer" && !state.dirty.value) {
        leaveDiff(state);
        return;
      }
      restoreUI(state);
    },
  });
}

/** A save refused because the file moved. Text on disk becomes `original` and the view diffs the
 *  buffer, which is kept, against it; anything else waits for the reader's choice. */
function onStaleSave(state: FileState, refusal: FileRefusal): void {
  const eol = state.eol;
  const onDisk = staleDiskText(refusal);
  if (onDisk === null || eol === null) {
    showSaveRefusal(state, refusal);
    return;
  }
  const disk = normalize(onDisk.text);
  commitBaseline(state, {
    fileId: onDisk.fileId,
    text: disk.text,
    eol: { saved: disk.record, tracker: eol.tracker },
  });
  state.alert.value = { kind: "stale", sentence: refusal.error };
  enterView(state, { kind: "diff", diffSource: bufferDiffSource() });
  restoreUI(state);
}

function showSaveRefusal(state: FileState, r: FileRefusal | "uncertain"): void {
  state.alert.value = { kind: "refused", reason: r };
  restoreUI(state);
}

/** The refused-save banner's ways on, painted by editor-modes with a `data-save-way` each. */
function onSaveWay(e: Event): void {
  const way = (e.target as Element | null)
    ?.closest("[data-save-way]")
    ?.getAttribute("data-save-way");
  const state = fileStates.get(getActiveFilePath());
  if (state === undefined || state.alert.value?.kind !== "refused") {
    return;
  }
  switch (way) {
    case "overwrite":
      state.alert.value = null;
      restoreUI(state);
      dispatchSave(state, state.current.value, undefined);
      return;
    case "download":
      downloadBuffer(state);
      return;
    case "discard":
      discardEdits(state);
      state.loaded = false;
      enterView(state, { kind: "text", editing: false });
      activateFile(state.path);
      return;
    default:
      return;
  }
}

function basename(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

/** Save the buffer as a download, with the endings the record holds. */
function downloadBuffer(state: FileState): void {
  const text = state.current.value;
  const record = recordFor(state, text);
  if (record === null) {
    return;
  }
  const blob = new Blob([serialize(text, record)], {
    type: "application/octet-stream",
  });
  const url = URL.createObjectURL(blob);
  clickDownload(url, basename(state.path));
  setTimeout(() => {
    URL.revokeObjectURL(url);
  }, 0);
}

/** Download the bytes the view shows, pinned to their identity when it has one. */
function downloadActive(): void {
  const state = fileStates.get(getActiveFilePath());
  if (state !== undefined) {
    clickDownload(identifiedDownloadURL(state), basename(state.path));
  }
}

/** Safe only because the server answers `Content-Disposition: attachment`: a navigated `.svg`
 *  is script-capable. */
function clickDownload(href: string, name: string): void {
  const a = el("a", { href, download: name, rel: "noopener" });
  document.body.appendChild(a);
  a.click();
  a.remove();
}
