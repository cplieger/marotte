import type { Route } from "./route-path.js";
import { relBeneath, workspaceRoot } from "./workspace.js";

/** The root before the handshake has named it; the container's default. */
const DEFAULT_ROOT = "/workspace";

/** internal/preview's maxFolderBytes. */
const MAX_FOLDER_BYTES = 512;

const folderBytes = new TextEncoder();

/** Whether the preview server would grant `path` (internal/preview `openPageFolder`): an
 *  absolute, clean `.html`/`.htm` path in its own folder beneath the workspace root, with no
 *  component starting with `.` or holding a `\`. `internal/preview/testdata/page-shapes.json`
 *  holds both sides to the same answers. */
export function isPreviewablePage(path: string): boolean {
  if (!/\.html?$/i.test(path) || path.includes("\0")) {
    return false;
  }
  const rel = relBeneath(workspaceRoot() || DEFAULT_ROOT, path);
  if (rel === null) {
    return false;
  }
  const parts = rel.split("/");
  // One component is a page directly in the root, which would grant the whole workspace.
  if (parts.length < 2 || parts.some((p) => p === "" || p.startsWith(".") || p.includes("\\"))) {
    return false;
  }
  const folder = path.slice(0, path.lastIndexOf("/"));
  return folderBytes.encode(folder).length <= MAX_FOLDER_BYTES;
}

/** Where a click on the file at `abs` lands: a previewable page with no line is its Preview tab,
 *  anything else the editor (at `line`), since a line asks for the source. */
export function fileOpenRoute(
  abs: string,
  line?: number,
): Extract<Route, { kind: "file" | "web" }> {
  if (line === undefined) {
    return isPreviewablePage(abs) ? { kind: "web", path: abs } : { kind: "file", path: abs };
  }
  return { kind: "file", path: abs, line };
}
