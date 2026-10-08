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
  /** Why `currentPath`'s last listing failed, "" when it did not; the list renders it in place of the rows. */
  listError = "";
  entryMap = new Map<string, FileEntry>();
  dirWritable = true;
  sortedNames: string[] = [];
  /** Every files tab paints into one shared scroller, so the offset lives here; a directory change resets it. */
  scrollTop = 0;

  /**
   * True until the origin folder loads, so an unreachable origin falls back to the mounts listing once. Per browser:
   * N browsers share one fetch holder. A reader's own move retires it, or that folder's failure would heal to root.
   */
  pendingRestore = true;

  /**
   * The path field moved here and no answer has said what `currentPath` is yet: it is unpublished, and a file answer
   * opens it. Held here, not by the request, so a superseded request leaves it for the next load; any move clears it.
   */
  pendingOpen = false;

  /** A browser opened at `at` with both nav buttons disabled (`navigate` would push and enable Back). */
  constructor(at: string = FB_ROOT) {
    this.currentPath = at;
    this.history = [at];
  }

  /** Push `path` onto the trail; false (and nothing changes) when it is already the current folder. */
  navigate(path: string): boolean {
    if (path === this.currentPath) {
      return false;
    }
    this.history.length = this.historyIdx + 1;
    this.history.push(path);
    this.historyIdx = this.history.length - 1;
    this.enter(path);
    return true;
  }

  goBack(): boolean {
    if (this.historyIdx <= 0) {
      return false;
    }
    this.historyIdx--;
    this.enter(this.history[this.historyIdx]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    return true;
  }

  goForward(): boolean {
    if (this.historyIdx >= this.history.length - 1) {
      return false;
    }
    this.historyIdx++;
    this.enter(this.history[this.historyIdx]!); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    return true;
  }

  /**
   * Swap the current trail entry for `path` without pushing, collapsing onto an equal predecessor so the trail never
   * holds one folder twice in a row.
   */
  replaceCurrent(path: string): void {
    if (this.historyIdx > 0 && this.history[this.historyIdx - 1] === path) {
      this.history.length = this.historyIdx;
      this.historyIdx--;
    } else {
      this.history[this.historyIdx] = path;
    }
    this.enter(path);
  }

  /**
   * Point at a directory from outside the trail (history entry, deep link). Adjacent-first: while active, the document
   * trail and this trail are one, so a move onto a neighbour steps rather than pushes.
   */
  pointTo(dir: string): void {
    if (dir === this.currentPath) {
      this.pendingOpen = false;
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
    this.history.length = 0;
    this.history.push(FB_ROOT);
    this.historyIdx = 0;
    this.enter(FB_ROOT);
    this.entries = [];
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

  /** What every move onto a folder resets; the listing state (`entries`) stays until the new answer lands. */
  private enter(path: string): void {
    this.currentPath = path;
    this.answered = false;
    this.listError = "";
    this.pendingOpen = false;
    this.scrollTop = 0;
    this.selected.clear();
    this.lastClickedName = "";
  }
}
