// The server's `notification` frame: the in-page twin of its Web Push, words and subject
// included. Every page gets it; the server's notice ledger names the one page per profile that
// shows it (`show_in`).

import { onSSE } from "../bus.js";
import { showNotice } from "../notify.js";
import { pageID } from "../page-view.js";

onSSE("notification", (_chatID, p) => {
  if (p.show_in?.includes(pageID()) === true) {
    showNotice(p);
  }
});
