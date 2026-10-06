// One router from a transcript SUBJECT to the surface that answers it. Never the
// shell panel: it is a live PTY whose next frame can interleave with anything written.

import { openFile, openFileDiff, openFileGitDiff } from "./editor-openers.js";
import {
  activateTab,
  openGitView,
  openTab,
  parentChatRef,
  setTabParent,
  tabIdFor,
} from "./tabs.js";
import { isSafeURL } from "./url-safety.js";
import { absPath } from "./workspace.js";

/** Open a file's change vs `ref`: the write already landed, so git holds the
 *  before. The one place the two path spaces cross: a workspace-relative path is
 *  made absolute here, or the granted-roots allow-list answers 403. */
export function openChange(path: string, ref = "HEAD"): void {
  if (path === "") {
    return;
  }
  openFileGitDiff(absPath(path), ref);
}

/** Open a tool call's own before/after pair. The path is relative (see openChange). */
export function openCallDiff(path: string, oldText: string, newText: string): void {
  if (path === "") {
    return;
  }
  openFileDiff(absPath(path), oldText, newText);
}

/** Open the git view's changes tab. Shows the WORKING TREE's changes, not only
 *  the turn's. */
export function openChangeSet(): void {
  void openGitView("changes");
}

/** Open the editor at a line; a relative path is normalised as in openChange. */
export function openAtLine(path: string, line?: number): void {
  if (path === "") {
    return;
  }
  openFile(absPath(path), line);
}

/** Open `dir`'s spec tab, nested under `chatID`'s tab when given. `open_tab`
 *  discards `parent` on a second open, so the parent goes through `reparent_tab`. */
export async function openSpec(dir: string, chatID?: string): Promise<void> {
  if (dir === "") {
    return;
  }
  const parent = chatID === undefined || chatID === "" ? "" : tabIdFor("chat", chatID);
  const existing = tabIdFor("spec", dir);
  if (existing !== "" && parent !== "" && parentChatRef(existing) === "") {
    await setTabParent(existing, parent);
    activateTab(existing);
    return;
  }
  await openTab(
    parent === ""
      ? { kind: "spec", ref: dir, owns: false }
      : { kind: "spec", ref: dir, parent, owns: false },
  );
}

/** Never auto-opens (an SSE-driven `window.open` is popup-blocked). False when the
 *  URL is unsafe, so the caller renders plain text. */
export function openExternal(url: string): boolean {
  if (!isSafeURL(url)) {
    return false;
  }
  window.open(url, "_blank", "noopener,noreferrer");
  return true;
}
