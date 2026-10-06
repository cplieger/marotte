// ---------------------------------------------------------------------------
// URL safety utilities.
// ---------------------------------------------------------------------------

import { isViewableImage } from "./file-extensions.js";
import type { Route } from "./route-path.js";
import { UPLOADS_DIR } from "./upload-policy.js";

/** URL safety predicate for a rendered href/src: http, https and mailto are the only absolute
 *  schemes; scheme-less resolves against the document. Strips every C0 control THEN trims, at least
 *  what the WHATWG URL parser strips before reading a scheme (else `\x01javascript:` stays live). */
export function isSafeUrl(url: string): boolean {
  const cleaned = url
    // eslint-disable-next-line no-control-regex
    .replace(/[\x00-\x1f]/g, "")
    .trim()
    .toLowerCase();
  const scheme = /^[a-z][a-z0-9+.-]*:/.exec(cleaned)?.[0];
  return scheme === undefined || scheme === "http:" || scheme === "https:" || scheme === "mailto:";
}

const EXFIL_QUERY_MIN_LEN = 200;
const EXFIL_PERCENT_RE = /%[0-9A-Fa-f]{2}(?:%[0-9A-Fa-f]{2}){20,}/i;
const EXFIL_CREDENTIAL_RE =
  /(?:(?:AKIA|ASIA)[A-Z0-9]{16}|(?:ssh-rsa|ssh-ed25519)[\s+%]|BEGIN[\s+%](?:RSA|DSA|EC|OPENSSH)[\s+%]PRIVATE[\s+%]KEY|xox[bpas]-[0-9a-zA-Z-]+|gh[pousr]_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{22,})/i;
// `=` counts only as trailing padding, so `name=` plus a short id does not fuse into one run.
const EXFIL_B64_RE = /[A-Za-z0-9+/]{40,}={0,2}|[A-Za-z0-9+/]{39}=|[A-Za-z0-9+/]{38}==/i;

/** Whether a link that LEAVES the origin carries an encoded payload in its query or fragment: a
 *  200+ char query, 21+ consecutive %XX escapes, a 40+ base64 run, or a credential marker. A
 *  heuristic ceiling: no host or shape waiver, since a waived shape is an injected prompt's channel.
 *  Path and subdomain are untested, a known hole. Normalizes as `isSafeUrl`. */
export function exfilShaped(url: string): boolean {
  const cleaned = url
    // eslint-disable-next-line no-control-regex
    .replace(/[\x00-\x1f]/g, "")
    .trim();
  const scheme = /^[a-z][a-z0-9+.-]*:/i.exec(cleaned)?.[0].toLowerCase();
  if (scheme === "mailto:" || (scheme === undefined && !/^[/\\]{2}/.test(cleaned))) {
    return false;
  }
  const at = cleaned.search(/[?#]/);
  if (at < 0) {
    return false;
  }
  const query = cleaned.slice(at + 1);
  return (
    query.length >= EXFIL_QUERY_MIN_LEN ||
    EXFIL_PERCENT_RE.test(query) ||
    EXFIL_CREDENTIAL_RE.test(query) ||
    EXFIL_B64_RE.test(query)
  );
}

/** The route that serves a workspace file's BYTES (`/api/file` is JSON and 415s binaries), through
 *  the mount's confined `os.Root` with `Content-Disposition: attachment`, a SECURITY control: an
 *  `.svg` is served as `image/svg+xml`, script-capable if NAVIGATED to. Never hand this URL to an
 *  anchor, an `<iframe>` or `window.open`. */
export function fileDownloadURL(path: string): string {
  return `/api/file/download?path=${encodeURIComponent(path)}`;
}

/** The roots the byte route can serve (granted browse mounts, `browseRoots` in
 *  internal/composition/config.go); outside them the SPA answers index.html, a broken image.
 *  `/config` is deliberately absent: it holds the chat store, MCP secrets and tool state. */
const SERVED_ROOTS = ["/workspace/", `${UPLOADS_DIR}/`] as const;

/** Is this an absolute path the byte route can serve? */
function isServedPath(path: string): boolean {
  return SERVED_ROOTS.some((root) => path.startsWith(root));
}

/** Rewrite an image `src` under a served root to the byte-serving route, so an agent's
 *  `![shot](/workspace/out/shot.png)` renders instead of hitting the SPA fallback. Anything outside
 *  SERVED_ROOTS, or without an image extension, is returned untouched. */
export function rewriteServedImageSrc(src: string): string {
  const path = servedPath(src);
  if (path === null || !isViewableImage(path)) {
    return src;
  }
  return fileDownloadURL(path);
}

/** The file path a markdown destination names under SERVED_ROOTS, else null. Percent-decoded once.
 *  A `.` or `..` segment answers null: `/workspace/../config/x` passes the prefix test. */
export function servedPath(dest: string): string | null {
  const raw = dest.trim();
  let path = raw;
  try {
    path = decodeURIComponent(raw);
  } catch {
    // A bare `%` is a literal character of the filename.
  }
  if (!isServedPath(path) || path.split("/").some((seg) => seg === "." || seg === "..")) {
    return null;
  }
  return path;
}

/** The editor route a link destination names, else null. A `#L<line>`
 *  fragment, or a `:line[:col]` suffix when there is no fragment, becomes the
 *  line; any other fragment is dropped, since the editor has no anchors.
 *
 *  A query or a trailing `/` answers null: neither names a file. */
export function servedFileRoute(dest: string): Extract<Route, { kind: "file" }> | null {
  const trimmed = dest.trim();
  const hashAt = trimmed.indexOf("#");
  const beforeHash = hashAt < 0 ? trimmed : trimmed.slice(0, hashAt);
  const decoded = beforeHash.includes("?") ? null : servedPath(beforeHash);
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
