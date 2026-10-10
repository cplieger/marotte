// The file surface's path-space contract: one container-absolute space rooted at "/" for the whole /api/file*
// surface and everything reading it (`internal/filebrowse/search.go`'s FileMatch.Path).

import { describe, it, expect, beforeEach } from "vitest";

import { joinPath, parentPath, normalizeDirPath } from "./files-shared.js";
import { FileBrowserState } from "./files-state.js";
import { parseRoute, buildPath } from "./route-path.js";
import { statusForPath, statusUnder, _setReposForTest } from "./git-status-store.js";
import { absPath, setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";
import type { GitRepoStatus, GitFileEntry } from "./git-types.js";

function repo(name: string, files: { path: string; status: string }[]): GitRepoStatus {
  return {
    repo: name,
    is_repo: true,
    branch: "main",
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

function walk(...names: string[]): string {
  const state = new FileBrowserState();
  for (const name of names) {
    state.navigate(joinPath(state.currentPath, name));
  }
  return state.currentPath;
}

beforeEach(() => {
  resetWorkspace();
  _setReposForTest([]);
});

describe("the browser's composed row path is a key statusForPath can match", () => {
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
    _setReposForTest([repo("marotte", [{ path: "static-src/files.ts", status: "M" }])]);
  });

  it("starts a fresh listing on the filesystem root, in the space it composes into", () => {
    // The root listing is synthetic, but children compose from its path.
    expect(new FileBrowserState().currentPath).toBe("/");
  });

  it("returns to that same root on reset", () => {
    const state = new FileBrowserState();
    state.navigate("/workspace/marotte");
    state.reset();
    expect(state.currentPath).toBe("/");
  });

  it("composes the container-absolute path, walking mount then repo then dir", () => {
    expect(walk("workspace", "marotte", "static-src", "files.ts")).toBe(
      "/workspace/marotte/static-src/files.ts",
    );
  });

  it("finds the file's letter under the path the browser composed", () => {
    expect(statusForPath(walk("workspace", "marotte", "static-src", "files.ts"))).toBe("M");
  });

  it("finds a directory's rollup under the path the browser composed", () => {
    expect(statusUnder(walk("workspace", "marotte", "static-src"))).toBe("M");
    expect(statusUnder(walk("workspace", "marotte"))).toBe("M");
  });

  it("hands openChange's absPath a path it passes through untouched", () => {
    // openChange normalises through absPath because three callers pass workspace-relative paths.
    const row = walk("workspace", "marotte", "static-src", "files.ts");
    expect(absPath(row)).toBe(row);
  });

  it("reaches the parent listing's own path by the same spelling", () => {
    const dir = walk("workspace", "marotte", "static-src");
    expect(parentPath(dir)).toBe("/workspace/marotte");
    expect(parentPath("/workspace")).toBe("/");
    expect(parentPath("/")).toBe("/");
  });
});

describe("the /files route and the browser agree on the root", () => {
  // The router spells the root as a literal rather than importing FB_ROOT; this holds the two equal.
  it("parses /files to the path a fresh listing starts on", () => {
    expect(parseRoute("/files", "").kind).toBe("files");
    expect((parseRoute("/files", "") as { path: string }).path).toBe(
      new FileBrowserState().currentPath,
    );
  });

  it("builds /files back from that path, with no empty segment", () => {
    expect(buildPath({ kind: "files", path: new FileBrowserState().currentPath })).toBe("/files");
    expect(buildPath({ kind: "files", path: walk("workspace", "marotte") })).toBe(
      "/files/workspace/marotte",
    );
  });

  it("round-trips a directory below the root", () => {
    const dir = walk("workspace", "marotte");
    const url = buildPath({ kind: "files", path: dir });
    expect((parseRoute(url, "") as { path: string }).path).toBe(dir);
  });
});

describe("a repo AT the workspace root", () => {
  // `repo: "."` means the workspace root is the repository.
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
    _setReposForTest([repo(".", [{ path: "notes.md", status: "?" }])]);
  });

  it("matches the letter for a file directly under the mount", () => {
    expect(statusForPath(walk("workspace", "notes.md"))).toBe("?");
  });

  it("rolls the change up to the mount row", () => {
    expect(statusUnder(walk("workspace"))).toBe("?");
  });
});

describe("a mount that is not the workspace", () => {
  // `/config` is a granted mount in no repository, so it carries no letter.
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
    _setReposForTest([repo("marotte", [{ path: "static-src/files.ts", status: "M" }])]);
  });

  it("composes the mount's own absolute path", () => {
    expect(walk("config", "mcp.json")).toBe("/config/mcp.json");
  });

  it("reports no letter for it", () => {
    expect(statusForPath(walk("config", "mcp.json"))).toBe("");
  });
});

describe("normalizeDirPath is the one door into the space", () => {
  // One normaliser at the entry keeps `currentPath` absolute by construction.
  const cases: [string, string][] = [
    ["", "/"],
    ["/", "/"],
    [".", "/"],
    ["workspace/marotte", "/workspace/marotte"],
    ["/workspace/marotte", "/workspace/marotte"],
    ["//workspace//", "/workspace"],
    ["  /workspace/marotte  ", "/workspace/marotte"],
    ["workspace/marotte/", "/workspace/marotte"],
    // A surviving `//` would mint a second tab for an open folder.
    ["/workspace//x", "/workspace/x"],
    ["/workspace///deep//er/", "/workspace/deep/er"],
  ];

  for (const [input, expected] of cases) {
    it(`normalises ${JSON.stringify(input)} to ${expected}`, () => {
      expect(normalizeDirPath(input)).toBe(expected);
    });
  }

  it("keeps a normalised path a joinable base", () => {
    expect(joinPath(normalizeDirPath("workspace/marotte"), "static-src")).toBe(
      "/workspace/marotte/static-src",
    );
    expect(joinPath(normalizeDirPath(""), "workspace")).toBe("/workspace");
  });
});
