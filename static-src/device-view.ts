// The per-device localStorage fields; the ONE owner of `marotte.ui-state`. Every write is a read-modify-write of one
// blob, so a second writer drops fields. The key is never migrated. prepaint.js reads it through these readers.

import { LS_UI_STATE_KEY } from "./ls-keys.js";

/** The recorded theme choice; "system" (follow the OS) is a value, not an absence. */
export type ThemeChoice = "dark" | "light" | "system";

/** Which pointer this screen is driven by. "coarse" is a finger or a pen,
 *  "fine" a mouse, a trackpad or a stylus with a cursor. */
export type PointerTier = "fine" | "coarse";

/** The four fields, as one record. Read together because they are stored
 *  together; written one at a time because that is how they change. */
interface DeviceView {
  active_view: string;
  shell_open: boolean;
  shell_h: number;
  sidebar_w: number;
}

function empty(): DeviceView {
  return { active_view: "", shell_open: false, shell_h: 0, sidebar_w: 0 };
}

function validLength(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) && v >= 0 ? v : null;
}

/** The whole blob, or `{}`. Never throws: storage can be disabled. */
function readBlob(): Record<string, unknown> {
  try {
    const raw = localStorage.getItem(LS_UI_STATE_KEY);
    if (raw === null) {
      return {};
    }
    const parsed: unknown = JSON.parse(raw);
    return typeof parsed === "object" && parsed !== null ? (parsed as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

/** Merge `patch` into the blob: the one read-modify-write. */
function writeBlob(patch: Record<string, unknown>): void {
  try {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ ...readBlob(), ...patch }));
  } catch {
    // Quota, or storage disabled. Nothing here is recoverable state.
  }
}

/** Every field validated: an invalid one falls back to its default while valid siblings are kept. */
export function loadDeviceView(): DeviceView {
  const o = readBlob();
  const e = empty();
  return {
    active_view: typeof o["active_view"] === "string" ? o["active_view"] : e.active_view,
    shell_open: typeof o["shell_open"] === "boolean" ? o["shell_open"] : e.shell_open,
    shell_h: validLength(o["shell_h"]) ?? e.shell_h,
    sidebar_w: validLength(o["sidebar_w"]) ?? e.sidebar_w,
  };
}

/** Which tab this screen was last looking at. "" when nothing is recorded. */
export function activeView(): string {
  return loadDeviceView().active_view;
}

export function setActiveView(id: string): void {
  writeBlob({ active_view: id });
}

/** Whether the terminal panel was showing. */
export function shellOpen(): boolean {
  return loadDeviceView().shell_open;
}

export function setShellOpen(open: boolean): void {
  writeBlob({ shell_open: open });
}

/** The dragged panel height in px; 0 means "the CSS default" (16rem). */
export function shellHeight(): number {
  return loadDeviceView().shell_h;
}

export function setShellHeight(px: number): void {
  writeBlob({ shell_h: px });
}

/** The dragged sidebar width in px; 0 means the CSS default (the minimum). */
export function sidebarWidth(): number {
  return loadDeviceView().sidebar_w;
}

export function setSidebarWidth(px: number): void {
  writeBlob({ sidebar_w: px });
}

/** The cached theme choice or null; a cache of `config.json`'s value that never outranks it. */
export function cachedTheme(): ThemeChoice | null {
  const t = readBlob()["theme"];
  return t === "dark" || t === "light" || t === "system" ? t : null;
}

/** The last observed pointer tier or null: above the capability guess, below a stated choice (`pointer-tier.ts`). */
export function cachedPointerTier(): PointerTier | null {
  const t = readBlob()["pointer"];
  return t === "fine" || t === "coarse" ? t : null;
}

export function cachePointerTier(tier: PointerTier): void {
  writeBlob({ pointer: tier });
}

/** The tier the user chose, or null. A separate field so no input event can overturn it. */
export function pointerModeChoice(): PointerTier | null {
  const t = readBlob()["pointer_mode"];
  return t === "fine" || t === "coarse" ? t : null;
}

export function setPointerModeChoice(tier: PointerTier): void {
  writeBlob({ pointer_mode: tier });
}

/** Whether a coarse pointer ever drove this screen; sticky (it reveals the toggle). A non-boolean reads false. */
export function coarseEverSeen(): boolean {
  return readBlob()["pointer_coarse_seen"] === true;
}

export function markCoarseSeen(): void {
  writeBlob({ pointer_coarse_seen: true });
}

/** Refresh the cache for the next load's pre-paint; `null` clears it, so prepaint.js follows the OS. */
export function cacheTheme(theme: ThemeChoice | null): void {
  if (theme === null) {
    const o = readBlob();
    delete o["theme"];
    try {
      localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify(o));
    } catch {}
    return;
  }
  writeBlob({ theme });
}
