// Shared git wire types for /api/git/* and /api/forges/* responses.

import type { ForgeKind } from "./forge-types.js";
import type { FieldFill } from "./wire/types.gen.js";

/**
 * A single file entry from git status. One path can produce two entries, one per side of the index, so stage,
 * unstage and discard act per side; count changed files with `changedPathCount`, never `files.length`. `orig_path`
 * rides only a rename or copy entry (not the worktree half of a staged rename).
 */
export interface GitFileEntry {
  path: string;
  status: string;
  staged: boolean;
  display: string;
  orig_path?: string;
}

/** Per-repo status from /api/git/status-all. */
export interface GitRepoStatus {
  repo: string;
  is_repo: boolean;
  branch: string;
  ahead: number;
  behind: number;
  files: GitFileEntry[];
  has_dirty: boolean;
  stashes: number;
}

/** Subset of GitRepoStatus used by the badge (no files array needed). */
export type GitRepoStatusBadge = Pick<
  GitRepoStatus,
  "repo" | "is_repo" | "branch" | "ahead" | "behind" | "has_dirty"
>;

/**
 * What one Pull-all pass did to one repository. Four exclusive verdicts (internal/git/handlers_pullall.go):
 * `pulled`, `blocked` (`reason` names the hazard), `failed` (`detail` carries git's words), `skipped`. Plain strings:
 * the server's vocabulary can grow, and a union would make the fallbacks read as dead code.
 */
export interface GitPullResult {
  repo: string;
  verdict: string;
  reason?: string;
  detail?: string;
}

/** Whether a repo was left un-pulled in a way a reader must act on. `pulled` and `skipped` get the summary count only. */
export function isPullHeld(r: GitPullResult): boolean {
  return r.verdict === "blocked" || r.verdict === "failed";
}

/** The one-line outcome of a Pull-all pass. Held repos are counted here and named on their own blocks. */
export function summarizePullAll(results: readonly GitPullResult[]): string {
  const pulled = results.filter((r) => r.verdict === "pulled").length;
  const held = results.filter(isPullHeld).length;
  if (pulled === 0 && held === 0) {
    return "Nothing to pull";
  }
  const first = pulled === 1 ? "Pulled 1 repo" : `Pulled ${String(pulled)} repos`;
  return held === 0 ? first : `${first}, ${String(held)} left alone`;
}

/** Short enough for a collapsed row, specific enough to name the hazard. An unknown reason falls back to the plain fact. */
const PULL_HELD_WORDS: Readonly<Record<string, string>> = {
  in_progress: "mid-merge",
  conflict: "conflict",
  unreadable: "unreadable",
  diverged: "diverged",
  local_changes: "local changes",
};

export function pullHeldWord(r: GitPullResult): string {
  if (r.verdict === "failed") {
    return "pull failed";
  }
  return PULL_HELD_WORDS[r.reason ?? ""] ?? "not pulled";
}

/**
 * What a PR row's controls read; every member is always present. Enumerated members are plain strings: the server's
 * vocabulary can grow, and a union would make git-pr-status.ts's fallbacks read as dead code.
 */
export interface GitPRAction {
  /** "yes" | "no" | "unknown". */
  mergeable: string;
  /** "unknown" | "passing" | "failing" | "pending" | "neutral". */
  checks: string;
  checks_failing: number;
  checks_total: number;
  /** "yes" | "no" | "unknown": the forge will merge this itself once its
   *  requirements are met. */
  auto_merge_armed: string;
  queue_state: string;
  /** -1 when the row is in no known queue position. */
  queue_position: number;
  /** "unknown" | "none" | "draft" | "conflicts" | "checks_failing"
   *  | "checks_running" | "behind" | "blocked". */
  merge_blocked: string;
}

/** A pull request from the forge API. */
export interface GitPR {
  /** The repository's id, the segment its routes take. */
  repo_id: string;
  /** The repository's display path. */
  repo: string;
  number: number;
  title: string;
  /** "open" | "closed" | "merged" | "unknown"; a draft is `draft`, never a state. */
  state: string;
  draft?: boolean;
  source_branch: string;
  target_branch: string;
  url?: string;
  author?: string;
  updated_at?: number;
  /** Head commit of the source branch; the merge pins itself to this. */
  head_sha?: string;
  action: GitPRAction;
  /** Why each field this family's list lacks holds its value: present on an
   *  inventory row of GitLab and the Gitea family. */
  fill?: readonly FieldFill[];
}

/** One repository's open pull requests on one connection (the PRs tab's
 *  section), derived from the inventory by git-prs-state.ts. */
export interface GitRepoGroup {
  forge_id: string;
  forge_kind: ForgeKind;
  /** The repository's id, the segment its routes take. */
  repo_id: string;
  /** The display path before its last `/` (a GitLab namespace path). */
  owner: string;
  name: string;
  /** The display path. */
  full_name: string;
  prs: GitPR[];
}

/**
 * Every status character `git status --porcelain=v1` emits, mirroring internal/git/parse.go statusLabels so a tooltip
 * never disagrees with the label beside it.
 */
const GIT_STATUS_LABELS: Readonly<Record<string, string>> = {
  M: "Modified",
  T: "Typechange",
  A: "Added",
  D: "Deleted",
  R: "Renamed",
  C: "Copied",
  "?": "Untracked",
  U: "Unmerged",
};

export function statusLetter(s: string): string {
  if (s.length >= 1) {
    return s.charAt(0);
  }
  return "?";
}

export function describeStatus(s: string): string {
  return GIT_STATUS_LABELS[s.charAt(0)] ?? s;
}

/**
 * How many entries `git stash push` would take. Not `files.length`: the server stashes without `-u`
 * (internal/git/handlers_sync.go) while the status parse runs `-uall`, so untracked entries are not stashable.
 */
export function stashableCount(files: readonly GitFileEntry[]): number {
  return files.filter((f) => statusLetter(f.status) !== "?").length;
}

// Entries are per side of the index; a person counts files. Within one side the server emits at most one entry per
// path, so these guard that invariant from internal/git/parse.go rather than dedup live.

/** The distinct paths, in first-seen order; also the shape a bulk mutation payload wants. */
export function distinctPaths(files: readonly GitFileEntry[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const f of files) {
    if (!seen.has(f.path)) {
      seen.add(f.path);
      out.push(f.path);
    }
  }
  return out;
}

/** How many FILES these entries describe. */
export function changedPathCount(files: readonly GitFileEntry[]): number {
  return new Set(files.map((f) => f.path)).size;
}

/**
 * Paths on both sides of the index (staged, then changed again). They render as two rows in different groups, so the
 * panel marks both from this set.
 */
export function partiallyStagedPaths(files: readonly GitFileEntry[]): ReadonlySet<string> {
  const staged = new Set<string>();
  const unstaged = new Set<string>();
  for (const f of files) {
    (f.staged ? staged : unstaged).add(f.path);
  }
  const both = new Set<string>();
  for (const p of staged) {
    if (unstaged.has(p)) {
      both.add(p);
    }
  }
  return both;
}
