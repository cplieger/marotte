import { openTab } from "./tabs.js";
import { absPath } from "./workspace.js";

/** `activate: false` opens the tab behind the active one, with no route write. */
export function openWebPreview(path: string, opts: { activate?: boolean } = {}): void {
  const ref = absPath(path);
  void openTab(
    opts.activate === false ? { kind: "web", ref, activate: false } : { kind: "web", ref },
  ).catch(() => {
    /* the tab action reports its own failure */
  });
}
