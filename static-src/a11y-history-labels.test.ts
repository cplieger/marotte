// In its own file for a MODULE-GRAPH reason: this suite mounts the REAL history.js over
// stubbed actions, and `vi.doMock` never affects a module a sibling already imported.
// File-level mocks are hoisted before any import, so these stubs always take.
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
// actions/index.js is reduced to one symbol here, so an unmocked action def cannot build.
vi.mock("./actions/runs.js", () => ({ deleteRun: { dispatch: noop } }));
vi.mock("./confirm.js", () => ({ confirm: async () => false }));
vi.mock("./actions/index.js", () => ({ registerCleanup: noop }));
vi.mock("./chat.js", () => ({ openPreviousSession: noop, openChatTab: noop }));
vi.mock("./run-view.js", () => ({ openRunView: noop }));
// The real dock module reaches actions/index.js past the stub above. No run here has an ask.
vi.mock("./decision-dock.js", () => ({
  runPendingAsks: () => ({ count: 0, asked: [], label: "" }),
}));
// The transcript's find, which a search row hands its hit to: its graph reaches
// the block dispatcher and the store, neither of which this suite stages.
vi.mock("./find-in-chat.js", () => ({ openChatFindAt: noop }));
vi.mock("./tabs.js", () => ({
  hasTab: () => false,
  // The pane's route sync: a no-op here, because no tab is open to point.
  setHistoryTab: noop,
  tabSetVersion: () => 0,
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
// For the LINK: Browser Mode links ESM for real, so one missing export fails the file.
vi.mock("./navigate.js", () => ({
  openChange: noop,
  openAtLine: noop,
  openCallDiff: noop,
  openSpec: noop,
}));
vi.mock("./scroll.js", () => ({
  setUserScrolledUp: noop,
}));
import { loadHistoryView, refreshHistoryView } from "./history.js";

describe("a11y: History row accessible names", () => {
  // A history row's OPEN control is a real button named for its click ("Open X"); a
  // role="button" row would flatten the delete button out of the accessibility tree (axe
  // nested-interactive). A settled run's outcome word lives only in that accessible name.
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
