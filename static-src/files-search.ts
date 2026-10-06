// Find in files, server-side by necessity: the client holds one listing and the server owns confinement.

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { $, byId } from "./dom.js";
import { apiGetTyped } from "./api-client.js";
import { openAtLine } from "./navigate.js";
import { reconcile } from "./reconcile.js";
import { fileIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { caseParam, createSearchShell, searchField, wireSearchKeys } from "./search-shell.js";
import type { SearchShell } from "./search-shell.js";
import { FB_ROOT } from "./files-shared.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { getActiveTabId, getActiveTabKind } from "./tabs.js";
import { classify, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns } from "./textsearch/copy.js";
import type { FileMatch, FileSearchResult } from "./wire/types.gen.js";
import { decodeFileSearchResult } from "./wire/decoders.gen.js";

/** A match is a row (a line, or a name); the scan reads files. */
const NOUNS: Nouns = {
  match: { one: "match", many: "matches" },
  scanned: { one: "file", many: "files" },
};

/** A `*` does not cross a `/`; stated where the user meets the convention. */
const GLOB_HINT =
  "One or more patterns, comma separated. " +
  "A pattern without a slash matches the file name at any depth, for example *.go. " +
  "A pattern with a slash matches the path under the folder searched, for example src/*.go. " +
  "Exclude also skips a whole folder, for example node_modules.";

export interface FilesSearchCtx {
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
let includeEl: HTMLInputElement | null = null;
let excludeEl: HTMLInputElement | null = null;
/** Its own abort signal: sharing the browser's would cancel searches and directory loads against each other. */
let shell: SearchShell | null = null;
let lastMatches: FileMatch[] = [];
/** Unsubscribe for the tab teardown, so a rebuilt module does not stack subscribers. */
let unsubTab: (() => void) | null = null;
/** The files tab the open bar belongs to, "" when closed or opened on an empty strip (the teardown never fires for ""). */
let searchOwnerID = "";

/** The search URL. `case=1` only when asked: the server reads absence as insensitive, as for the transcript search. */
export function searchURL(
  path: string,
  query: string,
  opts: { caseSensitive?: boolean; include?: string; exclude?: string } = {},
): string {
  const q = new URLSearchParams({ path, q: query });
  const flag = caseParam(opts.caseSensitive === true);
  if (flag !== "") {
    q.set("case", flag);
  }
  if ((opts.include ?? "") !== "") {
    q.set("include", opts.include ?? "");
  }
  if ((opts.exclude ?? "") !== "") {
    q.set("exclude", opts.exclude ?? "");
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

function globField(id: string, placeholder: string, label: string): HTMLInputElement {
  return searchField({
    id,
    className: "fb-search-field",
    label,
    placeholder,
    title: GLOB_HINT,
  });
}

function ensureBuilt(): void {
  if (barEl !== null) {
    return;
  }
  includeEl = globField("fb-search-include", "Include: *.go", "Include patterns");
  excludeEl = globField("fb-search-exclude", "Exclude: node_modules", "Exclude patterns");
  const globRow = el("div", { className: "fb-search-row fb-search-globs" }, includeEl, excludeEl);

  // The glob row is this surface's alone, so it arrives through `compose`; the rest is the shell's.
  const built = createSearchShell<FileSearchResult>({
    id: "fb-search",
    regionClass: "fb-search hidden",
    inputClass: "fb-search-field",
    buttonClass: "fb-search-btn",
    caseClass: "fb-search-case",
    noteClass: "fb-search-note",
    label: "Find in files",
    placeholder: "Find in files\u2026",
    inputTitle: "Find in files. Press Ctrl+F again to use the browser's find.",
    matchCase: true,
    note: true,
    closeButton: true,
    compose: ({ input, caseButton, closeButton, note }) => [
      el("div", { className: "fb-search-row" }, input, caseButton, closeButton),
      globRow,
      note,
    ],
    query: async (query, qctx) => {
      const trimmed = query.trim();
      // An empty root means no browser is bound.
      if (trimmed === "" || ctx === null || ctx.getSearchPath() === "") {
        return null;
      }
      return apiGetTyped(
        searchURL(ctx.getSearchPath(), trimmed, {
          caseSensitive: qctx.caseSensitive,
          include: includeEl?.value.trim() ?? "",
          exclude: excludeEl?.value.trim() ?? "",
        }),
        decodeFileSearchResult,
        qctx.signal,
      );
    },
    render: (res, query) => {
      const searchPath = ctx?.getSearchPath() ?? "";
      if (query.trim() === "") {
        lastMatches = [];
        renderResults(searchPath);
        built.setNote("");
        return;
      }
      if (res === null) {
        built.setNote(emptyNote({ kind: "failed" }, NOUNS));
        return;
      }
      lastMatches = res.matches;
      renderResults(searchPath);
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
            NOUNS,
          ),
        );
        return;
      }
      built.setNote(scanNote(res, res.matches.length, NOUNS));
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

  // The glob fields share wireSearchKeys, so Escape means the same in all three.
  for (const target of [includeEl, excludeEl]) {
    target.addEventListener("input", () => {
      built.schedule();
    });
    wireSearchKeys(target, {
      onDismiss: () => {
        closeFilesSearch();
      },
      onSubmit: () => {
        built.run();
      },
    });
  }

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

/** Line decides shape (content hit: `:N` + excerpt; name hit: icon + label). Kind decides the click's destination. */
function hitRow(m: FileMatch, label: string): HTMLElement {
  const isDir = m.kind === "dir";
  const isName = m.line === 0;
  const row = el(
    "div",
    {
      className: "fb-row fb-search-hit",
      role: "listitem",
      tabindex: "0",
      "data-path": m.path,
      "data-line": String(m.line),
      "data-kind": m.kind,
    },
    el("span", { className: "fb-icon" }, iconEl(fileIcon(m.path, isDir))),
    el("span", { className: "fb-name fb-name-link" }, label),
    ...(isName
      ? []
      : [
          el("span", { className: "fb-search-lineno" }, `:${String(m.line)}`),
          el("span", { className: "fb-search-excerpt" }, m.excerpt),
        ]),
  );
  const open = (): void => {
    switch (m.kind) {
      case "dir":
        ctx?.openFolder(m.path);
        return;
      case "name":
        openAtLine(m.path);
        return;
      case "content":
        openAtLine(m.path, m.line);
        return;
    }
  };
  row.addEventListener("click", open);
  row.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      open();
    }
  });
  return row;
}

function renderResults(searchPath: string): void {
  if (resultsEl === null) {
    return;
  }
  reconcile(resultsEl, lastMatches, {
    key: hitKey,
    mount: (m: FileMatch) => hitRow(m, hitLabel(searchPath, m.path)),
    // A re-run produces a new hit set; an unchanged row shows the same line.
    update: () => undefined,
  });
}

/**
 * Open state is the bar's own class. Not a popup: results render outside the panel, so outside-click dismissal would
 * close it on a hit click.
 */
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

/** Drop the search: close it and forget the query and globs. */
export function resetFilesSearch(): void {
  closeFilesSearch();
  // Unconditional: `closeFilesSearch` early-returns on a closed bar.
  searchOwnerID = "";
  if (shell !== null) {
    shell.input.value = "";
  }
  if (includeEl !== null) {
    includeEl.value = "";
  }
  if (excludeEl !== null) {
    excludeEl.value = "";
  }
}

/** @internal Test seam: whether the bar is open. */
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
export function _filesSearchBar(): HTMLElement | null {
  return document.getElementById("fb-search");
}

/** @internal Test seam: the results list, once it exists. */
export function _filesSearchResults(): HTMLElement {
  return byId<HTMLDivElement>("fb-search-results");
}
