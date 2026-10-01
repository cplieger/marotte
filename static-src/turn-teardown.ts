// The outcome-independent half of "a turn is no longer running". Three doors reach it —
// `turn_closed`, `BUS_RECONCILE` and a newest-page GET stating `live === false` — and each
// applies whatever else it knows itself.

import { setThinking, get, tabStatusFor } from "./store.js";
import { setTabStatus, tabIdFor } from "./tabs.js";
import { hasPendingDecision } from "./decision-dock.js";

/** Bring one chat's local turn state to rest. `setTabStatus` takes the opaque server-minted
 *  TAB id, so the subject is resolved rather than passed through. */
export function clearTurnState(chatID: string): void {
  setThinking(chatID, false);
  setTabStatus(tabIdFor("chat", chatID), tabStatusFor(get(chatID), hasPendingDecision(chatID)));
}
