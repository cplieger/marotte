// File browser state, DOM-free.

import { FB_ROOT, type FileEntry } from "./files-shared.js";

export class FileBrowserState {
  currentPath: string;
  history: string[];
  historyIdx = 0;
  selected = new Set<string>();
  lastClickedName = "";
  entries: FileEntry[] = [];
  /** `entries` starts as `[]`, so an empty directory and an unread one look alike; the placeholder arms only for the latter. */
  answered = false;
  entryMap = new Map<string, FileEntry>();
  dirWritable = true;
  sortedNames: string[] = [];
  /** Every files tab paints into one shared scroller, so the offset lives here; a directory change resets it. */
  scrollTop = 0;

  /**
   * True until the origin folder loads, so an unreachable origin falls back to the mounts listing once. Per browser:
   * N browsers share one fetch holder.
   */
  pendingRestore = true;

  /** A browser opened at `at` with both nav buttons disabled (`navigate` would push and enable Back). */
  constructor(at: string = FB_ROOT) {
    this.currentPath = at;
    this.history = [at];
  }

  navigate(path: string): void {
    this.currentPath = path;
    this.answered = false;
    this.scrollTop = 0;
    this.selected.clear();
    this.lastClickedName = "";
    this.history.length = this.historyIdx + 1;
    this.history.push(path);
    this.historyIdx = this.history.length - 1;
  }

  goBack(): boolean {
    if (this.historyIdx <= 0) {
      return false;
    }
    this.historyIdx--;
    this.currentPath = this.history[this.historyIdx]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    this.scrollTop = 0;
    this.selected.clear();
    this.lastClickedName = "";
    return true;
  }

  goForward(): boolean {
    if (this.historyIdx >= this.history.length - 1) {
      return false;
    }
    this.historyIdx++;
    this.currentPath = this.history[this.historyIdx]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    this.scrollTop = 0;
    this.selected.clear();
    this.lastClickedName = "";
    return true;
  }

  /**
   * Point at a directory from outside the trail (history entry, deep link). Adjacent-first: while active, the document
   * trail and this trail are one, so a move onto a neighbour steps rather than pushes.
   */
  pointTo(dir: string): void {
    if (dir === this.currentPath) {
      return;
    }
    if (this.history[this.historyIdx - 1] === dir) {
      this.goBack();
      return;
    }
    if (this.history[this.historyIdx + 1] === dir) {
      this.goForward();
      return;
    }
    this.navigate(dir);
  }

  /** Back to the mounts listing, not the origin: the auto-heal is the one caller, and healing to an unreachable origin loops. */
  reset(): void {
    this.currentPath = FB_ROOT;
    this.history.length = 0;
    this.history.push(FB_ROOT);
    this.historyIdx = 0;
    this.scrollTop = 0;
    this.selected.clear();
    this.lastClickedName = "";
    this.entries = [];
    this.answered = false;
    this.entryMap.clear();
    this.dirWritable = true;
    this.sortedNames = [];
  }

  selectEntry(name: string): void {
    this.selected.add(name);
    this.lastClickedName = name;
  }

  deselectEntry(name: string): void {
    this.selected.delete(name);
    this.lastClickedName = name;
  }

  deselectAll(): void {
    this.selected.clear();
  }
}
