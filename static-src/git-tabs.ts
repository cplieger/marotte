// Git view tab bar: Changes / Pull requests / Sources. Mirrors settings-tabs.ts.

import { computed, subscribe } from "@cplieger/reactive";
import type { GitTab } from "./route-path.js";
import { setGitTab, subTabOf } from "./tabs.js";
import { initSegmentedBar, segmentId } from "./segmented-bar.js";
import { setPageSubtitle } from "./page-title.js";
import { maybeEl, queryAll } from "./dom.js";
import { SharedScroll } from "./view-scroll.js";

// GitTab lives in route-path.ts, the URL source of truth; re-exported for existing callers.
export type { GitTab };

const GIT_TABS: readonly GitTab[] = ["changes", "prs", "sources"] as const;

/** Every sub-tab's panel scrolls in `#git-view`, so each sub-tab keeps its own offset. */
const panelScroll = new SharedScroll(
  () => maybeEl("git-view"),
  () => shownTab.peek() ?? "",
);

const GIT_TAB_LABELS: Readonly<Record<GitTab, string>> = {
  changes: "Changes",
  prs: "Pull requests",
  sources: "Sources",
};

/** The sub-tab the git row's route names, null while no git row is open. */
const shownTab = computed(() => subTabOf("git"));

/** Subscribe to the shown sub-tab. Fires at once when a git row is open, and never for null: with
 *  no row nothing is on screen to repaint. */
export function onGitTabChange(fn: (tab: GitTab) => void): () => void {
  return subscribe(shownTab, (tab) => {
    if (tab !== null) {
      fn(tab);
    }
  });
}

/** The shown sub-tab at this moment, or null with no git row. */
export function getGitTab(): GitTab | null {
  return shownTab.peek();
}

/** The shown sub-tab as a reactive read: inside an `effect` it subscribes. */
export function readGitTab(): GitTab | null {
  return shownTab.value;
}

/** Wire the tab buttons and panel visibility. Idempotent; acts only when the tab bar is in the DOM. */
export function initGitTabs(): void {
  const bar = maybeEl("git-tab-bar");
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
    for (const panel of queryAll<HTMLDivElement>("[data-git-panel]")) {
      const panelTab = panel.dataset["gitPanel"] ?? "";
      panel.classList.toggle("hidden", panelTab !== tab);
      panel.setAttribute("role", "tabpanel");
      panel.id = `git-panel-${panelTab}`;
      panel.setAttribute("aria-labelledby", segmentId("git", panelTab));
    }
    panelScroll.restore(tab);
    setPageSubtitle("git", GIT_TAB_LABELS[tab]);
  });
}
