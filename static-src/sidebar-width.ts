import { sidebarWidth } from "./device-view.js";

const PREF = "--sidebar-w-pref";

/** Write the preferred width the `--sidebar-w` clamp reads, or remove it with null. */
export function writeSidebarPref(px: number | null): void {
  const root = document.documentElement.style;
  if (px === null) {
    root.removeProperty(PREF);
  } else {
    root.setProperty(PREF, `${String(px)}px`);
  }
}

/** Apply the stored width; a stored 0 (the minimum) writes nothing. */
export function applyStoredSidebarWidth(): void {
  const w = sidebarWidth();
  if (w > 0) {
    writeSidebarPref(w);
  }
}
