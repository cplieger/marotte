// Actions for the Kiro configuration browser (/docs).

import { apiAction, fromEndpoint, retryNetwork, RETRY_STANDARD } from "./index.js";
import { postFilesActionEndpoint, postWorkspaceKiroDocsNewEndpoint } from "../wire/client.gen.js";
import { join as joinKey } from "@cplieger/keyenc";
import { asObject, reqStr } from "../wire/validators.gen.js";

/** Delete one `.kiro` document. `path` is the row's `doc.path` verbatim, the spelling `openFile`
 *  uses. The docs scan's signature includes each category's entry names, so no invalidation. */
export const deleteDoc = apiAction<{ path: string; name: string }>({
  name: "docs.delete",
  scope: (args) => "doc:" + args.path,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  idempotencyKey: (args) => joinKey("docs.delete", args.path),
  // The file browser's own delete: one place for the mount-root and protected-directory refusals.
  request: (args) =>
    fromEndpoint(postFilesActionEndpoint.request({ body: { action: "delete", path: args.path } })),
  success: (args) => `Deleted ${args.name}`,
  error: (args) => `Could not delete ${args.name}`,
});

export interface NewDocRequest {
  category: "steering" | "skill" | "prompt" | "agent" | "hook";
  name: string;
  inclusion?: "always" | "manual";
  description?: string;
  trigger?: string;
  matcher?: string;
  action?: "command" | "agent";
  content?: string;
  timeout?: number;
}

const NP = "$.created";

/** `error: false`: the form shows the server's refusal in place. The key makes a retried create
 *  replay rather than meet its own 409. */
export const createDoc = apiAction<NewDocRequest, { path: string }>({
  name: "docs.create",
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: (body) => fromEndpoint(postWorkspaceKiroDocsNewEndpoint.request({ body })),
  decode: (data) => ({ path: reqStr(asObject(data, NP), "path", NP) }),
  error: false,
});
