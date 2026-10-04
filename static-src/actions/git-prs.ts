// Actions for the Git PRs tab: create, merge, close, refresh, and the reads the
// row's controls follow (capabilities, affordances, a merge's state).
// generate() (AI PR-description) stays INLINE (error surfaces in the
// dialog) and is intentionally excluded.
// ---------------------------------------------------------------------------

import { apiAction, defineAction, ActionError, retryNetwork, RETRY_STANDARD } from "./index.js";
import type { ApiErrorInfo } from "./index.js";

import type { ForgeKind } from "../forge-types.js";
import {
  decodeCapabilities,
  decodeInventoryRefresh,
  decodeMergeResult,
  decodeMergeStatus,
  decodePR,
  decodePRChanged,
  decodeRepoAffordances,
} from "../wire/decoders.gen.js";
import type {
  Capabilities,
  InventoryRefresh,
  MergeResult,
  MergeStatus,
  PR,
  PRChanged,
  RepoAffordances,
} from "../wire/types.gen.js";

// --- Types ---

export interface PRArgs {
  forge_id: string;
  /** The repository's id: the segment the route takes. */
  repo_id: string;
  /** owner and name build the action's scope. */
  owner: string;
  name: string;
  pr_number: number;
}

/** Args for a PR action pinned to the head commit the row was rendered
 *  from, so the forge refuses when the branch moved since. Shared by merge,
 *  auto-merge and re-run, because the row's check chip is the folded state of
 *  that one commit. An empty pin leaves a re-run unpinned; a merge refuses it. */
interface PinnedPRArgs extends PRArgs {
  head_sha: string;
}

/** Args for a merge-shaped action: the head pin plus the merge strategy the
 *  user chose in the dialog, one of the repository's own spellings, and the forge
 *  kind, which decides how that choice is sent. */
interface MergePRArgs extends PinnedPRArgs {
  forge_kind: ForgeKind;
  strategy: string;
}

/** The merge body for the dialog's choice. GitHub and the Gitea family name the
 *  strategy, because their default intent is a merge commit. GitLab refuses any
 *  strategy and merges by the project's own method, so its choice is an intent:
 *  squash, or that method without squashing. */
function mergeBody(args: MergePRArgs, auto: boolean): Record<string, unknown> {
  return { ...mergeChoice(args), head_sha: args.head_sha, ...(auto ? { auto: true } : {}) };
}

function mergeChoice(args: MergePRArgs): Record<string, string> {
  if (args.forge_kind !== "gitlab") {
    return { intent: "default", strategy: args.strategy };
  }
  return { intent: args.strategy === "squash" ? "squash" : "no_squash" };
}

/** Build the API path for a PR action (merge/close/reopen/rerun). */
function prPath(args: PRArgs, action: string): string {
  return `/api/forges/${encodeURIComponent(args.forge_id)}/repos/${encodeURIComponent(args.repo_id)}/prs/${args.pr_number}/${action}`;
}

/** The query string of the re-run route, which carries the head pin. */
function pinnedQuery(args: PinnedPRArgs): string {
  return args.head_sha === ""
    ? ""
    : "?" + new URLSearchParams({ head_sha: args.head_sha }).toString();
}

// --- Actions ---

/** Merge a pull request with the chosen method, pinned to the head commit
 *  the row read, answering the outcome. No optimistic removal: an accepted
 *  merge is still open, so the caller decides from the outcome whether the
 *  row goes. No toast: the row renders the refusal in the server's words. */
export const mergePR = apiAction<MergePRArgs, MergeResult>({
  name: "git.merge_pr",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({
    method: "POST",
    path: prPath(args, "merge"),
    body: mergeBody(args, false),
  }),
  decode: (data) => decodeMergeResult(data),
  decodeError: keepMovedBody,
  error: false,
  // Not retryable: a timed-out merge may have succeeded server-side. The
  // head pin makes a retry SAFER (a second attempt against a moved head
  // fails closed rather than merging the wrong commit) but not safe.
});

/** Arm the forge's own auto-merge: it merges once its requirements are
 *  met. Carries the merge method for the same reason mergePR does.
 *  Deliberately NOT an optimistic remove: arming does not merge, so
 *  the row must stay and re-render as armed once the server confirms.
 *  No toast: the row renders the refusal. */
export const armAutoMerge = apiAction<MergePRArgs, MergeResult>({
  name: "git.arm_auto_merge",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({ method: "POST", path: prPath(args, "merge"), body: mergeBody(args, true) }),
  decode: (data) => decodeMergeResult(data),
  decodeError: keepMovedBody,
  error: false,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Close a pull request without merging. No optimistic step and no toast: the
 *  tab holds the close behind its undo window, takes the row off the list as it
 *  sends, and puts it back with the refusal. */
export const closePR = apiAction<PRArgs, PRChanged>({
  name: "git.close_pr",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({ method: "POST", path: prPath(args, "close"), body: {} }),
  decode: (data) => decodePRChanged(data),
  decodeError: keepMovedBody,
  error: false,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Send a held close as the page goes away. The unload cancels an ordinary
 *  request and keeps a keepalive one, and no page is left to show its answer. */
export function sendCloseOnUnload(args: PRArgs): void {
  fetch(prPath(args, "close"), {
    method: "POST",
    keepalive: true,
    headers: { "Content-Type": "application/json" },
    body: "{}",
  }).catch(() => undefined);
}

/** Reopen a closed pull request. No optimistic step: the PRs tab lists
 *  open PRs, so a reopened one is not in a group to mutate, and the cycle
 *  the server asks for after the mutation brings it. No toast: the row
 *  renders the refusal. */
export const reopenPR = apiAction<PRArgs>({
  name: "git.reopen_pr",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({ method: "POST", path: prPath(args, "reopen"), body: {} }),
  decodeError: keepMovedBody,
  error: false,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Re-run the failed CI of a pull request's head, pinned to the commit whose
 *  red chip the row shows: the server refuses when the branch moved since, and
 *  unpinned (no head SHA reported) re-runs the live head's failure. No
 *  optimistic step: the chip flips only once the forge says so. No toast: the
 *  row renders the refusal, and an instance that cannot re-run answers
 *  `capability_unsupported` with the evidence that becomes its message. */
export const rerunChecks = apiAction<PinnedPRArgs>({
  name: "git.rerun_checks",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) => ({
    method: "POST",
    path: prPath(args, "rerun") + pinnedQuery(args),
    body: {},
  }),
  decodeError: (info) => keepMovedBody(info) ?? capabilityRefusal(info),
  error: false,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** A refusal naming a moved repository keeps its body as the error's cause, since
 *  the body says where the repository went (git-pr-status.ts `movedRepository`). */
function keepMovedBody(info: ApiErrorInfo): { kind: "error"; error: ActionError } | undefined {
  if (info.code !== "repo_ref_stale") {
    return undefined;
  }
  return {
    kind: "error",
    error: new ActionError(info.message, {
      status: info.status,
      code: info.code,
      cause: info.body,
    }),
  };
}

/** A re-run the instance cannot do, worded by its capability's evidence. */
function capabilityRefusal(info: ApiErrorInfo): { kind: "error"; error: ActionError } | undefined {
  const detail = info.code === "capability_unsupported" ? capabilityDetail(info.body) : "";
  if (detail === "") {
    return undefined;
  }
  return {
    kind: "error",
    error: new ActionError(detail, { status: info.status, code: "capability_unsupported" }),
  };
}

/** The evidence detail of a refusal body's `capability`, or "". */
function capabilityDetail(body: unknown): string {
  if (typeof body !== "object" || body === null) {
    return "";
  }
  const cap = (body as Record<string, unknown>)["capability"];
  if (typeof cap !== "object" || cap === null) {
    return "";
  }
  const detail = (cap as Record<string, unknown>)["detail"];
  return typeof detail === "string" ? detail : "";
}

/** Read what a connection can do (`GET /api/forges/{id}/capabilities`); the
 *  row's Re-run reads its `rerun_checks`. A failed read answers null and no
 *  toast: the row then offers the press and lets a refusal decide. */
export const readCapabilities = apiAction<{ forge_id: string }, Capabilities>({
  name: "git.read_capabilities",
  dedupe: true,
  request: (args) => ({
    method: "GET",
    path: `/api/forges/${encodeURIComponent(args.forge_id)}/capabilities`,
  }),
  decode: (data) => decodeCapabilities(data),
  error: false,
});

/** Read what a repository allows (`GET .../affordances`, through the server's
 *  cache); the merge dialog offers its merge strategies. No toast: the dialog
 *  states a failed read. */
export const readAffordances = apiAction<{ forge_id: string; repo_id: string }, RepoAffordances>({
  name: "git.read_affordances",
  dedupe: true,
  request: (args) => ({
    method: "GET",
    path: `/api/forges/${encodeURIComponent(args.forge_id)}/repos/${encodeURIComponent(args.repo_id)}/affordances`,
  }),
  decode: (data) => decodeRepoAffordances(data),
  error: false,
});

/** Read a pull request's merge state back (`GET .../prs/{n}/merge`), the read
 *  that follows an accepted or queued merge. No toast: the row follows it. */
export const readMergeStatus = apiAction<PRArgs, MergeStatus>({
  name: "git.read_merge_status",
  dedupe: true,
  request: (args) => ({ method: "GET", path: prPath(args, "merge") }),
  decode: (data) => decodeMergeStatus(data),
  error: false,
});

/** Args for opening a new pull request. */
interface CreatePRArgs {
  forge_id: string;
  repo_id: string;
  owner: string;
  name: string;
  source_branch: string;
  target_branch: string;
  title: string;
  body: string;
  draft: boolean;
}

/** Open a new pull request; the answer is its row. The create dialog says the
 *  outcome in its own status line, so `error: false` (no toast). NOT retryable:
 *  a timed-out create may have opened the PR server-side, so a retry could open
 *  a duplicate (same rationale as mergePR). */
export const createPR = apiAction<CreatePRArgs, PR>({
  name: "git.create_pr",
  scope: (args) =>
    "git:" +
    args.forge_id +
    ":" +
    args.owner +
    "/" +
    args.name +
    ":" +
    args.source_branch +
    ">" +
    args.target_branch,
  dedupe: true,
  request: (args) => ({
    method: "POST",
    path: `/api/forges/${encodeURIComponent(args.forge_id)}/repos/${encodeURIComponent(args.repo_id)}/prs`,
    body: {
      source_branch: args.source_branch,
      target_branch: args.target_branch,
      title: args.title,
      body: args.body,
      draft: args.draft,
    },
  }),
  decode: (data) => decodePR(data),
  error: false,
});

/** Ask the poller for a cycle (`POST /api/forges/inventory/refresh`); a cycle in
 *  flight is joined rather than doubled. Its entries arrive as forge_inventory
 *  frames, and the answer names the cycle that serves the ask. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no args
export const requestPRCycle = apiAction<void, InventoryRefresh>({
  name: "git.request_pr_cycle",
  dedupe: true,
  request: () => ({ method: "POST", path: "/api/forges/inventory/refresh" }),
  decode: decodeInventoryRefresh,
  error: false,
});

interface WatchArgs {
  watching: boolean;
  /** The stream's SSE-Client tag, which the server's presence table keys on. */
  tag: string;
  /** This page's name, so sibling pages sharing the tag are separate viewers. */
  page: string;
}

/** Tell the server whether this browser profile shows the pull-request list, so
 *  the poller holds its short cycle only while someone looks. No toast: a lost
 *  watch costs the list's cadence, not its contents. */
export const watchPRView = apiAction<WatchArgs>({
  name: "git.watch_prs",
  request: (args) => ({
    method: "POST",
    path: "/api/forges/inventory/watch",
    body: { watching: args.watching, page: args.page },
    headers: { "SSE-Client": args.tag },
  }),
  error: false,
});

/** Read every connection's pull requests from the server's inventory, which
 *  costs no forge request; the refresh button asks for a cycle itself. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type arguments for an action with no args and no result
export const refreshPRs = defineAction<void, void>({
  name: "git.refresh_prs",
  dedupe: true,
  run: async (_args, signal) => {
    const { refreshPRs } = await import("../git-prs-tab.js");
    try {
      await refreshPRs(signal);
    } catch (e) {
      if (signal.aborted) {
        throw new ActionError("cancelled", { code: "cancelled", cause: e });
      }
      throw new ActionError(e instanceof Error ? e.message : "network error", {
        code: "network",
        cause: e,
      });
    }
  },
  error: "Could not refresh PRs",
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});
