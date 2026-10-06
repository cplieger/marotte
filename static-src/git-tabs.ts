// Git view tab bar: Changes / Pull requests / Sources. Mirrors settings-tabs.ts.

import { signal, subscribe } from "@cplieger/reactive";
import { pushRoute } from "./router.js";
import type { GitTab } from "./route-path.js";
import { setGitTab as setGitTabRoute } from "./tabs.js";
import { initSegmentedBar } from "./segmented-bar.js";
import { setPageSubtitle } from "./page-title.js";

// GitTab lives in route-path.ts, the URL source of truth; re-exported for existing callers.
export type { GitTab };

const GIT_TABS: readonly GitTab[] = ["changes", "prs", "sources"] as const;

const GIT_TAB_LABELS: Readonly<Record<GitTab, string>> = {
  changes: "Changes",
  prs: "Pull requests",
  sources: "Sources",
};

type Listener = (tab: GitTab) => void;

// Deduped: a same-value write is a no-op. subscribe() fires immediately on attach to init panel visibility.
const activeTab = signal<GitTab>("changes");

/** Subscribe to tab changes. Fires immediately with the current tab. */
export function onGitTabChange(fn: Listener): () => void {
  return subscribe(activeTab, fn);
}

/**
 * Switch to a tab; no-op if already active. Pushes the URL and syncs the git view tab's route, so the sub-tabs are
 * deep-linkable and back/forward navigable.
 */
export function setGitTab(tab: GitTab): void {
  if (tab === activeTab.peek()) {
    return;
  }
  setGitTabRoute(tab);
  pushRoute({ kind: "git", tab });
  activeTab.value = tab;
}

/** Current active tab. */
export function getGitTab(): GitTab {
  return activeTab.peek();
}

/**
 * The active sub-tab as a reactive read: inside an `effect` it subscribes. `getGitTab` peeks on purpose, since most
 * callers want the value at a moment.
 */
export function readGitTab(): GitTab {
  return activeTab.value;
}

/**
 * Force the active sub-tab without pushing a URL, for the router on back/forward to /git/<tab>. Safe before the git
 * view tab exists.
 */
export function forceGitTab(tab: GitTab): void {
  setGitTabRoute(tab);
  activeTab.value = tab;
}

/** Wire the tab buttons and panel visibility. Idempotent; acts only when the tab bar is in the DOM. */
export function initGitTabs(): void {
  const bar = document.getElementById("git-tab-bar");
  if (bar === null) {
    return;
  }

  const paint = initSegmentedBar(bar, {
    attr: "data-git-tab",
    idPrefix: "git",
    tabs: GIT_TABS.map((id) => ({ id, label: GIT_TAB_LABELS[id] })),
    onSelect: setGitTab,
  });

  onGitTabChange((tab) => {
    paint(tab);
    // The panel half of the pairing the controller's `aria-controls` writes.
    for (const panel of document.querySelectorAll<HTMLDivElement>("[data-git-panel]")) {
      const panelTab = panel.dataset["gitPanel"] ?? "";
      panel.classList.toggle("hidden", panelTab !== tab);
      panel.setAttribute("role", "tabpanel");
      panel.id = `git-panel-${panelTab}`;
      panel.setAttribute("aria-labelledby", `git-tab-${panelTab}`);
    }
    setPageSubtitle("git", GIT_TAB_LABELS[tab]);
  });
}
