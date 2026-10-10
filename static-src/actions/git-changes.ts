// Git Changes tab actions. The server answers HTTP 200 for both outcomes; failures surface beside
// the pressed control, so no action toasts one.

import { apiAction, ActionError, hasErrorString, retryNetwork, RETRY_STANDARD } from "./index.js";

import { summarizePullAll, type GitPullResult } from "../git-types.js";

interface GitRepoArgs {
  repo: string;
}

interface GitRepoFilesArgs extends GitRepoArgs {
  files: string[];
}

/** Result envelope of every /api/git mutation. decodeGitResult throws on the error arm, so a
 *  RESOLVED dispatch is a genuine success. */
export interface GitCmdResult {
  output?: string;
}

function liftOutput(parsed: unknown): GitCmdResult {
  if (typeof parsed === "object" && parsed !== null && "output" in parsed) {
    const out = (parsed as { output?: unknown }).output;
    if (typeof out === "string") {
      return { output: out };
    }
  }
  return {};
}

/** The message a failed /api/git envelope carries: the kind, plus `detail` when supplied. */
function errorMessage(data: { error: string }): string {
  const detail = (data as { detail?: unknown }).detail;
  return typeof detail === "string" && detail !== "" ? `${data.error}: ${detail}` : data.error;
}

/** Decodes the /api/git 200-with-error envelope (internal/git/helpers.go writeCmdResult) into an
 *  ActionError with code "git", never retried: the command may have had side effects. Shared with
 *  git-branch.ts. */
export function decodeGitResult(data: unknown): GitCmdResult {
  if (hasErrorString(data) && data.error !== "") {
    throw new ActionError(errorMessage(data), { code: "git" });
  }
  return liftOutput(data);
}

/** Stage files (used for both "stage all" and single-file stage). */
export const stage = apiAction<GitRepoFilesArgs, GitCmdResult>({
  name: "git.stage",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stage", body: args }),
  decode: decodeGitResult,
  error: false,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Discard files (used for both "discard all" and single-file discard). */
export const discard = apiAction<GitRepoFilesArgs, GitCmdResult>({
  name: "git.discard",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/discard", body: args }),
  decode: decodeGitResult,
  error: false,
  // Not retryable: a timed-out discard may have succeeded server-side.
});

export const unstage = apiAction<GitRepoFilesArgs, GitCmdResult>({
  name: "git.unstage",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/unstage", body: args }),
  decode: decodeGitResult,
  error: false,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

export const pull = apiAction<GitRepoArgs, GitCmdResult>({
  name: "git.pull",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/pull", body: args }),
  decode: decodeGitResult,
  success: (args) => (args.repo !== "" ? `Pulled ${args.repo}` : "Pulled"),
  error: false,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** The pull-all response's per-repo rows. A missing or non-array `repos` is a FAILURE, not
 *  "nothing to pull"; narrowed field by field from unknown, since a cast would make the checks dead. */
function decodePullAll(data: unknown): GitPullResult[] {
  const rec = data as Record<string, unknown> | null;
  const repos = rec?.["repos"];
  if (!Array.isArray(repos)) {
    throw new ActionError("unexpected response", { code: "decode" });
  }
  const out: GitPullResult[] = [];
  for (const raw of repos as unknown[]) {
    const e = raw as Record<string, unknown>;
    const repo = e["repo"];
    const verdict = e["verdict"];
    if (typeof repo !== "string" || typeof verdict !== "string") {
      throw new ActionError("unexpected response", { code: "decode" });
    }
    const reason = e["reason"];
    const detail = e["detail"];
    out.push({
      repo,
      verdict,
      ...(typeof reason === "string" ? { reason } : {}),
      ...(typeof detail === "string" ? { detail } : {}),
    });
  }
  return out;
}

/** Fast-forwards every repo where safe and reports the rest, in one request: the safety judgement
 *  must be atomic with the pull. `dedupe` collapses a second press; no retry, Refresh recovers. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no args
export const pullAll = apiAction<void, GitPullResult[]>({
  name: "git.pull_all",
  dedupe: true,
  request: () => ({ method: "POST", path: "/api/git/pull-all" }),
  decode: decodePullAll,
  success: (_args, result) => summarizePullAll(result),
  error: false,
});

export const push = apiAction<GitRepoArgs, GitCmdResult>({
  name: "git.push",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/push", body: args }),
  decode: decodeGitResult,
  success: (args) => (args.repo !== "" ? `Pushed ${args.repo}` : "Pushed"),
  error: false,
  // Not retryable: a timed-out push may have succeeded server-side.
});

export const stash = apiAction<GitRepoArgs, GitCmdResult>({
  name: "git.stash",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stash", body: args }),
  decode: decodeGitResult,
  error: false,
  idempotencyKey: true,
  // Idempotent server-side via Idempotency-Key; left non-retryable.
});

export const stashPop = apiAction<GitRepoArgs, GitCmdResult>({
  name: "git.stash_pop",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/stash-pop", body: args }),
  decode: decodeGitResult,
  error: false,
  idempotencyKey: true,
  // Idempotent server-side via Idempotency-Key; left non-retryable.
});

export const commit = apiAction<{ repo: string; message: string }, GitCmdResult>({
  name: "git.commit",
  scope: (args) => "git:" + args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/commit", body: args }),
  decode: decodeGitResult,
  success: "Committed",
  error: false,
  idempotencyKey: true,
  // Not retryable: a timed-out commit may have succeeded; a retry duplicates it.
});

export const generateCommitMessage = apiAction<GitRepoArgs, GitCmdResult>({
  name: "git.generate_message",
  scope: (args) => "git:" + args.repo,
  dedupe: (args) => args.repo,
  request: (args) => ({ method: "POST", path: "/api/git/commit-message", body: args }),
  decode: decodeGitResult,
  error: false,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});
