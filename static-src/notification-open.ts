// The one place a notification click becomes navigation. Below both notify.ts and
// handlers/push-message.ts, because handler -> feature is the allowed direction.

import type { Route } from "./route-path.js";
import { pushTargetRoute, type PushTarget } from "./push-subject.js";

let opener: ((route: Route) => void) | null = null;

/** Register the navigation a notification click performs. Called once, from the
 *  composition root, BEFORE the message listener is installed; last registration wins. */
export function registerNotificationOpener(open: (route: Route) => void): void {
  opener = open;
}

/** Navigate to what a notification is about. THROWS when nothing is registered: the
 *  wiring is one line in the composition root, and a silent dead click is the defect
 *  this seam exists to remove. */
export function openPushTarget(target: PushTarget): void {
  if (opener === null) {
    throw new Error(
      "notification-open: no opener registered; the composition root must call " +
        "registerNotificationOpener before initPushMessages",
    );
  }
  opener(pushTargetRoute(target));
}

/** Drop the registration. Test isolation only; production never unregisters. */
export function _resetNotificationOpenerForTest(): void {
  opener = null;
}
