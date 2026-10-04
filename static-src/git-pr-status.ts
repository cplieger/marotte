// ---------------------------------------------------------------------------
// Pure read-outs for a PR row: the CI check chip, the merge verdict, the
// merge queue, and which per-forge controls a row may offer.
//
// DOM-free on purpose. The row renderer in git-prs-tab.ts turns these
// descriptors into elements; keeping the prose and the rules here is what
// makes every branch testable without a document.
// ---------------------------------------------------------------------------

import type { ForgeKind } from "./forge-types.js";
import type { GitPR } from "./git-types.js";
import type { Affordance, RepoSuccessor } from "./wire/types.gen.js";

/** A rendered CI chip: text, a CSS state class, and its hover detail. */
export interface CheckChip {
  text: string;
  className: string;
  tooltip: string;
}

/** Why the row holds the value it does for `field`, when its family's list
 *  lacks that field: "filled", "not_on_list", "unread" or "not_supplied". */
function fillReason(pr: GitPR, field: string): string | undefined {
  return pr.fill?.find((f) => f.field === field)?.reason;
}

const NOT_READ = "This forge's list does not carry checks. They are read while this list is shown.";
const UNREAD = "the last read of this pull request failed. The next cycle reads it again.";

/** Describe the CI chip for a PR, or null when the forge has no verdict to give.
 *  A verdict the family's list does not carry gets a chip saying it was not
 *  read, so a row with failing CI cannot read as a quiet one. */
export function checkChip(pr: GitPR): CheckChip | null {
  const total = pr.action.checks_total;
  const failing = pr.action.checks_failing;
  switch (pr.action.checks) {
    case "passing":
      return {
        text: "checks passed",
        className: "git-pr-check-pass",
        tooltip:
          total > 0 ? countText(total, "check passed", "checks passed") : "The checks passed.",
      };
    case "failing":
      return {
        text: failing > 0 ? `${String(failing)} failing` : "checks failing",
        className: "git-pr-check-fail",
        tooltip:
          total > 0 && failing > 0
            ? `${String(failing)} of ${String(total)} checks failing`
            : "A required check is failing.",
      };
    case "pending":
      return {
        text: "checks running",
        className: "git-pr-check-pending",
        tooltip: total > 0 ? countText(total, "check running", "checks running") : "CI is running.",
      };
    case "neutral":
      return {
        text: "checks neutral",
        className: "git-pr-check-neutral",
        tooltip:
          total > 0
            ? countText(total, "check finished neutral", "checks finished neutral")
            : "The checks finished without a pass or a failure.",
      };
    default:
      return unreadChip(pr);
  }
}

function unreadChip(pr: GitPR): CheckChip | null {
  switch (fillReason(pr, "checks")) {
    case "not_on_list":
      return { text: "checks not read", className: "git-pr-check-unknown", tooltip: NOT_READ };
    case "unread":
      return {
        text: "checks unread",
        className: "git-pr-check-unknown",
        tooltip: capitalise(UNREAD),
      };
    default:
      return null;
  }
}

function countText(n: number, one: string, many: string): string {
  return `${String(n)} ${n === 1 ? one : many}`;
}

/** `s` with its first character upper-cased, for a clause that starts a sentence. */
export function capitalise(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** Whether a row can merge: `ready`, `blocked` (the forge refuses, or names a
 *  cause), or `unknown` (a value the merge needs has not been given). */
type MergeState = "ready" | "blocked" | "unknown";

/** A row's merge verdict and, unless it is ready, the cause in words. */
export interface MergeVerdict {
  state: MergeState;
  reason: string;
}

/** The row's merge verdict. Every refusal names its cause.
 *
 *  Only `none` is ready on its own. A cause this build does not know is still a
 *  cause, reported with the server's own word, so it cannot read as mergeable.
 *  Every family refuses a merge with no head pin, so a row without `head_sha`
 *  cannot merge whatever the forge says about it. */
export function mergeVerdict(pr: GitPR): MergeVerdict {
  const cause = pr.action.merge_blocked;
  switch (cause) {
    case "none":
      return headVerdict(pr);
    case "draft":
      return blocked(DRAFT_REASON);
    case "conflicts":
      return blocked("this PR conflicts with its target branch. Rebase or merge the target in.");
    case "checks_failing":
      return blocked("a required check is failing.");
    case "checks_running":
      return blocked("required checks are still running.");
    case "behind":
      return blocked("the source branch is behind its target and must be updated first.");
    case "blocked":
      return blocked(
        "the forge's merge policy refuses this merge. Check its review, approval and protected-branch rules.",
      );
    case "unknown":
      return unnamedCauseVerdict(pr);
    default:
      return blocked(
        `the forge refuses this merge and reports a cause this build does not recognise (${oneLine(cause)}).`,
      );
  }
}

const DRAFT_REASON = "this PR is a draft. Mark it ready for review first.";
const READY: MergeVerdict = { state: "ready", reason: "" };

function blocked(reason: string): MergeVerdict {
  return { state: "blocked", reason };
}

/** The verdict for a row whose forge names no block cause. The Gitea family
 *  never names one and GitLab does not while it is still computing the merge
 *  status, so the draft flag and the `mergeable` verdict decide: reading
 *  `unknown` alone as blocked would disable Merge on every Gitea and Codeberg
 *  row. */
function unnamedCauseVerdict(pr: GitPR): MergeVerdict {
  if (pr.draft === true) {
    return blocked(DRAFT_REASON);
  }
  switch (pr.action.mergeable) {
    case "yes":
      return headVerdict(pr);
    case "no":
      return blocked("the forge reports this PR is not mergeable and does not say why.");
    default:
      return { state: "unknown", reason: unreadVerdictReason(pr) };
  }
}

/** Why a verdict is missing: GitLab's list lacks the block reason, the Gitea
 *  family's the `mergeable` verdict, and a viewed cycle reads them. */
function unreadVerdictReason(pr: GitPR): string {
  const reasons = [fillReason(pr, "merge_blocked"), fillReason(pr, "mergeable")];
  if (reasons.includes("not_on_list")) {
    return "this forge's list does not say whether it can merge. It is read while this list is shown.";
  }
  if (reasons.includes("unread")) {
    return UNREAD;
  }
  return "the forge has not said whether this can merge yet.";
}

function hasHead(pr: GitPR): boolean {
  return pr.head_sha !== undefined && pr.head_sha !== "";
}

function headVerdict(pr: GitPR): MergeVerdict {
  if (hasHead(pr)) {
    return READY;
  }
  switch (fillReason(pr, "head_sha")) {
    case "not_on_list":
      return {
        state: "unknown",
        reason: "the head commit has not been read yet. It is read while this list is shown.",
      };
    case "unread":
      return { state: "unknown", reason: UNREAD };
    default:
      return { state: "unknown", reason: "the forge did not report the head commit." };
  }
}

/** Render a server-produced word for a reader: one line, bounded. A future
 *  value could be long or carry newlines, so it is normalised here rather than
 *  trusted for its provenance. */
function oneLine(word: string): string {
  const MAX = 40;
  const line = word.replace(/\s+/g, " ").trim();
  return line.length > MAX ? line.slice(0, MAX - 1) + "\u2026" : line;
}

/** What the merge queue says of a row, or "" for a row in none. */
export function queueText(pr: GitPR): string {
  const state = pr.action.queue_state;
  if (state === "none" || state === "unknown") {
    return "";
  }
  const parts = ["in merge queue"];
  const words = QUEUE_WORDS.get(state);
  if (words === undefined) {
    parts[0] = `in merge queue (${oneLine(state)})`;
  } else if (words !== "") {
    parts.push(words);
  }
  if (pr.action.queue_position >= 0) {
    parts.push(`position ${String(pr.action.queue_position)}`);
  }
  return parts.join(", ");
}

const QUEUE_WORDS: ReadonlyMap<string, string> = new Map([
  ["queued", ""],
  ["awaiting_checks", "awaiting checks"],
  ["mergeable", "ready to merge"],
  ["unmergeable", "cannot merge"],
  ["locked", "locked"],
]);

/** Whether a row offers Re-run, and if so whether it is enabled (`reason` "")
 *  or why not. */
export type RerunControl = { offer: false } | { offer: true; reason: string };

/** The Re-run control of a failing row, from its connection's `rerun_checks`
 *  capability (undefined: not answered yet; null: the read failed) and the
 *  reason a coded refusal left on its repository.
 *
 *  GitHub decides a re-run per repository, so no connection read can answer and
 *  its control stays enabled until a refusal says otherwise. Every other family
 *  refuses a re-run its capability does not read `yes`, so an unknown there is
 *  disabled with the evidence. */
export function rerunControl(
  kind: ForgeKind,
  cap: Affordance | null | undefined,
  refusal: string | undefined,
): RerunControl {
  if (refusal !== undefined) {
    return { offer: true, reason: refusal };
  }
  if (cap?.support === "no") {
    return { offer: false };
  }
  if (kind === "github" || cap === null || cap?.support === "yes") {
    return { offer: true, reason: "" };
  }
  if (cap === undefined) {
    return { offer: false };
  }
  const detail = cap.detail === "" ? "" : ` (${cap.detail})`;
  return {
    offer: true,
    reason: `the forge could not establish that it can re-run checks${detail}.`,
  };
}

/** The reason a re-run refusal leaves on its repository's Re-run, or undefined
 *  for a refusal of the one press: only these two codes say every later press
 *  is refused too. */
export function rerunRefusal(code: string | undefined, message: string): string | undefined {
  switch (code) {
    case "capability_unsupported":
      return message === ""
        ? "this forge cannot re-run checks for this repository."
        : `this forge cannot re-run checks for this repository (${message}).`;
    case "scope_insufficient":
      return `the token is missing a permission this needs. Add it to the token on the forge. ${message}`;
    default:
      return undefined;
  }
}

/** Whether a refusal says the row's repository moved, and where to: `to` is null
 *  when the forge could not name the successor. */
export type Moved =
  { readonly moved: false } | { readonly moved: true; readonly to: RepoSuccessor | null };

/** Read a refusal for a moved repository: its code, and the successor its body
 *  carries (the action keeps the body as the error's cause). */
export function movedRepository(err: { readonly code?: string; readonly cause?: unknown }): Moved {
  if (err.code !== "repo_ref_stale") {
    return { moved: false };
  }
  const body = err.cause;
  const succ =
    typeof body === "object" && body !== null
      ? (body as Record<string, unknown>)["successor"]
      : undefined;
  if (typeof succ !== "object" || succ === null) {
    return { moved: true, to: null };
  }
  const { repo_id: id, display_path: path } = succ as Record<string, unknown>;
  if (typeof id !== "string" || id === "" || typeof path !== "string") {
    return { moved: true, to: null };
  }
  return { moved: true, to: { repo_id: id, display_path: path } };
}

/** Whether a row should offer to arm the forge's auto-merge: the checks
 *  are not settled yet, nothing else blocks the merge, the forge is not
 *  already holding it, and the row has a head pin. "Nothing else blocks
 *  it" gates both arms: a bare pending verdict says nothing about a draft,
 *  a conflict or a policy block, which the forge would not honour when the
 *  check went green. An unnamed cause with a `yes` verdict on a non-draft
 *  row counts as nothing blocking, as it does for mergeVerdict. */
export function canArmAutoMerge(pr: GitPR): boolean {
  if (pr.action.auto_merge_armed === "yes" || pr.state !== "open" || !hasHead(pr)) {
    return false;
  }
  const cause = pr.action.merge_blocked;
  if (cause === "checks_running") {
    return true;
  }
  const clear =
    cause === "none" || (cause === "unknown" && pr.action.mergeable === "yes" && pr.draft !== true);
  return clear && pr.action.checks === "pending";
}
