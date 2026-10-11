import { $ } from "./dom.js";
import { isRetentionEnabled, onRetentionChange } from "./retention.js";
import { openTab } from "./tabs.js";

/** Wire the sidebar's History button. A door, so it opens the tab and never closes it; hidden while
 *  retention is off, since no closed chat survives to list. */
export function initSidebarHistory(): void {
  $.historyBtn.addEventListener("click", () => {
    void openTab({ kind: "history" });
    // At once, as a chat row's tap does: `openTab` dismisses the drawer only after its round trip.
    $.sidebar.classList.remove("open");
  });
  onRetentionChange(() => {
    $.historyBtn.classList.toggle("hidden", !isRetentionEnabled());
  });
}
