// The server's `notification` frame: the in-page twin of its Web Push, words and subject
// included, delivered by agent-finished-cue.ts.

import { onSSE } from "../bus.js";
import { deliverNotification } from "../agent-finished-cue.js";

onSSE("notification", (chatID, p) => {
  deliverNotification(p.chat_id ?? chatID, p);
});
