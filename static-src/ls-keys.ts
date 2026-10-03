// Single source of truth for the localStorage keys and the theme attribute used
// across modules, prepaint.js included: it bundles this module rather than
// repeating the literals.

/** The blob holding everything about THIS SCREEN: the active tab, the two shell
 *  fields, the sidebar width, the pointer fields, and the theme's pre-paint
 *  cache. Owned end to end by `device-view.ts`, which is the only writer — every
 *  write is a read-modify-write of one JSON document, so a second writer drops
 *  whatever landed between its read and its write.
 *
 *  The name is a leftover from when this key held the whole UI arrangement, and
 *  it is KEPT deliberately: nothing is migrated, and renaming it would silently
 *  reset every reader's shell height and theme. The arrangement itself is
 *  server-owned: the tab SET is its own collection (`internal/tabs`, projected by
 *  `tabs.ts`), and the theme and browser path are `config.json` keys. */
export const LS_UI_STATE_KEY = "marotte.ui-state";

/** The `<html>` attribute the theme paints, beside the key because both
 *  `createTheme` constructions (`theme.ts`, `prepaint-steps.ts`) pass the pair:
 *  two literals could drift and paint the first frame on a different attribute. */
export const THEME_ATTRIBUTE = "data-theme";

/** Per-chat, per-turn fold overrides: which turns THIS reader has opened or
 *  folded by hand.
 *
 *  Per-device rather than in the server-owned arrangement, and that is a
 *  reversal: it used to be `ui-state.turn_folds`. A fold is a DISCLOSURE state,
 *  which the companion audit's table files as correctly the viewer's ("which
 *  sections this reader has open"), and sharing it reproduced the very defect
 *  that moved the arrangement server-side — a fold on one screen rearranged a
 *  transcript someone else was reading. */
export const LS_TURN_FOLDS_KEY = "marotte.turn-folds";

/** Per-chat dismissed banner codes: which notices THIS reader has acknowledged.
 *
 *  Per-device for the fold's reason, one step stronger: an acknowledgement is
 *  the viewer's (web-terminal's rule verbatim), so a phone dismissing a banner
 *  must not silence the desktop. It used to be `ui-state.dismissed_banners`. */
export const LS_DISMISSED_BANNERS_KEY = "marotte.dismissed-banners";

/** Whether THIS DEVICE has already had its notification-permission ask.
 *
 *  Per-device because browser permission is per-origin-per-device: a grant on the
 *  desktop says nothing about the phone, so a server-side flag would answer for the
 *  wrong machine. It is written by every door the question can be answered through —
 *  the automatic ask raising the prompt, the Settings toggle going on (which raises
 *  it too), and the Settings toggle going OFF, which is a refusal.
 *
 *  That last writer is what the marker exists for. `notifications_enabled` reaches
 *  the client as a resolved boolean, so "never opted in" and "switched off on
 *  purpose" are one value there, and without the marker a later grant would quietly
 *  reverse the refusal. See `notify-ask.ts`. */
export const LS_NOTIFY_ASK_KEY = "marotte.notify-ask";

/** Per-page preview width picks: which viewport THIS screen chose for each
 *  previewed page. Per-device because a width is a property of the screen in
 *  front of the reader, the same reason `shell_h` is. */
export const LS_WEB_VIEWPORT_KEY = "marotte.web-viewport";

/** Every key above, so a sign-out can drop them without naming them one by one.
 *
 *  Here rather than at the three owning modules because this file is by
 *  construction the complete list: a fourth key declared above joins the sweep, and
 *  a key added anywhere else is already a defect this file exists to prevent. The
 *  in-MEMORY halves are not this file's — `fold-state.ts` caches its document and is
 *  reset beside this call (`boot.ts` `forgetDeviceState`). */
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
