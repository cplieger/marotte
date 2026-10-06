// What `git stash push` can take: the server runs it without `-u`, so untracked files are not stashed, while the
// status parse reports them. Gating Stash on `files.length` would offer it where git answers "No local changes".

import { describe, it, expect } from "vitest";
import {
  stashableCount,
  describeStatus,
  changedPathCount,
  distinctPaths,
  partiallyStagedPaths,
} from "./git-types.js";
import type { GitFileEntry } from "./git-types.js";

function entry(status: string, staged = false): GitFileEntry {
  return { path: `f-${status}-${String(staged)}.ts`, status, staged, display: status };
}

describe("stashableCount", () => {
  it("is zero on a clean tree", () => {
    expect(stashableCount([])).toBe(0);
  });

  it("is zero when every change is an untracked file", () => {
    // Both are real changes on disk and neither goes into a stash.
    expect(stashableCount([entry("?"), entry("?")])).toBe(0);
  });

  it("counts tracked changes on both sides of the index", () => {
    // A path modified in both the index and the worktree arrives as two entries, and stash takes both.
    expect(stashableCount([entry("M", true), entry("M", false)])).toBe(2);
  });

  it("counts every tracked status letter git status can emit", () => {
    const tracked = ["M", "A", "D", "R", "C", "U"].map((s) => entry(s));
    expect(stashableCount(tracked)).toBe(tracked.length);
  });

  it("counts the tracked changes and ignores the untracked ones beside them", () => {
    expect(stashableCount([entry("?"), entry("M"), entry("?"), entry("A")])).toBe(2);
  });
});

// Mirrors the server's table (internal/git/parse.go statusLabels), including the least guessable letters.

describe("describeStatus", () => {
  it("names every letter git status --porcelain=v1 can emit", () => {
    const want: Record<string, string> = {
      M: "Modified",
      T: "Typechange",
      A: "Added",
      D: "Deleted",
      R: "Renamed",
      C: "Copied",
      U: "Unmerged",
      "?": "Untracked",
    };
    for (const [letter, word] of Object.entries(want)) {
      expect(describeStatus(letter), `letter ${letter}`).toBe(word);
    }
  });

  it("names a copy, which used to come back as the bare letter", () => {
    expect(describeStatus("C")).not.toBe("C");
  });

  it("names a typechange, which used to come back as the bare letter", () => {
    // ` T`/`T ` is a regular file swapped with a symlink.
    expect(describeStatus("T")).not.toBe("T");
  });

  it("falls back to the input for a letter it does not know", () => {
    expect(describeStatus("Z")).toBe("Z");
  });
});

// A path staged and edited again yields two entries; a count a person reads is per file.

describe("changedPathCount", () => {
  it("is zero on a clean tree", () => {
    expect(changedPathCount([])).toBe(0);
  });

  it("counts a path once however many sides of the index it sits on", () => {
    expect(changedPathCount([entry("M", true), entry("M", false)])).toBe(2);
    const same: GitFileEntry[] = [
      { path: "p.ts", status: "M", staged: true, display: "Modified" },
      { path: "p.ts", status: "M", staged: false, display: "Modified" },
    ];
    expect(changedPathCount(same)).toBe(1);
  });

  it("differs from the entry count exactly on a partially-staged path", () => {
    const files: GitFileEntry[] = [
      { path: "p.ts", status: "M", staged: true, display: "Modified" },
      { path: "p.ts", status: "M", staged: false, display: "Modified" },
      { path: "q.ts", status: "M", staged: false, display: "Modified" },
    ];
    expect(files).toHaveLength(3);
    expect(changedPathCount(files)).toBe(2);
  });
});

describe("distinctPaths", () => {
  it("keeps first-seen order and drops repeats", () => {
    const files: GitFileEntry[] = [
      { path: "z.ts", status: "M", staged: true, display: "Modified" },
      { path: "a.ts", status: "M", staged: false, display: "Modified" },
      { path: "z.ts", status: "M", staged: false, display: "Modified" },
    ];
    expect(distinctPaths(files)).toEqual(["z.ts", "a.ts"]);
  });

  it("is empty for no entries", () => {
    expect(distinctPaths([])).toEqual([]);
  });
});

describe("partiallyStagedPaths", () => {
  it("finds the path present on both sides of the index", () => {
    const files: GitFileEntry[] = [
      { path: "p.ts", status: "M", staged: true, display: "Modified" },
      { path: "p.ts", status: "M", staged: false, display: "Modified" },
      { path: "q.ts", status: "M", staged: false, display: "Modified" },
      { path: "s.ts", status: "A", staged: true, display: "Added" },
    ];
    expect([...partiallyStagedPaths(files)]).toEqual(["p.ts"]);
  });

  it("is empty when no path repeats", () => {
    expect(partiallyStagedPaths([entry("M", true), entry("M", false)]).size).toBe(0);
  });

  it("is empty on a clean tree", () => {
    expect(partiallyStagedPaths([]).size).toBe(0);
  });
});
