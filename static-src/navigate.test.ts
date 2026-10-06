import { describe, it, expect, vi, beforeEach } from "vitest";

const calls: string[] = [];
vi.mock("./editor-openers.js", () => ({
  openFile: (p: string, line?: number) => {
    calls.push(`file:${p}:${String(line)}`);
  },
  openFileGitDiff: (p: string, ref: string) => {
    calls.push(`gitdiff:${p}:${ref}`);
  },
  openFileDiff: (p: string, oldText: string, newText: string) => {
    calls.push(`diff:${p}:${oldText}>${newText}`);
  },
}));
// Browser Mode links the module for real: one missing export fails the whole file.
vi.mock("./tabs.js", () => ({
  openGitView: (tab: string) => {
    calls.push(`gitview:${tab}`);
    return Promise.resolve();
  },
  activateTab: () => undefined,
  openTab: () => Promise.resolve("failed"),
  parentChatRef: () => "",
  setTabParent: () => Promise.resolve(false),
  tabIdFor: () => "",
}));
vi.mock("./git-tabs.js", () => ({
  setGitTab: (tab: string) => {
    calls.push(`gittab:${tab}`);
  },
}));

import { openChange, openCallDiff, openChangeSet, openAtLine, openExternal } from "./navigate.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";

beforeEach(() => {
  calls.length = 0;
  resetWorkspace();
});

describe("openChange", () => {
  it("opens the file's diff against HEAD", () => {
    openChange("src/a.ts");
    expect(calls).toEqual(["gitdiff:src/a.ts:HEAD"]);
  });

  it("honours an explicit ref", () => {
    openChange("src/a.ts", "origin/main");
    expect(calls).toEqual(["gitdiff:src/a.ts:origin/main"]);
  });

  it("does nothing for an empty path", () => {
    openChange("");
    expect(calls).toEqual([]);
  });
});

describe("openCallDiff", () => {
  it("opens the call's own before/after pair rather than a git diff", () => {
    openCallDiff("src/a.ts", "one", "two");
    expect(calls).toEqual(["diff:src/a.ts:one>two"]);
  });

  it("does nothing for an empty path", () => {
    openCallDiff("", "one", "two");
    expect(calls).toEqual([]);
  });
});

describe("openChangeSet", () => {
  it("routes the multi-file review to the git view's changes tab", () => {
    openChangeSet();
    expect(calls).toEqual(["gitview:changes"]);
  });
});

describe("openAtLine", () => {
  it("passes the line through", () => {
    openAtLine("src/a.ts", 42);
    expect(calls).toEqual(["file:src/a.ts:42"]);
  });

  it("opens without a line when none is given", () => {
    openAtLine("src/a.ts");
    expect(calls).toEqual(["file:src/a.ts:undefined"]);
  });

  it("does nothing for an empty path", () => {
    openAtLine("");
    expect(calls).toEqual([]);
  });
});

describe("path-space normalisation", () => {
  beforeEach(() => {
    setWorkspaceRoot("/workspace");
  });

  it("makes a relative change path absolute", () => {
    expect.assertions(1);
    openChange("hello.sh");
    expect(calls).toEqual(["gitdiff:/workspace/hello.sh:HEAD"]);
  });

  it("leaves an absolute change path alone", () => {
    expect.assertions(1);
    openChange("/workspace/sub/a.go");
    expect(calls).toEqual(["gitdiff:/workspace/sub/a.go:HEAD"]);
  });

  it("leaves a path in another granted mount alone", () => {
    expect.assertions(1);
    openChange("/config/mcp.json");
    expect(calls).toEqual(["gitdiff:/config/mcp.json:HEAD"]);
  });

  it("makes a relative stats path absolute", () => {
    expect.assertions(1);
    openCallDiff("main.go", "one", "two");
    expect(calls).toEqual(["diff:/workspace/main.go:one>two"]);
  });

  it("makes a relative line reference absolute", () => {
    expect.assertions(1);
    openAtLine("src/a.ts", 42);
    expect(calls).toEqual(["file:/workspace/src/a.ts:42"]);
  });
});

describe("openExternal", () => {
  it("refuses a URL that is not http(s), and says so", () => {
    const open = vi.fn();
    vi.stubGlobal("open", open);
    expect(openExternal("javascript:alert(1)")).toBe(false);
    expect(open).not.toHaveBeenCalled();
  });

  it("opens an https URL with noopener", () => {
    const open = vi.fn();
    vi.stubGlobal("open", open);
    expect(openExternal("https://example.com")).toBe(true);
    expect(open).toHaveBeenCalledWith("https://example.com", "_blank", "noopener,noreferrer");
  });
});
