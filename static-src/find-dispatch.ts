// Find belongs to the active tab, and so does the toolbar button: the chord opens the tab's find, and a second press
// from inside its box goes to the browser's native find.

import { getActiveTabKind } from "./tabs.js";
import type { TabKind } from "./tabs.js";
import { handleFindHotkey, toggleChatFind } from "./find-in-chat.js";
import { handleFindInFilesHotkey, toggleFilesSearch } from "./files-search.js";
import { handleEditorFindHotkey, toggleEditorFind, editorFindAvailable } from "./editor-find.js";
import { pageFind } from "./find-registry.js";
import type { FindKind } from "./find-registry.js";

type FindDestination =
  /** The transcript popup (find-in-chat.ts). */
  | "transcript"
  /** The in-flow buffer bar (editor-find.ts), which declines per surface. */
  | "editor"
  /** The in-flow recursive search panel (files-search.ts). */
  | "files"
  /** A registered page popup (search-popup.ts, via find-registry.ts). */
  | "page"
  /** Nothing to search or filter on this page at all. */
  | "none";

/** One table over every tab kind: a `Record<TabKind, …>`, not a switch with a default, so a new kind must state its answer. */
const DESTINATION: Readonly<Record<TabKind, FindDestination>> = {
  chat: "transcript",
  editor: "editor",
  files: "files",
  docs: "page",
  history: "page",
  git: "page",
  run: "none",
  subagent: "none",
  spec: "none",
  settings: "none",
  // A cross-origin frame cannot be searched from the app.
  web: "none",
};

/** The three built-ins all search: each reaches past what is on screen. */
const BUILTIN_KIND: Readonly<Record<FindDestination, FindKind>> = {
  transcript: "search",
  editor: "search",
  files: "search",
  page: "filter",
  none: "filter",
};

/** No tab open reads as the transcript, which the app shows then. */
function destination(): FindDestination {
  const kind = getActiveTabKind();
  return kind === null ? "transcript" : DESTINATION[kind];
}

/** Route Ctrl-F / Cmd-F to the active tab's find. Capture phase, so native find is pre-empted. */
export function handleFindKey(e: KeyboardEvent): void {
  switch (destination()) {
    case "editor":
      // Over a non-source surface both handlers decline, so native find gets the key.
      if (handleEditorFindHotkey(e)) {
        return;
      }
      handleFindHotkey(e);
      return;
    case "files":
      handleFindInFilesHotkey(e);
      return;
    case "page":
      if (openRegistered(e)) {
        return;
      }
      handleFindHotkey(e);
      return;
    case "none":
      // Nothing to search: the chord is the browser's.
      return;
    default:
      handleFindHotkey(e);
  }
}

/** Toggle the find that belongs to the active tab. What `#find-btn` means. */
export function toggleFindForActiveTab(): void {
  switch (destination()) {
    case "editor":
      toggleEditorFind();
      return;
    case "files":
      toggleFilesSearch();
      return;
    case "page":
      pageFind(getActiveTabKind() ?? "")?.toggle();
      return;
    case "none":
      return;
    default:
      toggleChatFind();
  }
}

/** What the toolbar magnifier paints for the active tab, from one table read so the two halves cannot disagree. */
export function findAffordanceForActiveTab(): { available: boolean; kind: FindKind } {
  // Read the registry first: `pageFind` is what subscribes the caller's effect to registration.
  const find = pageFind(getActiveTabKind() ?? "");
  const dest = destination();
  if (dest === "page") {
    if (find === undefined) {
      return { available: false, kind: BUILTIN_KIND.page };
    }
    return { available: find.available?.() ?? true, kind: find.kind() };
  }
  const available = dest === "editor" ? editorFindAvailable() : dest !== "none";
  return { available, kind: BUILTIN_KIND[dest] };
}

/** The shared chord guard, so pre-checks use the same test. */
function isFindChord(e: KeyboardEvent): boolean {
  return e.key.toLowerCase() === "f" && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey;
}

/** Consumes the chord only when the popup accepted; a press from inside the open box goes to native find. */
function openRegistered(e: KeyboardEvent): boolean {
  if (!isFindChord(e)) {
    return false;
  }
  const find = pageFind(getActiveTabKind() ?? "");
  if (find === undefined || find.focused()) {
    return false;
  }
  const opened = find.open();
  if (opened) {
    e.preventDefault();
  }
  return opened;
}
