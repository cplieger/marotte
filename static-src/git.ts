// Git view orchestrator: a tabbed dashboard across every cloned repo (Changes, Pull requests, Sources). Each tab module
// owns its fetch, render and wiring; this wires the tab nav and the initial loads.

import { initGitTabs, onGitTabChange, getGitTab, readGitTab, type GitTab } from "./git-tabs.js";
import { initChangesTab, refreshChanges, changesFind } from "./git-changes-tab.js";
import { initPRsTab, prsFind } from "./git-prs-tab.js";
import { refreshPRs } from "./actions/git-prs.js";
import { initSourcesTab, refreshSources } from "./git-sources-tab.js";
import { initGitBadge, refreshGitBadge as refreshBadgeImpl } from "./git-badge.js";
import { refreshGitStatus } from "./git-status-store.js";
import { registerFind } from "./find-registry.js";
import type { PageFind } from "./find-registry.js";
import type { SearchPopup } from "./search-popup.js";

/**
 * Find routed to the active panel, as docs.ts does for its sub-tabs. Sources declines: `open` answers false so the
 * chord falls through to the browser's find, and `available` collapses the toolbar's magnifier.
 */
const gitFind: PageFind = {
  open: () => activeFind()?.open() ?? false,
  toggle: () => {
    activeFind()?.toggle();
  },
  focused: () => activeFind()?.focused() ?? false,
  // Both panels filter rows already fetched, so the toolbar shows a funnel. The fallback is never rendered.
  kind: () => activeFind()?.kind() ?? "filter",
  available: () => activeFind() !== null,
};

function activeFind(): SearchPopup | null {
  // The reactive read, so the toolbar's affordance effect re-runs on a sub-tab switch.
  switch (readGitTab()) {
    case "changes":
      return changesFind;
    case "prs":
      return prsFind;
    default:
      return null;
  }
}

let initialized = false;

/** Wire and paint the git view. Idempotent and fetchless: `tabs.ts` calls `refreshGitView` right after. */
export function initGitPanel(): void {
  if (!initialized) {
    initialized = true;
    initGitTabs();
    initChangesTab();
    initPRsTab();
    initSourcesTab();
    initGitBadge();
    // Through the leaf registry: importing find-dispatch would drag find-in-chat and scroll.ts's self-initialising
    // singleton into the git view.
    registerFind("git", gitFind);

    // The whole callback is gated on `painted`: the attach-time fire is a DOM sync, not a switch.
    onGitTabChange((tab) => {
      if (painted) {
        // A filter belongs to one panel, so the box closes on a switch; closing clears the query, so switching back finds the
        // panel whole.
        changesFind.close();
        prsFind.close();
        refreshGitTab(tab);
      }
      painted = true;
    });
  }
}

/** Same gate, same reason, as `settings-tabs.ts`'s. */
let painted = false;

function refreshGitTab(tab: GitTab): void {
  switch (tab) {
    case "changes":
      // A sub-tab arrival is explicit navigation, so it opts into the per-repo `git fetch`.
      void refreshChanges(true);
      break;
    case "prs":
      // No force here: the rows are the poller's inventory, so arriving reads memory and costs no forge request; the
      // refresh button asks for a cycle.
      void refreshPRs.dispatch();
      break;
    case "sources":
      void refreshSources();
      break;
  }
}

/**
 * Refetch the active sub-tab: a git tab's `refresh` and the invalidation triggers' way in. Runs `initGitPanel` first,
 * which is one-shot.
 */
export function refreshGitView(): void {
  initGitPanel();
  refreshGitTab(getGitTab());
}

/** Boot entry used by app.ts; the name predates the multi-repo view. */
export function loadGitRepos(): void {
  initGitPanel();
}

/** Refresh the Changes view and the sidebar badge, after an agent turn that touched files. */
export function refreshGitBadge(): void {
  void refreshChanges();
  void refreshBadgeImpl();
}

/**
 * Mark git state dirty so every git surface refetches: a write to the tree is the fact that it changed. `paths` are
 * the workspace-relative paths the caller knows changed, narrowing the scan to their repositories; omit them only when
 * the caller cannot name what moved, since a wrong path scopes the scan away from the real change.
 */
export function markGitDirty(paths?: readonly string[]): void {
  void refreshGitStatus(paths);
  void refreshChanges();
  void refreshBadgeImpl();
}
