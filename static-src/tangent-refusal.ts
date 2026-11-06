// Whether a tangent can start off a chat, and the sentence saying why not.

import { isEmptyChat } from "./store.js";
import type { Session } from "./types.js";

export const NO_CONVERSATION_TANGENT = "Send a message first, then start a tangent from it";

/** Why a tangent cannot start off `session`, or null when it can. */
export function tangentRefusal(session: Session | undefined): string | null {
  return isEmptyChat(session) ? NO_CONVERSATION_TANGENT : null;
}
