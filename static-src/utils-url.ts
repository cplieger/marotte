// ---------------------------------------------------------------------------
// URL safety utilities.
// ---------------------------------------------------------------------------

import { isViewableImage } from "./file-extensions.js";
import type { Route } from "./route-path.js";
import { UPLOADS_DIR } from "./upload-policy.js";
import { additionalDirectories, workspaceRootOrDefault } from "./workspace.js";

/** URL safety predicate for a rendered href/src: http, https and mailto are the only absolute
 *  schemes; scheme-less resolves against the document. Strips every C0 control THEN trims, at least
 *  what the WHATWG URL parser strips before reading a scheme (else `\x01javascript:` stays live). */
export function isHttpMailtoOrRelativeURL(url: string): boolean {
  const cleaned = url
    // eslint-disable-next-line no-control-regex
    .replace(/[\x00-\x1f]/g, "")
    .trim()
    .toLowerCase();
  const scheme = /^[a-z][a-z0-9+.-]*:/.exec(cleaned)?.[0];
  return scheme === undefined || scheme === "http:" || scheme === "https:" || scheme === "mailto:";
}

/** The route that serves a workspace file's BYTES (`/api/file` is JSON and 415s binaries), through
 *  the mount's confined `os.Root` with `Content-Disposition: attachment`, a SECURITY control: an
 *  `.svg` is served as `image/svg+xml`, script-capable if NAVIGATED to. Never hand this URL to an
 *  anchor, an `<iframe>` or `window.open`. */
export function fileDownloadURL(path: string): string {
  return `/api/file/download?path=${encodeURIComponent(path)}`;
}

/** The roots a link routes to the byte route (elsewhere the SPA answers index.html): the workspace
 *  (unless it is `/`, which is never a mount), the uploads mount and the additional directories; never
 *  the config mount, which holds the chat store and credentials. The server's allow and deny lists decide access. */
function servedRoots(): readonly string[] {
  const root = workspaceRootOrDefault();
  return [...(root === "/" ? [] : [root]), UPLOADS_DIR, ...additionalDirectories()].map(
    (r) => `${r}/`,
  );
}

function isServedPath(path: string): boolean {
  return servedRoots().some((root) => path.startsWith(root));
}

/** Rewrite an image `src` under a served root to the byte-serving route, so an agent's
 *  `![shot](/workspace/out/shot.png)` renders instead of hitting the SPA fallback. Anything outside
 *  the served roots, or without an image extension, is returned untouched. */
export function rewriteServedImageSrc(src: string): string {
  const path = servedPath(src);
  if (path === null || !isViewableImage(path)) {
    return src;
  }
  return fileDownloadURL(path);
}

/** A markdown destination, trimmed and percent-decoded once; a malformed escape is kept literally. */
export function decodeDestination(dest: string): string {
  const raw = dest.trim();
  try {
    return decodeURIComponent(raw);
  } catch {
    // A bare `%` is a literal character of the filename.
    return raw;
  }
}

/** The file path a markdown destination names under a served root, else null. Percent-decoded
 *  once. A `.` or `..` segment answers null: `/workspace/../config/x` passes the prefix test. */
export function servedPath(dest: string): string | null {
  const path = decodeDestination(dest);
  if (!isServedPath(path) || path.split("/").some((seg) => seg === "." || seg === "..")) {
    return null;
  }
  return path;
}

/** The editor route a link destination names, else null. A `#L<line>`
 *  fragment, or a `:line[:col]` suffix when there is no fragment, becomes the
 *  line; any other fragment is dropped, since the editor has no anchors. A
 *  query is dropped too: left on the link, it would send the SPA to a chat.
 *
 *  A trailing `/` answers null: it names a directory, not a file. */
export function servedFileRoute(dest: string): Extract<Route, { kind: "file" }> | null {
  const trimmed = dest.trim();
  const hashAt = trimmed.indexOf("#");
  const beforeHash = hashAt < 0 ? trimmed : trimmed.slice(0, hashAt);
  const queryAt = beforeHash.indexOf("?");
  const decoded = servedPath(queryAt < 0 ? beforeHash : beforeHash.slice(0, queryAt));
  if (decoded === null) {
    return null;
  }
  let path = decoded;
  let digits: string | undefined;
  if (hashAt >= 0) {
    digits = /^#L(\d+)$/.exec(trimmed.slice(hashAt))?.[1];
  } else {
    const suffix = /:(\d+)(?::\d+)?$/.exec(decoded);
    if (suffix !== null) {
      path = decoded.slice(0, suffix.index);
      digits = suffix[1];
    }
  }
  if (path.endsWith("/")) {
    return null;
  }
  const line = lineNumber(digits);
  return line === undefined ? { kind: "file", path } : { kind: "file", path, line };
}

/** A positive line that `Number` holds exactly, else undefined. */
function lineNumber(digits: string | undefined): number | undefined {
  if (digits === undefined) {
    return undefined;
  }
  const n = Number(digits);
  return Number.isSafeInteger(n) && n > 0 ? n : undefined;
}
