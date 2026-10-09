import { describe, it, expect, beforeEach, vi } from "vitest";

// Replacing the actions layer keeps the first subscriber's read off the network; tests seed with `_setReposForTest`.
// Which triggers read is git-status-triggers.test.ts's subject.
const { refreshDispatch } = vi.hoisted(() => ({
  refreshDispatch:
    vi.fn<
      (args: {
        paths?: readonly string[];
        gen?: number;
      }) => Promise<{ repos: GitRepoStatus[] } | undefined>
    >(),
}));
vi.mock("./actions/index.js", () => ({
  apiAction: () => ({ dispatch: () => Promise.resolve({ repos: [] }) }),
  defineAction: () => ({ dispatch: refreshDispatch }),
}));

import {
  _setReposForTest,
  statusForPath,
  statusUnder,
  currentRepos,
  onGitStatusChange,
  refreshGitStatus,
} from "./git-status-store.js";
import type { GitRepoStatus } from "./git-types.js";
import { onWorkspaceRoot, setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";

function repo(
  name: string,
  files: { path: string; status: string; staged?: boolean }[],
): GitRepoStatus {
  return {
    repo: name,
    is_repo: true,
    branch: "main",
    remote: "origin",
    ahead: 0,
    behind: 0,
    has_dirty: files.length > 0,
    stashes: 0,
    files: files.map((f) => ({
      path: f.path,
      status: f.status,
      staged: f.staged ?? false,
      display: f.path,
    })),
  };
}

beforeEach(() => {
  resetWorkspace();
  _setReposForTest([]);
});

describe("the letter a path reads", () => {
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
  });

  it("keys on repo AND path, so the same relative path in two repos is distinct", () => {
    _setReposForTest([
      repo("alpha", [{ path: "README.md", status: "M" }]),
      repo("beta", [{ path: "README.md", status: "A" }]),
    ]);
    expect(statusForPath("/workspace/alpha/README.md")).toBe("M");
    expect(statusForPath("/workspace/beta/README.md")).toBe("A");
  });

  it("takes the first letter of a two-character porcelain status", () => {
    _setReposForTest([repo("r", [{ path: "a.md", status: "MM" }])]);
    expect(statusForPath("/workspace/r/a.md")).toBe("M");
  });

  it("keeps the first entry when a path appears twice (staged + unstaged)", () => {
    _setReposForTest([
      repo("r", [
        { path: "a.md", status: "A", staged: true },
        { path: "a.md", status: "M", staged: false },
      ]),
    ]);
    expect(statusForPath("/workspace/r/a.md")).toBe("A");
  });

  it("ignores directories that are not repos", () => {
    const notARepo: GitRepoStatus = {
      ...repo("x", [{ path: "a.md", status: "M" }]),
      is_repo: false,
    };
    _setReposForTest([notARepo]);
    expect(statusForPath("/workspace/x/a.md")).toBe("");
  });

  it("clears stale entries when a poll returns a cleaner tree", () => {
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    expect(statusForPath("/workspace/r/a.md")).toBe("M");
    // The file was committed: the next read no longer lists it.
    _setReposForTest([repo("r", [])]);
    expect(statusForPath("/workspace/r/a.md")).toBe("");
  });
});

describe("a / work dir that is itself a repository", () => {
  beforeEach(() => {
    setWorkspaceRoot("/");
  });

  it("keys its files at the filesystem root, not under a doubled slash", () => {
    _setReposForTest([repo(".", [{ path: "srv/notes.md", status: "M" }])]);
    expect(statusForPath("/srv/notes.md")).toBe("M");
  });

  it("rolls a change up to / itself", () => {
    _setReposForTest([repo(".", [{ path: "srv/notes.md", status: "M" }])]);
    expect(statusUnder("/srv")).toBe("M");
    expect(statusUnder("/")).toBe("M");
  });
});

// The fixture shape is the point: /api/git/status-all names each repo by a bare directory name under the workspace
// ("." for the root), never an absolute path (discoverRepos in internal/git/repos.go). Absolute fixture names once
// let a suite pass while every real key missed.
describe("statusForPath", () => {
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
  });

  it("answers for an absolute path, so a consumer needs no repo split rule", () => {
    expect.assertions(1);
    _setReposForTest([repo("marotte", [{ path: "static-src/files.ts", status: "M" }])]);
    expect(statusForPath("/workspace/marotte/static-src/files.ts")).toBe("M");
  });

  it("answers for a file in the workspace-root repo, reported as '.'", () => {
    // A file written straight into the workspace root, whose repo name is ".".
    expect.assertions(1);
    _setReposForTest([repo(".", [{ path: "hello.sh", status: "?" }])]);
    expect(statusForPath("/workspace/hello.sh")).toBe("?");
  });

  it("does not key a '.' repo's files under a literal './' prefix", () => {
    expect.assertions(1);
    _setReposForTest([repo(".", [{ path: "hello.sh", status: "?" }])]);
    expect(statusForPath("./hello.sh")).toBe("");
  });

  it("returns empty for a clean path and for a directory (that is statusUnder's job)", () => {
    expect.assertions(2);
    _setReposForTest([repo("r", [{ path: "a/b.md", status: "M" }])]);
    expect(statusForPath("/workspace/r/a/other.md")).toBe("");
    expect(statusForPath("/workspace/r/a")).toBe("");
  });

  it("tolerates a trailing slash", () => {
    expect.assertions(1);
    _setReposForTest([repo("r", [{ path: "a.md", status: "A" }])]);
    expect(statusForPath("/workspace/r/a.md/")).toBe("A");
  });

  it("returns empty before the handshake states the workspace root", () => {
    // Nothing can be keyed absolutely yet, so the answer is no letter, not one from a guessed root.
    expect.assertions(1);
    resetWorkspace();
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    expect(statusForPath("/workspace/r/a.md")).toBe("");
  });

  it("builds no absolute key at all before the handshake, not a wrong one", () => {
    // For the "." repo, joining onto an unknown root yields "/hello.sh", which would answer for a real path at the
    // filesystem root; declining to index makes the previous case a property.
    expect.assertions(2);
    resetWorkspace();
    _setReposForTest([repo(".", [{ path: "hello.sh", status: "M" }])]);
    expect(statusForPath("/hello.sh")).toBe("");
    expect(statusUnder("/")).toBe("");
  });
});

// The handshake and the first read race with no ordering, so a read that won left the absolute indexes unbuildable.
describe("the root landing after a poll", () => {
  it("rebuilds the absolute index against the root that just arrived", () => {
    expect.assertions(2);
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    expect(statusForPath("/workspace/r/a.md")).toBe("");
    setWorkspaceRoot("/workspace");
    expect(statusForPath("/workspace/r/a.md")).toBe("M");
  });

  it("rebuilds the directory rollup too", () => {
    expect.assertions(2);
    _setReposForTest([repo("r", [{ path: "a/b.md", status: "M" }])]);
    expect(statusUnder("/workspace/r/a")).toBe("");
    setWorkspaceRoot("/workspace");
    expect(statusUnder("/workspace/r/a")).toBe("M");
  });

  it("republishes so rows painted with no letter get repainted", () => {
    // The index changed while the data did not, and `repos` is all consumers watch, so it is republished.
    expect.assertions(1);
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    let repaints = 0;
    const off = onGitStatusChange(() => {
      repaints++;
    });
    // onGitStatusChange fires immediately with the current value.
    repaints = 0;
    setWorkspaceRoot("/workspace");
    off();
    expect(repaints).toBe(1);
  });

  it("does not wake consumers on a reconnect that restates the same root", () => {
    expect.assertions(1);
    setWorkspaceRoot("/workspace");
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    let repaints = 0;
    const off = onGitStatusChange(() => {
      repaints++;
    });
    repaints = 0;
    setWorkspaceRoot("/workspace");
    off();
    expect(repaints).toBe(0);
  });

  it("keeps the store subscribed to the root for the module's life", () => {
    // The subscription is module wiring, so the test seam cannot switch it off.
    expect.assertions(1);
    let subscribers = 0;
    const off = onWorkspaceRoot(() => {
      subscribers++;
    });
    resetWorkspace();
    setWorkspaceRoot("/workspace");
    off();
    expect(subscribers).toBe(1);
  });
});

// A later handshake naming another root comes from a restarted server whose workspace the earlier read never saw.
describe("a later handshake naming another root", () => {
  it("drops the previous server's letters rather than keying them under the new root", () => {
    expect.assertions(4);
    setWorkspaceRoot("/workspace");
    _setReposForTest([repo("marotte", [{ path: "a.md", status: "M" }])]);
    setWorkspaceRoot("/srv/proj");
    expect(statusForPath("/srv/proj/marotte/a.md")).toBe("");
    expect(statusUnder("/srv/proj/marotte")).toBe("");
    expect(statusForPath("/workspace/marotte/a.md")).toBe("");
    expect(currentRepos()).toEqual([]);
  });

  it("re-reads the whole tree for a subscribed consumer and shows the new server's letters", async () => {
    setWorkspaceRoot("/workspace");
    const off = onGitStatusChange(() => undefined);
    _setReposForTest([repo("marotte", [{ path: "a.md", status: "M" }])]);
    refreshDispatch.mockClear();
    refreshDispatch.mockResolvedValueOnce({
      repos: [repo("marotte", [{ path: "b.md", status: "A" }])],
    });

    setWorkspaceRoot("/srv/proj");
    await vi.waitFor(() => {
      expect(statusForPath("/srv/proj/marotte/b.md")).toBe("A");
    });
    off();
    expect(refreshDispatch.mock.calls).toHaveLength(1);
    expect(refreshDispatch.mock.calls[0]?.[0].paths).toBeUndefined();
  });

  it("drops a read the previous server answers after the root moved, and keeps the new server's", async () => {
    setWorkspaceRoot("/workspace");
    const off = onGitStatusChange(() => undefined);
    let answerOld: (v: { repos: GitRepoStatus[] }) => void = () => undefined;
    refreshDispatch.mockClear();
    refreshDispatch.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          answerOld = resolve;
        }),
    );
    refreshDispatch.mockResolvedValueOnce({
      repos: [repo("p", [{ path: "new.md", status: "A" }])],
    });
    const oldRead = refreshGitStatus();

    setWorkspaceRoot("/srv/proj");
    await vi.waitFor(() => {
      expect(statusForPath("/srv/proj/p/new.md")).toBe("A");
    });
    answerOld({ repos: [repo("p", [{ path: "old.md", status: "M" }])] });
    await oldRead;
    off();

    expect(statusForPath("/srv/proj/p/old.md")).toBe("");
    expect(statusForPath("/srv/proj/p/new.md")).toBe("A");
    const [oldGen, newGen] = refreshDispatch.mock.calls.map((c) => c[0].gen ?? -1);
    expect(newGen).toBeGreaterThan(oldGen ?? Infinity);
  });
});

describe("statusUnder", () => {
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
  });

  it("rolls a nested change up to every ancestor, including the repo root", () => {
    expect.assertions(3);
    _setReposForTest([repo("r", [{ path: "a/b/c.md", status: "M" }])]);
    expect(statusUnder("/workspace/r/a/b")).toBe("M");
    expect(statusUnder("/workspace/r/a")).toBe("M");
    expect(statusUnder("/workspace/r")).toBe("M");
  });

  it("stops at the repo root — a sibling repo's parent is not decorated", () => {
    expect.assertions(1);
    _setReposForTest([repo("r", [{ path: "a.md", status: "M" }])]);
    expect(statusUnder("/workspace")).toBe("");
  });

  it("rolls up to the workspace root for the '.' repo, which IS that root", () => {
    expect.assertions(2);
    _setReposForTest([repo(".", [{ path: "a/b.md", status: "M" }])]);
    expect(statusUnder("/workspace/a")).toBe("M");
    expect(statusUnder("/workspace")).toBe("M");
  });

  it("reports the WORST letter beneath it, so a conflict outranks an untracked file", () => {
    expect.assertions(1);
    _setReposForTest([
      repo("r", [
        { path: "a/untracked.md", status: "?" },
        { path: "a/conflict.md", status: "U" },
        { path: "a/mod.md", status: "M" },
      ]),
    ]);
    expect(statusUnder("/workspace/r/a")).toBe("U");
  });

  it("orders the whole precedence chain U > D > M > R > A > ?", () => {
    expect.assertions(5);
    const worst = (letters: string[]): string => {
      _setReposForTest([
        repo(
          "r",
          letters.map((s, i) => ({ path: `a/f${String(i)}.md`, status: s })),
        ),
      ]);
      return statusUnder("/workspace/r/a");
    };
    expect(worst(["?", "A"])).toBe("A");
    expect(worst(["A", "R"])).toBe("R");
    expect(worst(["R", "M"])).toBe("M");
    expect(worst(["M", "D"])).toBe("D");
    expect(worst(["D", "U"])).toBe("U");
  });

  it("does not let an unrecognised letter win by accident", () => {
    expect.assertions(1);
    _setReposForTest([
      repo("r", [
        { path: "a/x.md", status: "Z" },
        { path: "a/y.md", status: "?" },
      ]),
    ]);
    expect(statusUnder("/workspace/r/a")).toBe("?");
  });

  it("returns empty for a directory with nothing changed beneath it", () => {
    expect.assertions(1);
    _setReposForTest([repo("r", [{ path: "a/b.md", status: "M" }])]);
    expect(statusUnder("/workspace/r/other")).toBe("");
  });

  it("clears the rollup when the tree goes clean", () => {
    expect.assertions(2);
    _setReposForTest([repo("r", [{ path: "a/b.md", status: "M" }])]);
    expect(statusUnder("/workspace/r/a")).toBe("M");
    _setReposForTest([repo("r", [])]);
    expect(statusUnder("/workspace/r/a")).toBe("");
  });
});

describe("currentRepos", () => {
  it("exposes the repos array for aggregate consumers (the badge)", () => {
    _setReposForTest([repo("a", [{ path: "x", status: "M" }]), repo("b", [])]);
    expect(currentRepos().map((r) => r.repo)).toEqual(["a", "b"]);
  });
});
