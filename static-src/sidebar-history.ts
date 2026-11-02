import { $ } from "./dom.js";
import { isRetentionEnabled, onRetentionChange } from "./retention.js";
import { toggleHistoryView } from "./tabs.js";

/** Wire the sidebar's History button: a toggle like the toolbar's, hidden while retention is off, since no
 *  closed chat survives to list. */
export function initSidebarHistory(): void {
  $.historyBtn.addEventListener("click", () => {
    void toggleHistoryView();
    // At once, as a chat row's tap does: the toggle settles only after its round trip.
    $.sidebar.classList.remove("open");
  });
  onRetentionChange(() => {
    $.historyBtn.classList.toggle("hidden", !isRetentionEnabled());
  });
}
