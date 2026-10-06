// The workspace root (stated by the server on the SSE `connected` handshake; never hardcoded) and
// the ONE conversion between agent-RELATIVE paths and the file surface's ABSOLUTE ones. A copied
// rule once keyed git-status letters in the wrong space, so they silently never matched.

/** Absolute workspace root, or "" until the handshake arrives. */
let root = "";

/** Consumers that derive something from the root and must recompute when it
 *  lands. See onWorkspaceRoot for why this exists at all. */
const listeners = new Set<() => void>();

/** Record the workspace root from the `connected` handshake; a no-op when unchanged (every
 *  reconnect), so index-rebuilding listeners are not woken. */
export function setWorkspaceRoot(abs: string): void {
  const next = cleanRoot(abs);
  if (next === root) {
    return;
  }
  root = next;
  for (const fn of [...listeners]) {
    fn();
  }
}

/** The configured root may carry trailing slashes; "/" is the one clean root
 *  that ends in one. */
function cleanRoot(abs: string): string {
  const trimmed = abs.replace(/\/+$/, "");
  return trimmed === "" && abs !== "" ? "/" : trimmed;
}

/** `abs` relative to the clean root `base`, or null when it is not beneath it. */
export function relBeneath(base: string, abs: string): string | null {
  const prefix = base === "/" ? "/" : `${base}/`;
  return abs.startsWith(prefix) ? abs.slice(prefix.length) : null;
}

/** The absolute workspace root, or "" when the handshake has not landed. */
export function workspaceRoot(): string {
  return root;
}

/** Subscribe to the root becoming known; returns an unsubscribe. The handshake and consumers race
 *  (`pollAction` ticks synchronously on start), and an index built before the root is keyed in the
 *  wrong space until the next poll. */
export function onWorkspaceRoot(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Resolve a path into the ABSOLUTE space: absolute input is returned unchanged, relative is joined
 *  onto the root. Before the handshake the input is returned as-is, never rewritten. */
export function absPath(path: string): string {
  if (path === "" || path.startsWith("/") || root === "") {
    return path;
  }
  return root === "/" ? `/${path}` : `${root}/${path}`;
}

/** Strip the workspace root from an absolute path, giving the agent's RELATIVE form; unchanged when
 *  not under it (another mount, or root unknown). The separator test stops "/workspace-old/x". */
export function relToWorkspace(abs: string): string {
  if (root === "") {
    return abs;
  }
  return relBeneath(root, abs) ?? abs;
}

/** @internal Test seam: reset the root between cases. Listeners are neither cleared (module wiring
 *  a test may assert) nor notified (the workspace did not move). */
export function _resetForTest(): void {
  root = "";
}
