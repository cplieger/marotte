import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import { decodeGitResult } from "./git-changes.js";

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
