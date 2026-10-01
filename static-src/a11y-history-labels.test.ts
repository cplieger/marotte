// A history row's accessible names, in its own file for a MODULE-GRAPH reason:
// this suite mounts the REAL history.js over stubbed actions, and in
// a11y-labels.test.ts that arrangement leaned on `vi.doMock` taking effect for
// modules an earlier sibling had already imported — which it never does (a
// mocked module is evaluated once and cached). The lean broke the day tabs.ts
// grew a real edge to actions/chat.js (the optimistic close's composer
// restore), because the settings-tab case imports tabs.js first. File-level
// mocks are hoisted before any import, so here the stubs always take.
import { describe, it, expect, vi } from "vitest";

// Hoisted alongside the vi.mock factories that reference it — a plain const
// would be read before its initialization when the factories run.
const { noop } = vi.hoisted(() => ({
  noop: (): void => {
    /* noop */
  },
}));

vi.mock("./actions/chat.js", () => ({
  loadSessions: {
    dispatch: () =>
      Promise.resolve({
        sessions: [],
        runs: [{ workflow_id: "wf_a11y", name: "nightly-sweep", status: "failed", updated_at: 1 }],
      }),
    cancel: noop,
  },
  deleteChat: { dispatch: noop },
}));
vi.mock("./actions/chat-search.js", () => ({
  searchChats: { dispatch: noop, cancel: noop },
}));
// The row's delete control: two actions plus the confirm dialog, stubbed for
// the same reason the others are — actions/index.js is reduced to one symbol
// here, so an unmocked action def cannot build.
vi.mock("./actions/runs.js", () => ({ deleteRun: { dispatch: noop } }));
vi.mock("./confirm.js", () => ({ confirm: async () => false }));
vi.mock("./actions/index.js", () => ({ registerCleanup: noop }));
vi.mock("./chat.js", () => ({ openPreviousSession: noop, openChatTab: noop }));
vi.mock("./run-view.js", () => ({ openRunView: noop }));
// The dock's queue, which a run row reads for its `input` mark: the real module
// builds the three ask cards and reaches actions/index.js past the one symbol
// stubbed above. No run here has an ask.
vi.mock("./decision-dock.js", () => ({
  runPendingAsks: () => ({ count: 0, nodes: new Set<string>(), label: "" }),
}));
// The transcript's find, which a search row hands its hit to: its graph reaches
// the block dispatcher and the store, neither of which this suite stages.
vi.mock("./find-in-chat.js", () => ({ openChatFindAt: noop }));
vi.mock("./tabs.js", () => ({
  hasTab: () => false,
  // The pane's route sync: a no-op here, because no tab is open to point.
  setHistoryTab: noop,
}));
vi.mock("./router.js", () => ({ pushRoute: noop }));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: () => ({ cancel: noop }),
}));
vi.mock("./editor-openers.js", () => ({
  openFileDiff: noop,
  openFile: undefined,
  openFileGitDiff: undefined,
}));
// `openSpec` is here for the LINK, not for a case: a module in this graph imports
// it, and Browser Mode links for real rather than reading properties off a
// namespace object, so one missing export fails the whole file.
vi.mock("./navigate.js", () => ({
  openChange: noop,
  openAtLine: noop,
  openCallDiff: noop,
  openSpec: noop,
}));
vi.mock("./scroll.js", () => ({
  setUserScrolledUp: noop,
  preserveReadingPosition: (fn: () => void) => {
    fn();
  },
}));
import { loadHistoryView, refreshHistoryView } from "./history.js";

describe("a11y: History row accessible names", () => {
  // A history row's OPEN control is a real button whose name says what a click
  // does ("Open X"); the row itself carries no role, because a role="button" on it
  // is Children-Presentational and flattens the delete button beside it out of the
  // accessibility tree (axe nested-interactive, serious, every row).
  // A settled run also states its OUTCOME, and that outcome is a glyph — so the
  // word has exactly one home, that control's accessible name, and must not be
  // duplicated as visible text beside the glyph it replaced.
  it("a settled run row names the outcome once, in the accessible name only", async () => {
    const view = document.createElement("div");
    view.id = "history-view";
    view.innerHTML = `
      <nav id="history-tab-bar" aria-label="History categories">
        <button type="button" class="seg" data-history-tab="chats"><span class="seg-label">Chats</span></button>
        <button type="button" class="seg" data-history-tab="runs"><span class="seg-label">Runs</span></button>
      </nav>
      <div data-history-panel="chats"><div id="history-table" class="list-container" role="list"></div></div>
      <div data-history-panel="runs" class="hidden"><div id="history-runs" class="list-container" role="list"></div></div>`;
    document.body.appendChild(view);
    const host = document.getElementById("history-runs")!;

    loadHistoryView();
    refreshHistoryView();
    await vi.waitFor(() => {
      if (host.querySelector("[data-key]") === null) {
        throw new Error("not rendered");
      }
    });

    const row = host.querySelector<HTMLElement>('[data-key="r:wf_a11y"]')!;
    // The row is a list item, not a control; its open button and its delete button
    // are siblings inside it.
    expect(row.getAttribute("role")).toBe("listitem");
    expect(row.getAttribute("tabindex")).toBeNull();
    const open = row.querySelector<HTMLElement>("button.entry-open")!;
    // The name still opens with the action, then states the verdict.
    expect(open.getAttribute("aria-label")).toBe("Open nightly-sweep, failed");
    // The slot carrying the mark is decorative: the name already says the word.
    expect(row.querySelector(".tool-icon")?.getAttribute("aria-hidden")).toBe("true");
    expect(row.textContent).not.toContain("failed");

    document.body.removeChild(view);
  });
});
