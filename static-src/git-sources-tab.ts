// Mounts the forge-auth panel into #git-sources-mount and refreshes it on SSE; forge-auth.ts owns the data.

import { el } from "@cplieger/reactive";

import { onSSE } from "./bus.js";
import { renderForgesPanel } from "./forge-auth.js";

export function initSourcesTab(): void {
  onSSE("forges_changed", () => {
    void refreshSources();
  });
  void refreshSources();
}

/** Re-render the Sources tab; the forge-auth panel fetches its own data. */
export async function refreshSources(): Promise<void> {
  const root = document.getElementById("git-sources-mount");
  if (root === null) {
    return;
  }
  if (root.querySelector("#forges-panel") === null) {
    const inner = el("div", {
      id: "forges-panel",
      role: "region",
      "aria-label": "Connected forges",
    });
    root.replaceChildren(inner);
  }
  await renderForgesPanel();
}
