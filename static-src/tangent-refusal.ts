// Whether a tangent can start, and the sentence saying why not, for every door that starts one: Chat options,
// `/tangent` and the selection popover's "Ask in a tangent".

import { isEmptyChat } from "./store.js";
import type { Session } from "./types.js";

export const NO_CONVERSATION_TANGENT = "Send a message first, then start a tangent from it";

/** Why a tangent cannot start off `session`, or null when it can. A chat with no conversation has nothing to fork
 *  server-side (`errForkParentUnknown`), and the failure would arrive after the sub-tab opened. */
export function tangentRefusal(session: Session | undefined): string | null {
  return isEmptyChat(session) ? NO_CONVERSATION_TANGENT : null;
}
