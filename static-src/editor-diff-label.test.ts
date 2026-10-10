// ---------------------------------------------------------------------------
// The diff panes' captions.
//
// A diff's pane makes a CLAIM about what it holds. When git owns no revision of
// the file — a workspace file outside every repo, or a workspace root that is not
// itself a repo — the left pane is empty, and captioning it "HEAD" asserts that
// HEAD has the file and that the file is empty there. The RIGHT pane carries the
// same problem in the other direction: a deleted file has no working copy, and an
// empty pane captioned "working tree" says the file is there and empty. The load
// reports what it found on each side (`internal/git.kindNotInRepo` vs a real
// failure; a 404 from /api/file vs a real read failure), and both captions are
// taken from the load rather than from what was asked for.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";

interface DiffResult {
  kind: "diff";
  read: null;
  oldText: string;
  base: "text";
  baseLabel: string;
  workingLabel: string;
}

let diffResult: DiffResult | { kind: "too_large" } = {
  kind: "diff",
  read: null,
  oldText: "",
  base: "text",
  baseLabel: "HEAD",
  workingLabel: "working tree",
};

vi.mock("./tabs.js", () => ({
  openEditorView: vi.fn(() => Promise.resolve()),
  // `editorTabID` is gone with the `editor:<path>` id convention: ids are opaque
  // and server-minted, and this is the one lookup from a path to one.
  tabIdFor: () => "",
  getActiveTabId: () => "",
  setTabDirty: vi.fn(),
}));
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve(null)),
  apiGetOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
  apiGetTypedOrError: vi.fn(() => Promise.resolve({ ok: false, status: 0, data: null, error: "" })),
}));
vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
vi.mock("./editor-conflict.js", () => ({
  abortSuggestion: vi.fn(),
  clearSuggestionState: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGetTyped: vi.fn(),
}));
vi.mock("./editor-modes.js", () => ({ restoreUI: vi.fn() }));
vi.mock("./editor-ui.js", () => ({
  applyPendingLine: vi.fn(),
  fetchAgentLines: vi.fn(),
  clearAgentLineCache: vi.fn(),
  requestedBy: () => "diff",
  showSurface: vi.fn(),
}));
vi.mock("./viewer-live.js", () => ({
  liveActivate: vi.fn(),
  liveDispose: vi.fn(),
  paintLiveButton: vi.fn(),
  setLiveReload: vi.fn(),
}));
vi.mock("./actions/editor.js", () => ({
  loadDiff: {
    dispatch: () => ({
      outcome: Promise.resolve({ status: "success", value: diffResult }),
    }),
  },
}));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  pollAction: vi.fn(() => () => undefined),
}));
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: () => document.createElement("div"),
    },
  ),
  // `skeleton.ts` in this graph imports the name, and Browser Mode links for real
  // rather than reading properties off a namespace object — so an absent export
  // fails the whole FILE at collection. `setBusy`'s own body rather than
  // `undefined`, because the editor's open path reaches an admitted
  // `paintPlaceholder` and a placeholder marks its host busy through this name.
  setBusy: (el: Element, busy: boolean) => {
    if (busy) {
      el.setAttribute("aria-busy", "true");
    } else {
      el.removeAttribute("aria-busy");
    }
  },
}));

const { fetchGitDiffSources, TOO_LARGE_TO_DIFF } = await import("./editor-openers.js");
const { fileStates, freshState } = await import("./editor-types.js");

const PATH = "/workspace/hello.sh";

/** A file parked in diff mode against a ref, the state `open` leaves behind when
 *  it routes a git source into the fetch. */
function stageDiffState(ref: string): ReturnType<typeof freshState> {
  const state = freshState(PATH);
  state.mode.value = {
    kind: "diff",
    diffSource: {
      oldText: "",
      newText: "",
      oldLabel: ref,
      newLabel: "working tree",
      kind: "git",
      ref,
      base: "text",
      pending: true,
    },
  };
  fileStates.set(PATH, state);
  return state;
}

function labelOf(state: ReturnType<typeof freshState>): string {
  const m = state.mode.value;
  return m.kind === "diff" ? m.diffSource.oldLabel : "";
}

function workingLabelOf(state: ReturnType<typeof freshState>): string {
  const m = state.mode.value;
  return m.kind === "diff" ? m.diffSource.newLabel : "";
}

beforeEach(() => {
  fileStates.clear();
});

describe("the base pane's caption", () => {
  it("says the ref when git holds a revision", async () => {
    expect.assertions(1);
    diffResult = {
      kind: "diff",
      read: null,
      oldText: "old",
      base: "text",
      baseLabel: "HEAD",
      workingLabel: "working tree",
    };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(labelOf(state)).toBe("HEAD");
  });

  it("says 'not in git' when no repo owns the file", async () => {
    expect.assertions(2);
    diffResult = {
      kind: "diff",
      read: null,
      oldText: "",
      base: "text",
      baseLabel: "not in git",
      workingLabel: "working tree",
    };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(labelOf(state)).toBe("not in git");
    // Still a correct all-add diff: the file exists, it just has no "before".
    expect(state.error.value).toBe("");
  });

  it("says 'not in HEAD' when the repo owns the file but the ref does not", async () => {
    // The third base-pane state, and the only one where git DOES own the file:
    // untracked or staged-new, so `handleShow` answers empty content plus the
    // `absent` marker. The opener staged "HEAD" as the placeholder, so the load
    // has to overwrite it — captioned "HEAD" an empty pane claims HEAD holds
    // the file and holds it empty.
    expect.assertions(3);
    diffResult = {
      kind: "diff",
      read: null,
      oldText: "",
      base: "text",
      baseLabel: "not in HEAD",
      workingLabel: "working tree",
    };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(labelOf(state)).toBe("not in HEAD");
    expect(workingLabelOf(state)).toBe("working tree");
    // An all-add diff is a correct rendering, not an error state.
    expect(state.error.value).toBe("");
  });

  it("carries a non-HEAD ref through unchanged", async () => {
    expect.assertions(1);
    diffResult = {
      kind: "diff",
      read: null,
      oldText: "old",
      base: "text",
      baseLabel: "origin/main",
      workingLabel: "working tree",
    };
    const state = stageDiffState("origin/main");
    await fetchGitDiffSources(state, "", "origin/main");
    expect(labelOf(state)).toBe("origin/main");
  });
});

describe("the working pane's caption", () => {
  it("says 'working tree' for an ordinary change", async () => {
    expect.assertions(1);
    diffResult = {
      kind: "diff",
      read: null,
      oldText: "old",
      base: "text",
      baseLabel: "HEAD",
      workingLabel: "working tree",
    };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(workingLabelOf(state)).toBe("working tree");
  });

  it("says 'deleted' when the working copy is gone", async () => {
    // The placeholder the opener staged says "working tree", so the load has to
    // overwrite it: an empty right pane under that caption claims the file is
    // still there and empty.
    expect.assertions(3);
    diffResult = {
      kind: "diff",
      read: null,
      oldText: "gone\n",
      base: "text",
      baseLabel: "HEAD",
      workingLabel: "deleted",
    };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(workingLabelOf(state)).toBe("deleted");
    expect(labelOf(state)).toBe("HEAD");
    // An all-deletions diff is a correct rendering, not an error state.
    expect(state.error.value).toBe("");
  });
});

// A side too large to diff falls back to the file's own view, saying so.
describe("a diff too large to show", () => {
  it("leaves the diff for the file view with a note", async () => {
    diffResult = { kind: "too_large" };
    const state = stageDiffState("HEAD");
    await fetchGitDiffSources(state, "", "HEAD");
    expect(state.mode.value).toEqual({ kind: "text", editing: false });
    expect(state.note).toBe(TOO_LARGE_TO_DIFF);
  });
});
