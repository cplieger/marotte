import { $ } from "./dom.js";
import { showHistoryView } from "./tabs.js";

/** Wire the History row at the foot of the sidebar's sections: a destination, so a second press
 *  keeps History. */
export function initSidebarHistory(): void {
  $.historyBtn.addEventListener("click", () => {
    showHistoryView();
  });
}
