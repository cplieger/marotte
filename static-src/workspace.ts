// The workspace root (stated by the server on the SSE `connected` handshake; never hardcoded) and
// the ONE conversion between agent-RELATIVE paths and the file surface's ABSOLUTE ones. A copied
// rule once keyed git-status letters in the wrong space, so they silently never matched.

/** Absolute workspace root, or "" until the handshake arrives. */
let root = "";

/** The container's default work dir (`KIRO_WORK_DIR` unset). */
const DEFAULT_ROOT = "/workspace";

/** Consumers that derive something from the root and must recompute when it
 *  lands. See onWorkspaceRoot for why this exists at all. */
const listeners = new Set<() => void>();

/** A rendered surface that decided something from the root, held weakly and re-decided at every root change. */
interface RootDependent {
  readonly target: WeakRef<object>;
  /** Answers false once the surface no longer needs following. */
  readonly redo: (target: object) => boolean;
}

let dependents: RootDependent[] = [];

/** The live count a sweep last left, so pruning dead entries costs amortised O(1) per registration. */
let sweptLength = 0;

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
  // A snapshot: a redo that re-renders registers its replacement, which already decided against this root.
  const current = dependents;
  dependents = [];
  for (const dep of current) {
    const target = dep.target.deref();
    if (target !== undefined && dep.redo(target)) {
      dependents.push(dep);
    }
  }
  sweptLength = dependents.length;
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

/** The workspace root, or the container's default work dir before the handshake names it. */
export function workspaceRootOrDefault(): string {
  return root || DEFAULT_ROOT;
}

/** Re-decide `target` at every later root change by `redo(target)`, which answers whether to keep following it.
 *  `target` is held weakly and `redo` must not capture it: an element a re-render dropped is then collected and its
 *  entry pruned. A surface rendered before the handshake is the common case, since the handshake and the first
 *  paint race; a server restarted under another work dir is the rest. */
export function followRoot<T extends object>(target: T, redo: (target: T) => boolean): void {
  if (dependents.length >= 2 * Math.max(sweptLength, 128)) {
    dependents = dependents.filter((dep) => dep.target.deref() !== undefined);
    sweptLength = dependents.length;
  }
  dependents.push({ target: new WeakRef(target), redo: redo as (target: object) => boolean });
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

/** @internal Test seam: how many surfaces are followed, collected ones included until a sweep drops them. */
export function _followedCountForTest(): number {
  return dependents.length;
}

/** @internal Test seam: reset the root between cases. Listeners are neither cleared (module wiring
 *  a test may assert) nor notified (the workspace did not move); followed surfaces are dropped with
 *  the case that rendered them. */
export function _resetForTest(): void {
  root = "";
  dependents = [];
  sweptLength = 0;
}
