// The activity dot on a SUBAGENT tab row: one effect over every open subagent tab. The fact is the
// invocation tool call's status: a closed union (`failed` distinct), persisted with its
// `agent_subtask_id`, and ingested with no active-chat gate. It describes the LEAF the tab names
// (as the label does, `subagentTabName`), never a pipeline. No `input` state: the parent chat row
// carries the delegate's asks. No factory seed (the slot is reserved) and no state of its own: the
// tab set is the membership, enumerated per pass (cheap; else use `chat.ts`'s per-tab registry).

import { effect, touch } from "@cplieger/reactive";
import { openSubagentRefs, setTabStatus, tabIdFor } from "./tabs.js";
import { get, messagesVersionOf, subagentStatusFor, turnLive } from "./store.js";
import { findSubagentInvocation } from "./subagent-slice.js";
import { parseSubagentRef } from "./tab-materialize.js";

function repaint(): void {
  for (const ref of openSubagentRefs()) {
    const { chatID, subtaskID } = parseSubagentRef(ref);
    if (chatID === "") {
      // A malformed persisted ref never paints; the tab-set dependency covers its removal.
      continue;
    }
    // TRACKED before any bail: this repaints `working` as `done` on the delegate's own update, so a
    // pass that finds nothing must stay subscribed.
    touch(messagesVersionOf(chatID));
    // Same synchronous pass as `openSubagentRefs`, so the id resolves; `setTabStatus` no-ops on an
    // unknown id.
    const id = tabIdFor("subagent", ref);
    const session = get(chatID);
    const invocation =
      session === undefined ? undefined : findSubagentInvocation(session, subtaskID);
    // The second input: the chat's turn liveness tells a stale spinner from work (`delegateStatusFor`).
    setTabStatus(
      id,
      subagentStatusFor(invocation?.status, session === undefined || turnLive(session)),
    );
  }
}

/** Wire the effect from the composition root: at import it would paint an unrestored strip. */
export function installSubagentDotSubscriber(): void {
  effect(() => {
    // Subscribes to the tab SET (openSubagentRefs) and, inside the loop, to each
    // launching chat's own transcript version. Those two are the whole input: a
    // subagent tab landing or leaving, and the delegate's invocation changing.
    repaint();
  });
}
