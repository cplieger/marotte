import { $ } from "./dom.js";
import { toggleHistoryView } from "./tabs.js";

/** Wire the History row at the foot of the sidebar's sections: a toggle like the toolbar's. */
export function initSidebarHistory(): void {
  $.historyBtn.addEventListener("click", () => {
    void toggleHistoryView();
    // At once, as a chat row's tap does: the toggle settles only after its round trip.
    $.sidebar.classList.remove("open");
  });
}
