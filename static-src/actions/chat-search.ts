// Cross-chat search (the History page's box): finds the conversation, where find-in-chat.ts finds
// the position within one.

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import { decodeSearchAllResult } from "../wire/decoders.gen.js";
import type { SearchAllResult } from "../wire/types.gen.js";

export const searchChats = apiAction<string, SearchAllResult>({
  name: "chat.search_all",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: (q) => ({ method: "GET", path: `/api/chats/search?q=${encodeURIComponent(q)}` }),
  decode: decodeSearchAllResult,
  // The box shows its own inline note; a toast per keystroke would be noise.
  error: false,
});
