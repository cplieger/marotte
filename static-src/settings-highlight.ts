// Deep-link to one Settings CONTROL: `?highlight=<element-id>`.

import { openSettingsView } from "./tabs.js";
import { flashTarget } from "./flash-target.js";
import { forceSettingsTab } from "./settings-tabs.js";
import { pushRoute } from "./router.js";
import type { SettingsTab } from "./route-path.js";

/** The `?highlight=` value this page load carried, read once at import time (see the mechanic
 *  above). Consumed at most once. */
let pendingTarget: string | null = readTargetFromURL();

function readTargetFromURL(): string | null {
  try {
    const raw = new URLSearchParams(location.search).get("highlight");
    return raw === null || raw.trim() === "" ? null : raw.trim();
  } catch {
    // A malformed search string is not worth failing a boot over.
    return null;
  }
}

/** Scroll a Settings control into view and flash a ring around it. */
export function highlightControl(id: string): void {
  // An empty id means the CALLER had no target, which `flashTarget` would spend its whole frame
  // budget failing to find.
  if (id === "") {
    return;
  }
  flashTarget(() => document.getElementById(id));
}

/** Open Settings on `tab` and highlight `controlID`. The in-app form of the deep link: what a
 *  message naming a setting calls. */
export function openSetting(tab: SettingsTab, controlID: string): void {
  // Awaited through the promise rather than detached, because everything below addresses the panel
  // the open produces — and the highlight is a DOM write against a view that has to exist first.
  void openSettingsView(tab).then(() => {
    applySetting(tab, controlID);
  });
}

/** Swap the panel, push the URL and highlight. */
function applySetting(tab: SettingsTab, controlID: string): void {
  // Swaps the panel and fires the tab's lazy loader; the URL is pushed here rather than by
  // forceSettingsTab, which is the router's own callee.
  forceSettingsTab(tab);
  pushRoute({ kind: "settings", tab });
  highlightControl(controlID);
}

/** Fire the `?highlight=` this page load carried, if any. Called from the router's settings
 *  branch once the panel's data loader has run. One-shot: a later popstate back to the same URL
 *  must not re-flash a control the reader has already been shown. */
export function flushURLHighlight(): void {
  const id = pendingTarget;
  if (id === null) {
    return;
  }
  pendingTarget = null;
  highlightControl(id);
}

/** Test-only: restore the module to its pre-boot state with a chosen target. */
export function _setPendingTargetForTest(id: string | null): void {
  pendingTarget = id;
}
