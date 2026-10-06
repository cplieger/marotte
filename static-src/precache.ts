// The shell precache's two pure decisions: which manifest documents are usable, and which request
// paths the cache may answer.

/** The build-emitted asset list (cmd/bundle writePrecacheManifest). `stamp` moves when the set
 *  of names moves, and each name carries its own content hash; `assets` are root-relative paths,
 *  each already carrying its leading slash. */
export interface PrecacheManifest {
  readonly stamp: string;
  readonly assets: readonly string[];
}

/** The one name shape whose bytes its own name pins: esbuild's `chunks/[name]-[hash]`, the hash
 *  8 uppercase base32 characters. */
const CONTENT_HASHED_CHUNK = /^\/chunks\/[^/]+-[A-Z0-9]{8}\.js$/u;

/** Whether the shell cache may answer `pathname`. Content-hashed names only: the server marks
 *  `/app.js` and `/style.css` `no-cache` because a release replaces their bytes under those
 *  names, so a cache-first answer for either pairs a fresh index.html with the previous build's
 *  bundle. */
export function isShellPath(pathname: string): boolean {
  return CONTENT_HASHED_CHUNK.test(pathname);
}

/** A web preview's document or asset. The worker stays out of these entirely: a preview frame's
 *  load must not trigger a precache sync, and its bytes are never the worker's to cache. */
export function isPreviewPath(pathname: string): boolean {
  return pathname.startsWith("/preview/");
}

/** The manifest a document just served, or null when it is unusable. Every field is checked
 *  rather than cast — a half-written or foreign document must leave the existing cache alone
 *  rather than emptying it — and an asset name is rejected outright if it is absolute or climbs,
 *  so the caller cannot be talked into caching a path off the build's own output. */
export function parseManifest(d: unknown): PrecacheManifest | null {
  if (typeof d !== "object" || d === null) {
    return null;
  }
  const rec = d as Record<string, unknown>;
  const stamp = rec["stamp"];
  const assets = rec["assets"];
  if (typeof stamp !== "string" || stamp === "" || !Array.isArray(assets)) {
    return null;
  }
  const paths: string[] = [];
  for (const a of assets as unknown[]) {
    if (typeof a !== "string" || a === "" || a.startsWith("/") || a.includes("..")) {
      return null;
    }
    paths.push(`/${a}`);
  }
  return { stamp, assets: paths };
}
