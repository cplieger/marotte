import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import { decodeGitResult, type GitCmdResult } from "./git-changes.js";

import { truncate } from "../strings.js";

interface CheckoutArgs {
  repo: string;
  branch: string;
  create: boolean;
}

// Optimistic UI belongs to the caller, so args stay structuredClone-safe for retry.
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const checkoutBranch = apiAction<CheckoutArgs, void>({
  name: "git.checkout_branch",
  scope: (args) => "git:" + args.repo,
  request: ({ repo, branch, create }) => ({
    method: "POST",
    path: "/api/git/checkout",
    body: { repo, branch, create },
  }),
  // Like every git mutation, a failed checkout arrives as HTTP 200 + {"error": …} (writeCmdResult).
  decode: (data) => {
    decodeGitResult(data);
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: (args) => `Could not check out "${truncate(args.branch)}"`,
});

/** Ask the utility agent for a branch name describing the repo's work in
 *  progress (uncommitted changes, else recent commits). The result lands
 *  in the branch-switcher's create input for the user to edit or accept. */
export const suggestBranchName = apiAction<{ repo: string }, GitCmdResult>({
  name: "git.suggest_branch_name",
  scope: (args) => "git:" + args.repo,
  dedupe: (args) => args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/branch-name", body: args }),
  decode: decodeGitResult,
  error: "Could not suggest a branch name",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});
