// Find in the open file over the buffer (`FileState.current`), never the disk: an unsaved edit is
// what the reader sees. Source (the read view, the textarea, a conflict) is searched as a string,
// each hit placed by editor-scroll.ts; a diff pane, rendered markdown and the conflict overlay are
// searched as DOM, and both engines' hits share one cursor. An image declines the chord, so
// native find opens there. A buffer not yet read is no answer (blank counter, re-run when it
// lands); one that could not be read answers "File not read".

import { el } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { ICON_CHEVRON_DOWN, ICON_CHEVRON_UP } from "./icons.js";
import { fileStates, getActiveFilePath } from "./editor-types.js";
import type { FileState } from "./editor-types.js";
import { clearEditorMark, revealBufferHit } from "./editor-scroll.js";
import { parseFindQuery } from "./viewer-query.js";
import { FindEngine } from "./find-engine.js";
import { createSearchShell, searchIconButton } from "./search-shell.js";
import type { SearchShell } from "./search-shell.js";
import { BUS_EDITOR_VIEW_CHANGED, BUS_TAB_CHANGED, onBus } from "./bus.js";
import { occurrences, prepare } from "./textsearch/scan.js";
import { classify, cursorCount, emptyNote } from "./textsearch/copy.js";
import type { Nouns } from "./textsearch/copy.js";

/** One match: the 1-based line it sits on, and its offset within the buffer,
 *  which is what places the mark and the selection. */
interface BufferMatch {
  readonly line: number;
  readonly offset: number;
}

/**
 * Every occurrence of `needle` in `text`, with the 1-based line each sits on.
 * The scan is textsearch/scan.ts's, so occurrences do not overlap (`aa` in `aaa`
 * is one hit, as on every other surface) and offsets index the ORIGINAL text.
 * Lines are counted with a running cursor rather than by splitting the buffer,
 * which would allocate a large file a second time for the same answer.
 */
export function findInBuffer(text: string, needle: string, caseSensitive: boolean): BufferMatch[] {
  const out: BufferMatch[] = [];
  let line = 1;
  let scanned = 0;
  for (const offset of occurrences(text, prepare(needle, caseSensitive))) {
    for (let i = scanned; i < offset; i++) {
      if (text[i] === "\n") {
        line++;
      }
    }
    scanned = offset;
    out.push({ line, offset });
  }
  return out;
}

const NOUNS: Nouns = {
  match: { one: "match", many: "matches" },
  scanned: { one: "file", many: "files" },
};

let barEl: HTMLElement | null = null;
let shell: SearchShell | null = null;
let countEl: HTMLElement | null = null;
/** What the last run produced, and where the cursor sits in its list. */
let result: FindResult | null = null;
let current = -1;
/** The query `result` was produced for, so a step can tell "move the cursor"
 *  from "the box says something else now". */
let searched = "";
let unsubTab: (() => void) | null = null;
let unsubView: (() => void) | null = null;

/** What one run produced. The engines hold their own marks, so a result owns
 *  what a later run or the close has to clear. */
type FindResult =
  /** The diff pane or rendered markdown: the walker's marks are the list. Null
   *  when the surface was not on screen to walk. */
  | { readonly engine: "dom"; readonly find: FindEngine | null }
  /** Source. The list is the conflict overlay's marks, when there is one, and
   *  then the buffer's hits; `length` is the query's, so a hit spans
   *  `[offset, offset + length)` in `text`. */
  | {
      readonly engine: "buffer";
      readonly text: string;
      readonly length: number;
      readonly hits: readonly BufferMatch[];
      readonly overlay: FindEngine | null;
    }
  /** No answer yet, or none possible. */
  | { readonly engine: "none"; readonly buffer: "not-loaded" | "unreadable" }
  /** The query breaks the one grammar every engine shares. */
  | { readonly engine: "invalid"; readonly message: string };

/** How many matches the cursor can step through. */
function total(): number {
  switch (result?.engine) {
    case "dom":
      return result.find?.total ?? 0;
    case "buffer":
      return (result.overlay?.total ?? 0) + result.hits.length;
    default:
      return 0;
  }
}

/** Drop the previous run's marks. Called at the top of every run and on every
 *  close, because a `<mark>` left in a diff pane is welded there for the rest of
 *  the session — the same rule find-in-chat.ts records for the transcript. */
function clearMarks(): void {
  switch (result?.engine) {
    case "dom":
      result.find?.clear();
      break;
    case "buffer":
      result.overlay?.clear();
      break;
    default:
      break;
  }
  clearEditorMark();
}

/** Which engine the active editor surface needs, or null when it has no text to
 *  search at all. `buffer` is the source pre or textarea (a conflict included): a
 *  match is a position in `FileState.current` that editor-scroll.ts places. `dom`
 *  is the diff pane or rendered markdown: a match is a mark the shared walker
 *  made, with no line geometry, because none holds over reflowed prose or a
 *  two-pane diff. `null` is an image; the chord falls through to native find.
 *  Reads only the active-path and mode SIGNALS, never the buffer, so an effect
 *  over it re-runs on a file or mode switch and not on every keystroke. */
type SurfaceEngine = "buffer" | "dom";

function surfaceEngine(): SurfaceEngine | null {
  const path = getActiveFilePath();
  if (path === "") {
    return null;
  }
  const state = fileStates.get(path);
  if (state === undefined) {
    return null;
  }
  const m = state.mode.value;
  switch (m.kind) {
    case "image":
    case "binary":
    case "large":
      return null;
    case "diff":
      // A git diff with no text base shows a notice, not a pane.
      return m.diffSource.kind === "git" && m.diffSource.base !== "text" ? null : "dom";
    case "markdown":
      return "dom";
    case "text":
    case "conflict":
      return "buffer";
  }
}

/** The rendered root a `dom` search walks, or null when it is not on screen yet.
 *
 *  Selected by which surface is unhidden rather than by re-deriving the mode:
 *  `editor-modes.ts` owns that toggle, and asking the DOM which one it revealed
 *  cannot disagree with it. */
function domRoot(): HTMLElement | null {
  for (const id of ["editor-diff-pane", "editor-markdown"]) {
    const host = document.getElementById(id);
    if (host !== null && !host.classList.contains("hidden")) {
      return host;
    }
  }
  return null;
}

/** The conflict overlay when it is showing, by the same rule as `domRoot`. */
function conflictOverlay(): HTMLElement | null {
  const host = $.editorConflictOverlay;
  return host.classList.contains("hidden") ? null : host;
}

/** Whether the toolbar's search button has a destination on this editor tab.
 *
 *  Deliberately does NOT wait for the file to load, which is the one difference
 *  from the two searchable-content readers below. Gating it on `loaded` too would
 *  collapse and re-expand the toolbar for the duration of every file read — a bar
 *  that flinches each time you open a file, to answer a question nobody asked. The
 *  mode already says whether the surface has text, and that is decided when the
 *  file is opened. */
export function editorFindAvailable(): boolean {
  return surfaceEngine() !== null;
}

/** What a `buffer` search has to work with. Three answers, because two of them
 *  are not "no matches": a read that failed leaves `FileState.error` set with
 *  the pane showing that sentence instead of any text, and a buffer that has
 *  not arrived has nothing to answer about yet. */
type Buffer =
  | { readonly kind: "buffer"; readonly text: string }
  | { readonly kind: "not-loaded" }
  | { readonly kind: "unreadable" };

function searchableBuffer(state: FileState): Buffer {
  if (state.error.value !== "") {
    return { kind: "unreadable" };
  }
  if (!state.loaded) {
    return { kind: "not-loaded" };
  }
  return { kind: "buffer", text: state.current.value };
}

function isOpen(): boolean {
  return barEl !== null && !barEl.classList.contains("hidden");
}

/** The counter's text: blank for no query or no buffer yet, the classifier's
 *  sentence for an empty answer, the cursor otherwise. */
function counterText(query: string): string {
  if (query === "" || result === null) {
    return "";
  }
  if (result.engine === "none" && result.buffer === "not-loaded") {
    return "";
  }
  if (result.engine === "invalid") {
    return result.message;
  }
  const n = total();
  if (n > 0) {
    return cursorCount(current + 1, n);
  }
  const unreadable = result.engine === "none" && result.buffer === "unreadable";
  return emptyNote(classify({ unreadable, matched: 0, shown: 0, truncated: false }), NOUNS);
}

function updateCounter(query: string): void {
  if (countEl === null) {
    return;
  }
  const text = counterText(query);
  countEl.textContent = text;
  barEl?.classList.toggle("editor-find-no-results", text !== "" && total() === 0);
}

/** Take the cursor to the current match: point the engine that holds it at the
 *  same index, then bring it on screen. Over source a hit is a LINE plus an
 *  offset, so editor-scroll.ts scrolls the line, flashes it and marks the span,
 *  and in edit mode the textarea's selection is set as well so leaving the box
 *  lands the caret on the match. Over a diff pane, rendered prose or the
 *  conflict overlay a hit is a NODE, so the browser scrolls the `<mark>` itself.
 *  Synchronous: a held Enter must settle on the hit it stopped at, and the
 *  surfaces are laid out by the time a bar is open over them. */
function reveal(): void {
  if (result === null || current < 0) {
    return;
  }
  switch (result.engine) {
    case "dom":
      result.find?.setCurrent(current);
      scrollMarkIntoView(result.find?.currentMark() ?? null);
      return;
    case "buffer":
      revealBufferCursor(result);
      return;
    case "none":
    case "invalid":
      return;
  }
}

function revealBufferCursor(found: Extract<FindResult, { engine: "buffer" }>): void {
  const overlayTotal = found.overlay?.total ?? 0;
  if (found.overlay !== null && current < overlayTotal) {
    clearEditorMark();
    found.overlay.setCurrent(current);
    scrollMarkIntoView(found.overlay.currentMark());
    return;
  }
  found.overlay?.clearCurrent();
  const hit = found.hits[current - overlayTotal];
  if (hit === undefined) {
    return;
  }
  revealBufferHit(found.text, hit.offset, found.length, hit.line);
}

function scrollMarkIntoView(mark: HTMLElement | null): void {
  // The method is optional on the platform, so the call is guarded rather than
  // assumed — the same shape find-in-chat.ts uses.
  const scrollFn = (mark as { scrollIntoView?: (o?: ScrollIntoViewOptions) => void } | null)
    ?.scrollIntoView;
  if (typeof scrollFn === "function" && mark !== null) {
    scrollFn.call(mark, { block: "center", inline: "nearest" });
  }
}

function step(dir: 1 | -1): void {
  if (shell === null) {
    return;
  }
  // A fast type-then-Enter must SEARCH first and land on the first match, which
  // is what native find does; only a step against the query already searched
  // moves the cursor. find-in-chat's step carries the same check for the same
  // reason, and without it Enter after typing is a no-op.
  if (searched !== shell.value) {
    shell.run();
    return;
  }
  const n = total();
  if (n === 0) {
    return;
  }
  current = (current + dir + n) % n;
  updateCounter(shell.value);
  reveal();
}

function runQuery(query: string, caseSensitive: boolean): FindResult {
  clearMarks();
  const parsed = parseFindQuery(query);
  if (parsed.kind === "invalid") {
    return { engine: "invalid", message: parsed.message };
  }
  if (surfaceEngine() === "dom") {
    const root = domRoot();
    const find = root === null ? null : new FindEngine(root);
    find?.search(query, caseSensitive);
    return { engine: "dom", find };
  }
  const state = fileStates.get(getActiveFilePath());
  const buffer: Buffer = state === undefined ? { kind: "not-loaded" } : searchableBuffer(state);
  if (buffer.kind !== "buffer") {
    return { engine: "none", buffer: buffer.kind };
  }
  const overlayRoot = conflictOverlay();
  const overlay = overlayRoot === null ? null : new FindEngine(overlayRoot);
  overlay?.search(query, caseSensitive);
  return {
    engine: "buffer",
    text: buffer.text,
    length: query.length,
    hits: findInBuffer(buffer.text, query, caseSensitive),
    overlay,
  };
}

function ensureBuilt(): void {
  if (barEl !== null) {
    return;
  }
  // A DIV, and `search-status` beside its own class, for find-in-chat.ts's reason:
  // the counter is a LINE under the controls (24-find.css `.search-status`), which
  // collapses to zero height while empty, so it needs a block box of its own.
  const count = el("div", {
    id: "editor-find-count",
    className: "editor-find-count search-status",
    role: "status",
    "aria-live": "polite",
    "aria-atomic": "true",
  });
  countEl = count;

  const prevBtn = searchIconButton(
    "editor-find-btn",
    "Previous match",
    "Previous (Shift+Enter)",
    ICON_CHEVRON_UP,
    () => {
      step(-1);
    },
  );
  const nextBtn = searchIconButton(
    "editor-find-btn",
    "Next match",
    "Next (Enter)",
    ICON_CHEVRON_DOWN,
    () => {
      step(1);
    },
  );

  const built = createSearchShell<FindResult>({
    id: "editor-find",
    regionClass: "editor-find hidden",
    inputClass: "editor-find-input",
    buttonClass: "editor-find-btn",
    caseClass: "editor-find-case",
    label: "Find in file",
    placeholder: "Find in file\u2026",
    inputTitle: "Find in this file. Press Ctrl+F again to use the browser's find.",
    matchCase: true,
    closeButton: true,
    compose: ({ input, caseButton, closeButton }) => [
      el(
        "div",
        { className: "editor-find-row" },
        input,
        el("div", { className: "editor-find-nav" }, caseButton, prevBtn, nextBtn, closeButton),
      ),
      count,
    ],
    // SYNCHRONOUS: there is no network here, so the count paints in the same tick
    // as the keystroke that asked for it rather than a microtask later.
    query: (query, qctx) => runQuery(query, qctx.caseSensitive),
    render: (found, query) => {
      searched = query;
      result = found;
      current = total() > 0 ? 0 : -1;
      updateCounter(query);
      reveal();
    },
    onDismiss: () => {
      closeEditorFind();
    },
    onSubmit: (shift) => {
      step(shift ? -1 : 1);
    },
  });
  shell = built;

  // The bar sits between the conflict overlay and the scroller, in normal flow:
  // `.editor-body` is the scroller, so a docked bar shrinks it instead of
  // covering the first lines of the file. That is the same reasoning 19-files.css
  // records for the file browser's in-flow bar.
  const overlay = $.editorConflictOverlay;
  overlay.parentElement?.insertBefore(built.region, overlay.nextSibling);
  barEl = built.region;

  // A tab switch closes the bar and FORGETS the query, the same rule
  // find-in-chat.ts records. It matters more here than there: `openEditorFind`
  // runs on open and the buffer under the box is a different FILE, so a retained
  // query searched the next file for a string typed against the previous one and
  // reported a count for it.
  unsubTab?.();
  unsubTab = onBus(BUS_TAB_CHANGED, () => {
    closeEditorFind();
    if (shell !== null) {
      shell.input.value = "";
    }
  });
  unsubView?.();
  unsubView = onBus(BUS_EDITOR_VIEW_CHANGED, ({ path }) => {
    if (!isOpen() || path !== getActiveFilePath()) {
      return;
    }
    if (surfaceEngine() === null) {
      closeEditorFind();
    } else {
      shell?.run();
    }
  });
}

/** Open (or refocus) the in-file find. No-op when the active surface has no text
 *  at all (an image), so the caller's `preventDefault` can be conditioned on it
 *  and native find stays reachable where this one declines. Gated on the SURFACE,
 *  not the bytes: a `dom` search reads what is rendered, so it has nothing to wait
 *  for, and a `buffer` search over an unloaded file opens with a blank counter and
 *  answers when the bytes land, where refusing left the chord doing nothing. */
export function openEditorFind(): boolean {
  if (surfaceEngine() === null) {
    return false;
  }
  ensureBuilt();
  if (barEl === null || shell === null) {
    return false;
  }
  barEl.classList.remove("hidden");
  shell.focus();
  shell.run();
  return true;
}

export function closeEditorFind(): void {
  if (!isOpen() || barEl === null) {
    return;
  }
  shell?.cancel();
  barEl.classList.add("hidden");
  clearMarks();
  result = null;
  current = -1;
  searched = "";
  updateCounter("");
}

export function toggleEditorFind(): void {
  if (isOpen()) {
    closeEditorFind();
    return;
  }
  openEditorFind();
}

/** Ctrl-F / Cmd-F over an editor tab.
 *
 *  Carries the same second-press escape hatch every destination owns: a repeat
 *  press while our field has focus falls through with no preventDefault. And it
 *  only claims the key when there IS text to search — over an image the
 *  browser's find is the better tool and gets the chord. */
export function handleEditorFindHotkey(e: KeyboardEvent): boolean {
  if (e.key.toLowerCase() !== "f" || !(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) {
    return false;
  }
  if (isOpen() && shell !== null && document.activeElement === shell.input) {
    return true;
  }
  if (surfaceEngine() === null) {
    return false;
  }
  e.preventDefault();
  openEditorFind();
  return true;
}

/** @internal Test seam. */
// deadset:ignore DS1004 -- test seam: observes whether the editor find bar is open
export function _isEditorFindOpen(): boolean {
  return isOpen();
}
