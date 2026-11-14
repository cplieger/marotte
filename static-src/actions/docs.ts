// Actions for the Kiro configuration browser (/docs).

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import { asObject, reqStr } from "../wire/validators.gen.js";

/** Delete what one `.kiro` row stands for, which the server decides. `path` is the row's `doc.path`
 *  verbatim. */
export const deleteDoc = apiAction<{ path: string; name: string }>({
  name: "docs.delete",
  scope: (args) => "doc:" + args.path,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  idempotencyKey: true,
  request: (args) => ({
    method: "POST",
    path: "/api/workspace/kiro-docs/delete",
    body: { path: args.path },
  }),
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
  request: (body) => ({ method: "POST", path: "/api/workspace/kiro-docs/new", body }),
  decode: (data) => ({ path: reqStr(asObject(data, NP), "path", NP) }),
  error: false,
});
