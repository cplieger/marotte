// Git PRs tab actions. generate() (AI PR description) stays inline: its error surfaces in the dialog.

import {
  apiAction,
  defineAction,
  ActionError,
  fromEndpoint,
  retryNetwork,
  RETRY_STANDARD,
} from "./index.js";
import {
  getForgesByIdCapabilitiesEndpoint,
  getForgesByIdReposByRepoAffordancesEndpoint,
  getForgesByIdReposByRepoPrsByNumberMergeEndpoint,
  postForgesByIdReposByRepoPrsByNumberCloseEndpoint,
  postForgesByIdReposByRepoPrsByNumberMergeEndpoint,
  postForgesByIdReposByRepoPrsByNumberReopenEndpoint,
  postForgesByIdReposByRepoPrsByNumberRerunEndpoint,
  postForgesByIdReposByRepoPrsEndpoint,
  postForgesInventoryRefreshEndpoint,
  postForgesInventoryWatchEndpoint,
} from "../wire/client.gen.js";
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
  PRMerge,
  RepoAffordances,
} from "../wire/types.gen.js";

export interface PRArgs {
  forge_id: string;
  /** The repository's id: the segment the route takes. */
  repo_id: string;
  /** owner and name build the action's scope. */
  owner: string;
  name: string;
  pr_number: number;
}

/** Args pinned to the head commit the row was rendered from, so the forge refuses a moved branch;
 *  the check chip is that commit's folded state. An empty pin leaves a re-run unpinned; a merge
 *  refuses it. */
interface PinnedPRArgs extends PRArgs {
  head_sha: string;
}

/** The head pin plus the dialog's merge strategy (the repository's own spelling) and the forge
 *  kind, which decides how that choice is sent. */
interface MergePRArgs extends PinnedPRArgs {
  forge_kind: ForgeKind;
  strategy: string;
}

/** GitHub and the Gitea family name the strategy, because their default intent is a merge commit.
 *  GitLab refuses any strategy and merges by the project's own method, so its choice is an intent:
 *  squash, or that method without squashing. */
function mergeBody(args: MergePRArgs, auto: boolean): PRMerge {
  return { ...mergeChoice(args), head_sha: args.head_sha, ...(auto ? { auto: true } : {}) };
}

function mergeChoice(args: MergePRArgs): Pick<PRMerge, "intent" | "strategy"> {
  if (args.forge_kind !== "gitlab") {
    return { intent: "default", strategy: args.strategy };
  }
  return { intent: args.strategy === "squash" ? "squash" : "no_squash" };
}

/** The path arguments every pull-request route takes. */
function prRoute(args: PRArgs): { id: string; repo: string; number: string } {
  return { id: args.forge_id, repo: args.repo_id, number: String(args.pr_number) };
}

/** Merge a pull request with the chosen method, pinned to the row's head commit. No optimistic
 *  removal (an accepted merge is still open) and no toast (the row renders the refusal). */
export const mergePR = apiAction<MergePRArgs, MergeResult>({
  name: "git.merge_pr",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) =>
    fromEndpoint(
      postForgesByIdReposByRepoPrsByNumberMergeEndpoint.request({
        ...prRoute(args),
        body: mergeBody(args, false),
      }),
    ),
  decode: (data) => decodeMergeResult(data),
  decodeError: keepMovedBody,
  error: false,
  // Not retryable: a timed-out merge may have succeeded. The head pin makes a retry fail closed
  // against a moved head, but not safe.
});

/** Arm the forge's own auto-merge, with the merge method as mergePR. Not optimistic: arming does
 *  not merge, so the row stays and re-renders as armed. No toast. */
export const armAutoMerge = apiAction<MergePRArgs, MergeResult>({
  name: "git.arm_auto_merge",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) =>
    fromEndpoint(
      postForgesByIdReposByRepoPrsByNumberMergeEndpoint.request({
        ...prRoute(args),
        body: mergeBody(args, true),
      }),
    ),
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
  request: (args) =>
    fromEndpoint(postForgesByIdReposByRepoPrsByNumberCloseEndpoint.request(prRoute(args))),
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
  const spec = postForgesByIdReposByRepoPrsByNumberCloseEndpoint.request(prRoute(args));
  fetch(spec.path, {
    method: spec.method,
    keepalive: true,
    headers: { "Content-Type": "application/json" },
    body: "{}",
  }).catch(() => undefined);
}

/** Reopen a closed pull request. No optimistic step (the tab lists open PRs; the requested cycle
 *  brings it back) and no toast. */
export const reopenPR = apiAction<PRArgs>({
  name: "git.reopen_pr",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) =>
    fromEndpoint(postForgesByIdReposByRepoPrsByNumberReopenEndpoint.request(prRoute(args))),
  decodeError: keepMovedBody,
  error: false,
  idempotencyKey: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
});

/** Re-run the failed CI of the head the row's red chip shows; the server refuses a moved branch,
 *  and unpinned re-runs the live head. Not optimistic, no toast: an instance that cannot re-run
 *  answers `capability_unsupported` with the evidence that becomes its message. */
export const rerunChecks = apiAction<PinnedPRArgs>({
  name: "git.rerun_checks",
  scope: (args) => "git:" + args.forge_id + ":" + args.owner + "/" + args.name,
  dedupe: true,
  request: (args) =>
    fromEndpoint(
      postForgesByIdReposByRepoPrsByNumberRerunEndpoint.request(
        args.head_sha === "" ? prRoute(args) : { ...prRoute(args), head_sha: args.head_sha },
      ),
    ),
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
 *  toast: the row then offers the press and lets a refusal decide. Not
 *  deduped: an ask after the connection changed must not join a read issued
 *  before it. */
export const readCapabilities = apiAction<{ forge_id: string }, Capabilities>({
  name: "git.read_capabilities",
  request: (args) => fromEndpoint(getForgesByIdCapabilitiesEndpoint.request({ id: args.forge_id })),
  decode: (data) => decodeCapabilities(data),
  error: false,
});

/** Read what a repository allows (`GET .../affordances`, through the server's
 *  cache); the merge dialog offers its merge strategies. No toast: the dialog
 *  states a failed read. */
export const readAffordances = apiAction<{ forge_id: string; repo_id: string }, RepoAffordances>({
  name: "git.read_affordances",
  dedupe: true,
  request: (args) =>
    fromEndpoint(
      getForgesByIdReposByRepoAffordancesEndpoint.request({
        id: args.forge_id,
        repo: args.repo_id,
      }),
    ),
  decode: (data) => decodeRepoAffordances(data),
  error: false,
});

/** Read a pull request's merge state back (`GET .../prs/{n}/merge`), the read
 *  that follows an accepted or queued merge. No toast: the row follows it. */
export const readMergeStatus = apiAction<PRArgs, MergeStatus>({
  name: "git.read_merge_status",
  dedupe: true,
  request: (args) =>
    fromEndpoint(getForgesByIdReposByRepoPrsByNumberMergeEndpoint.request(prRoute(args))),
  decode: (data) => decodeMergeStatus(data),
  error: false,
});

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

/** Open a new pull request; the answer is its row. `error: false`: the dialog says the outcome.
 *  NOT retryable: a timed-out create may have opened it, so a retry could duplicate. */
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
  request: (args) =>
    fromEndpoint(
      postForgesByIdReposByRepoPrsEndpoint.request({
        id: args.forge_id,
        repo: args.repo_id,
        body: {
          source_branch: args.source_branch,
          target_branch: args.target_branch,
          title: args.title,
          body: args.body,
          draft: args.draft,
        },
      }),
    ),
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
  request: () => fromEndpoint(postForgesInventoryRefreshEndpoint.request()),
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
  request: (args) =>
    fromEndpoint(
      postForgesInventoryWatchEndpoint.request({
        body: { watching: args.watching, page: args.page },
        sseClient: args.tag,
      }),
    ),
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
