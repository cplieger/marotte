import { apiGetOrError } from "./api-client.js";
import type { FileEntry } from "./files-shared.js";

/** `/api/files`'s code for a path that names a file; owned by `internal/filebrowse/list.go` `codeNotADirectory`. */
const NOT_A_DIRECTORY = "not_a_directory";

/** Shown when the request left and no answer came back; any server answer is shown in its own words. */
const UNREACHABLE = "Could not reach the server";

/**
 * One listing request's outcome. `stale`: a newer request on the same holder superseded it, so nothing may paint;
 * `not-dir`: the path names a file; `error`: the message to show.
 */
export type DirResult =
  | { kind: "ok"; files: FileEntry[]; writable: boolean }
  | { kind: "stale" }
  | { kind: "not-dir" }
  | { kind: "error"; message: string };

/** Per-caller abort state for fetchDir; each caller passes its own holder so they cannot abort each other. */
export interface FetchDirOpts {
  controllerHolder: { current: AbortController | null };
}

/** Fetch a directory listing. A newer request cancels the caller's previous one, which then answers `stale`. */
export async function fetchDir(path: string, opts: FetchDirOpts): Promise<DirResult> {
  const holder = opts.controllerHolder;
  holder.current?.abort();
  holder.current = new AbortController();
  const { signal } = holder.current;
  const r = await apiGetOrError<{ files?: FileEntry[]; writable?: boolean }>(
    `/api/files?path=${encodeURIComponent(path)}`,
    signal,
  );
  if (signal.aborted) {
    return { kind: "stale" };
  }
  if (r.ok) {
    // An empty 2xx is a broken route, not an empty folder.
    if (r.data === null) {
      return { kind: "error", message: `Empty answer from the server (HTTP ${String(r.status)})` };
    }
    return { kind: "ok", files: r.data.files ?? [], writable: r.data.writable ?? false };
  }
  if (r.status === 400 && r.code === NOT_A_DIRECTORY) {
    return { kind: "not-dir" };
  }
  if (r.status === 0) {
    if (r.code === "cancelled") {
      return { kind: "stale" };
    }
    // Only these two left the browser; `invalid` failed to build, so its own message says why.
    if (r.code === "network" || r.code === "timeout") {
      return { kind: "error", message: UNREACHABLE };
    }
  }
  return { kind: "error", message: r.error };
}
