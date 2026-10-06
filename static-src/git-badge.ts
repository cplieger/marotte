// The dot on the sidebar git button, derived across every cloned repo and connected forge. Priority: error (a
// forge has last_error; red) > dirty (a repo dirty or ahead; amber) > hidden. Behind origin is not a state: nothing
// runs `git fetch`, so `behind` is stale by construction. A failed /api/forges fetch keeps the last state rather than
// raising an alarm. Owns no fetch and no timer; it paints from the two shared stores.

import { $ } from "./dom.js";
import { onGitStatusChange, currentRepos } from "./git-status-store.js";
import { initForgeStore, onForgeChange, currentForges, refreshForges } from "./forge-store.js";
import type { GitRepoStatusBadge } from "./git-types.js";
import type { ConfiguredForge } from "./wire/types.gen.js";

type BadgeState =
  { kind: "none" } | { kind: "dirty"; dirtyCount: number } | { kind: "error"; forgeIds: string[] };

type RepoStatus = GitRepoStatusBadge;
interface StatusAllResponse {
  repos?: RepoStatus[] | null;
}

let started = false;
let lastState: BadgeState = { kind: "none" };
let lastTooltip = "";

/** Wire the badge to both shared stores. Idempotent; starts no fetch, it repaints from the stores' subscriptions. */
export function initGitBadge(): void {
  if (started) {
    return;
  }
  started = true;
  initForgeStore();
  // Both stores fire immediately with their current value, and subscribing to the git store starts its one read.
  onGitStatusChange(() => {
    repaint();
  });
  onForgeChange(() => {
    repaint();
  });
}

/** Recompute the badge from current data, refreshing forges first. */
export async function refreshGitBadge(): Promise<void> {
  await refreshForges();
  repaint();
}

function repaint(): void {
  const state = deriveState({ repos: [...currentRepos()] }, currentForges());
  applyBadge(state, deriveTooltip(state));
}

/** @internal Pure derivation, exported for its own tests. */
export function deriveState(
  status: StatusAllResponse,
  forges: readonly ConfiguredForge[],
): BadgeState {
  // A forge error outranks everything: PR and clone operations would fail.
  const erroredIds: string[] = [];
  for (const f of forges) {
    if (f.connected && f.last_error !== undefined && f.last_error !== "") {
      erroredIds.push(f.id);
    }
  }
  if (erroredIds.length > 0) {
    return { kind: "error", forgeIds: erroredIds };
  }

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
    case "error": {
      const ids = state.forgeIds;
      if (ids.length === 1) {
        return `Forge auth issue: ${ids[0]!}`; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      }
      return `${ids.length} forges with auth issues`;
    }
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
