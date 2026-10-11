import type { Route } from "./route-path.js";
import { knownConfigDir } from "./versions.js";
import { relBeneath, workspaceRootOrDefault } from "./workspace.js";

/** internal/preview's maxFolderBytes. */
const MAX_FOLDER_BYTES = 512;

const folderBytes = new TextEncoder();

/** Whether the preview server would grant `path` (internal/preview `openPageFolder`): an
 *  absolute, clean `.html`/`.htm` path in its own folder beneath the workspace root, with no
 *  component starting with `.` or holding a `\`, in a folder that neither is nor encloses the
 *  server's config directory. A folder inside that directory is left to the server's deny list,
 *  which the client does not hold, so a grant there can still be refused.
 *  `internal/preview/testdata/page-shapes.json` holds both sides to the same answers. */
export function isPreviewablePage(path: string): boolean {
  if (!/\.html?$/i.test(path) || path.includes("\0")) {
    return false;
  }
  const rel = relBeneath(workspaceRootOrDefault(), path);
  if (rel === null) {
    return false;
  }
  const parts = rel.split("/");
  // One component is a page directly in the root, which would grant the whole workspace.
  if (parts.length < 2 || parts.some((p) => p === "" || p.startsWith(".") || p.includes("\\"))) {
    return false;
  }
  const folder = path.slice(0, path.lastIndexOf("/"));
  const configDir = knownConfigDir();
  if (folder === configDir || relBeneath(folder, configDir) !== null) {
    return false;
  }
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
