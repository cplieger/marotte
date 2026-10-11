// The dot on the sidebar git button, derived across every cloned repo: amber when a repo is dirty or ahead, else
// hidden. Behind origin is not a state: nothing runs `git fetch`, so `behind` is stale by construction. Owns no fetch
// and no timer; it paints from the shared git status store.

import { $ } from "./dom.js";
import { onGitStatusChange, currentRepos } from "./git-status-store.js";
import { initForgeStore, refreshForges } from "./forge-store.js";
import type { GitRepoStatusBadge } from "./git-types.js";

type BadgeState = { kind: "none" } | { kind: "dirty"; dirtyCount: number };

type RepoStatus = GitRepoStatusBadge;
interface StatusAllResponse {
  repos?: RepoStatus[] | null;
}

let started = false;
let lastState: BadgeState = { kind: "none" };
let lastTooltip = "";

/** Wire the badge to the git status store and start the shared forge poll. Idempotent. */
export function initGitBadge(): void {
  if (started) {
    return;
  }
  started = true;
  // No other init path starts the forge poll, and the PRs tab reads the forge list it keeps fresh.
  initForgeStore();
  // The store fires immediately with its current value, and subscribing starts its one read.
  onGitStatusChange(() => {
    repaint();
  });
}

/** Refresh the shared forge list after an agent turn, and repaint the badge from current data. */
export async function refreshGitBadge(): Promise<void> {
  await refreshForges();
  repaint();
}

function repaint(): void {
  const state = deriveState({ repos: [...currentRepos()] });
  applyBadge(state, deriveTooltip(state));
}

/** @internal Pure derivation, exported for its own tests. */
export function deriveState(status: StatusAllResponse): BadgeState {
  // `ahead` counts: an unpushed commit is local work the reader still owns.
  let dirtyCount = 0;
  for (const r of status.repos ?? []) {
    if (!r.is_repo) {
      continue;
    }
    if (r.has_dirty || r.ahead > 0) {
      dirtyCount++;
    }
  }
  if (dirtyCount > 0) {
    return { kind: "dirty", dirtyCount };
  }
  return { kind: "none" };
}

/** @internal Tooltip text derived from the same data, exported for its own tests. */
export function deriveTooltip(state: BadgeState): string {
  switch (state.kind) {
    // "local changes", not "uncommitted": a repo whose only change is an unpushed commit is committed.
    case "dirty":
      return `${state.dirtyCount} repo${state.dirtyCount === 1 ? "" : "s"} with local changes`;
    case "none":
      return "";
  }
}

function applyBadge(state: BadgeState, tooltip: string): void {
  if (state.kind === lastState.kind && tooltip === lastTooltip) {
    return;
  }
  lastState = state;
  lastTooltip = tooltip;
  const el = $.gitBadge;
  if (state.kind === "none") {
    el.classList.add("hidden");
    el.removeAttribute("data-state");
    const btn = el.parentElement;
    if (btn !== null) {
      btn.setAttribute("data-tooltip", "Toggle git");
    }
    return;
  }
  el.classList.remove("hidden");
  el.dataset["state"] = state.kind;
  // The badge has pointer-events: none, so hover lands on the parent button; the tooltip goes there.
  const btn = el.parentElement;
  if (btn !== null) {
    btn.setAttribute("data-tooltip", tooltip);
  }
}
