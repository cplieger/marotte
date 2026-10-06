// The PR tab's canonical state: each connection's inventory entry as the poller wrote it, and the rows an optimistic
// close or merge hides. Groups are derived on every read, so the entries stay the one copy. Its own module to break
// the git-prs-tab.ts / actions/git-prs.ts cycle.

import { join } from "@cplieger/keyenc";

import type { GitPR as PR, GitRepoGroup as RepoGroup } from "./git-types.js";
import type {
  CloneRepo,
  ConfiguredForge,
  InventoryEntry,
  InventoryList,
} from "./wire/types.gen.js";

export interface PRRemoveResult {
  group: RepoGroup;
  pr: PR;
}

/** A pull request the account opened in a repository none of its connection's
 *  owners hold: the tab lists it read only, as a link to the forge. */
export interface ElsewherePR {
  forge_id: string;
  forge_kind: ConfiguredForge["kind"];
  forge_host: string;
  pr: PR;
}

/**
 * A hidden row's connection and the cycle its mutation named. An entry from that cycle or later is the forge's answer
 * about the row; an earlier one may predate the mutation.
 */
interface Mark {
  forgeId: string;
  floor: string | undefined;
}

const entries = new Map<string, InventoryEntry>();
const hidden = new Map<string, Mark>();
const forges = new Map<string, ConfiguredForge>();
let held = false;
let paintFn: (() => void) | null = null;

/** Wire the repaint callback (called once by git-prs-tab at init). */
export function bindPRPaint(paint: () => void): void {
  paintFn = paint;
}

/** Whether `a` names a later cycle than `b`. Both are decimal cycle ids with no
 *  leading zeros, so the longer is the larger and equal lengths compare as text. */
export function cycleAfter(a: string, b: string): boolean {
  return a.length !== b.length ? a.length > b.length : a > b;
}

/** The connection rows the derived groups take their kind and host from. */
export function setPRForges(rows: readonly ConfiguredForge[]): void {
  forges.clear();
  for (const f of rows) {
    forges.set(f.id, f);
  }
}

/** The connected connections, in id order: the order the inventory answers in. */
export function connectedForges(): ConfiguredForge[] {
  return [...forges.values()].filter((f) => f.connected).sort((a, b) => cmp(a.id, b.id));
}

/** The connections that need a new sign-in, in id order. The inventory reads
 *  none of them, so the tab names each one rather than dropping it. */
export function lapsedForges(): ConfiguredForge[] {
  return [...forges.values()]
    .filter((f) => !f.connected && f.reconnect_required)
    .sort((a, b) => cmp(a.id, b.id));
}

/**
 * Adopt a read of the whole inventory. An omitted entry is dropped; an entry is replaced when the list's is from a
 * later cycle, or always on `reset` (a reconcile, which also covers a restarted cycle count). Answers the indexes of
 * the adopted entries.
 */
export function applyInventoryList(list: InventoryList, opts: { reset?: boolean } = {}): number[] {
  const named = new Set(list.entries.map((e) => e.forge_id));
  for (const id of [...entries.keys()]) {
    if (!named.has(id)) {
      entries.delete(id);
      dropMarks(id);
    }
  }
  const adopted: number[] = [];
  list.entries.forEach((e, i) => {
    const cur = entries.get(e.forge_id);
    if (opts.reset === true || cur === undefined || cycleAfter(e.cycle_id, cur.cycle_id)) {
      entries.set(e.forge_id, e);
      if (opts.reset === true) {
        dropMarks(e.forge_id);
      } else {
        settleMarks(e);
      }
      adopted.push(i);
    }
  });
  held = true;
  return adopted;
}

/** Adopt one connection's entry from a forge_inventory frame. A frame whose
 *  cycle is not after the held entry's is a late one and is ignored. Answers
 *  whether the entry was replaced. */
export function applyInventoryEntry(e: InventoryEntry): boolean {
  const cur = entries.get(e.forge_id);
  if (cur !== undefined && !cycleAfter(e.cycle_id, cur.cycle_id)) {
    return false;
  }
  entries.set(e.forge_id, e);
  settleMarks(e);
  return true;
}

/** The held entry of one connection. */
export function heldEntry(forgeId: string): InventoryEntry | undefined {
  return entries.get(forgeId);
}

/** Whether the tab has read the inventory at least once. */
export function inventoryHeld(): boolean {
  return held;
}

/** The workspace clone checked out in `dir`, as the inventory joined it. */
export function cloneInDir(dir: string): CloneRepo | undefined {
  for (const e of entries.values()) {
    const c = e.clones.find((x) => x.dir === dir);
    if (c !== undefined) {
      return c;
    }
  }
  return undefined;
}

/** The directory of a workspace clone of the repository, or undefined. */
export function cloneDirOf(forgeId: string, repoId: string): string | undefined {
  return entries.get(forgeId)?.clones.find((c) => c.repo_id === repoId)?.dir;
}

/**
 * The repository groups the tab paints: every row of each connection's owner and added scopes, and every authored row
 * under one of its owners, grouped by `repo_id` and sorted by path. A loading or failed connection has no groups.
 */
export function getPRGroups(): RepoGroup[] {
  return listed()
    .flatMap((d) => d.groups)
    .sort((a, b) => cmp(a.full_name, b.full_name) || cmp(a.forge_id, b.forge_id));
}

/** The authored rows no group lists, by display path, newest first. */
export function getElsewherePRs(): ElsewherePR[] {
  return listed()
    .flatMap((d) => d.elsewhere)
    .sort(
      (a, b) =>
        cmp(a.pr.repo, b.pr.repo) || b.pr.number - a.pr.number || cmp(a.forge_id, b.forge_id),
    );
}

interface Derived {
  groups: RepoGroup[];
  elsewhere: ElsewherePR[];
}

function listed(): Derived[] {
  const out: Derived[] = [];
  for (const e of [...entries.values()].sort((a, b) => cmp(a.forge_id, b.forge_id))) {
    const f = forges.get(e.forge_id);
    if (f !== undefined && (e.state === "ready" || e.state === "partial")) {
      out.push(derive(e, f));
    }
  }
  return out;
}

/** A row two scopes carry is listed once, and the scopes are read first, so a row they carry is never elsewhere. */
function derive(e: InventoryEntry, f: ConfiguredForge): Derived {
  const owners = listedOwners(e, f);
  const byRepo = new Map<string, RepoGroup>();
  const elsewhere: ElsewherePR[] = [];
  const seen = new Set<string>();
  const scopes = [
    ...e.scopes.filter((s) => s.scope === "owner" || s.scope === "added"),
    ...e.scopes.filter((s) => s.scope === "authored"),
  ];
  for (const s of scopes) {
    for (const pr of s.rows) {
      const key = hiddenKey(e.forge_id, pr.repo_id, pr.number);
      if (seen.has(key) || hidden.has(key)) {
        continue;
      }
      seen.add(key);
      if (s.scope === "authored" && !underOwner(owners, pr.repo)) {
        elsewhere.push({ forge_id: f.id, forge_kind: f.kind, forge_host: f.host, pr });
        continue;
      }
      let g = byRepo.get(pr.repo_id);
      if (g === undefined) {
        const cut = pr.repo.lastIndexOf("/");
        g = {
          forge_id: f.id,
          forge_kind: f.kind,
          forge_host: f.host,
          repo_id: pr.repo_id,
          owner: cut < 0 ? "" : pr.repo.slice(0, cut),
          name: pr.repo.slice(cut + 1),
          full_name: pr.repo,
          prs: [],
        };
        byRepo.set(pr.repo_id, g);
      }
      g.prs.push(pr);
    }
  }
  for (const g of byRepo.values()) {
    g.prs.sort((a, b) => b.number - a.number);
  }
  return { groups: [...byRepo.values()], elsewhere };
}

/**
 * The listed owners, lowercased (forges compare without case): each scope's owner and the login. On GitLab, which
 * refuses the login's owner scope, the authored rows under it stand in.
 */
function listedOwners(e: InventoryEntry, f: ConfiguredForge): string[] {
  const out: string[] = [];
  for (const s of e.scopes) {
    if ((s.scope === "owner" || s.scope === "added") && s.owner !== undefined && s.owner !== "") {
      out.push(s.owner.toLowerCase());
    }
  }
  if (f.username !== undefined && f.username !== "") {
    out.push(f.username.toLowerCase());
  }
  return out;
}

/** A GitLab group's subgroups are its own, as its list reads them. */
function underOwner(owners: readonly string[], path: string): boolean {
  const p = path.toLowerCase();
  return owners.some((o) => p.startsWith(`${o}/`));
}

/** Hide a row by identity (connection and `repo_id`), and repaint. The row
 *  stays hidden until an entry omits it or `settleRemoval` names the cycle
 *  that decides it. Returns what the rollback and the settle need, or
 *  undefined if the row is not on the list. */
export function removePRFromGroups(
  forgeId: string,
  repoId: string,
  prNumber: number,
): PRRemoveResult | undefined {
  const group = getPRGroups().find((g) => g.forge_id === forgeId && g.repo_id === repoId);
  const pr = group?.prs.find((p) => p.number === prNumber);
  if (group === undefined || pr === undefined) {
    return undefined;
  }
  hidden.set(hiddenKey(forgeId, repoId, prNumber), { forgeId, floor: undefined });
  paintFn?.();
  return { group, pr };
}

/** Record the cycle a hidden row's mutation named. When the held entry is
 *  already from it or later, that entry decides the row now. */
export function settleRemoval(result: PRRemoveResult, cycleId: string): void {
  const forgeId = result.group.forge_id;
  const key = hiddenKey(forgeId, result.group.repo_id, result.pr.number);
  const mark = hidden.get(key);
  if (mark === undefined) {
    return;
  }
  const cur = entries.get(forgeId);
  if (cur !== undefined && !cycleAfter(cycleId, cur.cycle_id)) {
    hidden.delete(key);
    paintFn?.();
    return;
  }
  mark.floor = cycleId;
}

/** Show a hidden row again (rollback). A second call, or one after an entry
 *  ended the mark, changes nothing. */
export function reinsertPRInGroups(result: PRRemoveResult): void {
  if (hidden.delete(hiddenKey(result.group.forge_id, result.group.repo_id, result.pr.number))) {
    paintFn?.();
  }
}

/** Forget everything held, for a suite's next case. */
export function _resetForTest(): void {
  entries.clear();
  hidden.clear();
  forges.clear();
  held = false;
}

function dropMarks(forgeId: string): void {
  for (const [key, mark] of [...hidden]) {
    if (mark.forgeId === forgeId) {
      hidden.delete(key);
    }
  }
}

/** End the marks `e` decides: a row it omits, and a row whose mutation named
 *  `e`'s cycle or an earlier one. */
function settleMarks(e: InventoryEntry): void {
  let listed: Set<string> | undefined;
  for (const [key, mark] of [...hidden]) {
    if (mark.forgeId !== e.forge_id) {
      continue;
    }
    if (mark.floor !== undefined && !cycleAfter(mark.floor, e.cycle_id)) {
      hidden.delete(key);
      continue;
    }
    listed ??= new Set(
      e.scopes.flatMap((s) => s.rows.map((pr) => hiddenKey(e.forge_id, pr.repo_id, pr.number))),
    );
    if (!listed.has(key)) {
      hidden.delete(key);
    }
  }
}

function hiddenKey(forgeId: string, repoId: string, prNumber: number): string {
  return join(forgeId, repoId, String(prNumber));
}

function cmp(a: string, b: string): number {
  return a.localeCompare(b);
}
