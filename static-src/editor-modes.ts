// ---------------------------------------------------------------------------
// The editor pane's one painter. Every editor tab paints the same elements, so every transition
// ends in restoreUI, which derives the whole pane (surface, banner, controls, conflict overlay,
// live toggle) from the file's record and keeps nothing of its own.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { paintIfChanged } from "./paint-sig.js";
import { renderDiffModeUI } from "./editor-diff.js";
import { renderConflictModeUI, renderConflictOverlay } from "./editor-conflict.js";
import {
  paintCommonControls,
  renderLoadingSurface,
  renderTextModeUI,
  showSurface,
} from "./editor-ui.js";
import { isShown, setPainter, type FileState } from "./editor-types.js";
import { GONE_SENTENCE, paintLiveButton } from "./viewer-live.js";
import type { FileRefusal } from "./wire/types.gen.js";

/** Paint `state` onto the pane. A file that is not on screen paints nothing: its record keeps
 *  every outcome for the activation that shows it, and a retired record has no activation. */
export function restoreUI(state: FileState): void {
  if (!isShown(state)) {
    return;
  }
  paintBanner(state);
  if (state.error.value !== "" || awaiting(state)) {
    if (state.error.value === "") {
      renderLoadingSurface(state);
    } else {
      showSurface("none");
    }
    $.editorEditBtn.disabled = true;
    $.editorEditBtn.classList.add("hidden");
    $.editorCancelBtn.classList.add("hidden");
    $.editorSaveBtn.classList.add("hidden");
    $.editorDiffBtn.classList.add("hidden");
    paintCommonControls(state);
  } else {
    switch (state.mode.value.kind) {
      case "diff":
        renderDiffModeUI(state);
        break;
      case "conflict":
        renderConflictModeUI(state);
        break;
      case "text":
      case "markdown":
      case "image":
      case "binary":
      case "large":
        renderTextModeUI(state);
        break;
    }
  }
  renderConflictOverlay(state);
  paintLiveButton(state);
}

setPainter(restoreUI);

/** Whether the view waits on a load: a git diff its load has not settled, or a file not yet read.
 *  A diff that carries its own pair paints without one. */
function awaiting(state: FileState): boolean {
  const m = state.mode.value;
  if (m.kind === "diff") {
    return m.diffSource.kind === "git" && m.diffSource.pending;
  }
  return !state.loaded;
}

type SaveWay = "overwrite" | "download" | "discard";

const WAY_LABELS: Record<SaveWay, string> = {
  overwrite: "Overwrite",
  download: "Download my version",
  discard: "Discard my changes",
};

/** The banner: a failed load, else the save outcome the record keeps, else a file gone from
 *  disk. */
function bannerOf(state: FileState): { text: string; ways: readonly SaveWay[] } | null {
  if (state.error.value !== "") {
    return { text: state.error.value, ways: [] };
  }
  const alert = state.alert.value;
  if (alert?.kind === "refused") {
    const ways: readonly SaveWay[] =
      alert.reason === "uncertain" ? ["download", "discard"] : ["overwrite", "download", "discard"];
    return { text: refusalSentence(alert.reason), ways };
  }
  if (alert !== null) {
    return { text: alert.sentence, ways: [] };
  }
  return state.live === "gone" ? { text: GONE_SENTENCE, ways: [] } : null;
}

/** Signature-guarded, so a repaint keeps the focus on a way-on button. editor-core handles the
 *  buttons' clicks by their `data-save-way`. */
function paintBanner(state: FileState): void {
  const banner = bannerOf(state);
  const host = $.editorError;
  host.classList.toggle("hidden", banner === null);
  if (banner === null) {
    paintIfChanged(host, [], () => []);
    return;
  }
  paintIfChanged(host, [banner.text, ...banner.ways], () => {
    if (banner.ways.length === 0) {
      return [document.createTextNode(banner.text)];
    }
    const buttons = banner.ways.map((way) =>
      el(
        "button",
        { type: "button", className: "btn-small", "data-save-way": way },
        WAY_LABELS[way],
      ),
    );
    return [
      el("span", { className: "editor-error-text" }, banner.text),
      el("span", { className: "editor-error-actions" }, ...buttons),
    ];
  });
}

/** Why a save was refused, in the reader's terms. */
function refusalSentence(r: FileRefusal | "uncertain"): string {
  if (r === "uncertain") {
    return "This file mixes line endings, and the last edit could not keep each line's ending exactly, so it was not saved.";
  }
  switch (r.content_kind) {
    case "too_large":
      return "This file changed on disk and is now larger than 2 MiB.";
    case "binary":
      return "This file changed on disk and is now binary.";
    case "not_utf8":
      return "This file changed on disk and is no longer UTF-8 text.";
    default:
      return r.error;
  }
}
