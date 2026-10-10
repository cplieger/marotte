// Whether a notification's own chat or run is the tab the reader is looking at: the one case
// that shows nothing. The same rule as web-terminal-ui's `shouldNotify`, keyed on the subject.

import { getActiveTabId, tabIdFor } from "./tabs.js";
import type { PushTarget } from "./push-subject.js";

/** The open tab `target` names, "" when it names none or that tab is closed. */
function tabOf(target: PushTarget): string {
  switch (target.kind) {
    case "chat":
      return tabIdFor("chat", target.chatID);
    case "run":
      return tabIdFor("run", target.workflowID);
    case "pr":
    case "workspace":
      return "";
  }
}

/** True only while the page is visible AND `target`'s tab is the active one. */
export function targetOnScreen(target: PushTarget): boolean {
  if (document.visibilityState !== "visible") {
    return false;
  }
  const tab = tabOf(target);
  return tab !== "" && tab === getActiveTabId();
}
