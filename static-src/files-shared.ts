import { apiGet } from "./api-client.js";
import { el } from "@cplieger/reactive";

export const FB_ROW = "fb-row";
export const FB_NAME = "fb-name";
export const FB_NAME_LINK = "fb-name-link";
export const FB_CHECK = "fb-check";
export const FB_META = "fb-meta";

export interface FileEntry {
  name: string;
  isDir: boolean;
  size: number;
  mode: string;
  modTime: number;
}

interface DirListing {
  files: FileEntry[];
  writable: boolean;
  error?: string;
}

/** Per-caller abort state for fetchDir; each caller passes its own holder so they cannot abort each other. */
export interface FetchDirOpts {
  controllerHolder: { current: AbortController | null };
}

/** Fetch a directory listing; on failure an empty listing with `error` set. A newer request cancels the caller's previous one. */
export async function fetchDir(path: string, opts: FetchDirOpts): Promise<DirListing> {
  const holder = opts.controllerHolder;
  holder.current?.abort();
  holder.current = new AbortController();
  const { signal } = holder.current;
  try {
    const d = await apiGet<{ files?: FileEntry[]; writable?: boolean; error?: string }>(
      `/api/files?path=${encodeURIComponent(path)}`,
      signal,
    );
    if (signal.aborted) {
      return { files: [], writable: false, error: "stale" };
    }
    if (d === null) {
      return { files: [], writable: false, error: "fetch failed" };
    }
    if (d.error !== undefined) {
      return { files: [], writable: false, error: d.error };
    }
    return { files: d.files ?? [], writable: d.writable ?? false };
  } catch {
    if (signal.aborted) {
      return { files: [], writable: false, error: "stale" };
    }
    return { files: [], writable: false, error: "fetch failed" };
  }
}

/** Sort directory entries: directories first, then alphabetical by name. */
export function sortEntries<T extends { name: string; isDir: boolean }>(entries: T[]): T[] {
  return [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) {
      return a.isDir ? -1 : 1;
    }
    return a.name.localeCompare(b.name);
  });
}

/** Wire an editable path input (click to edit, Enter/Escape/blur). `onNavigate` gets the path through `normalizeDirPath`. */
export function initEditablePath(
  input: HTMLInputElement,
  opts: {
    onNavigate: (path: string) => void;
    getCurrentPath: () => string;
  },
): void {
  input.addEventListener("click", () => {
    if (!input.readOnly) {
      return;
    }
    input.readOnly = false;
    input.select();
  });
  input.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      input.readOnly = true;
      opts.onNavigate(normalizeDirPath(input.value));
      input.blur();
    } else if (e.key === "Escape") {
      input.readOnly = true;
      input.value = opts.getCurrentPath();
      input.blur();
    }
  });
  input.addEventListener("blur", () => {
    input.readOnly = true;
    input.value = opts.getCurrentPath();
  });
}

export function formatSize(bytes: number): string {
  if (bytes < 1024) {
    return `${String(bytes)} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(1)} KB`;
  }
  if (bytes < 1024 * 1024 * 1024) {
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}

export function formatDate(ms: number): string {
  const d = new Date(ms);
  return (
    d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) +
    " " +
    d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
  );
}

/** The root listing: the synthetic list of granted mounts `/api/files` answers for "/". Every child path composes from it. */
export const FB_ROOT = "/";

/** Join a listing's path with one entry name; container-absolute in, container-absolute out. */
export function joinPath(base: string, name: string): string {
  return `${base.replace(/\/+$/, "")}/${name}`;
}

/** One level up, bottoming out at `FB_ROOT`. */
export function parentPath(p: string): string {
  const parts = p.split("/").filter((s) => s !== "");
  parts.pop();
  return parts.length === 0 ? FB_ROOT : `/${parts.join("/")}`;
}

/**
 * The one door into the path space for an outside path (persisted `fb_path`, a `/files/<path>` deep link, typed
 * text), so `currentPath` is absolute by construction.
 */
export function normalizeDirPath(raw: string): string {
  // Interior runs collapse too, so `/files/workspace//x` cannot become a second tab ref.
  const trimmed = raw.trim().replace(/\/+/g, "/").replace(/^\//, "").replace(/\/$/, "");
  return trimmed === "" || trimmed === "." ? FB_ROOT : `/${trimmed}`;
}

/** Build an error row element safely (no innerHTML with user content). */
export function errorRow(msg: string, onRetry?: () => void): HTMLDivElement {
  const row = el(
    "div",
    { className: FB_ROW },
    el("span", { className: FB_META }, msg),
  ) as HTMLDivElement;
  if (onRetry !== undefined) {
    const btn = el("button", { type: "button", className: "btn-small" }, "Retry");
    btn.addEventListener("click", onRetry);
    row.appendChild(btn);
  }
  return row;
}
