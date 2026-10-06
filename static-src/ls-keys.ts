// The localStorage keys and theme attribute shared across modules; prepaint.js bundles this module.

/**
 * Everything about this screen: active tab, shell fields, sidebar width, pointer fields, the theme's pre-paint cache.
 * `device-view.ts` is the only writer (read-modify-write of one JSON document). The name is kept on purpose: renaming
 * would reset every reader's shell height and theme.
 */
export const LS_UI_STATE_KEY = "marotte.ui-state";

/** The `<html>` attribute the theme paints, beside the key because both `createTheme` constructions pass the pair. */
export const THEME_ATTRIBUTE = "data-theme";

/**
 * Per-chat, per-turn fold overrides by this reader. Per-device: a fold is a disclosure state, and sharing it let one
 * screen rearrange a transcript someone else was reading.
 */
export const LS_TURN_FOLDS_KEY = "marotte.turn-folds";

/**
 * Per-chat dismissed banner codes. Per-device: an acknowledgement is the viewer's, so a phone dismissing must not
 * silence the desktop.
 */
export const LS_DISMISSED_BANNERS_KEY = "marotte.dismissed-banners";

/**
 * Whether this device has had its notification-permission ask. Per-device because permission is per origin per
 * device. Written by every answer, including the Settings toggle going off: `notifications_enabled` cannot tell "never
 * opted in" from "switched off". See `notify-ask.ts`.
 */
export const LS_NOTIFY_ASK_KEY = "marotte.notify-ask";

/** Per-page preview width picks for this screen; a width is a property of the screen. */
export const LS_WEB_VIEWPORT_KEY = "marotte.web-viewport";

/**
 * Every key above, so a sign-out drops them all; a key declared here joins the sweep. The in-memory halves are reset
 * beside this call (`boot.ts` `forgetDeviceState`).
 */
export function clearDeviceKeys(): void {
  for (const key of [
    LS_UI_STATE_KEY,
    LS_TURN_FOLDS_KEY,
    LS_DISMISSED_BANNERS_KEY,
    LS_NOTIFY_ASK_KEY,
    LS_WEB_VIEWPORT_KEY,
  ]) {
    try {
      localStorage.removeItem(key);
    } catch {
      /* a storage-denied browser has nothing to clear */
    }
  }
}
