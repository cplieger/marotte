import { transportAction } from "./index.js";
import { errorAbout } from "./subject.js";
import { stillOr } from "./still-applying.js";

interface RewindArgs {
  chatID: string;
  /** The USER message to revert to. That message and everything after it are
   *  dropped, so this is not "revert to just after N" — see the confirm text. */
  messageID: string;
}

/** rewindChat reverts the chat to a past turn: KAS drops the addressed user message and its
 *  successors and rolls the files back. The window converges from the `turn_revert` record over SSE
 *  (store.ts `applyRevert`), so success refetches nothing. `idempotencyKey`: a duplicate would
 *  revert a SECOND time from the truncated transcript; KAS's revert-in-progress guard is only the
 *  backstop. */
export const rewindChat = transportAction<RewindArgs>({
  name: "rewind.revert",
  networkMode: "always",
  scope: (args) => "rewind:" + args.chatID,
  idempotencyKey: true,
  command: ({ chatID, messageID }) => ({
    type: "rewind_chat",
    chat_id: chatID,
    payload: { message_id: messageID },
  }),
  error: errorAbout(
    ({ chatID }: RewindArgs) => chatID,
    stillOr("rewinding the chat", "Could not rewind chat", "chat"),
  ),
});
