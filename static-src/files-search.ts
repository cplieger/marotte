// Find in files, server-side by necessity: the client holds one listing and the server owns confinement.

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { rovingFocus, type RovingFocusController } from "@cplieger/ui-primitives/roving-focus";
import { $, byId } from "./dom.js";
import { apiGetTypedOrError } from "./api-client.js";
import { openAtLine, openFileOrPage } from "./navigate.js";
import { reconcile } from "./reconcile.js";
import { paintIfChanged, wireSignature } from "./paint-sig.js";
import { fileIcon, ICON_EYE_UI, ICON_FILE_TEXT_UI } from "./icons.js";
import { iconEl } from "./icon-el.js";
import {
  caseParam,
  createSearchShell,
  latchedIconButton,
  searchField,
  wireSearchKeys,
} from "./search-shell.js";
import type { SearchShell } from "./search-shell.js";
import { FB_ROOT } from "./files-shared.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { getActiveTabId, getActiveTabKind } from "./tabs.js";
import { classify, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns } from "./textsearch/copy.js";
import type { FileMatch, FileSearchResult, MatchRange } from "./wire/types.gen.js";
import { decodeFileSearchResult } from "./wire/decoders.gen.js";

type SearchMode = "names" | "contents";

const NOUNS: Record<SearchMode, Nouns> = {
  names: {
    match: { one: "entry", many: "entries" },
    scanned: { one: "entry", many: "entries" },
  },
  contents: {
    match: { one: "match", many: "matches" },
    scanned: { one: "file", many: "files" },
  },
};

const FIELD_COPY: Record<SearchMode, string> = {
  names: "Find files by name\u2026",
  contents: "Find text in files\u2026",
};

const NAMES_HINT = "Type a name, or a pattern like *.css";
const INVALID_NOTE = "Incomplete or invalid pattern";
const IGNORED_ROOT_NOTE = "This folder is ignored. Turn on Include ignored files to search it.";

/** A 400 is the pattern's fault, which the reader fixes by typing on, so it is told apart from a
 *  search that could not run. */
type SearchAnswer =
  | { readonly kind: "result"; readonly result: FileSearchResult }
  | { readonly kind: "invalid" }
  | { readonly kind: "failed" };

/** The filter grammar, stated where the user meets it; `internal/filebrowse/searchglob.go` is the authority. */
const FILES_HINT =
  "Comma-separated. A bare name matches that file or folder at any depth, and .md matches that extension. " +
  "* and ? stay within a folder, ** crosses folders, ! excludes.";

interface FilesSearchCtx {
  /** The folder the browser is showing, which is the search ROOT. */
  getSearchPath: () => string;
  /** Bring the browser into view; injected (importing files.ts would be a cycle). Must show, never toggle. */
  activateBrowser: () => void;
  /** Navigate to a name hit's folder and close the search; injected (files imports files-search). */
  openFolder: (path: string) => void;
}

let ctx: FilesSearchCtx | null = null;
let barEl: HTMLElement | null = null;
let resultsEl: HTMLElement | null = null;
let filesEl: HTMLInputElement | null = null;
let contentsBtn: HTMLButtonElement | null = null;
let ignoredBtn: HTMLButtonElement | null = null;
let rowNav: RovingFocusController | null = null;
/** Its own abort signal: sharing the browser's would cancel searches and directory loads against each other. */
let shell: SearchShell | null = null;
let mode: SearchMode = "names";
let includeIgnored = false;
let lastMatches: FileMatch[] = [];
/** Unsubscribe for the tab teardown, so a rebuilt module does not stack subscribers. */
let unsubTab: (() => void) | null = null;
/** The files tab the open bar belongs to, "" when closed or opened on an empty strip (the teardown never fires for ""). */
let searchOwnerID = "";

/** Every optional parameter is sent only when set: the server reads absence as the default. */
export function searchURL(
  path: string,
  query: string,
  opts: { caseSensitive?: boolean; mode?: SearchMode; files?: string; ignored?: boolean } = {},
): string {
  const q = new URLSearchParams({ path, q: query });
  if (opts.mode === "contents") {
    q.set("mode", "contents");
  }
  const flag = caseParam(opts.caseSensitive === true);
  if (flag !== "") {
    q.set("case", flag);
  }
  if ((opts.files ?? "") !== "") {
    q.set("files", opts.files ?? "");
  }
  if (opts.ignored === true) {
    q.set("ignored", "1");
  }
  return `/api/files/search?${q.toString()}`;
}

/** Relative to the folder searched when under it, else absolute (a root search spans mounts). */
export function hitLabel(searchPath: string, abs: string): string {
  // Both the hit path and the searched folder are container-absolute (`FileMatch.Path`), so the prefix is the path itself.
  if (searchPath === "" || searchPath === FB_ROOT) {
    return abs;
  }
  const root = `${searchPath.replace(/\/+$/, "")}/`;
  return abs.startsWith(root) ? abs.slice(root.length) : abs;
}

/** Reconcile key: path and line through keyenc, since a colon is a legal filename character. */
export function hitKey(m: FileMatch): string {
  return join("hit", m.path, String(m.line));
}

/** A range out of order or past the text is dropped rather than trusted. */
function markedText(text: string, ranges: readonly MatchRange[]): Node[] {
  const out: Node[] = [];
  let at = 0;
  for (const r of ranges) {
    if (r.start < at || r.end <= r.start || r.end > text.length) {
      continue;
    }
    if (r.start > at) {
      out.push(document.createTextNode(text.slice(at, r.start)));
    }
    out.push(el("mark", { className: "fb-search-mark" }, text.slice(r.start, r.end)));
    at = r.end;
  }
  if (at < text.length) {
    out.push(document.createTextNode(text.slice(at)));
  }
  return out;
}

function splitPath(abs: string): { parent: string; base: string } {
  const cut = abs.lastIndexOf("/");
  return { parent: abs.slice(0, cut), base: abs.slice(cut + 1) };
}

/** The query as the server reads it: a contents needle is literal, spaces included, while names-mode spaces only
 *  separate terms. */
function wireQuery(query: string): string {
  return mode === "contents" ? query : query.trim();
}

function isIdle(query: string): boolean {
  if (wireQuery(query) !== "") {
    return false;
  }
  return mode === "contents" || (filesEl?.value.trim() ?? "") === "";
}

function applyMode(next: SearchMode): void {
  mode = next;
  contentsBtn?.setAttribute("aria-pressed", next === "contents" ? "true" : "false");
  if (shell !== null) {
    shell.input.placeholder = FIELD_COPY[next];
    shell.input.setAttribute("aria-label", FIELD_COPY[next]);
  }
}

function idleNote(): string {
  return mode === "names" ? NAMES_HINT : "";
}

function ensureBuilt(): void {
  if (barEl !== null) {
    return;
  }
  contentsBtn = latchedIconButton(
    "fb-search-btn fb-search-toggle",
    "Search file contents",
    "Search inside files",
    ICON_FILE_TEXT_UI,
    (on) => {
      applyMode(on ? "contents" : "names");
      // Forced, as `Aa` is: the query text did not change.
      shell?.run();
    },
  );
  ignoredBtn = latchedIconButton(
    "fb-search-btn fb-search-toggle",
    "Include ignored files",
    "Include files .gitignore excludes, and node_modules",
    ICON_EYE_UI,
    (on) => {
      includeIgnored = on;
      shell?.run();
    },
  );
  filesEl = searchField({
    id: "fb-search-files",
    className: "fb-search-field fb-search-files",
    label: "Files to include or exclude",
    placeholder: "Files: *.css, src/**, !node_modules",
    title: FILES_HINT,
  });

  const built = createSearchShell<SearchAnswer>({
    id: "fb-search",
    regionClass: "fb-search hidden",
    inputClass: "fb-search-field",
    buttonClass: "fb-search-btn",
    caseClass: "fb-search-case",
    noteClass: "fb-search-note",
    label: "Find in files",
    placeholder: FIELD_COPY.names,
    inputTitle: "Find in files. Press Ctrl+F again to use the browser's find.",
    matchCase: true,
    note: true,
    closeButton: true,
    compose: ({ input, caseButton, closeButton, note }) => [
      el("div", { className: "fb-search-row" }, input, caseButton, contentsBtn, closeButton),
      el("div", { className: "fb-search-row" }, filesEl, ignoredBtn),
      note,
    ],
    query: async (query, qctx) => {
      // An empty root means no browser is bound.
      if (isIdle(query) || ctx === null || ctx.getSearchPath() === "") {
        return null;
      }
      const res = await apiGetTypedOrError(
        searchURL(ctx.getSearchPath(), wireQuery(query), {
          caseSensitive: qctx.caseSensitive,
          mode,
          files: filesEl?.value.trim() ?? "",
          ignored: includeIgnored,
        }),
        decodeFileSearchResult,
        qctx.signal,
      );
      if (res.ok && res.data !== null) {
        return { kind: "result", result: res.data };
      }
      if (res.status === 400) {
        return { kind: "invalid" };
      }
      if (!qctx.signal.aborted) {
        console.warn("files search failed", res.status, res.error);
      }
      return { kind: "failed" };
    },
    render: (answer, query) => {
      const searchPath = ctx?.getSearchPath() ?? "";
      if (isIdle(query)) {
        lastMatches = [];
        renderResults(searchPath);
        built.setNote(idleNote());
        return;
      }
      if (answer === null || answer.kind === "failed") {
        built.setNote(emptyNote({ kind: "failed" }, NOUNS[mode]));
        return;
      }
      if (answer.kind === "invalid") {
        built.setNote(INVALID_NOTE);
        return;
      }
      const res = answer.result;
      lastMatches = res.matches;
      renderResults(searchPath);
      if (res.matches.length === 0 && res.root_ignored) {
        built.setNote(IGNORED_ROOT_NOTE);
        return;
      }
      if (res.matches.length === 0) {
        // A stopped scan is stated, or an empty result reads as "nowhere".
        built.setNote(
          emptyNote(
            classify({
              matched: res.matched,
              shown: 0,
              scanned: res.scanned,
              truncated: res.truncated,
            }),
            NOUNS[mode],
          ),
        );
        return;
      }
      built.setNote(scanNote(res, res.matches.length, NOUNS[mode]));
    },
    onDismiss: () => {
      closeFilesSearch();
    },
    onSubmit: () => {
      built.run();
    },
  });
  shell = built;

  const results = el("div", {
    id: "fb-search-results",
    className: "fb-list fb-search-results hidden",
    role: "list",
  });

  // Registered before rovingFocus's own keydown, so the first row hands focus back to the field.
  results.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "ArrowUp" && e.target === results.querySelector(".fb-search-hit")) {
      e.preventDefault();
      e.stopImmediatePropagation();
      built.input.focus();
    }
  });
  rowNav = rovingFocus(results, ".fb-search-hit", { wrap: false });

  for (const field of [built.input, filesEl]) {
    field.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "ArrowDown" && lastMatches.length > 0) {
        e.preventDefault();
        rowNav?.focusFirst();
      }
    });
  }
  filesEl.addEventListener("input", () => {
    built.schedule();
  });
  wireSearchKeys(filesEl, {
    onDismiss: () => {
      closeFilesSearch();
    },
    onSubmit: () => {
      built.run();
    },
  });

  $.fbList.insertAdjacentElement("beforebegin", built.region);
  $.fbList.insertAdjacentElement("afterend", results);
  barEl = built.region;
  resultsEl = results;

  // Keyed on tab identity, not kind: files tab A to B is still `kind: "files"`.
  unsubTab?.();
  unsubTab = onBus(BUS_TAB_CHANGED, (e) => {
    if (searchOwnerID !== "" && e.to !== searchOwnerID) {
      resetFilesSearch();
    }
  });
}

type ResultItem =
  | { readonly kind: "name"; readonly match: FileMatch }
  | { readonly kind: "group"; readonly path: string; readonly count: number }
  | { readonly kind: "line"; readonly match: FileMatch };

/** Relies on the server's path order: one file's content rows arrive together. */
function groupItems(matches: readonly FileMatch[]): ResultItem[] {
  const items: ResultItem[] = [];
  // A hit's path is container-absolute, so the empty path never names a real file.
  let group = { kind: "group" as const, path: "", count: 0 };
  for (const m of matches) {
    if (m.kind !== "content") {
      items.push({ kind: "name", match: m });
      group = { kind: "group", path: "", count: 0 };
      continue;
    }
    if (group.path !== m.path) {
      group = { kind: "group", path: m.path, count: 0 };
      items.push(group);
    }
    group.count++;
    items.push({ kind: "line", match: m });
  }
  return items;
}

function itemKey(item: ResultItem): string {
  return item.kind === "group" ? join("group", item.path) : hitKey(item.match);
}

/** The item a row shows NOW: a key outlives a reply, so a kept row is repainted, never re-bound. */
const rowItems = new WeakMap<HTMLElement, ResultItem>();

function openItem(item: ResultItem): void {
  switch (item.kind) {
    case "group":
      openAtLine(item.path);
      return;
    case "line":
      openAtLine(item.match.path, item.match.line);
      return;
    case "name":
      if (item.match.kind === "dir") {
        ctx?.openFolder(item.match.path);
        return;
      }
      openFileOrPage(item.match.path);
      return;
  }
}

function rowClass(item: ResultItem): string {
  switch (item.kind) {
    case "name":
      return "";
    case "group":
      return "fb-search-group";
    case "line":
      return "fb-search-line";
  }
}

function nameChildren(m: FileMatch, searchPath: string): Node[] {
  const { parent, base } = splitPath(m.path);
  const where = hitLabel(searchPath, `${parent}/`).replace(/\/$/, "");
  return [
    el("span", { className: "fb-icon" }, iconEl(fileIcon(m.path, m.kind === "dir"))),
    el("span", { className: "fb-name fb-name-link fb-search-base" }, ...markedText(base, m.ranges)),
    ...(where === ""
      ? []
      : [el("span", { className: "fb-search-parent" }, el("bdi", { dir: "ltr" }, where))]),
  ];
}

function rowChildren(item: ResultItem, searchPath: string): Node[] {
  switch (item.kind) {
    case "name":
      return nameChildren(item.match, searchPath);
    case "group":
      return [
        el("span", { className: "fb-icon" }, iconEl(fileIcon(item.path, false))),
        el("span", { className: "fb-name fb-name-link" }, hitLabel(searchPath, item.path)),
        el("span", { className: "fb-search-count" }, String(item.count)),
      ];
    case "line":
      return [
        el("span", { className: "fb-search-lineno" }, `:${String(item.match.line)}`),
        el(
          "span",
          { className: "fb-search-excerpt" },
          ...markedText(item.match.excerpt, item.match.ranges),
        ),
      ];
  }
}

function paintRow(row: HTMLElement, item: ResultItem, searchPath: string): void {
  rowItems.set(row, item);
  const data = item.kind === "group" ? { path: item.path, line: 0, kind: "file" } : item.match;
  row.dataset["path"] = data.path;
  row.dataset["line"] = String(data.line);
  row.dataset["kind"] = data.kind;
  paintIfChanged(row, [wireSignature(item), searchPath], () => rowChildren(item, searchPath));
}

function mountRow(item: ResultItem, searchPath: string): HTMLElement {
  const row = el("div", {
    className: `fb-row fb-search-hit ${rowClass(item)}`.trim(),
    role: "listitem",
    tabindex: "-1",
  });
  row.addEventListener("click", () => {
    const current = rowItems.get(row);
    if (current !== undefined) {
      openItem(current);
    }
  });
  paintRow(row, item, searchPath);
  return row;
}

function renderResults(searchPath: string): void {
  if (resultsEl === null) {
    return;
  }
  reconcile(resultsEl, groupItems(lastMatches), {
    key: itemKey,
    mount: (item: ResultItem) => mountRow(item, searchPath),
    update: (row, item: ResultItem) => {
      paintRow(row, item, searchPath);
    },
  });
  rowNav?.refresh();
}

/** Not a popup: results render outside the panel, so outside-click dismissal would close it on a hit
 *  click. */
function isOpen(): boolean {
  return barEl !== null && !barEl.classList.contains("hidden");
}

export function initFilesSearch(c: FilesSearchCtx): void {
  ctx = c;
}

/** Open or refocus the bar; reachable from the toolbar too, since a Ctrl-F-only feature is undiscoverable. */
export function openFilesSearch(): void {
  ctx?.activateBrowser();
  ensureBuilt();
  if (barEl === null || shell === null || resultsEl === null) {
    return;
  }
  if (!isOpen()) {
    resetToggles();
  }
  // Read after activateBrowser, so it records the tab the bar opens over.
  searchOwnerID = getActiveTabId();
  barEl.classList.remove("hidden");
  resultsEl.classList.remove("hidden");
  $.fbList.classList.add("hidden");
  $.findBtn.setAttribute("aria-pressed", "true");
  shell.focus();
  shell.run();
}

export function closeFilesSearch(): void {
  if (!isOpen() || barEl === null || resultsEl === null) {
    return;
  }
  searchOwnerID = "";
  shell?.cancel();
  barEl.classList.add("hidden");
  resultsEl.classList.add("hidden");
  resultsEl.replaceChildren();
  lastMatches = [];
  $.fbList.classList.remove("hidden");
  $.findBtn.setAttribute("aria-pressed", "false");
  shell?.setNote("");
}

/** Unlike `closeFilesSearch`, also forgets the query, the filter and both toggles. */
export function resetFilesSearch(): void {
  closeFilesSearch();
  // Unconditional: `closeFilesSearch` early-returns on a closed bar.
  searchOwnerID = "";
  if (shell !== null) {
    shell.input.value = "";
  }
  if (filesEl !== null) {
    filesEl.value = "";
  }
  resetToggles();
}

function resetToggles(): void {
  applyMode("names");
  includeIgnored = false;
  ignoredBtn?.setAttribute("aria-pressed", "false");
}

/** @internal Test seam: whether the bar is open. */
// deadset:ignore DS1004 -- test seam: observes whether the files search bar is open
export function _isFilesSearchOpen(): boolean {
  return isOpen();
}

/**
 * Ctrl-F / Cmd-F for a files or editor tab, from app.ts. A repeat press while the field has focus falls through with
 * no preventDefault, so the browser's own find stays reachable.
 */
export function handleFindInFilesHotkey(e: KeyboardEvent): void {
  if (e.key.toLowerCase() !== "f" || !(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) {
    return;
  }
  if (isOpen() && shell !== null && document.activeElement === shell.input) {
    return;
  }
  e.preventDefault();
  openFilesSearch();
}

/**
 * The first printable keystroke on a bound files tab opens the bar and lands in it: no `preventDefault`, and the
 * field is focused during this keydown.
 */
export function handleFilesTypeAhead(e: KeyboardEvent): void {
  // The tab store answers which view is on screen, as F2 and find-dispatch.ts ask it.
  if (getActiveTabKind() !== "files") {
    return;
  }
  if (isOpen()) {
    return;
  }
  if (e.ctrlKey || e.metaKey || e.altKey || e.isComposing) {
    return;
  }
  // `length === 1` excludes named keys; Space activates a focused control.
  if (e.key.length !== 1 || e.key === " ") {
    return;
  }
  const active = document.activeElement;
  if (
    active instanceof HTMLInputElement ||
    active instanceof HTMLTextAreaElement ||
    active instanceof HTMLSelectElement ||
    (active instanceof HTMLElement &&
      (active.isContentEditable || active.closest("#shell-panel, dialog[open], .wt-root") !== null))
  ) {
    return;
  }
  // A keystroke can land before the lazy bind, where the root is "".
  if ((ctx?.getSearchPath() ?? "") === "") {
    return;
  }
  openFilesSearch();
}

/** Toggle the file search: what the toolbar and dispatcher buttons mean. */
export function toggleFilesSearch(): void {
  if (isOpen()) {
    closeFilesSearch();
    return;
  }
  openFilesSearch();
}

/** @internal Test seam: the lazily-built search bar, once it exists. */
// deadset:ignore DS1004 -- test seam: observes the lazily built search bar
export function _filesSearchBar(): HTMLElement | null {
  return document.getElementById("fb-search");
}

/** @internal Test seam: the results list, once it exists. */
// deadset:ignore DS1004 -- test seam: observes the search results list
export function _filesSearchResults(): HTMLElement {
  return byId<HTMLDivElement>("fb-search-results");
}
