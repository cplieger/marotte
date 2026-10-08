// File picker modal: multi-select workspace files and attach their paths to the active chat.

import { closeModal, openModal } from "./modals.js";
import { fileIcon, FILE_ICONS } from "./icons.js";
import { iconEl } from "./icon-el.js";
import {
  joinPath,
  parentPath,
  FB_ROOT,
  normalizeDirPath,
  listNotice,
  sortEntries,
  initEditablePath,
  FB_ROW,
  FB_NAME,
  FB_NAME_LINK,
  FB_CHECK,
} from "./files-shared.js";
import { fetchDir, type FetchDirOpts } from "./files-fetch.js";
import { attachPathsToActiveChat } from "./chat.js";
import { byId } from "./dom.js";
import { upload } from "./actions/files.js";
import { screenUploads } from "./upload-policy.js";
import * as toast from "./toast.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { KEY_ATTR, reconcile } from "./reconcile.js";
import { fileRowsSkeleton, paintPlaceholder } from "./skeleton.js";
import { el } from "@cplieger/reactive";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";

type DirEntry = { kind: "up" } | { kind: "file"; name: string; isDir: boolean };

let currentPath = FB_ROOT;
const selected = new Set<string>();
let onUploadComplete: (() => void) | null = null;

/** Per-picker, so the browser's fetches and the picker's cannot abort each other. */
const pickerFetchHolder: FetchDirOpts = { controllerHolder: { current: null } };
registerCleanup(() => pickerFetchHolder.controllerHolder.current?.abort());

export function setOnUploadComplete(fn: () => void): void {
  onUploadComplete = fn;
}

export function openFilePicker(preUploadFiles?: FileList, startPath = FB_ROOT): void {
  currentPath = normalizeDirPath(startPath);
  selected.clear();
  loadDir();
  syncAttachBtn();
  openModal(byId<HTMLDivElement>("filepicker-modal"));
  if (preUploadFiles !== undefined && preUploadFiles.length > 0) {
    performUpload(preUploadFiles);
  }
}

export function initFilePicker(): void {
  byId("filepicker-close").addEventListener("click", () => {
    closeModal(byId<HTMLDivElement>("filepicker-modal"));
  });

  byId("filepicker-upload").addEventListener("click", () => {
    const input = el("input", { type: "file", multiple: true }) as HTMLInputElement;
    input.addEventListener("change", () => {
      if (input.files === null || input.files.length === 0) {
        return;
      }
      performUpload(input.files);
    });
    input.click();
  });

  bindLoadingState("files.upload", byId<HTMLButtonElement>("filepicker-upload"), {
    preserveDisabled: true,
  });

  byId("filepicker-attach").addEventListener("click", () => {
    if (selected.size === 0) {
      return;
    }
    // Detached: the modal closes and nothing after reads the chat.
    void attachPathsToActiveChat([...selected].map((name) => joinPath(currentPath, name)));
    selected.clear();
    closeModal(byId<HTMLDivElement>("filepicker-modal"));
  });

  const pathEl = byId<HTMLInputElement>("filepicker-path");
  initEditablePath(pathEl, {
    onNavigate: (target) => {
      currentPath = target;
      selected.clear();
      loadDir();
      syncAttachBtn();
    },
    getCurrentPath: () => currentPath,
  });
}

function performUpload(files: FileList): void {
  const modal = byId<HTMLDivElement>("filepicker-modal");
  // Screened like the other upload doors; the modal stays open so the user can pick again.
  const screened = screenUploads(files);
  if (screened.skipped !== "") {
    toast.error(screened.skipped);
  }
  if (screened.files === null) {
    return;
  }
  void upload.dispatch(
    { files: screened.files, targetDir: currentPath },
    {
      onSuccess: (paths) => {
        onUploadComplete?.();
        // Detached: the modal close is independent of the chat existing yet.
        void attachPathsToActiveChat(paths);
        closeModal(modal);
      },
    },
  );
}

function syncAttachBtn(): void {
  const btn = byId<HTMLButtonElement>("filepicker-attach");
  btn.disabled = selected.size === 0;
  btn.textContent =
    selected.size > 0
      ? `Attach ${String(selected.size)} item${selected.size > 1 ? "s" : ""}`
      : "Attach";
}

function loadDir(): void {
  const list = byId<HTMLDivElement>("filepicker-list");
  const pathEl = byId<HTMLInputElement>("filepicker-path");
  pathEl.value = currentPath;
  pathEl.readOnly = true;
  // Before the fetch, so a notice (an error, "Empty") goes the moment a move or a Retry starts.
  for (const child of [...list.children]) {
    if (child.getAttribute(KEY_ATTR) === null) {
      child.remove();
    }
  }
  const skeleton = skeletonTiming(() =>
    paintPlaceholder(list, () => fileRowsSkeleton({ meta: false })),
  );

  void fetchDir(currentPath, pickerFetchHolder).then((d) => {
    skeleton.cancel();
    if (d.kind === "stale") {
      return;
    }

    if (d.kind === "not-dir") {
      const name = currentPath.split("/").pop() ?? "";
      currentPath = parentPath(currentPath);
      selected.clear();
      selected.add(name);
      syncAttachBtn();
      loadDir();
      return;
    }

    if (d.kind === "error") {
      reconcile(list, [], { key: () => "", mount: () => el("div") });
      list.appendChild(listNotice(d.message, [{ label: "Retry", run: loadDir }]));
      return;
    }

    const sorted = sortEntries(d.files);
    const entries: DirEntry[] = [];
    if (currentPath !== FB_ROOT) {
      entries.push({ kind: "up" });
    }
    for (const f of sorted) {
      entries.push({ kind: "file", name: f.name, isDir: f.isDir });
    }

    reconcile(list, entries, {
      key: (e: DirEntry) => (e.kind === "up" ? "__up__" : `file:${e.name}`),
      mount: (e: DirEntry) => (e.kind === "up" ? upRow() : entryRow(e.name, e.isDir)),
      update: (row, e: DirEntry) => {
        if (e.kind === "file") {
          const check = row.querySelector<HTMLInputElement>(`.${FB_CHECK}`);
          if (check !== null) {
            check.checked = selected.has(e.name);
          }
        }
      },
    });

    if (sorted.length === 0 && currentPath === FB_ROOT) {
      list.appendChild(listNotice("Empty"));
    }
  });
}

function upRow(): HTMLDivElement {
  const icon = el("span", { className: "fb-icon" }, iconEl(FILE_ICONS["folder"] ?? ""));

  const nameSpan = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, "..");
  nameSpan.addEventListener("click", () => {
    currentPath = parentPath(currentPath);
    selected.clear();
    loadDir();
    syncAttachBtn();
  });

  return el("div", { className: FB_ROW }, icon, nameSpan) as HTMLDivElement;
}

function entryRow(name: string, isDir: boolean): HTMLDivElement {
  const check = el("input", {
    type: "checkbox",
    className: FB_CHECK,
    checked: selected.has(name),
    "aria-label": `Select ${name}`,
  }) as HTMLInputElement;
  check.addEventListener("change", () => {
    if (check.checked) {
      selected.add(name);
    } else {
      selected.delete(name);
    }
    syncAttachBtn();
  });

  const icon = el(
    "span",
    { className: "fb-icon" },
    iconEl(isDir ? (FILE_ICONS["folder"] ?? "") : fileIcon(name, false)),
  );

  const label = el("span", { className: `${FB_NAME} ${FB_NAME_LINK}` }, name);
  label.addEventListener("click", () => {
    if (isDir) {
      currentPath = joinPath(currentPath, name);
      selected.clear();
      loadDir();
      syncAttachBtn();
    } else {
      check.checked = !check.checked;
      if (check.checked) {
        selected.add(name);
      } else {
        selected.delete(name);
      }
      syncAttachBtn();
    }
  });

  return el("div", { className: FB_ROW }, check, icon, label) as HTMLDivElement;
}
