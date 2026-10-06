// Must this tab's view fetch on activation? Answered for all nine kinds off the digest subjects'
// version map. Never import `store.js` or `tabs.js`: a helper over either closes a cycle.
import { forgetSubject, hasSubject } from "./subject-versions.js";
import type { TabKind } from "./types.js";

/** The chat whose transcript a view projects: the chat itself, or the launching chat
 *  behind a subagent page's composite `<chatID>/<subtaskID>` ref (the first slash is the
 *  seam, `tab-materialize.ts`'s codec). Every other kind has no subject and reads "". */
function subjectChat(kind: TabKind, ref: string): string {
  switch (kind) {
    case "chat":
      return ref;
    case "subagent": {
      const cut = ref.indexOf("/");
      return cut > 0 ? ref.slice(0, cut) : "";
    }
    default:
      return "";
  }
}

/** A kind with a digest subject is fresh while the map holds its version. Other kinds read STALE
 *  and refetch every activation, which `files` needs: N browsers share one view element. */
export function viewStale(kind: TabKind, ref: string): boolean {
  const chat = subjectChat(kind, ref);
  return chat === "" || !hasSubject("chat", chat);
}

/** Drop a view's claim: its window (`evictChatMessages`) or subject (`removeChat`) is gone, so both
 *  chat subjects go. Not called on tab close: the subject survives, and reopening would pay a GET. */
export function forgetView(kind: TabKind, ref: string): void {
  const chat = subjectChat(kind, ref);
  if (chat === "") {
    return;
  }
  forgetSubject("chat", chat);
  forgetSubject("live_turn", chat);
}
