// ---------------------------------------------------------------------------
// Editor openers: file open, load, and adoption.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { batch, effect, el } from "@cplieger/reactive";
import { openEditorView, tabIdFor, setTabDirty, getActiveTabId } from "./tabs.js";
import { pushRoute } from "./router.js";
import { parseConflicts } from "./conflict.js";
import { abortSuggestion, clearSuggestionState } from "./editor-conflict.js";
import { apiGetTypedOrError } from "./api-client.js";
import { editorDocSkeleton, paintPlaceholder } from "./skeleton.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import type { SkeletonTimingController } from "@cplieger/ui-primitives/skeleton";
import { loadDiff as loadDiffAction } from "./actions/editor.js";
import type { FileMode, FileState } from "./editor-types.js";
import {
  commitBaseline,
  enterView,
  fileStates,
  getActiveFilePath,
  gitDiffSource,
  invalidate,
  isShown,
  issuedNow,
  setActiveFilePath,
  routeForPath,
  freshState,
} from "./editor-types.js";
import {
  applyPendingLine,
  fetchAgentLines,
  clearAgentLineCache,
  requestedBy,
} from "./editor-ui.js";
import { restoreUI } from "./editor-modes.js";
import { captureSelection, restoreEditorView } from "./editor-scroll.js";
import { registerCleanup } from "./actions/index.js";
import { BUS_EDITOR_VIEW_CHANGED, emitBus } from "./bus.js";
import { decodeFileStat } from "./wire/decoders.gen.js";
import type { FileRead, FileStat } from "./wire/types.gen.js";
import { rulesFor, type FileFacts, type Requested } from "./viewer-rules.js";
import { EolTracker, normalize } from "./viewer-eol.js";
import { isFileId, isIdentifiedText, statIdentity } from "./file-identity.js";
import { liveActivate, liveDispose, setLiveReload } from "./viewer-live.js";
import { paneBody, viewer } from "./editor-pane.js";
import { readFile } from "./viewer-reads.js";
import { configFilePath } from "./versions.js";
import { error as toastError } from "./toast.js";

// --- Active-load cancellation ---

/** Aborted on every activateFile call to cancel stale in-flight loads. */
let activeLoadController: AbortController | null = null;
registerCleanup(() => activeLoadController?.abort());

// --- Public openers ---

export function openFile(path: string, line?: number): void {
  // The view (text, markdown, image, binary, too large) is the rules' to pick once stat answers.
  const opts: OpenOpts = { mode: { kind: "text", editing: false } };
  if (line !== undefined) {
    opts.line = line;
  }
  open(path, opts);
}

/** Open a file in the server's config directory, or say why not when that directory is unknown.
 *  Whether the file is readable is the file browser's gate, not this opener's. */
export async function openConfigFile(name: "tools.json" | "config.json"): Promise<void> {
  const path = await configFilePath(name);
  if (path === null) {
    toastError(
      `Could not open ${name}: the server did not say where its config directory is. Try again.`,
    );
    return;
  }
  openFile(path);
}

export function openFileDiff(
  path: string,
  oldText: string,
  newText: string,
  opts: { oldLabel?: string; newLabel?: string } = {},
): void {
  open(path, {
    mode: {
      kind: "diff",
      diffSource: {
        kind: "pair",
        oldText,
        newText,
        oldLabel: opts.oldLabel ?? "before",
        newLabel: opts.newLabel ?? "after",
      },
    },
  });
}

/** Open a file in a BACKGROUND tab: no activation, no URL write. */
export function openFileInBackground(path: string): void {
  enterView(ensureFileState(path), { kind: "text", editing: false });
  void openEditorView(path, { activate: false });
}

/** Open a file's diff against a git ref; its activation fetches both sides. */
export function openFileGitDiff(path: string, ref = "HEAD"): void {
  open(path, { mode: { kind: "diff", diffSource: gitDiffSource(ref) } });
}

interface OpenOpts {
  mode: FileMode;
  line?: number;
  repo?: string;
}

// Per-file dirty->tab-indicator effects, disposed on close.
const dirtyTabUnbinds = new Map<string, () => void>();

/** This module's state for one path, created on first sight: by `open()`, or by `activateFile`
 *  for a tab this device did not open (restored at boot, or opened on another device). The dirty
 *  binding lives here so a restored tab is entitled to its unsaved mark. */
function ensureFileState(path: string): FileState {
  const existing = fileStates.get(path);
  if (existing !== undefined) {
    return existing;
  }
  const created = freshState(path);
  fileStates.set(path, created);
  dirtyTabUnbinds.set(
    path,
    effect(() => {
      // Resolved per run: the tab id is server-minted and does not exist on this effect's first run.
      setTabDirty(tabIdFor("editor", path), created.dirty.value);
    }),
  );
  return created;
}

function open(path: string, opts: OpenOpts): void {
  saveCurrentState();
  const state = ensureFileState(path);
  enterView(state, opts.mode);
  state.note = "";
  if (opts.repo !== undefined) {
    state.repo = opts.repo;
  }
  if (opts.line !== undefined && opts.line > 0) {
    state.pendingLine = opts.line;
  }
  // activateTab skips onShow when the tab was ALREADY active, so nothing would load it; read
  // before the open, which changes the answer. The non-empty check keeps two absences ("" for no
  // tab, "" for an empty strip) from reading as a match.
  const openID = tabIdFor("editor", path);
  const wasActive = openID !== "" && getActiveTabId() === openID;
  void openEditorView(path).then(() => {
    if (wasActive) {
      activateFile(path);
    }
  });
  const line = opts.line;
  pushRoute(line !== undefined && line > 0 ? { kind: "file", path, line } : { kind: "file", path });
}

type DiffOutcome = Awaited<ReturnType<typeof loadDiffAction.dispatch>["outcome"]>;

export async function fetchGitDiffSources(
  state: FileState,
  repo: string,
  ref: string,
): Promise<void> {
  const current = issuedNow(state);
  const o = await loadDiffAction.dispatch({ path: state.path, repo, ref }).outcome;
  if (current()) {
    settleGitDiff(state, o);
  }
}

/** Start the load a git diff on screen waits on; an inactive one's starts on its activation. */
export function startGitDiffLoad(state: FileState): void {
  const m = state.mode.value;
  if (isShown(state) && m.kind === "diff" && m.diffSource.kind === "git" && m.diffSource.pending) {
    void fetchGitDiffSources(state, state.repo, m.diffSource.ref);
  }
}

/** Paint a git diff's answer. Its working side and a clean buffer are adopted from one verified
 *  read; a dirty buffer is never replaced, so it keeps its own identity. The caller has checked
 *  the answer is still current. */
function settleGitDiff(state: FileState, o: DiffOutcome): void {
  const m = state.mode.value;
  if (o.status === "cancelled" || m.kind !== "diff" || m.diffSource.kind !== "git") {
    return;
  }
  if (o.status === "error") {
    failGitDiff(state, `Failed to load diff: ${o.error.message}`);
    return;
  }
  const result = o.value;
  if (result.kind === "too_large" || result.kind === "binary") {
    if (result.kind === "too_large") {
      state.note = TOO_LARGE_TO_DIFF;
    }
    state.mode.value = { kind: "text", editing: false };
    state.loaded = false;
    if (isShown(state)) {
      restoreUI(state);
      // loadFile announces the view it settles on.
      void loadFile(state, activeLoadController?.signal);
    } else {
      emitBus(BUS_EDITOR_VIEW_CHANGED, { path: state.path });
    }
    return;
  }
  const { oldContent, base, baseLabel, workingLabel, read } = result;
  const firstLoad = !state.loaded;
  if (read !== null) {
    const refusal = verifyRead(read);
    if (refusal !== null) {
      failGitDiff(state, refusal);
      return;
    }
    if (!state.dirty.value) {
      adoptDiskBytes(state, read);
    }
  }
  state.mode.value = {
    kind: "diff",
    diffSource: {
      ...m.diffSource,
      // Both captions are what the load FOUND there, not what was asked for.
      oldLabel: baseLabel,
      newLabel: workingLabel,
      oldText: oldContent,
      newText: read?.content ?? "",
      base,
      pending: false,
    },
  };
  state.loaded = true;
  state.error.value = "";
  if (isShown(state)) {
    repaint(state);
    // activateFile skipped the machine for an unloaded diff, so its first settle starts it.
    if (firstLoad) {
      liveActivate(state.path);
    }
  }
  emitBus(BUS_EDITOR_VIEW_CHANGED, { path: state.path });
}

function failGitDiff(state: FileState, message: string): void {
  state.loaded = true;
  state.error.value = message;
  if (isShown(state)) {
    restoreUI(state);
  }
  emitBus(BUS_EDITOR_VIEW_CHANGED, { path: state.path });
}

/** Whether live refresh may adopt a git diff's answer; anything else leaves the view as it is. */
function liveAdoptable(o: DiffOutcome): boolean {
  if (o.status !== "success") {
    return false;
  }
  const r = o.value;
  return r.kind !== "diff" || r.read === null || verifyRead(r.read) === null;
}

/** The note a diff that fell back to the file view shows in the header. */
export const TOO_LARGE_TO_DIFF = "Too large to diff. Showing the file.";

/** Whether the pane paints from content the state already holds, leaving the
 *  buffer read to serve only the Edit button. */
function paintsWithoutBuffer(state: FileState): boolean {
  const m = state.mode.value;
  return m.kind === "diff" && m.diffSource.kind === "pair";
}

export function activateFile(path: string): void {
  const leaving = fileStates.get(getActiveFilePath());
  saveCurrentState();
  if (leaving !== undefined) {
    invalidate(leaving);
  }
  abortSuggestion();
  activeLoadController?.abort();
  activeLoadController = new AbortController();
  // Created BEFORE `setActiveFilePath`: that write is the signal editor-core's effects re-run on,
  // and a state created afterwards would leave them subscribed to nothing.
  const state = ensureFileState(path);
  setActiveFilePath(path);
  // A <bdi>, so the RTL path box (20-editor.css) keeps the path's own order.
  $.editorFilename.replaceChildren(el("bdi", {}, routeForPath(path).displayPath));
  // The pane is shared by every editor tab, so a file shown for the first time must not inherit
  // the previous file's offset; a known file is restored by line.
  if (state.view === null) {
    paneBody().scrollTo(0, 0);
    $.editorContent.scrollTop = 0;
    $.editorContent.scrollLeft = 0;
  }

  void fetchAgentLines(path);

  const m = state.mode.value;
  if (m.kind === "diff" && m.diffSource.kind === "git" && m.diffSource.pending) {
    restoreUI(state);
    startGitDiffLoad(state);
    // A first load starts the machine when it settles; a file already read follows the disk now.
    if (state.loaded) {
      liveActivate(path);
    }
    return;
  }
  paint(state);
  if (!state.loaded) {
    void loadFile(state, activeLoadController.signal);
    return;
  }
  applyPendingLine(state);
  liveActivate(path);
}

/** Repaint a file onto the shared pane and put the pane back where this file was left. */
function repaint(state: FileState): void {
  captureSelection(state);
  paint(state);
}

function paint(state: FileState): void {
  restoreUI(state);
  if (isShown(state)) {
    restoreEditorView(state);
  }
}

function saveCurrentState(): void {
  const activeFilePath = getActiveFilePath();
  if (activeFilePath === "") {
    return;
  }
  const state = fileStates.get(activeFilePath);
  if (
    state !== undefined &&
    state.loaded &&
    ((state.mode.value.kind === "text" && state.mode.value.editing) ||
      state.mode.value.kind === "conflict")
  ) {
    state.current.value = $.editorContent.value;
    captureSelection(state);
  }
}

/** A failed buffer read, which is not always a failed PANE: where the pane paints itself the
 *  diff stays and `loaded` stays false, which withholds Edit. */
function failBufferLoad(state: FileState, message: string): void {
  if (paintsWithoutBuffer(state)) {
    return;
  }
  state.error.value = message;
  state.loaded = true;
  restoreUI(state);
  emitBus(BUS_EDITOR_VIEW_CHANGED, { path: state.path });
}

const LOAD_FAILED = "Failed to load file";

/** The sentence for a failed read: the server's own when it gave one, else LOAD_FAILED. A
 *  network failure's `error` is the transport's, never a sentence for the reader. */
function failureSentence(r: { status: number; error: string }): string {
  if (r.status === 0 || r.error === "") {
    return LOAD_FAILED;
  }
  return r.error;
}

/** The server's facts from a stat. */
function factsOf(stat: FileStat): FileFacts {
  if (stat.large) {
    return { kind: "large", size: stat.size };
  }
  return {
    kind: "unread",
    binary: stat.binary,
    utf8: stat.utf8,
    readOnly: stat.read_only,
    size: stat.size,
  };
}

/** The mode the rules pick for the facts on hand and what the reader asked for. */
function modeFor(state: FileState, requested: Requested): FileMode {
  const facts = state.facts.value;
  if (facts === null) {
    return state.mode.value;
  }
  const rules = rulesFor(facts, state.path, requested);
  switch (rules.view) {
    case "large":
    case "image":
    case "binary":
    case "markdown":
      return { kind: rules.view };
    case "conflict":
      return { kind: "conflict", conflict: parseConflicts(state.current.value), editing: true };
    case "diff":
    case "undiffable":
      return state.mode.value;
    case "text":
      return { kind: "text", editing: requested === "edit" && rules.edit };
  }
}

/** Read the file for its view. restoreUI has shown the loading surface; the placeholder fills
 *  it when the read is slow. */
async function loadFile(state: FileState, signal?: AbortSignal): Promise<void> {
  let skeleton: SkeletonTimingController | null = null;
  if (!paintsWithoutBuffer(state)) {
    skeleton = skeletonTiming(() => paintPlaceholder($.editorNotice, editorDocSkeleton), {
      ...(signal !== undefined ? { signal } : {}),
    });
  }
  const requested = requestedBy(state);
  const current = issuedNow(state);
  let read: FileRead | null = null;
  // Two passes at most: a file that turned binary between the stat and the read is refused by
  // the read, and a second stat names the view it now gets.
  for (let pass = 0; read === null; pass++) {
    const st = await apiGetTypedOrError(routeForPath(state.path).statURL, decodeFileStat, signal);
    if (!current()) {
      skeleton?.cancel();
      return;
    }
    if (!st.ok || st.data === null) {
      skeleton?.cancel();
      failBufferLoad(state, failureSentence(st));
      return;
    }
    const stat = st.data;
    const fileId = statIdentity(stat);
    if (fileId === null) {
      skeleton?.cancel();
      failBufferLoad(state, INCOMPLETE_READ);
      return;
    }
    const facts = factsOf(stat);
    const view = rulesFor(facts, state.path, requested).view;
    if (view === "large" || view === "image" || view === "binary") {
      skeleton?.cancel();
      adoptUnread(state, view, facts, stat.modified, fileId);
      finishLoad(state);
      return;
    }
    state.facts.value = facts;
    state.notice = { size: stat.size, modified: stat.modified };
    const answer = await readFile(state.path, signal);
    if (!current()) {
      skeleton?.cancel();
      return;
    }
    switch (answer.kind) {
      case "read":
        read = answer.read;
        break;
      case "too_large":
        skeleton?.cancel();
        adoptUnread(state, "large", { kind: "large", size: answer.size }, stat.modified, "");
        finishLoad(state);
        return;
      case "binary":
        if (pass > 0) {
          skeleton?.cancel();
          failBufferLoad(state, answer.error === "" ? LOAD_FAILED : answer.error);
          return;
        }
        break;
      case "failed":
        skeleton?.cancel();
        failBufferLoad(state, failureSentence(answer));
        return;
    }
  }
  skeleton?.cancel();
  const refusal = verifyRead(read);
  if (refusal !== null) {
    failBufferLoad(state, refusal);
    return;
  }
  adoptDiskBytes(state, read);
  if (state.mode.value.kind !== "diff") {
    state.mode.value = modeFor(state, requested);
  }
  finishLoad(state);
}

/** Take a view picked without a read: too large, an image or a binary file. */
function adoptUnread(
  state: FileState,
  view: "large" | "image" | "binary",
  facts: FileFacts,
  modified: string,
  fileId: string,
): void {
  batch(() => {
    state.facts.value = facts;
    state.notice = { size: facts.size, modified };
    commitBaseline(state, { fileId });
    state.error.value = "";
    state.mode.value = { kind: view };
  });
}

function finishLoad(state: FileState): void {
  state.loaded = true;
  if (isShown(state)) {
    repaint(state);
    applyPendingLine(state);
    liveActivate(state.path);
  }
  emitBus(BUS_EDITOR_VIEW_CHANGED, { path: state.path });
}

export const INCOMPLETE_READ = "The file arrived incomplete. Reload the tab to try again.";

/** A read is adopted only under a valid identity, and a UTF-8 one only when its bytes are the
 *  whole file that identity names. Null when it may be adopted, else the sentence to show. A
 *  non-UTF-8 read's bytes cannot be checked (JSON replaced them) and it is never offered for
 *  editing. */
function verifyRead(read: FileRead): string | null {
  if (!isFileId(read.file_id)) {
    return INCOMPLETE_READ;
  }
  if (read.utf8 && !isIdentifiedText(read.content, read.size, read.file_id)) {
    return INCOMPLETE_READ;
  }
  return null;
}

/** Take the bytes on disk as this buffer's clean state in one transition, so no reader sees the
 *  text of one read with the identity of another. The conflict fact comes from the adopted text. */
export function adoptDiskBytes(state: FileState, read: FileRead): void {
  const { text, record } = normalize(read.content);
  batch(() => {
    commitBaseline(state, {
      fileId: read.file_id,
      text,
      eol: { saved: record, tracker: new EolTracker(text, record) },
    });
    state.current.value = text;
    state.facts.value = {
      kind: "small",
      binary: false,
      utf8: read.utf8,
      conflict: parseConflicts(text).hunks.length > 0,
      readOnly: read.read_only,
      size: read.size,
    };
    state.notice = { size: read.size, modified: read.modified };
    state.error.value = "";
  });
  state.rows = null;
  state.runs = null;
}

/** Live refresh's re-read: reclassify a file that crossed the cap or turned binary, otherwise
 *  re-read and adopt at the same line, a view at the bottom staying pinned. */
async function reloadForLive(
  state: FileState,
  stat: FileStat,
  stillCurrent: () => boolean,
): Promise<void> {
  const requested = requestedBy(state);
  const facts = factsOf(stat);
  const view = rulesFor(facts, state.path, requested).view;
  const atBottom =
    isShown(state) && !$.editorViewer.classList.contains("hidden") && viewer().atBottom();
  if (view === "large" || view === "image" || view === "binary") {
    if (stillCurrent()) {
      // statFile answers only a stat carrying the identity its view needs.
      adoptUnread(state, view, facts, stat.modified, statIdentity(stat) ?? "");
      repaintAndAnnounce(state);
    }
    return;
  }
  const m = state.mode.value;
  if (m.kind === "diff" && m.diffSource.kind === "git") {
    const o = await loadDiffAction.dispatch({
      path: state.path,
      repo: state.repo,
      ref: m.diffSource.ref,
    }).outcome;
    if (stillCurrent() && liveAdoptable(o)) {
      settleGitDiff(state, o);
    }
    return;
  }
  const answer = await readFile(state.path);
  if (!stillCurrent()) {
    return;
  }
  if (answer.kind === "too_large") {
    adoptUnread(state, "large", { kind: "large", size: answer.size }, stat.modified, "");
    repaintAndAnnounce(state);
    return;
  }
  if (answer.kind !== "read" || verifyRead(answer.read) !== null) {
    return;
  }
  captureSelection(state);
  adoptDiskBytes(state, answer.read);
  if (m.kind !== "diff") {
    state.mode.value = modeFor(state, requested);
  }
  if (isShown(state)) {
    paint(state);
    if (atBottom) {
      viewer().pinBottom();
    }
  }
  emitBus(BUS_EDITOR_VIEW_CHANGED, { path: state.path });
}

function repaintAndAnnounce(state: FileState): void {
  if (isShown(state)) {
    repaint(state);
  }
  emitBus(BUS_EDITOR_VIEW_CHANGED, { path: state.path });
}

setLiveReload(reloadForLive);

/** The tab's refresh: one immediate live check. */
export function refreshEditorFile(path: string): void {
  if (fileStates.get(path)?.loaded === true) {
    liveActivate(path);
  }
}

/** Tear down one open file's client state. This is the editor tab's `onClose`, and nothing else
 *  calls it; to close a file programmatically, close its tab. Deleting the record retires it, so
 *  every answer still in flight for it is dropped (`owns`). */
export function closeEditorFile(path: string): void {
  const state = fileStates.get(path);
  if (state?.mode.value.kind === "conflict") {
    abortSuggestion(path);
  }
  liveDispose(path);
  dirtyTabUnbinds.get(path)?.();
  dirtyTabUnbinds.delete(path);
  fileStates.delete(path);
  clearAgentLineCache(path);
  clearSuggestionState(path);
  const activeFilePath = getActiveFilePath();
  if (activeFilePath === path) {
    setActiveFilePath("");
  }
}
