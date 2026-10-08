// File browser: navigate, create/delete/rename, upload by dialog or drop. The picker modal is files-picker.ts.

import { $ } from "./dom.js";
import { swapViews } from "./view-swap.js";
import { onBus, BUS_KEYS_ESCAPE } from "./bus.js";
import {
  filesTabIdFor,
  getActiveTabKind,
  openTab,
  renameTab,
  setFilesRoute,
  openFilesView,
  toggleFilesView,
} from "./tabs.js";
import { openFile, openFileInBackground } from "./editor-openers.js";
import { openChange } from "./navigate.js";
import { fileDownloadURL } from "./utils-url.js";
import { onGitStatusChange, statusForPath, statusUnder } from "./git-status-store.js";
import { describeStatus } from "./git-types.js";
import { confirm as confirmDialog } from "./confirm.js";
// The browser's path is a workspace preference in config.json, so another device lands where this one looked.
import { patchSettings } from "./persist.js";
import { fileIcon, FILE_ICONS, ICON_TAB_WEB } from "./icons.js";
import { isWorkspacePage } from "./preview-card.js";
import { openWebPreview } from "./web-open.js";
import { iconEl } from "./icon-el.js";
import { attachPathsToActiveChat } from "./chat.js";
import { initBrowserDragDrop } from "./files-browser-drop.js";
import { closeFilesSearch, initFilesSearch, resetFilesSearch } from "./files-search.js";
import { screenUploads } from "./upload-policy.js";
import * as toast from "./toast.js";
import {
  type FileEntry,
  formatSize,
  formatDate,
  joinPath,
  parentPath,
  FB_ROOT,
  normalizeDirPath,
  listNotice,
  FB_NOTICE,
  sortEntries,
  initEditablePath,
  FB_ROW,
  FB_NAME,
  FB_NAME_LINK,
  FB_CHECK,
  FB_META,
} from "./files-shared.js";
import { fetchDir, type DirResult, type FetchDirOpts } from "./files-fetch.js";
import { fileRowsSkeleton, paintPlaceholder } from "./skeleton.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { setOnUploadComplete } from "./files-picker.js";
import {
  createFile,
  createFolder,
  renameFile,
  deleteFilesBatch,
  upload,
  downloadFiles,
} from "./actions/files.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { el } from "@cplieger/reactive";
import { join as joinKey } from "@cplieger/keyenc";
import { KEY_ATTR, reconcile } from "./reconcile.js";
import { replaceRoute } from "./router.js";
import { buildPath } from "./route-path.js";
import { FileBrowserState } from "./files-state.js";
export { FileBrowserState } from "./files-state.js";

type FbEntry =
  { kind: "parent" } | { kind: "entry"; entry: FileEntry } | { kind: "error"; message: string };

const PARENT_KEY = "__parent__";

/** One holder for every browser: a switch must abort the outgoing tab's read, or it paints into the incoming listing. */
const browserFetchHolder: FetchDirOpts = { controllerHolder: { current: null } };
registerCleanup(() => browserFetchHolder.controllerHolder.current?.abort());

/** Keyed by the tab's normalised ref, so one folder has one key however spelled. */
const browserStates = new Map<string, FileBrowserState>();

/** The ref of the files tab the shared DOM is bound to, or "" when none is. */
let boundRef = "";

/** A detached state when nothing is bound, so callers need no null check; nothing binds to or renders from it. */
function cur(): FileBrowserState {
  return browserStates.get(boundRef) ?? new FileBrowserState();
}

/** The one creation site, so a tab ref enters the path space through `normalizeDirPath`. */
function stateFor(ref: string): FileBrowserState {
  const existing = browserStates.get(ref);
  if (existing !== undefined) {
    return existing;
  }
  const created = new FileBrowserState(normalizeDirPath(ref));
  browserStates.set(ref, created);
  return created;
}

/** Module-level because `patchSettings` debounces, so the settings payload may lag the last navigation. */
let defaultDir = FB_ROOT;

/** Seed the start folder for a new Files tab from loaded settings; "" means the mounts listing. Local only. */
export function noteDefaultBrowsePath(path: string): void {
  defaultDir = normalizeDirPath(path);
}

/** That folder, FB_ROOT when none was recorded. */
export function defaultBrowsePath(): string {
  return defaultDir;
}

/** Records both the local value and config.json, so the two cannot drift. */
function recordBrowsePath(path: string): void {
  defaultDir = normalizeDirPath(path);
  void patchSettings({ fb_path: path });
}

/** Bind the shared DOM to one files tab without loading; the same browser skips the repaint. */
export function bindFilesTab(ref: string): void {
  if (boundRef !== ref) {
    const st = stateFor(ref);
    boundRef = ref;
    // The label follows the published route, which a typed path has not reached yet.
    if (!st.pendingOpen) {
      renameTab(filesTabIdFor(ref), filesRowName(st.currentPath));
    }
    updateNavButtons();
    // From cached entries, so the switch paints instantly and the fetch corrects it.
    renderList({ transition: false });
  }
  // Restored on the same-tab path too: a hidden box keeps no offset.
  const wrap = $.fbList.parentElement;
  if (wrap !== null) {
    wrap.scrollTop = cur().scrollTop;
  }
}

/** A box with no layout reads 0, so nothing records while hidden. */
function trackListScroll(): void {
  const wrap = $.fbList.parentElement;
  wrap?.addEventListener(
    "scroll",
    () => {
      if (boundRef !== "" && wrap.getClientRects().length > 0) {
        cur().scrollTop = wrap.scrollTop;
      }
    },
    { passive: true },
  );
}

/** Bind and load — what a files tab's activation means. The factory's `refresh`. */
export function showFilesTab(ref: string): void {
  bindFilesTab(ref);
  loadDir();
}

/** Drop a closed tab's state, unbinding when it was on screen. Does not write `fb_path`, which says where a new tab starts. */
export function releaseFilesTab(ref: string): void {
  browserStates.delete(ref);
  if (boundRef !== ref) {
    return;
  }
  // Or its late answer acts for a tab that is gone: a route write, a heal's refetch.
  browserFetchHolder.controllerHolder.current?.abort();
  boundRef = "";
  resetFilesSearch();
  // Rows kept while hidden would replay their entry animation together.
  $.fbList.replaceChildren();
}

/** Point one files tab at an outside directory (normalised), by ref so either order with the activation is correct. */
export function pointFilesTab(ref: string, dir: string): void {
  const target = normalizeDirPath(dir);
  stateFor(ref).pointTo(target);
  publishRoute(ref, target);
  if (boundRef === ref) {
    updateNavButtons();
    loadWithTransition();
  }
}

/** Twin of tab-materialize.ts's `filesTabName`; this is the rename issued as the tab navigates. */
function filesRowName(dir: string): string {
  return dir === FB_ROOT ? "Files" : (dir.split("/").pop() ?? dir);
}

export function initFileBrowser(): void {
  $.filesBtn.addEventListener("click", () => {
    void toggleFilesView(defaultBrowsePath());
  });
  $.fbBack.addEventListener("click", goBack);
  $.fbForward.addEventListener("click", goForward);
  $.fbNewFile.addEventListener("click", () => {
    newFile();
    $.fbNewFile.closest<HTMLDetailsElement>(".fb-new-menu")?.removeAttribute("open");
  });
  $.fbNewFolder.addEventListener("click", () => {
    newFolder();
    $.fbNewFolder.closest<HTMLDetailsElement>(".fb-new-menu")?.removeAttribute("open");
  });
  $.fbRename.addEventListener("click", renameSelected);
  $.fbDelete.addEventListener("click", deleteSelected);
  $.fbDownload.addEventListener("click", downloadSelected);
  $.fbUpload.addEventListener("click", uploadViaDialog);
  $.fbAddToChat.addEventListener("click", addSelectedToChat);
  $.fbPreview.replaceChildren(iconEl(ICON_TAB_WEB));
  $.fbPreview.addEventListener("click", () => {
    const path = selectedPreviewPath();
    if (path !== null) {
      openWebPreview(path);
    }
  });

  initPathInput();
  trackListScroll();
  initBrowserDragDrop({
    getCurrentPath: () => cur().currentPath,
    getEntryMap: () => cur().entryMap,
    reload: loadDir,
  });
  initFilesSearch({
    // "" when unbound, which the bar refuses; the detached FB_ROOT would search the mounts root.
    getSearchPath: () => (boundRef === "" ? "" : cur().currentPath),
    // Show, never toggle: a Ctrl-F from another tab must bring the browser forward.
    activateBrowser: () => {
      void openFilesView(defaultBrowsePath());
    },
    // Close first: `#fb-list` is hidden while the bar is open.
    openFolder: (path) => {
      closeFilesSearch();
      navigate(path);
    },
  });

  // Disabled while any exclusive file operation is in flight (e.g. rename + delete).
  const fileOps = [
    "files.upload",
    "files.create_file",
    "files.create_folder",
    "files.rename",
    "files.delete",
  ] as const;
  bindLoadingState(fileOps, $.fbUpload, { preserveDisabled: true });
  bindLoadingState(fileOps, $.fbNewFile, { preserveDisabled: true });
  bindLoadingState(fileOps, $.fbNewFolder, { preserveDisabled: true });
  bindLoadingState(["files.download"], $.fbDownload, { preserveDisabled: true });
  bindLoadingState(fileOps, $.fbRename, { preserveDisabled: true });
  bindLoadingState(fileOps, $.fbDelete, { preserveDisabled: true });

  // Gated on the active tab: every Escape reaches this listener.
  onBus(BUS_KEYS_ESCAPE, () => {
    if (getActiveTabKind() !== "files") {
      return;
    }
    if (cur().selected.size > 0) {
      cur().deselectAll();
      updateActionButtons();
      updateRowHighlights();
    }
  });

  // F2 renames a single selected item; keyed on the tab, as find-dispatch.ts does.
  document.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key !== "F2") {
      return;
    }
    if (getActiveTabKind() !== "files") {
      return;
    }
    if (cur().selected.size !== 1) {
      return;
    }
    e.preventDefault();
    renameSelected();
  });

  // Bound, not active, so a backgrounded browser picks up an upload; unbound would paint the detached state.
  setOnUploadComplete(() => {
    if (boundRef !== "") {
      loadDir();
    }
  });
}

function initPathInput(): void {
  $.fbPath.setAttribute("aria-label", "File browser path");
  initEditablePath($.fbPath, {
    onNavigate: navigateTyped,
    getCurrentPath: () => cur().currentPath,
  });
}

let gitWatched = false;

/** Starts on the first listing, not at boot: the store scans every worktree for its first subscriber. */
function watchGitStatus(): void {
  if (gitWatched) {
    return;
  }
  gitWatched = true;
  registerCleanup(onGitStatusChange(repaintRows));
}

function loadDir(): void {
  watchGitStatus();
  const path = cur().currentPath;
  // A move or a Retry clears the error in state only; its notice would otherwise stay up, and block the placeholder,
  // until the next answer lands.
  if (cur().listError === "" && $.fbList.querySelector(`.${FB_NOTICE}`) !== null) {
    renderList({ transition: false });
  }
  // Only with nothing of this directory on screen and no answer yet, or a refetch would clear rows in use. The ".." row
  // is not this directory's, so the placeholder goes beside it.
  const skeleton =
    cur().entries.length === 0 && !cur().answered
      ? skeletonTiming(() =>
          paintPlaceholder($.fbList, fileRowsSkeleton, {
            content: `[${KEY_ATTR}]:not([${KEY_ATTR}="${PARENT_KEY}"])`,
            mount: "append",
          }),
        )
      : null;
  void fetchDir(path, browserFetchHolder).then((d) => {
    // Read before the cancel: the placeholder counts as content on screen.
    const onScreen = $.fbList.childElementCount > 0;
    skeleton?.cancel();
    // The first populate skips the fade: a list fade would cancel the still-running view-open fade.
    settle(path, d, onScreen);
  });
}

/** Renders synchronously on success, so callers can chain inline rename on the new row. */
function loadDirAsync(): Promise<void> {
  const path = cur().currentPath;
  return fetchDir(path, browserFetchHolder).then((d) => {
    settle(path, d, false);
  });
}

/** `path`'s answer, painted into the bound browser: any move or rebind loads again, which aborts this request. */
function settle(path: string, d: DirResult, transition: boolean): void {
  const st = cur();
  switch (d.kind) {
    case "stale":
      return;
    case "not-dir":
      settleOnParent(path, st.pendingOpen);
      return;
    case "error":
      // Heal an unreachable origin to the mounts listing once. `fb_path` is not cleared.
      if (st.pendingRestore && path !== FB_ROOT) {
        st.pendingRestore = false;
        st.reset();
        setFilesRoute(boundRef, FB_ROOT);
        updateNavButtons();
        loadDir();
        return;
      }
      publishPending(path);
      st.entries = [];
      st.answered = false;
      st.entryMap.clear();
      st.dirWritable = false;
      st.listError = d.message;
      updateWriteButtons();
      renderList({ transition });
      return;
    case "ok":
      publishPending(path);
      st.pendingRestore = false;
      applyListing(d.files, d.writable);
      renderList({ transition });
      return;
  }
}

function publishPending(path: string): void {
  if (cur().pendingOpen) {
    cur().pendingOpen = false;
    publishRoute(boundRef, path);
  }
}

function applyListing(files: FileEntry[], writable: boolean): void {
  cur().entries = files;
  cur().answered = true;
  cur().listError = "";
  cur().entryMap.clear();
  for (const e of files) {
    cur().entryMap.set(e.name, e);
  }
  cur().dirWritable = writable;
}

/**
 * `file` named a file: its folder takes its place in the trail, the route and `fb_path`. Only the path field `open`s it;
 * every other door (a deep link, the boot restore) lands on the folder alone, since an unasked editor tab is a surprise.
 */
function settleOnParent(file: string, open: boolean): void {
  const dir = parentPath(file);
  cur().replaceCurrent(dir);
  // A deep link put the file in the address bar; its entry becomes the folder's rather than gaining one.
  if (location.pathname === buildPath({ kind: "files", path: file })) {
    replaceRoute({ kind: "files", path: dir });
  }
  publishRoute(boundRef, dir);
  updateNavButtons();
  if (open) {
    openFile(file);
  }
  loadDir();
}

/** The row's route, not `pushRoute`: the projection is the one URL writer. */
function publishRoute(ref: string, path: string): void {
  recordBrowsePath(path);
  setFilesRoute(ref, path);
  renameTab(filesTabIdFor(ref), filesRowName(path));
}

/**
 * Every reader move ends here: the one place a reader's move retires `pendingRestore`, and where a shown error clears
 * even when the path is unchanged. `pointFilesTab` and `settleOnParent` bypass it.
 */
function readerMove(): void {
  cur().pendingRestore = false;
  cur().listError = "";
  updateNavButtons();
  loadWithTransition();
}

function publishDir(path: string): void {
  publishRoute(boundRef, path);
  readerMove();
}

function navigate(path: string): void {
  cur().navigate(path);
  publishDir(path);
}

/**
 * The path field's move publishes once the answer says what the path is, so a typed file never reaches the route or
 * `fb_path`. An unchanged path reloads.
 */
function navigateTyped(path: string): void {
  if (cur().navigate(path)) {
    cur().pendingOpen = true;
  }
  readerMove();
}

function goBack(): void {
  if (!cur().goBack()) {
    return;
  }
  publishDir(cur().currentPath);
}

function goForward(): void {
  if (!cur().goForward()) {
    return;
  }
  publishDir(cur().currentPath);
}

/** The same path, so no move clears the error; clearing it here takes the notice down before the answer. */
function retryListing(): void {
  cur().listError = "";
  loadDir();
}

function loadWithTransition(): void {
  // Only starts the fetch; renderList fades the list when it lands. swapViews still cancels a previous transition.
  swapViews(() => {
    loadDir();
  });
}

function updateNavButtons(): void {
  $.fbBack.disabled = cur().historyIdx <= 0;
  $.fbForward.disabled = cur().historyIdx >= cur().history.length - 1;
  $.fbPath.value = cur().currentPath;
  $.fbPath.readOnly = true;
  updateToolbarContext();
}

function updateActionButtons(): void {
  const count = cur().selected.size;
  const single = count === 1;
  const any = count > 0;
  $.fbDownload.disabled = !any;
  $.fbRename.disabled = !single || !cur().dirWritable;
  $.fbDelete.disabled = !any || !cur().dirWritable;
  $.fbAddToChat.disabled = !any;
  $.fbPreview.disabled = selectedPreviewPath() === null;
  updateWriteButtons();
}

function selectedPreviewPath(): string | null {
  const state = cur();
  if (state.selected.size !== 1) {
    return null;
  }
  const name = [...state.selected][0] ?? "";
  const entry = state.entries.find((e) => e.name === name);
  const path = joinPath(state.currentPath, name);
  return entry !== undefined && !entry.isDir && isWorkspacePage(path) ? path : null;
}

/** Mobile toolbar priority: navigation while browsing, selection actions with a selection. */
function updateToolbarContext(): void {
  $.fbBack
    .closest<HTMLElement>(".view-toolbar-inner")
    ?.classList.toggle("has-selection", cur().selected.size > 0);
}

function updateWriteButtons(): void {
  $.fbNewFile.disabled = !cur().dirWritable;
  $.fbNewFolder.disabled = !cur().dirWritable;
  $.fbUpload.disabled = !cur().dirWritable;
  $.fbNewFile
    .closest<HTMLDetailsElement>(".fb-new-menu")
    ?.toggleAttribute("data-unavailable", !cur().dirWritable);
  updateToolbarContext();
}

function renderList(opts: { transition?: boolean } = {}): void {
  updateNavButtons();

  const swap = (): HTMLElement => {
    const sorted = sortEntries(cur().entries);
    cur().sortedNames = sorted.map((e) => e.name);

    const items: FbEntry[] = [];
    const listError = cur().listError;
    if (listError !== "") {
      // A notice is not a list item, so the list role goes with the rows.
      $.fbList.removeAttribute("role");
      items.push({ kind: "error", message: listError });
    } else {
      $.fbList.setAttribute("role", "list");
      if (cur().currentPath !== FB_ROOT) {
        items.push({ kind: "parent" });
      }
      for (const entry of sorted) {
        items.push({ kind: "entry", entry });
      }
    }

    reconcile($.fbList, items, {
      key: fbEntryKey,
      mount: mountFbEntry,
      update: (row: HTMLElement, e: FbEntry) => {
        if (e.kind !== "entry") {
          return;
        }
        // Metadata can change on refetch; selection is updateRowHighlights()'s.
        const meta = row.querySelector(`.${FB_META}`);
        if (meta !== null) {
          const parts: string[] = [];
          if (!e.entry.isDir) {
            parts.push(formatSize(e.entry.size));
          }
          parts.push(formatDate(e.entry.modTime));
          parts.push(e.entry.mode);
          meta.textContent = parts.join("   ·   ");
        }
      },
    });

    updateActionButtons();
    updateRowHighlights();
    return $.fbList;
  };

  // transition: false for createEntry's inline rename and for in-place re-renders.
  if (opts.transition === false) {
    swap();
  } else {
    swapViews(swap);
  }
}

/** Keyed by path and message, so a reload that fails the same way keeps its node and any other failure remounts. */
function fbEntryKey(e: FbEntry): string {
  switch (e.kind) {
    case "parent":
      return PARENT_KEY;
    case "entry":
      return `entry:${e.entry.name}`;
    case "error":
      return joinKey("error", cur().currentPath, e.message);
  }
}

function mountFbEntry(e: FbEntry): HTMLElement {
  switch (e.kind) {
    case "parent":
      return parentRow();
    case "entry":
      return entryRow(e.entry);
    case "error":
      return errorNotice(e.message);
  }
}

function errorNotice(message: string): HTMLDivElement {
  const actions = [{ label: "Retry", run: retryListing }];
  // Off the root, offer the way back (a nested granted root's parent is not browsable; a deleted directory).
  if (cur().currentPath !== FB_ROOT) {
    actions.push({
      label: "Go to root",
      run: () => {
        navigate(FB_ROOT);
      },
    });
  }
  return listNotice(message, actions);
}

/** Middle-click opens in the background; no engine emits `click` for it. The `mousedown` companion cancels autoscroll. */
function wireBackgroundOpen(row: HTMLElement, open: () => void): void {
  row.addEventListener("auxclick", (e: MouseEvent) => {
    if (e.button !== 1) {
      return;
    }
    if ((e.target as HTMLElement).closest(`.${FB_CHECK}, .fb-git-letter`) !== null) {
      return;
    }
    e.preventDefault();
    open();
  });
  row.addEventListener("mousedown", (e: MouseEvent) => {
    if (e.button === 1) {
      e.preventDefault();
    }
  });
}

function parentRow(): HTMLDivElement {
  const checkSpan = el("span", { className: FB_CHECK });

  const icon = el("span", { className: "fb-icon" }, iconEl(FILE_ICONS["folder"] ?? ""));

  const nameSpan = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, "..");
  nameSpan.addEventListener("click", () => {
    navigate(parentPath(cur().currentPath));
  });

  const metaSpan = el("span", { className: FB_META });

  const row = el(
    "div",
    { className: FB_ROW, role: "listitem" },
    checkSpan,
    icon,
    nameSpan,
    metaSpan,
  ) as HTMLDivElement;
  // The same pair on "..", without checkbox or badge.
  wireBackgroundOpen(row, () => {
    void openTab({
      kind: "files",
      ref: parentPath(cur().currentPath),
      activate: false,
    });
  });
  return row;
}

/** A file's own status, or a directory's worst beneath it, in the existing `git-st-<letter>` colours. */
function statusBadge(absPath: string, isDir: boolean): HTMLElement | null {
  const letter = isDir ? statusUnder(absPath) : statusForPath(absPath);
  if (letter === "") {
    return null;
  }
  const label = describeStatus(letter);
  const badge = el("span", {
    className: `fb-git-letter git-st-${letter.toLowerCase()}`,
    "data-tooltip": isDir ? `Contains changes: ${label}` : label,
    "aria-label": isDir ? `Contains changes: ${label}` : `Git status: ${label}`,
  });
  badge.textContent = letter;
  if (!isDir) {
    badge.classList.add("fb-git-clickable");
    badge.setAttribute("role", "button");
    badge.addEventListener("click", (e: MouseEvent) => {
      e.stopPropagation();
      openChange(absPath);
    });
  }
  return badge;
}

function entryRow(entry: FileEntry): HTMLDivElement {
  const check = el("input", {
    type: "checkbox",
    className: FB_CHECK,
    checked: cur().selected.has(entry.name),
  }) as HTMLInputElement;
  check.addEventListener("change", () => {
    if (check.checked) {
      cur().selectEntry(entry.name);
    } else {
      cur().deselectEntry(entry.name);
    }
    updateActionButtons();
    updateRowHighlights();
  });

  const icon = el("span", { className: "fb-icon" }, iconEl(fileIcon(entry.name, entry.isDir)));

  const name = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, entry.name);
  name.addEventListener("click", (e: MouseEvent) => {
    if (e.shiftKey && cur().lastClickedName !== "") {
      shiftSelect(cur().lastClickedName, entry.name);
      return;
    }
    if (entry.isDir) {
      navigate(joinPath(cur().currentPath, entry.name));
    } else {
      openFile(joinPath(cur().currentPath, entry.name));
    }
  });

  const parts: string[] = [];
  if (!entry.isDir) {
    parts.push(formatSize(entry.size));
  }
  parts.push(formatDate(entry.modTime));
  parts.push(entry.mode);
  const meta = el("span", { className: FB_META }, parts.join("   ·   "));

  const abs = joinPath(cur().currentPath, entry.name);
  const badge = statusBadge(abs, entry.isDir);

  const row = el(
    "div",
    {
      className: FB_ROW,
      role: "listitem",
      "data-name": entry.name,
      "data-is-dir": String(entry.isDir),
      "data-path": abs,
    },
    check,
    icon,
    name,
    ...(badge !== null ? [badge] : []),
    meta,
  ) as HTMLDivElement;

  wireBackgroundOpen(row, () => {
    if (entry.isDir) {
      void openTab({ kind: "files", ref: normalizeDirPath(abs), activate: false });
    } else {
      openFileInBackground(abs);
    }
  });

  return row;
}

/** In place: the poll must not blow away selection or scroll. */
function repaintRows(): void {
  for (const row of $.fbList.querySelectorAll<HTMLElement>(`.${FB_ROW}[data-path]`)) {
    const abs = row.dataset["path"] ?? "";
    const isDir = row.dataset["isDir"] === "true";
    row.querySelector(".fb-git-letter")?.remove();
    const badge = statusBadge(abs, isDir);
    if (badge !== null) {
      row.insertBefore(badge, row.querySelector(`.${FB_META}`));
    }
  }
}

/** @internal Test seam: the poll's repaint callback, without the poll. */
export function _repaintRowsForTest(): void {
  repaintRows();
}

function shiftSelect(from: string, to: string): void {
  const a = cur().sortedNames.indexOf(from);
  const b = cur().sortedNames.indexOf(to);
  if (a === -1 || b === -1) {
    return;
  }
  const lo = Math.min(a, b);
  const hi = Math.max(a, b);
  for (let i = lo; i <= hi; i++) {
    cur().selected.add(cur().sortedNames[i]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
  }
  cur().lastClickedName = to;
  updateActionButtons();
  updateRowHighlights();
}

function updateRowHighlights(): void {
  for (const row of [...$.fbList.children]) {
    const node = row as HTMLDivElement;
    const name = node.dataset["name"];
    if (name === undefined) {
      continue;
    }
    node.classList.toggle("fb-row-selected", cur().selected.has(name));
    const check = node.querySelector<HTMLInputElement>(`.${FB_CHECK}`);
    if (check !== null) {
      check.checked = cur().selected.has(name);
    }
  }
}

function newFile(): void {
  createEntry("touch", "new file");
}
function newFolder(): void {
  createEntry("mkdir", "new folder");
}

function createEntry(action: "touch" | "mkdir", name: string): void {
  const actionFn = action === "mkdir" ? createFolder : createFile;
  void actionFn.dispatch(
    {
      dir: cur().currentPath,
      name,
    },
    {
      onSuccess: () => {
        void loadDirAsync().then(() => {
          startInlineRename(name);
        });
      },
    },
  );
}

function addSelectedToChat(): void {
  if (cur().selected.size === 0) {
    return;
  }
  // Directories attach as plain paths too. Detached.
  void attachPathsToActiveChat(
    [...cur().selected].map((name) => joinPath(cur().currentPath, name)),
  );
}

function renameSelected(): void {
  if (cur().selected.size !== 1) {
    return;
  }
  startInlineRename([...cur().selected][0]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
}

function startInlineRename(targetName: string): void {
  const row = [...$.fbList.children].find(
    (child) => (child as HTMLDivElement).dataset["name"] === targetName,
  ) as HTMLDivElement | undefined;
  if (row === undefined) {
    return;
  }

  const nameEl = row.querySelector(`.${FB_NAME}`)!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  const original = nameEl.textContent ?? ""; // eslint-disable-line @typescript-eslint/no-unnecessary-condition

  const input = el("input", {
    type: "text",
    className: "fb-name-edit",
    value: original,
  }) as HTMLInputElement;
  nameEl.replaceWith(input);
  input.focus();
  const dotIdx = original.lastIndexOf(".");
  if (dotIdx > 0) {
    input.setSelectionRange(0, dotIdx);
  } else {
    input.select();
  }

  let committed = false;
  const restore = (text: string): HTMLElement => {
    const span = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, text);
    input.replaceWith(span);
    return span;
  };

  const commit = (): void => {
    if (committed) {
      return;
    }
    committed = true;
    const newName = input.value.trim();
    const span = restore(newName !== "" ? newName : original);
    if (newName === "" || newName === original) {
      return;
    }

    void renameFile.dispatch(
      { dir: cur().currentPath, original, newName },
      {
        onSuccess: () => {
          // Reload to rebuild rows with handlers and the post-rename sort.
          cur().deselectAll();
          updateActionButtons();
          loadDir();
        },
        onError: () => {
          span.textContent = original;
        },
      },
    );
  };

  const cancel = (): void => {
    if (committed) {
      return;
    }
    committed = true;
    restore(original);
  };

  input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      commit();
    } else if (e.key === "Escape") {
      e.preventDefault();
      cancel();
    }
  });
  input.addEventListener("blur", () => {
    commit();
  });
}

function deleteSelected(): void {
  if (cur().selected.size === 0) {
    return;
  }
  const names = [...cur().selected];
  const label = names.length === 1 ? names[0]! : `${String(names.length)} items`; // eslint-disable-line @typescript-eslint/no-non-null-assertion
  const capturedDir = cur().currentPath;
  void (async () => {
    const ok = await confirmDialog(
      `Delete ${label}? This cannot be undone.`,
      "Delete",
      "destructive",
    );
    if (!ok) {
      return;
    }
    void deleteFilesBatch.dispatch(
      { dir: capturedDir, names, listEl: $.fbList },
      {
        onSuccess: () => {
          cur().deselectAll();
          updateActionButtons();
          setTimeout(loadDir, 200);
        },
        onError: () => {
          loadDir();
        },
      },
    );
  })();
}

function downloadSelected(): void {
  if (cur().selected.size === 0) {
    return;
  }
  const names = [...cur().selected];
  // Single file: the GET endpoint. The anchor click is idempotent, so no double-click guard.
  const singleName = names.length === 1 ? names[0] : undefined;
  if (singleName !== undefined && cur().entryMap.get(singleName)?.isDir !== true) {
    // Safe only because the server answers `Content-Disposition: attachment`: a navigated `.svg` is script-capable.
    const a = el("a", {
      href: fileDownloadURL(joinPath(cur().currentPath, singleName)),
      download: singleName,
      rel: "noopener",
    });
    document.body.appendChild(a);
    a.click();
    a.remove();
    return;
  }
  const paths = names.map((n) => joinPath(cur().currentPath, n));
  void downloadFiles.dispatch({ paths });
}

function uploadViaDialog(): void {
  const input = el("input", { type: "file", multiple: true }) as HTMLInputElement;
  input.addEventListener("change", () => {
    if (input.files === null || input.files.length === 0) {
      return;
    }
    // Screened: the OS picker hides the limit, and multi-select reaches the total cap without a large file.
    const screened = screenUploads(input.files);
    if (screened.skipped !== "") {
      toast.error(screened.skipped);
    }
    if (screened.files === null) {
      return;
    }
    void upload.dispatch(
      { files: screened.files, targetDir: cur().currentPath },
      {
        onSuccess: (paths) => {
          loadDir();
          // Detached.
          void attachPathsToActiveChat(paths);
        },
      },
    );
  });
  input.click();
}
