// Where lazily loaded pages hand Ctrl-F their entry point. A leaf with no imports, so the dispatcher can read it without
// loading those pages. The version signal makes registration reactive.

import { signal, touch } from "@cplieger/reactive";

/** Search reaches past what is on screen; filter narrows loaded rows. Decides glyph and wording. */
export type FindKind = "search" | "filter";

/** A page's answer to the chord and the magnifier: open, focused, kind (and optionally available). */
export interface PageFind {
  /** Open or refocus; `false` means declined, leaving the chord to native find. */
  open: () => boolean;
  toggle: () => void;
  /** Whether this page's find is open AND holding the caret. */
  focused: () => boolean;
  /** A function: a sub-tabbed page can differ per tab (the git view). */
  kind: () => FindKind;
  /** Whether the magnifier has a destination here; optional (the git view's Sources tab has none). */
  available?: () => boolean;
}

const registry = new Map<string, PageFind>();

/** Bumped on every registration, read by `pageFind`. */
const version = signal(0);

/** Register a page's find by tab kind. Idempotent: a page may call it on every mount. */
export function registerFind(kind: string, find: PageFind): void {
  const had = registry.get(kind);
  registry.set(kind, find);
  if (had !== find) {
    version.value = version.value + 1;
  }
}

/** The registered find for a tab kind; reads the version signal, so an effect re-runs on registration. */
export function pageFind(kind: string): PageFind | undefined {
  touch(version);
  return registry.get(kind);
}

/** @internal Test seam: drop every registration. */
// deadset:ignore DS1004 -- test seam: resets the find registrations and their version
export function _resetFindRegistry(): void {
  registry.clear();
  version.value = version.value + 1;
}
