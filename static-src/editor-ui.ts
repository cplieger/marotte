// ---------------------------------------------------------------------------
// Editor UI: the pane's surfaces, the read surface of each view, and the toolbar.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { highlightRuns } from "./highlight.js";
import { getActiveId } from "./store.js";
import { fetchAgentLines as fetchAgentLinesAction } from "./actions/editor.js";
import { goToLine, paintGoto } from "./editor-goto.js";
import { renderMarkdownDoc } from "./editor-markdown.js";
import { fileDownloadURL } from "./utils-url.js";
import type { FileState } from "./editor-types.js";
import { getActiveFilePath, fileStates, isShown, issuedNow } from "./editor-types.js";
import { rulesFor, type Requested, type Rules } from "./viewer-rules.js";
import { WholeRows, countLines } from "./viewer-rows.js";
import { editSurface, paneBody, viewer } from "./editor-pane.js";

// --- Agent line tracking ---

interface LineRange {
  start_line: number;
  end_line: number;
}
const agentLineCache = new Map<string, LineRange[]>();
const agentLineSetCache = new Map<string, Set<number>>();

export function getAgentLines(path: string): Set<number> {
  const cached = agentLineSetCache.get(path);
  if (cached !== undefined) {
    return cached;
  }
  const ranges = agentLineCache.get(path);
  if (ranges === undefined) {
    return new Set();
  }
  const lines = new Set<number>();
  for (const r of ranges) {
    for (let i = r.start_line; i <= r.end_line; i++) {
      lines.add(i);
    }
  }
  agentLineSetCache.set(path, lines);
  return lines;
}

export async function fetchAgentLines(path: string): Promise<void> {
  const chatID = getActiveId();
  if (chatID === "") {
    return;
  }
  fetchAgentLinesAction.cancel();
  const data = await fetchAgentLinesAction.dispatch({ chatID, path });
  if (data === null) {
    return;
  }
  if (getActiveFilePath() !== path) {
    return;
  }
  agentLineCache.set(path, data.changes);
  agentLineSetCache.delete(path);
  const state = fileStates.get(path);
  if (state?.loaded === true) {
    refreshLineMarks(state);
  }
}

/** Clear cached agent-line data for a path. Called when a file is
 *  closed so the per-file Maps don't grow unbounded over a session. */
export function clearAgentLineCache(path: string): void {
  agentLineCache.delete(path);
  agentLineSetCache.delete(path);
}

/** Re-draw the agent-modified marks on whichever surface shows line numbers. */
function refreshLineMarks(state: FileState): void {
  const marks = getAgentLines(state.path);
  if (!$.editorContent.classList.contains("hidden")) {
    editSurface().setLines(countLines(state.current.value), marks);
  } else if (!$.editorViewer.classList.contains("hidden") && state.rows !== null) {
    viewer().update({ rows: state.rows, runs: state.runs, agentLines: marks });
  }
}

// --- Surfaces ---

/** What the visible surface was painted from, empty once anything else takes the pane. */
let paintedFrom: readonly unknown[] = [];

/** Paint a surface unless it is already on screen from the same `parts`, so a transition that
 *  moves only chrome keeps the reader's selection, scroll and image. `paint` shows the surface. */
export function paintSurface(parts: readonly unknown[], paint: () => void): void {
  if (parts.length === paintedFrom.length && parts.every((p, i) => p === paintedFrom[i])) {
    return;
  }
  paint();
  paintedFrom = parts;
}

/** The pane's surfaces, exactly one visible. */
type Surface = "viewer" | "edit" | "markdown" | "image" | "notice" | "diff" | "none";

export function showSurface(surface: Surface): void {
  paintedFrom = [];
  $.editorViewer.classList.toggle("hidden", surface !== "viewer");
  $.editorContent.classList.toggle("hidden", surface !== "edit");
  $.editorEditGutter.classList.toggle("hidden", surface !== "edit");
  $.editorMarkdown.classList.toggle("hidden", surface !== "markdown");
  $.editorImage.classList.toggle("hidden", surface !== "image");
  $.editorNotice.classList.toggle("hidden", surface !== "notice");
  $.editorDiffPane.classList.toggle("hidden", surface !== "diff");
  paneBody().classList.toggle("is-editing", surface === "edit");
  if (surface !== "viewer") {
    viewer().clearMark();
  }
}

/** The textarea, with its gutter, showing the buffer. */
export function showEditSurface(state: FileState): void {
  if ($.editorContent.value !== state.current.value) {
    $.editorContent.value = state.current.value;
  }
  showSurface("edit");
  editSurface().setLines(countLines(state.current.value), getAgentLines(state.path));
}

/** The read state's rows for the buffer, rebuilt only when the text moved. */
function rowsFor(state: FileState): WholeRows {
  if (state.rows?.text !== state.current.value) {
    state.rows = new WholeRows(state.current.value);
    state.runs = null;
  }
  return state.rows;
}

let highlightTimer: ReturnType<typeof setTimeout> | undefined;

/** Paint source in the windowed viewer: plain at once, highlighted one task later, so tokenizing
 *  never delays the first paint. */
function renderTextSurface(state: FileState, highlight: boolean): void {
  const rows = rowsFor(state);
  paintSurface(["viewer", state, rows], () => {
    showSurface("viewer");
    viewer().show({ rows, runs: state.runs, agentLines: getAgentLines(state.path) }, state);
    scheduleHighlight(state, rows, highlight);
  });
}

function scheduleHighlight(state: FileState, rows: WholeRows, highlight: boolean): void {
  clearTimeout(highlightTimer);
  if (!highlight || state.runs !== null) {
    return;
  }
  highlightTimer = setTimeout(() => {
    if (!isShown(state) || state.rows !== rows || $.editorViewer.classList.contains("hidden")) {
      return;
    }
    state.runs = highlightRuns(rows.text, state.path);
    if (state.runs !== null) {
      viewer().update({ rows, runs: state.runs, agentLines: getAgentLines(state.path) });
    }
  }, 0);
}

/** Paint the IMAGE read surface: one `<img>` pointed at the byte-serving route, keyed by the
 *  identity the shell shows so the server refuses any other bytes.
 *
 *  Nothing else is the security requirement: the response for an `.svg` carries
 *  `Content-Type: image/svg+xml`, script-capable when NAVIGATED to, so this surface offers no
 *  "open raw" anchor and no `<iframe>`; an SVG referenced as an image cannot script. The `src`
 *  is a property on an element this function created, never an attribute funnel. */
function renderImageSurface(state: FileState): void {
  const img = el("img", { className: "editor-image-el" }) as HTMLImageElement;
  // The path IS the alt text: the reader knows what they opened, and naming it is what makes a
  // failed load legible.
  img.alt = state.path;
  img.addEventListener(
    "error",
    () => {
      img.replaceWith(
        el("span", { className: "img-missing" }, `Image not available: ${state.path}`),
      );
    },
    { once: true },
  );
  // Mounted before the src is assigned: `replaceWith` on a parentless node does nothing.
  $.editorImage.replaceChildren(img);
  img.src = identifiedDownloadURL(state);
  showSurface("image");
}

/** The pane while a load it is waiting on is in flight; loadFile's placeholder fills it. */
export function renderLoadingSurface(state: FileState): void {
  paintSurface(["loading", state], () => {
    showSurface("notice");
    $.editorNotice.replaceChildren();
  });
}

/** The download URL for the bytes this view shows: pinned to their identity when there is one. */
export function identifiedDownloadURL(state: FileState): string {
  const base = fileDownloadURL(state.path);
  return state.fileId === "" ? base : `${base}&file_id=${encodeURIComponent(state.fileId)}`;
}

const SIZE_UNITS = ["bytes", "KB", "MB", "GB", "TB"] as const;

function formatSize(bytes: number): string {
  let v = bytes;
  let u = 0;
  while (v >= 1024 && u < SIZE_UNITS.length - 1) {
    v /= 1024;
    u++;
  }
  return u === 0 ? `${String(bytes)} bytes` : `${v.toFixed(1)} ${SIZE_UNITS[u] ?? ""}`;
}

/** The notice views: one sentence, and for a binary file its facts. */
export function renderNoticeSurface(
  state: FileState,
  kind: "binary" | "large" | "undiffable",
): void {
  paintSurface(["notice", state, kind, state.notice], () => {
    paintNotice(state, kind);
  });
}

function paintNotice(state: FileState, kind: "binary" | "large" | "undiffable"): void {
  if (kind === "large") {
    $.editorNotice.replaceChildren(
      el(
        "p",
        { className: "editor-notice-text" },
        "File is too large to display. Download it to view.",
      ),
    );
  } else if (kind === "undiffable") {
    $.editorNotice.replaceChildren(
      el("p", { className: "editor-notice-text" }, "Cannot show a diff of this file."),
    );
  } else {
    const facts: HTMLElement[] = [
      el(
        "p",
        { className: "editor-notice-text" },
        "This is a binary file. Download it to open it.",
      ),
    ];
    if (state.notice !== null) {
      const modified = new Date(state.notice.modified);
      facts.push(
        el(
          "dl",
          { className: "editor-notice-facts" },
          el("dt", {}, "Size"),
          el("dd", {}, formatSize(state.notice.size)),
          el("dt", {}, "Modified"),
          el(
            "dd",
            {},
            Number.isNaN(modified.getTime()) ? state.notice.modified : modified.toLocaleString(),
          ),
        ),
      );
    }
    $.editorNotice.replaceChildren(...facts);
  }
  showSurface("notice");
}

/** Paint the READ surface of a loaded file for its mode. */
export function renderReadSurface(state: FileState): void {
  switch (state.mode.value.kind) {
    case "image":
      paintSurface(["image", state, state.fileId], () => {
        renderImageSurface(state);
      });
      return;
    case "binary":
      renderNoticeSurface(state, "binary");
      return;
    case "large":
      renderNoticeSurface(state, "large");
      return;
    case "markdown":
      paintSurface(["markdown", state, state.current.value], () => {
        renderMarkdownDoc($.editorMarkdown, state.current.value);
        showSurface("markdown");
      });
      return;
    default:
      renderTextSurface(state, rulesOf(state).highlight);
  }
}

// --- Rules and the toolbar ---

/** What the mode asks of the rules. */
export function requestedBy(state: FileState): Requested {
  const m = state.mode.value;
  if (m.kind === "diff") {
    return "diff";
  }
  return (m.kind === "text" && m.editing) || m.kind === "conflict" ? "edit" : "read";
}

/** The rules for the file as it stands, and for a settled git diff what git said of its base. */
export function rulesOf(state: FileState): Rules {
  const m = state.mode.value;
  const gitBase =
    m.kind === "diff" && m.diffSource.kind === "git" && !m.diffSource.pending
      ? m.diffSource.base
      : null;
  return rulesFor(state.facts.value, state.path, requestedBy(state), gitBase);
}

/** The toolbar controls every view shares: Download, Go to line and the read-only label. The
 *  large view shows Download alone. */
export function paintCommonControls(state: FileState): void {
  const rules = rulesOf(state);
  $.editorDownloadBtn.classList.toggle("hidden", !rules.download || state.error.value !== "");
  $.editorDownloadBtn.dataset["fileId"] = state.fileId;
  paintGoto(state);
  const label =
    rules.view === "large"
      ? null
      : (rules.readOnlyReason ?? (state.note === "" ? null : state.note));
  $.editorReadonlyLabel.textContent = label ?? "";
  $.editorReadonlyLabel.classList.toggle("hidden", label === null);
}

/** The read or edit state of a text-like view, with its controls. */
export function renderTextModeUI(state: FileState): void {
  const rules = rulesOf(state);
  const isModified = state.current.value !== state.original.value;
  $.editorDiffBtn.classList.toggle("hidden", !isModified);
  $.editorDiffBtn.setAttribute("data-tooltip", "View diff vs saved");
  $.editorDiffBtn.setAttribute("aria-label", "View diff vs saved");
  const m = state.mode.value;
  const editingNow = m.kind === "text" && m.editing;
  if (editingNow) {
    showEditSurface(state);
    $.editorEditBtn.classList.add("hidden");
    $.editorCancelBtn.classList.remove("hidden");
    $.editorSaveBtn.classList.remove("hidden");
  } else {
    renderReadSurface(state);
    const offerEdit = rules.edit && (m.kind === "text" || m.kind === "markdown");
    $.editorEditBtn.disabled = !offerEdit;
    $.editorEditBtn.classList.toggle("hidden", !offerEdit);
    $.editorCancelBtn.classList.add("hidden");
    $.editorSaveBtn.classList.add("hidden");
    if (m.kind === "image" || m.kind === "binary" || m.kind === "large") {
      $.editorDiffBtn.classList.add("hidden");
    }
  }
  paintCommonControls(state);
}

// --- Line jump ---

/** Go to the line the file was opened at, two frames on so the restored pane has laid out. A
 *  transition in those frames drops the jump (`issuedNow`). */
export function applyPendingLine(state: FileState): void {
  const line = state.pendingLine;
  if (line === null) {
    return;
  }
  state.pendingLine = null;
  const current = issuedNow(state);
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      if (current()) {
        goToLine(line);
      }
    });
  });
}
