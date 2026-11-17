// The marotte mark standing in the transcript of a chat with nothing in it yet.

import { computed, effect } from "@cplieger/reactive";
import { byId } from "./dom.js";
import { isEmptyChat, watchPaintedId, watchSession } from "./store.js";

/** The painted chat's, because the mark sits in the mounted transcript. A send sets `thinking`
 *  before the server's turn lands, so a turn in flight hides it too. */
const shown = computed(() => {
  const id = watchPaintedId();
  const session = id === "" ? undefined : watchSession(id);
  return session !== undefined && isEmptyChat(session) && !session.thinking;
});

/** Bind the mark's visibility to the painted chat. Called once, from app.ts. */
export function initEmptyChatLogo(): void {
  const logo = byId("empty-chat-logo");
  effect(() => {
    logo.classList.toggle("hidden", !shown.value);
  });
}
