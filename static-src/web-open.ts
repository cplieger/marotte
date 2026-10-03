import { openTab } from "./tabs.js";
import { absPath } from "./workspace.js";

export function openWebPreview(path: string): void {
  void openTab({ kind: "web", ref: absPath(path) }).catch(() => {
    /* the tab action reports its own failure */
  });
}
