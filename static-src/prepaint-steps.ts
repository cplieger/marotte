// The visual per-device settings applied before first paint. Every import here must stay a leaf
// (storage reads, root attribute writes): this graph is bundled into the blocking /prepaint.js, so
// pulling in a DOM-registry or feature module would put the app in front of the first frame.

import { createTheme } from "@cplieger/ui-primitives/theme";
import { cachedTheme } from "./device-view.js";
import { LS_UI_STATE_KEY, THEME_ATTRIBUTE } from "./ls-keys.js";
import { applyStoredTier } from "./pointer-tier.js";
import { applyStoredShellPanel } from "./shell-height.js";
import { applyStoredSidebarWidth } from "./sidebar-width.js";

/** Paint the cached theme choice, resolved the way the live controller resolves it: the same
 *  `createTheme` with the read `settings.ts` answers before its first load, disposed at once so
 *  no OS-preference listener outlives the page's first frame. */
export function applyStoredTheme(): void {
  try {
    createTheme({
      storageKey: LS_UI_STATE_KEY,
      storage: { get: () => cachedTheme(), set: () => undefined },
      attribute: THEME_ATTRIBUTE,
    }).dispose();
  } catch {
    // Every stylesheet token keys off `data-theme`, so even a failed controller leaves one set.
    const g = globalThis as { readonly matchMedia?: (q: string) => MediaQueryList };
    const dark = g.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
    document.documentElement.setAttribute(THEME_ATTRIBUTE, dark ? "dark" : "light");
  }
}

/** Theme first: the stylesheet resolves its tokens against `data-theme`. */
export const PREPAINT_STEPS: readonly (() => void)[] = [
  applyStoredTheme,
  applyStoredSidebarWidth,
  applyStoredTier,
  applyStoredShellPanel,
];

/** Run every step, each isolated from the others' failures. Logs nothing: a missing or corrupt
 *  blob is an ordinary state, and each step already falls back to its default for it. */
export function runPrepaint(steps: readonly (() => void)[] = PREPAINT_STEPS): void {
  for (const step of steps) {
    try {
      step();
    } catch {
      // One step's failure must not cost the others.
    }
  }
}
