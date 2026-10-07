// Mounts the forge-auth panel into #git-sources-mount and refreshes it on SSE; forge-auth.ts owns the data.

import { el } from "@cplieger/reactive";

import { withAsyncFeedback } from "./async-button.js";
import { onSSE } from "./bus.js";
import { iconEl } from "./icon-el.js";
import { ICON_REFRESH } from "./icons.js";
import { renderForgesPanel } from "./forge-auth.js";

export function initSourcesTab(): void {
  onSSE("forges_changed", () => {
    void refreshSources();
  });
  const refreshBtn = document.getElementById("git-refresh-sources-btn") as HTMLButtonElement | null;
  if (refreshBtn !== null) {
    refreshBtn.replaceChildren(iconEl(ICON_REFRESH));
    refreshBtn.addEventListener("click", () => {
      void withAsyncFeedback(refreshBtn, async () => {
        if (!(await refreshSources({ fresh: true }))) {
          throw new Error("a read failed");
        }
      });
    });
  }
  void refreshSources();
}

/** Re-render the Sources tab; the forge-auth panel fetches its own data. `fresh` re-reads every
 *  repository list from its forge. False when a read failed (`renderForgesPanel`). */
export function refreshSources(opts: { fresh?: boolean } = {}): Promise<boolean> {
  const root = document.getElementById("git-sources-mount");
  if (root === null) {
    return Promise.resolve(true);
  }
  if (root.querySelector("#forges-panel") === null) {
    const inner = el("div", {
      id: "forges-panel",
      role: "region",
      "aria-label": "Connected forges",
    });
    root.replaceChildren(inner);
  }
  return renderForgesPanel(opts);
}
