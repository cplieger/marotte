// Mounts the forge-auth panel into #git-sources-mount; the panel repaints from forge-store.ts on its own.

import { el } from "@cplieger/reactive";

import { withAsyncFeedback } from "./async-button.js";
import { iconEl } from "./icon-el.js";
import { ICON_REFRESH } from "./icons.js";
import { renderForgesPanel } from "./forge-auth.js";
import { maybeEl } from "./dom.js";

export function initSourcesTab(): void {
  const refreshBtn = maybeEl<HTMLButtonElement>("git-refresh-sources-btn");
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

/** Mount the Sources panel and read its data again. `fresh` re-reads every repository list from its forge. False
 *  when a read failed (`renderForgesPanel`). */
export function refreshSources(opts: { fresh?: boolean } = {}): Promise<boolean> {
  const root = maybeEl("git-sources-mount");
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
