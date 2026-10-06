// Actions for the Kiro configuration browser (/docs).

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import { join as joinKey } from "@cplieger/keyenc";

/** The file browser's own delete endpoint: one place for the mount-root and protected-directory
 *  refusals. */
const API_FILES_ACTION = "/api/files/action";

/** Delete one `.kiro` document. `path` is the row's `doc.path` verbatim, the spelling `openFile`
 *  uses. The docs scan's signature includes each category's entry names, so no invalidation. */
export const deleteDoc = apiAction<{ path: string; name: string }>({
  name: "docs.delete",
  scope: (args) => "doc:" + args.path,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  idempotencyKey: (args) => joinKey("docs.delete", args.path),
  request: (args) => ({
    method: "POST",
    path: API_FILES_ACTION,
    body: { action: "delete", path: args.path },
  }),
  success: (args) => `Deleted ${args.name}`,
  error: (args) => `Could not delete ${args.name}`,
});
