// ---------------------------------------------------------------------------
// Editor types: shared types, state container, pure predicates, and diff
// source factories. Extracted to break circular imports between editor-core,
// editor-ui, and editor-openers.
// ---------------------------------------------------------------------------

import { signal, computed, type Signal, type ReadonlySignal } from "@cplieger/reactive";
import { lineDiff, type DiffLine } from "./diff.js";
import { relToWorkspace } from "./workspace.js";
import type { ConflictFile } from "./conflict.js";
import type { DiffBase, FileFacts } from "./viewer-rules.js";
import type { EolRecord, EolTracker } from "./viewer-eol.js";
import type { WholeRows } from "./viewer-rows.js";
import type { HighlightRuns } from "./highlight.js";
import type { FileRefusal } from "./wire/types.gen.js";

const DIFF_LABEL_WORKING_TREE = "working tree";

// The editor ADDRESSES a file absolutely, the namespace /api/file* serves and the form the file
// browser hands over; it DISPLAYS the file relative to the workspace.
export function routeForPath(path: string): {
  readURL: string;
  statURL: string;
  writeURL: string;
  displayPath: string;
} {
  const q = `?path=${encodeURIComponent(path)}`;
  return {
    readURL: `/api/file${q}`,
    statURL: `/api/file/stat${q}`,
    writeURL: `/api/file${q}`,
    displayPath: relToWorkspace(path),
  };
}

// --- Types ---

interface DiffLabels {
  oldLabel: string;
  newLabel: string;
}

/** The buffer against the saved text, both read off the record, so a save settling under the
 *  view moves its old side. */
type BufferDiff = DiffLabels & { kind: "buffer" };

/** A pair the opener supplied, such as a tool card's before and after. */
type PairDiff = DiffLabels & { kind: "pair"; oldText: string; newText: string };

/** `ref` is what a refetch asks git for; `oldLabel` and `base` are what the load found there.
 *  `pending` until a load settles it: the activation that finds it pending starts the load. */
type GitDiff = DiffLabels & {
  kind: "git";
  oldText: string;
  newText: string;
  ref: string;
  base: DiffBase;
  pending: boolean;
};

type DiffSource = BufferDiff | PairDiff | GitDiff;

/** Factory: a git diff (ref vs working tree) its load has yet to fill. */
export function gitDiffSource(ref: string): GitDiff {
  return {
    kind: "git",
    oldText: "",
    newText: "",
    oldLabel: ref,
    newLabel: DIFF_LABEL_WORKING_TREE,
    ref,
    base: "text",
    pending: true,
  };
}

/** Factory: the unsaved changes (saved vs unsaved). */
export function bufferDiffSource(): BufferDiff {
  return { kind: "buffer", oldLabel: "saved", newLabel: "unsaved" };
}

/** The two texts `src` compares for `state`. */
export function diffTexts(
  state: Pick<FileState, "original" | "current">,
  src: DiffSource,
): { readonly oldText: string; readonly newText: string } {
  return src.kind === "buffer"
    ? { oldText: state.original.value, newText: state.current.value }
    : src;
}

/** The view a file is in. A union so `restoreUI`'s exhaustive switch names every site a new
 *  view has to reach. */
export type FileMode =
  /** Source: the windowed renderer to read, the textarea when editing. */
  | { kind: "text"; editing: boolean }
  /** Rendered markdown, the read state of a small `.md`. */
  | { kind: "markdown" }
  | { kind: "diff"; diffSource: DiffSource }
  | { kind: "conflict"; conflict: ConflictFile; editing: true }
  | { kind: "image" }
  /** A notice, the file's size and modified time, and Download. */
  | { kind: "binary" }
  /** Over the viewer's cap: one sentence and Download, nothing else. */
  | { kind: "large" };

/** Where the reader left one file on the SHARED pane. Every editor tab paints into
 *  the same elements, so a position held by the DOM is the last file's, never
 *  this one's. */
export interface EditorView {
  /** The logical line at the top, so read and edit state and a reload all land on one line. */
  topLine: number;
  /** How many cut rows into `topLine` the read state's top row sits. */
  topRowInLine: number;
  left: number;
  /** The textarea pans a long line inside itself. */
  areaLeft: number;
  selection: { start: number; end: number; direction: "forward" | "backward" | "none" } | null;
  /** Keyed by the comparison it was taken in, so a different diff opens at its top. */
  diff: { key: string; top: number; left: number } | null;
}

/** The edit buffer's line endings: the record the file was read (or last saved) with, and the
 *  tracker shadowing the textarea since. */
interface FileEol {
  readonly saved: EolRecord;
  readonly tracker: EolTracker;
}

/** A save's outcome the reader has to see, kept on the record whichever file is on screen until a
 *  later save or a discard settles it. */
type SaveAlert =
  /** The disk text is now `original` and the view diffs the buffer against it. */
  | { readonly kind: "stale"; readonly sentence: string }
  /** Save stays disabled until the reader picks a way on. */
  | { readonly kind: "refused"; readonly reason: FileRefusal | "uncertain" }
  | { readonly kind: "failed"; readonly sentence: string };

/** The live-refresh machine's state for the file; viewer-live is its one writer. */
export type LiveStatus = "watching" | "changed" | "live" | "paused" | "gone";

/** The facts a binary or large view shows. */
interface FileNotice {
  readonly size: number;
  readonly modified: string;
}

export interface FileState {
  path: string;
  /** Null until the file has been shown once; a first open lands at the top. */
  view: EditorView | null;
  /** The saved text, LF-normalized. Reactive so `dirty` can derive from it. */
  original: Signal<string>;
  /** The edit buffer, LF-normalized. */
  current: Signal<string>;
  loaded: boolean;
  /** The identity the buffer or the view was read under; "" before the first read. */
  fileId: string;
  /** Advanced by every `commitBaseline`; see `baselineHeld`. */
  baseline: number;
  /** The `#L<n>` the file was opened at, until `applyPendingLine` takes it. */
  pendingLine: number | null;
  /** The server's facts; null only before the first stat answers. */
  facts: Signal<FileFacts | null>;
  notice: FileNotice | null;
  /** A header note the view adds, such as a diff that fell back to the file. */
  note: string;
  eol: FileEol | null;
  /** The read state's rows over `current`, rebuilt when it changes. */
  rows: WholeRows | null;
  /** Highlight runs for `rows`, computed after the first plain paint. */
  runs: HighlightRuns | null;
  /** Load/save failure for this file, "" when there is none. */
  error: Signal<string>;
  alert: Signal<SaveAlert | null>;
  live: LiveStatus;
  /** Bumped by every transition that leaves the view a read was issued for; see `issuedNow`. */
  gen: number;
  mode: Signal<FileMode>;
  /** True when `current` differs from `original`. */
  dirty: ReadonlySignal<boolean>;
  suggestions: Map<number, HunkSuggestion>;
  returnToGitDiff: { ref: string; repo: string } | null;
  /** Repo identifier for git-diff sources (empty string = default). */
  repo: string;
  /** Line diff of the current diff source (`diffTexts`), derived from `mode`. */
  cachedDiff: ReadonlySignal<DiffLine[]>;
}

/** The line endings `text` serializes with: the exact saved record for the saved text, whatever
 *  the tracker guessed on the way back to it, else the tracker's. */
export function recordFor(state: FileState, text: string): EolRecord | null {
  const eol = state.eol;
  if (eol === null) {
    return null;
  }
  return text === state.original.value ? eol.saved : eol.tracker.snapshot();
}

/** Drop every read in flight for the file: a pause, leaving its tab, a view change or an edit
 *  beginning. Closing the tab retires the record instead (`owns`). */
export function invalidate(state: FileState): void {
  state.gen++;
}

/** Whether `state` is still the record its path names. Closing a tab retires its record and a
 *  reopen makes a new one, so a path never proves that a delayed answer belongs to the record. */
function owns(state: FileState): boolean {
  return fileStates.get(state.path) === state;
}

/** Whether `state` is the record on the pane. */
export function isShown(state: FileState): boolean {
  return getActiveFilePath() === state.path && owns(state);
}

/** For a read issued now: whether its answer may still be adopted when it lands. */
export function issuedNow(state: FileState): () => boolean {
  const gen = state.gen;
  return () => state.gen === gen && owns(state);
}

/** A baseline write: the identity, and for a buffer the saved text with its line endings. */
type BaselineWrite =
  | { readonly fileId: string }
  | { readonly fileId: string; readonly text: string; readonly eol: FileEol };

/** The one writer of the disk baseline (`fileId`, `original`, `eol`): a read's adoption or a
 *  save's answer. */
export function commitBaseline(state: FileState, write: BaselineWrite): void {
  state.baseline++;
  state.fileId = write.fileId;
  if ("text" in write) {
    state.original.value = write.text;
    state.eol = write.eol;
  }
}

/** For a save dispatched now: whether its answer may still write the baseline. A read adopted
 *  meanwhile knows a newer disk than the save did. */
export function baselineHeld(state: FileState): () => boolean {
  const at = state.baseline;
  return () => state.baseline === at && owns(state);
}

/** A view the reader or a save outcome moves the file to; an adoption assigns `mode` itself. */
export function enterView(state: FileState, mode: FileMode): void {
  invalidate(state);
  state.mode.value = mode;
}

let painter: (state: FileState) => void = () => undefined;

/** Installed by editor-modes, whose `restoreUI` is the pane's one painter, for the modules it
 *  imports. */
export function setPainter(fn: (state: FileState) => void): void {
  painter = fn;
}

/** Paint `state` onto the pane, when it is the record on screen. */
export function repaint(state: FileState): void {
  painter(state);
}

interface HunkSuggestion {
  loading: boolean;
  preview: string | null;
  error: string;
}

// --- EditorState class: encapsulates shared mutable state ---

class EditorState {
  readonly files = new Map<string, FileState>();
  private activePath = signal<string>("");

  getActivePath(): string {
    return this.activePath.value;
  }
  setActivePath(path: string): void {
    this.activePath.value = path;
  }

  /** Reactive: whether the active file has unsaved changes, re-tracked on a tab switch. */
  isActiveDirty(): boolean {
    const s = this.files.get(this.activePath.value);
    return s ? s.dirty.value : false;
  }

  freshState(path: string): FileState {
    const mode = signal<FileMode>({ kind: "text", editing: false });
    const current = signal("");
    const original = signal("");
    const dirty = computed<boolean>(() => current.value !== original.value);
    const cachedDiff = computed<DiffLine[]>(() => {
      const m = mode.value;
      if (m.kind !== "diff") {
        return [];
      }
      const { oldText, newText } = diffTexts({ original, current }, m.diffSource);
      return lineDiff(oldText, newText);
    });
    return {
      path,
      view: null,
      original,
      current,
      loaded: false,
      fileId: "",
      baseline: 0,
      pendingLine: null,
      facts: signal<FileFacts | null>(null),
      notice: null,
      note: "",
      eol: null,
      rows: null,
      runs: null,
      error: signal(""),
      alert: signal<SaveAlert | null>(null),
      live: "watching",
      gen: 0,
      mode,
      dirty,
      suggestions: new Map(),
      returnToGitDiff: null,
      repo: "",
      cachedDiff,
    };
  }

  getCachedDiff(state: FileState): DiffLine[] {
    return state.cachedDiff.value;
  }
}

const editorState = new EditorState();

/** Reactive flag: true when the active editor file has unsaved changes. */
export const activeDirty = computed<boolean>(() => editorState.isActiveDirty());

export const fileStates = editorState.files;
export function setActiveFilePath(path: string): void {
  editorState.setActivePath(path);
}
export function getCachedDiff(state: FileState): DiffLine[] {
  return editorState.getCachedDiff(state);
}
export function freshState(path: string): FileState {
  return editorState.freshState(path);
}
export function getActiveFilePath(): string {
  return editorState.getActivePath();
}
