// The file browser's git-letter decoration: the repaint is in place, so the 15s poll keeps selection and focus.

import { vi, describe, it, expect, beforeEach } from "vitest";

vi.mock("./scroll.js", () => ({
  setUserScrolledUp: vi.fn(),
  scrollToBottom: vi.fn(),
  initScroll: vi.fn(),
}));
vi.mock("./editor-openers.js", () => ({
  openFile: vi.fn(),
  openFileDiff: undefined,
  openFileGitDiff: vi.fn(),
  // Browser Mode links for real, so a name absent from the factory fails collection.
  openFileInBackground: vi.fn(),
}));
// chat.ts mounts the transcript view at import time.
vi.mock("./chat.js", () => ({ attachPathsToActiveChat: vi.fn() }));

import { _repaintRowsForTest } from "./files.js";
import { FB_ROOT, joinPath } from "./files-shared.js";
import { openFileGitDiff } from "./editor-openers.js";
import { _setReposForTest } from "./git-status-store.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";
import type { GitRepoStatus, GitFileEntry } from "./git-types.js";

function repo(name: string, files: { path: string; status: string }[]): GitRepoStatus {
  return {
    repo: name,
    is_repo: true,
    branch: "main",
    remote: "origin",
    ahead: 0,
    behind: 0,
    has_dirty: files.length > 0,
    stashes: 0,
    files: files.map((f): GitFileEntry => ({
      path: f.path,
      status: f.status,
      staged: false,
      display: f.path,
    })),
  };
}

/** Shaped as entryRow builds it, with the path composed the same way (`joinPath` from the browser's root). */
function row(segments: string[], isDir = false): HTMLElement {
  let path = FB_ROOT;
  for (const seg of segments) {
    path = joinPath(path, seg);
  }
  const r = document.createElement("div");
  r.className = "fb-row";
  r.dataset["path"] = path;
  r.dataset["isDir"] = String(isDir);
  r.dataset["name"] = path.slice(path.lastIndexOf("/") + 1);
  const meta = document.createElement("span");
  meta.className = "fb-meta";
  r.appendChild(meta);
  return r;
}

function list(): HTMLElement {
  return document.getElementById("fb-list") as HTMLElement;
}

function letters(): string[] {
  return [...list().querySelectorAll(".fb-git-letter")].map((n) => n.textContent ?? "");
}

beforeEach(() => {
  document.body.replaceChildren();
  const l = document.createElement("div");
  l.id = "fb-list";
  document.body.appendChild(l);
  resetWorkspace();
  // /api/git/status-all names repos by directory under the workspace, so absolute keys need the stated root.
  setWorkspaceRoot("/w");
  _setReposForTest([]);
  vi.mocked(openFileGitDiff).mockClear();
});

describe("git letter decoration", () => {
  it("puts the file's own letter on its row, before the meta column", () => {
    _setReposForTest([repo("r", [{ path: "a/b.go", status: "M" }])]);
    list().append(row(["w", "r", "a", "b.go"]));
    _repaintRowsForTest();
    expect(letters()).toEqual(["M"]);
    expect(list().firstElementChild?.children[0]?.className).toContain("fb-git-letter");
  });

  it("reuses the app's git-st-* colour vocabulary rather than a browser-local one", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row(["w", "r", "a.go"]));
    _repaintRowsForTest();
    expect(list().querySelector(".fb-git-letter")?.className).toContain("git-st-m");
  });

  it("gives a directory the WORST letter beneath it", () => {
    _setReposForTest([
      repo("r", [
        { path: "a/untracked.go", status: "?" },
        { path: "a/conflict.go", status: "U" },
      ]),
    ]);
    list().append(row(["w", "r", "a"], true));
    _repaintRowsForTest();
    expect(letters()).toEqual(["U"]);
  });

  it("leaves a clean row undecorated", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row(["w", "r", "clean.go"]));
    _repaintRowsForTest();
    expect(letters()).toEqual([]);
  });

  it("opens the file's diff when its letter is clicked, without selecting the row", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    const r = row(["w", "r", "a.go"]);
    let rowClicks = 0;
    r.addEventListener("click", () => {
      rowClicks++;
    });
    list().append(r);
    _repaintRowsForTest();
    const badge = r.querySelector<HTMLElement>(".fb-git-letter");
    expect(badge?.classList.contains("fb-git-clickable")).toBe(true);
    badge?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(vi.mocked(openFileGitDiff)).toHaveBeenCalledWith("/w/r/a.go", "HEAD");
    // stopPropagation: clicking the badge must not toggle the row.
    expect(rowClicks).toBe(0);
  });

  it("does not make a directory's rollup letter clickable — it has no one diff", () => {
    _setReposForTest([repo("r", [{ path: "a/b.go", status: "M" }])]);
    list().append(row(["w", "r", "a"], true));
    _repaintRowsForTest();
    const badge = list().querySelector<HTMLElement>(".fb-git-letter");
    expect(badge?.classList.contains("fb-git-clickable")).toBe(false);
    badge?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(vi.mocked(openFileGitDiff)).not.toHaveBeenCalled();
  });

  it("repaints in place: the same row elements survive, so selection is untouched", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    const r = row(["w", "r", "a.go"]);
    r.classList.add("fb-row-selected");
    list().append(r);
    _repaintRowsForTest();
    expect(list().firstElementChild).toBe(r);
    expect(r.classList.contains("fb-row-selected")).toBe(true);
  });

  it("replaces the letter on the next poll rather than stacking a second one", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row(["w", "r", "a.go"]));
    _repaintRowsForTest();
    _setReposForTest([repo("r", [{ path: "a.go", status: "D" }])]);
    _repaintRowsForTest();
    expect(letters()).toEqual(["D"]);
  });

  it("drops the letter when the tree goes clean", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row(["w", "r", "a.go"]));
    _repaintRowsForTest();
    _setReposForTest([repo("r", [])]);
    _repaintRowsForTest();
    expect(letters()).toEqual([]);
  });
});
