import { transportAction } from "./index.js";
import { errorAbout } from "./subject.js";
import { loadMessages } from "../store-load.js";

interface RewindArgs {
  chatID: string;
  /** The USER message to revert to. That message and everything after it are
   *  dropped, so this is not "revert to just after N" — see the confirm text. */
  messageID: string;
}

/** rewindChat reverts the chat to a past turn: KAS drops the addressed user message and its
 *  successors and rolls the files back. The reverted transcript does NOT arrive over SSE
 *  (`chat_updated` is header-only and `upsertHeader` merges the count as `Math.max`), so
 *  `onSuccess` refetches the page. `idempotencyKey`: a duplicate would revert a SECOND time from
 *  the truncated transcript; KAS's revert-in-progress guard is only the backstop. */
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
  onSuccess: (_result, { chatID }) => {
    void loadMessages(chatID);
  },
  error: errorAbout(({ chatID }: RewindArgs) => chatID, "Could not rewind chat"),
});
