import { describe, it, expect, vi, beforeEach } from "vitest";
import { signal } from "@cplieger/reactive";
import type { PageFind } from "./find-registry.js";
import type * as ModHistory from "./history.js";
import { ICON_TAB_RUN, outcomeIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { absoluteTime } from "./relative-time.js";
import { forgetRun, noteRunChat } from "./run-store.js";

/**
 * Cache-buster: `vi.resetModules()` does not re-evaluate a module in Browser Mode (URL-keyed module map). The `.ts`
 * extension is load-bearing: written `.js`, coverage attributes every evaluation to a file that does not exist. Only
 * the module under test is busted, so `vi.mock` still intercepts its dependencies.
 */
let bootSeq = 0;

const dispatch = vi.fn();
const skeletonTimingMock = vi.fn(() => ({ cancel: vi.fn() }));
const cancelSessions = vi.fn();
// Resolves with an outcome: openRow refreshes the list on "gone" (the retention-off 404).
const openPreviousSession = vi.fn(() => Promise.resolve("opened"));
const openRunView = vi.fn();
// Resolves with the tab outcome: a search row hands the reader to the chat's find only once the tab is open.
const openChatTab = vi.fn(() => Promise.resolve("opened"));
const openChatFindAt = vi.fn();
const searchDispatch = vi.fn(async () => ({
  matches: [],
  scanned: 0,
  matched: 0,
  truncated: false,
}));
const deleteChatDispatch = vi.fn(async () => ({ ok: true }));
const deleteRunDispatch = vi.fn(async () => ({ ok: true }));
const confirmMock = vi.fn(async () => true);
const closeTab = vi.fn();
const setHistoryTab = vi.fn();
const pushRoute = vi.fn();

vi.mock("./actions/chat.js", () => ({
  loadSessions: { dispatch, cancel: cancelSessions },
  deleteChat: { dispatch: deleteChatDispatch },
}));
// Unmocked, these build through actions/index.js, which this suite reduces to one symbol.
vi.mock("./actions/runs.js", () => ({
  deleteRun: { dispatch: deleteRunDispatch },
}));
vi.mock("./confirm.js", () => ({ confirm: confirmMock }));
// The subtitle joins each row against the chat record the client holds, so the store is a real input here.
const storeGet = vi.fn((_id: string): unknown => undefined);
vi.mock("./store.js", () => ({ get: storeGet }));
vi.mock("./roles.js", () => ({
  labelForMode: (id: string) => (id === "vibe" ? "Default" : id),
  // Present-but-inert for real-ESM linking; no case calls them.
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
}));
vi.mock("./chat.js", () => ({ openPreviousSession, openChatTab }));
// Unmocked it builds through actions/index.js, which this suite stubs to one symbol.
vi.mock("./actions/chat-search.js", () => ({
  searchChats: { dispatch: searchDispatch, cancel: vi.fn() },
}));
// The seam is what a match row hands the transcript's find; the real module would reach the whole store surface.
vi.mock("./find-in-chat.js", () => ({ openChatFindAt }));
vi.mock("./run-view.js", () => ({ openRunView }));
// A signal, since the real `runPendingAsks` is a tracked read and the page's repaint-on-ask rides it; a plain set
// would leave the live case nothing to fire.
const asking = signal<ReadonlySet<string>>(new Set());
vi.mock("./decision-dock.js", () => ({
  runPendingAsks: (id: string) => ({
    count: asking.value.has(id) ? 1 : 0,
    nodes: new Set<string>(),
    label: "",
  }),
}));
// Keyed by `(kind, ref)`: ids are opaque and server-minted, so a chat id is not a tab id.
const hasTab = vi.fn((_kind: string, _ref?: string) => false);
// A signal, since the real `tabSetVersion` is a tracked read and the page re-derives its rows on it.
const tabsVersion = signal(0);
vi.mock("./tabs.js", () => ({
  hasTab,
  setHistoryTab,
  tabSetVersion: () => tabsVersion.value,
}));
// The pane switch pushes its URL; a forced switch (the router's) must not.
vi.mock("./router.js", () => ({ pushRoute }));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  // A spy: whether the arm fires is what the settled-empty case pins.
  skeletonTiming: skeletonTimingMock,
}));
// Stubs the four leaves tool-card.ts needs, so the real glyph is tested without staging the app.
const noop = (): void => {
  /* noop */
};
vi.mock("./editor-openers.js", () => ({ openFileDiff: noop }));
// `openSpec` is for the link: Browser Mode links for real, so one missing export fails the whole file.
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
const chatRow = {
  session_id: "sess_chat",
  title: "A conversation",
  status: "idle",
  description: "reading files",
  updated_at: 3000,
};
const ownedRow = {
  session_id: "sess_owned",
  title: "Already open",
  status: "idle",
  chat_id: "c-existing",
  updated_at: 2000,
};
const failedRow = {
  session_id: "sess_failed",
  title: "Went wrong",
  status: "failed",
  updated_at: 1000,
};
const runRow = {
  workflow_id: "wf_1",
  name: "feature-pipeline",
  status: "completed",
  updated_at: 2500,
};

const runAt = (status: string) => ({ ...runRow, status });

/** As `static/index.html` lays it out: the bar and two panes, each with its list container. */
const VIEW_HTML = `
  <div id="history-view">
    <nav id="history-tab-bar" aria-label="History categories">
      <button type="button" class="seg" data-history-tab="chats"><span class="seg-label">Chats</span></button>
      <button type="button" class="seg" data-history-tab="runs"><span class="seg-label">Runs</span></button>
    </nav>
    <div data-history-panel="chats"><div id="history-table" class="list-container" role="list"></div></div>
    <div data-history-panel="runs" class="hidden"><div id="history-runs" class="list-container" role="list"></div></div>
  </div>`;

function mountView(): HTMLElement {
  document.body.innerHTML = VIEW_HTML;
  return document.getElementById("history-view")!;
}

const chatsPane = (): HTMLElement => document.getElementById("history-table")!;
const runsPane = (): HTMLElement => document.getElementById("history-runs")!;
const rowOf = (key: string): HTMLElement =>
  document.querySelector<HTMLElement>(`[data-key="${key}"]`)!;
const keysIn = (pane: HTMLElement): (string | null)[] =>
  [...pane.querySelectorAll("[data-key]")].map((r) => r.getAttribute("data-key"));

/** The name lives on the open button; a role on the list item would flatten the delete button out of the tree. */
const openName = (row: Element): string | null =>
  row.querySelector("button.entry-open")?.getAttribute("aria-label") ?? null;

const openBtn = (row: Element): HTMLElement => row.querySelector<HTMLElement>("button.entry-open")!;

/**
 * The controller is a module singleton whose effect outlives the view, so each test closes the previous instance or
 * it repaints the next mount.
 */
let current: typeof ModHistory | null = null;

async function freshModule(): Promise<typeof ModHistory> {
  current = (await import(/* @vite-ignore */ `./history.ts?boot=${bootSeq}`)) as typeof ModHistory;
  return current;
}

async function render(payload: unknown): Promise<HTMLElement> {
  const view = mountView();
  dispatch.mockResolvedValue(payload);
  // The pair `activateTabQuietly` runs; `openTab` opens the tab and paints nothing here.
  const { loadHistoryView, refreshHistoryView } = await freshModule();
  loadHistoryView();
  refreshHistoryView();
  await vi.waitFor(() => {
    if (view.querySelectorAll("[data-key]").length === 0) {
      throw new Error("not rendered");
    }
  });
  return view;
}

function resetAll(): void {
  current?.teardownHistoryView();
  current = null;
  vi.clearAllMocks();
  vi.resetModules();
  bootSeq++;
  hasTab.mockReturnValue(false);
  asking.value = new Set();
  forgetRun("wf_1");
}

describe("history: two panes behind one bar", () => {
  beforeEach(resetAll);

  it("puts chats on the Chats pane and runs on the Runs pane, each newest first", async () => {
    await render({
      sessions: [failedRow, chatRow],
      runs: [runRow, { ...runRow, workflow_id: "wf_2", updated_at: 9000 }],
    });
    expect(keysIn(chatsPane())).toEqual(["s:sess_chat", "s:sess_failed"]);
    expect(keysIn(runsPane())).toEqual(["r:wf_2", "r:wf_1"]);
  });

  it("opens on the Chats pane, with the bar and the panels wired as a tablist", async () => {
    const view = await render({ sessions: [chatRow], runs: [runRow] });
    const bar = view.querySelector("#history-tab-bar")!;
    expect(bar.getAttribute("role")).toBe("tablist");
    const chats = bar.querySelector('[data-history-tab="chats"]')!;
    const runs = bar.querySelector('[data-history-tab="runs"]')!;
    expect(chats.classList.contains("active")).toBe(true);
    expect(chats.getAttribute("aria-selected")).toBe("true");
    expect(chats.getAttribute("aria-controls")).toBe("history-panel-chats");
    expect(runs.getAttribute("aria-selected")).toBe("false");
    // The panel half of that pairing.
    const chatsPanel = view.querySelector('[data-history-panel="chats"]')!;
    const runsPanel = view.querySelector('[data-history-panel="runs"]')!;
    expect(chatsPanel.id).toBe("history-panel-chats");
    expect(chatsPanel.getAttribute("role")).toBe("tabpanel");
    expect(chatsPanel.getAttribute("aria-labelledby")).toBe("history-tab-chats");
    expect(chatsPanel.classList.contains("hidden")).toBe(false);
    expect(runsPanel.classList.contains("hidden")).toBe(true);
  });

  it("switches panes from the bar, and the switch is what pushes the URL", async () => {
    const view = await render({ sessions: [chatRow], runs: [runRow] });
    view.querySelector<HTMLElement>('[data-history-tab="runs"]')!.click();
    expect(view.querySelector('[data-history-panel="runs"]')!.classList.contains("hidden")).toBe(
      false,
    );
    expect(view.querySelector('[data-history-panel="chats"]')!.classList.contains("hidden")).toBe(
      true,
    );
    expect(view.querySelector('[data-history-tab="runs"]')!.getAttribute("aria-selected")).toBe(
      "true",
    );
    expect(setHistoryTab).toHaveBeenCalledWith("runs");
    expect(pushRoute).toHaveBeenCalledWith({ kind: "history", tab: "runs" });
  });

  it("re-selecting the active pane pushes nothing", async () => {
    const view = await render({ sessions: [chatRow], runs: [runRow] });
    view.querySelector<HTMLElement>('[data-history-tab="chats"]')!.click();
    expect(pushRoute).not.toHaveBeenCalled();
  });

  it("forceHistoryTab swaps the pane WITHOUT a push, for the router", async () => {
    const view = mountView();
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [runRow] });
    const { forceHistoryTab, loadHistoryView } = await freshModule();
    // Before the page is wired, as a deep link arrives: the choice survives into the first paint.
    forceHistoryTab("runs");
    loadHistoryView();
    expect(view.querySelector('[data-history-panel="runs"]')!.classList.contains("hidden")).toBe(
      false,
    );
    expect(setHistoryTab).toHaveBeenCalledWith("runs");
    expect(pushRoute).not.toHaveBeenCalled();
  });

  it("keeps the kind chips and the status word off the row", async () => {
    // One kind per pane, so no chip; a run's state is the mark in its leading slot.
    await render({ sessions: [chatRow, failedRow], runs: [runRow] });
    const rows = [...document.querySelectorAll("[data-key]")].map((r) => r.textContent).join(" ");
    expect(rows).not.toContain("Run");
    expect(rows).not.toContain("Chat");
    expect(rows).not.toContain("failed");
    expect(rows).not.toContain("completed");
    expect(rows).not.toContain("idle");
  });
});

describe("history: opening a row", () => {
  beforeEach(resetAll);

  it("opens an already-owned session as its existing chat", async () => {
    await render({ sessions: [ownedRow], runs: [] });
    openBtn(rowOf("s:sess_owned")).click();
    expect(openPreviousSession).toHaveBeenCalledWith(
      expect.objectContaining({ chat_id: "c-existing" }),
    );
    expect(openRunView).not.toHaveBeenCalled();
  });

  it("drops the row by refreshing when the reopen says the chat is GONE", async () => {
    // Retention off and the conversation deleted since the fetch: "gone" re-fetches, and the server's list drops the row.
    openPreviousSession.mockResolvedValueOnce("gone");
    await render({ sessions: [ownedRow], runs: [] });
    const before = dispatch.mock.calls.length;
    openBtn(rowOf("s:sess_owned")).click();
    await vi.waitFor(() => {
      expect(dispatch.mock.calls.length).toBeGreaterThan(before);
    });
  });

  it("leaves the list alone when the reopen merely failed", async () => {
    // A network failure is not the ephemeral case: the row may be live, so the list does not churn.
    openPreviousSession.mockResolvedValueOnce("failed");
    await render({ sessions: [ownedRow], runs: [] });
    const before = dispatch.mock.calls.length;
    openBtn(rowOf("s:sess_owned")).click();
    await new Promise((r) => setTimeout(r, 0));
    expect(dispatch.mock.calls.length).toBe(before);
  });

  it("routes a run to the read-only run view, never to a chat", async () => {
    // The third argument is the parent chat for the run's tab. Parentlessness is not passed: the composition root reads
    // it from the run store, since a chat-parented run whose chat tab is closed has an empty Parent.
    await render({ sessions: [chatRow], runs: [runRow] });
    openBtn(rowOf("r:wf_1")).click();
    expect(openRunView).toHaveBeenCalledWith("wf_1", "feature-pipeline", "");
    expect(openPreviousSession).not.toHaveBeenCalled();
  });

  it("hands over the launching chat so an agent-launched run nests under it", async () => {
    const parented = { ...runRow, parent_chat_id: "c-launcher" };
    await render({ sessions: [], runs: [parented] });
    openBtn(rowOf("r:wf_1")).click();
    // The chat id nests the run's tab under its conversation; `openRunView` resolves it to a tab id.
    expect(openRunView).toHaveBeenCalledWith("wf_1", "feature-pipeline", "c-launcher");
  });

  it("offers a Retry on load failure instead of an empty state, on both panes", async () => {
    const view = mountView();
    dispatch.mockResolvedValue(null);
    const { loadHistoryView, refreshHistoryView } = await freshModule();
    loadHistoryView();
    refreshHistoryView();
    await vi.waitFor(() => {
      if (view.querySelectorAll(".history-error").length < 2) {
        throw new Error("no error state");
      }
    });
    expect(chatsPane().querySelector(".list-empty")?.textContent).not.toContain("No previous");
    expect(runsPane().querySelector(".list-empty")?.textContent).not.toContain("No previous");
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [] });
    chatsPane().querySelector<HTMLElement>("button")!.click();
    await vi.waitFor(() => {
      if (chatsPane().querySelector("[data-key]") === null) {
        throw new Error("retry did not re-fetch");
      }
    });
  });

  async function failReload(): Promise<void> {
    await render({ sessions: [chatRow], runs: [runRow] });
    dispatch.mockResolvedValue(null);
    current!.refreshHistoryView();
    await vi.waitFor(() => {
      if (document.querySelectorAll(".history-error").length < 2) {
        throw new Error("no error state");
      }
    });
  }

  it("keeps a failed reload's Retry on both panes when the tab set changes", async () => {
    await failReload();
    tabsVersion.value++;
    expect(chatsPane().querySelector(".history-error")).not.toBeNull();
    expect(runsPane().querySelector(".history-error")).not.toBeNull();
    expect(document.querySelector("[data-key]")).toBeNull();
  });

  it("keeps a failed reload's Retry on the Runs pane when the filter is typed", async () => {
    await failReload();
    current!.forceHistoryTab("runs");
    await openFind();
    const input = document.getElementById("hist-filter-input") as HTMLInputElement;
    input.value = "feature";
    // Enter runs the filter in this tick, so the pane is read after its render.
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(runsPane().querySelector(".history-error")).not.toBeNull();
    expect(runsPane().querySelector("[data-key]")).toBeNull();
  });
});

// The server answers 200 with an empty list whether it read nothing or failed, so `sessions_state` / `runs_state`
// separate the two, per list.

describe("history: which empty state each pane shows", () => {
  beforeEach(resetAll);

  async function emptyTexts(payload: unknown): Promise<{ chats: string; runs: string }> {
    mountView();
    dispatch.mockResolvedValue(payload);
    const { loadHistoryView, refreshHistoryView } = await freshModule();
    loadHistoryView();
    refreshHistoryView();
    await vi.waitFor(() => {
      if (
        chatsPane().querySelector(".list-empty") === null ||
        runsPane().querySelector(".list-empty") === null
      ) {
        throw new Error("no empty state");
      }
    });
    return { chats: chatsPane().textContent ?? "", runs: runsPane().textContent ?? "" };
  }

  it("claims nothing to resume only when BOTH reads succeeded", async () => {
    const t = await emptyTexts({
      sessions: [],
      runs: [],
      sessions_state: "ready",
      runs_state: "ready",
    });
    expect(t.chats).toBe("No previous conversations in this workspace.");
    expect(t.runs).toBe("No previous workflow runs in this workspace.");
  });

  it("says the read failed rather than claiming the workspace is empty", async () => {
    const t = await emptyTexts({
      sessions: [],
      runs: [],
      sessions_state: "unavailable",
      runs_state: "unavailable",
    });
    expect(t.chats).toBe("Could not read previous conversations.");
    expect(t.runs).toBe("Could not read workflow runs.");
  });

  it("names WHICH list failed when only one did, on its own pane", async () => {
    const chats = await emptyTexts({
      sessions: [],
      runs: [],
      sessions_state: "unavailable",
      runs_state: "ready",
    });
    expect(chats.chats).toBe("Could not read previous conversations.");
    expect(chats.runs).toBe("No previous workflow runs in this workspace.");

    const runs = await emptyTexts({
      sessions: [],
      runs: [],
      sessions_state: "ready",
      runs_state: "unavailable",
    });
    expect(runs.chats).toBe("No previous conversations in this workspace.");
    expect(runs.runs).toBe("Could not read workflow runs.");
  });

  // The verdict is all the wire carries (the server logs the cause), so copy naming a cause would invent one.
  it("names no cause in any of the four states", async () => {
    const all = [
      await emptyTexts({ sessions: [], runs: [], sessions_state: "ready", runs_state: "ready" }),
      await emptyTexts({
        sessions: [],
        runs: [],
        sessions_state: "unavailable",
        runs_state: "unavailable",
      }),
      await emptyTexts({
        sessions: [],
        runs: [],
        sessions_state: "unavailable",
        runs_state: "ready",
      }),
      await emptyTexts({
        sessions: [],
        runs: [],
        sessions_state: "ready",
        runs_state: "unavailable",
      }),
    ]
      .map((t) => `${t.chats} ${t.runs}`)
      .join(" ")
      .toLowerCase();

    expect(all).not.toContain("account");
    expect(all).not.toContain("entitle");
    expect(all).not.toContain("permission");
    expect(all).not.toContain("expire");
    expect(all).not.toContain("connect");
    expect(all).not.toContain("sign in");
  });
});

describe("history: chats already open in a tab here", () => {
  beforeEach(resetAll);

  it("drops a tagged session whose chat is open here", async () => {
    hasTab.mockImplementation((_kind: string, ref?: string) => ref === "c-existing");
    await render({ sessions: [chatRow, ownedRow], runs: [] });
    expect(document.querySelector('[data-key="s:sess_owned"]')).toBeNull();
    // Only that row goes.
    expect(keysIn(chatsPane())).toEqual(["s:sess_chat"]);
    expect(hasTab).toHaveBeenCalledWith("chat", "c-existing");
  });

  it("keeps a tagged session whose chat is NOT open here", async () => {
    // Owned but closed is what this page is for: reopening it.
    await render({ sessions: [ownedRow], runs: [] });
    expect(document.querySelector('[data-key="s:sess_owned"]')).not.toBeNull();
  });

  it("never asks the tab store about an unowned session", async () => {
    // No `chat_id`: no marotte chat owns it, so it can be no tab.
    await render({ sessions: [chatRow], runs: [] });
    expect(document.querySelector('[data-key="s:sess_chat"]')).not.toBeNull();
    expect(hasTab).not.toHaveBeenCalled();
  });
});

describe("history: runs covered by an open tab", () => {
  beforeEach(resetAll);

  const openTabs = (open: readonly (readonly [kind: string, ref: string])[]): void => {
    hasTab.mockImplementation((kind: string, ref?: string) =>
      open.some(([k, r]) => k === kind && r === (ref ?? "")),
    );
  };

  it("drops a run whose launching chat is open here", async () => {
    openTabs([["chat", "c-parent"]]);
    await render({
      sessions: [],
      runs: [
        { ...runRow, parent_chat_id: "c-parent" },
        { ...runRow, workflow_id: "wf_solo" },
      ],
    });
    expect(keysIn(runsPane())).toEqual(["r:wf_solo"]);
  });

  it("drops a run whose own run tab is open", async () => {
    openTabs([["run", "wf_1"]]);
    await render({ sessions: [], runs: [runRow, { ...runRow, workflow_id: "wf_2" }] });
    expect(keysIn(runsPane())).toEqual(["r:wf_2"]);
  });

  it("drops a live run of an open chat", async () => {
    openTabs([["chat", "c-parent"]]);
    await render({
      sessions: [],
      runs: [
        { ...runAt("running"), parent_chat_id: "c-parent" },
        { ...runRow, workflow_id: "wf_solo" },
      ],
    });
    expect(keysIn(runsPane())).toEqual(["r:wf_solo"]);
  });

  it("keeps a finished run once its chat is closed, with its outcome glyph", async () => {
    openTabs([
      ["chat", "c-other"],
      ["run", "wf_other"],
    ]);
    await render({ sessions: [], runs: [{ ...runRow, parent_chat_id: "c-parent" }] });
    const row = rowOf("r:wf_1");
    expect(row.querySelector(".entry-lead .tool-icon")).not.toBeNull();
    expect(openName(row)).toBe("Open feature-pipeline, succeeded");
  });

  it("keeps a live parentless run with no tab, with its working mark", async () => {
    await render({ sessions: [], runs: [runAt("running")] });
    const mark = rowOf("r:wf_1").querySelector<HTMLElement>(".entry-lead > .entry-mark");
    expect(mark?.dataset["status"]).toBe("working");
  });

  it("falls back to the run store's launching chat when the row names none", async () => {
    noteRunChat("wf_1", "c-parent");
    openTabs([["chat", "c-parent"]]);
    await render({ sessions: [], runs: [runRow, { ...runRow, workflow_id: "wf_solo" }] });
    expect(keysIn(runsPane())).toEqual(["r:wf_solo"]);
  });

  it("still counts a run hidden by its own open tab in its chat's delete confirm", async () => {
    confirmMock.mockResolvedValue(false);
    openTabs([["run", "wf_2"]]);
    await render({
      sessions: [ownedRow],
      runs: [
        { ...runRow, parent_chat_id: "c-existing" },
        { ...runRow, workflow_id: "wf_2", parent_chat_id: "c-existing" },
      ],
    });
    expect(keysIn(runsPane())).toEqual(["r:wf_1"]);
    rowOf("s:sess_owned").querySelector<HTMLElement>("[data-history-delete]")!.click();
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalledWith(
        'Delete this conversation? "Already open" and its stored history are removed for good. It also deletes the 2 workflow runs it started.',
        "Delete",
        "destructive",
      );
    });
  });

  const launcherPayload = {
    sessions: [chatRow, ownedRow],
    runs: [
      { ...runRow, parent_chat_id: "c-existing" },
      { ...runRow, workflow_id: "wf_solo", updated_at: 100 },
    ],
  };

  it("brings a chat and its runs back when its tab closes on screen, without a refetch", async () => {
    openTabs([["chat", "c-existing"]]);
    await render(launcherPayload);
    expect(keysIn(chatsPane())).toEqual(["s:sess_chat"]);
    expect(keysIn(runsPane())).toEqual(["r:wf_solo"]);
    const fetches = dispatch.mock.calls.length;

    openTabs([]);
    tabsVersion.value++;

    expect(keysIn(chatsPane())).toEqual(["s:sess_chat", "s:sess_owned"]);
    expect(keysIn(runsPane())).toEqual(["r:wf_1", "r:wf_solo"]);
    expect(dispatch.mock.calls.length).toBe(fetches);
  });

  it("takes a chat and its runs out when its tab opens on screen, without a refetch", async () => {
    await render(launcherPayload);
    expect(keysIn(chatsPane())).toEqual(["s:sess_chat", "s:sess_owned"]);
    expect(keysIn(runsPane())).toEqual(["r:wf_1", "r:wf_solo"]);
    const fetches = dispatch.mock.calls.length;

    openTabs([["chat", "c-existing"]]);
    tabsVersion.value++;

    expect(keysIn(chatsPane())).toEqual(["s:sess_chat"]);
    expect(keysIn(runsPane())).toEqual(["r:wf_solo"]);
    expect(dispatch.mock.calls.length).toBe(fetches);
  });
});

// A run's state is its leading slot: the workflow mark while it moves, the outcome glyph once settled (via
// tool-card.ts's applyOutcome), nothing for an unknown status. Exhaustive over RunState.status plus two junk values.

describe("history: a run's state is its leading slot", () => {
  beforeEach(resetAll);

  const markup = (svg: string): string => (iconEl(svg) as HTMLElement).outerHTML;

  // `mark` null keeps the identity glyph (tinted green); otherwise the shared silhouette. One mark per row; its shape
  // changes for a non-success state.
  const settled = [
    { status: "completed", cls: "is-ok", mark: null, word: "succeeded" },
    { status: "failed", cls: "is-fail", mark: outcomeIcon("fail"), word: "failed" },
    { status: "aborted", cls: "is-warn", mark: outcomeIcon("warn"), word: "aborted" },
  ] as const;

  for (const s of settled) {
    it(`paints ${s.status} as one tinted mark in the lead, never the word`, async () => {
      await render({ sessions: [], runs: [runAt(s.status)] });
      const row = rowOf("r:wf_1");
      const icon = row.querySelector(".entry-lead .tool-icon")!;
      expect(icon.classList.contains(s.cls)).toBe(true);
      // One mark, never a glyph plus a badge.
      expect(icon.querySelectorAll("svg")).toHaveLength(1);
      // Tint alone fails WCAG 1.4.1, so the shape is required. The identity glyph is ICON_TAB_RUN, not a toolIcon, which is
      // why the writer captures it.
      expect(icon.querySelector("svg")!.outerHTML).toBe(markup(s.mark ?? ICON_TAB_RUN));
      // The word is in the accessible name only, after the row's "Open X".
      expect(openName(row)).toBe(`Open feature-pipeline, ${s.word}`);
      expect(row.textContent).not.toContain(s.word);
      expect(row.textContent).not.toContain(s.status);
      expect(row.querySelector(".entry-mark")).toBeNull();
    });
  }

  for (const [status, mark] of [
    ["running", "working"],
    ["paused", "waiting"],
  ] as const) {
    it(`gives a ${status} run the workflow mark at ${mark}, and no verdict`, async () => {
      await render({ sessions: [], runs: [runAt(status)] });
      const row = rowOf("r:wf_1");
      // The `data-status` vocabulary run-bar.ts writes, so one run looks the same everywhere.
      const dot = row.querySelector<HTMLElement>(".entry-lead > .entry-mark");
      expect(dot?.dataset["status"]).toBe(mark);
      expect(row.querySelector(".tool-icon")).toBeNull();
      expect(openName(row)).toBe("Open feature-pipeline");
      expect(row.textContent).not.toContain(status);
    });
  }

  it("paints `input` on a run whose ask is queued, ahead of its status, and withholds Delete", async () => {
    // KAS leaves an asking run `running`; the dock's queue outranks the status, as in the run bar and tab dots.
    asking.value = new Set(["wf_1"]);
    await render({ sessions: [], runs: [runAt("running")] });
    const row = rowOf("r:wf_1");
    expect(row.querySelector<HTMLElement>(".entry-lead > .entry-mark")?.dataset["status"]).toBe(
      "input",
    );
    expect(row.querySelector(".tool-icon")).toBeNull();
    // An asking run is moving, so its trash stays withheld.
    expect(row.querySelector("[data-history-delete]")).toBeNull();
  });

  it("repaints the mark when an ask arrives or is answered, with no fetch", async () => {
    // An ask emits no `runs:changed`, so the pane follows the dock's queue itself, on the same element.
    await render({ sessions: [], runs: [runAt("running")] });
    const row = rowOf("r:wf_1");
    const mark = (): string | undefined =>
      row.querySelector<HTMLElement>(".entry-lead > .entry-mark")?.dataset["status"];
    expect(mark()).toBe("working");
    const fetches = dispatch.mock.calls.length;
    asking.value = new Set(["wf_1"]);
    expect(mark()).toBe("input");
    expect(rowOf("r:wf_1")).toBe(row);
    asking.value = new Set();
    expect(mark()).toBe("working");
    expect(dispatch.mock.calls.length).toBe(fetches);
  });

  it("stops following the dock once the page is torn down", async () => {
    // A closed page must not keep re-deriving rows on every dock change; reopening re-sets the answer via `load()`.
    await render({ sessions: [], runs: [runAt("running")] });
    const row = rowOf("r:wf_1");
    const mark = (): string | undefined =>
      row.querySelector<HTMLElement>(".entry-lead > .entry-mark")?.dataset["status"];
    expect(mark()).toBe("working");
    const { teardownHistoryView } = await freshModule();
    teardownHistoryView();
    asking.value = new Set(["wf_1"]);
    expect(mark()).toBe("working");
  });

  it("guesses no verdict from a status it does not know", async () => {
    // `status` is an open string: an unknown value makes no claim, never a green check.
    await render({ sessions: [], runs: [runAt("quiesced")] });
    const row = rowOf("r:wf_1");
    // The slot stays, empty, so the title keeps its neighbours' offset.
    expect(row.querySelector(".entry-lead")?.childElementCount).toBe(0);
    expect(row.textContent).not.toContain("quiesced");
  });

  it("shows an empty lead when the run reports no status at all", async () => {
    await render({ sessions: [], runs: [runAt("")] });
    expect(rowOf("r:wf_1").querySelector(".entry-lead")?.childElementCount).toBe(0);
  });

  // The server lists agent-parented runs because their chat's transcript may be gone; this page is then the only door,
  // so the outcome is stated.
  it("states the verdict on an agent-parented run too", async () => {
    await render({
      sessions: [],
      runs: [{ ...runRow, status: "aborted", parent_chat_id: "c-owner" }],
    });
    const row = rowOf("r:wf_1");
    expect(row.querySelector(".tool-icon")).not.toBeNull();
    expect(openName(row)).toBe("Open feature-pipeline, aborted");
  });

  it("gives a chat row no lead at all", async () => {
    await render({ sessions: [failedRow], runs: [] });
    const row = rowOf("s:sess_failed");
    expect(row.querySelector(".entry-lead")).toBeNull();
    expect(row.querySelector(".tool-icon")).toBeNull();
  });

  it("repaints a run IN PLACE when a reload finds it settled", async () => {
    // Kept by key across a reload, so a finished run's mark becomes its verdict on the same element.
    await render({ sessions: [], runs: [runAt("running")] });
    const row = rowOf("r:wf_1");
    expect(row.querySelector(".entry-mark")).not.toBeNull();
    dispatch.mockResolvedValue({ sessions: [], runs: [runAt("failed")] });
    const { refreshHistoryView } = await freshModule();
    refreshHistoryView();
    await vi.waitFor(() => {
      if (row.querySelector(".tool-icon") === null) {
        throw new Error("not repainted");
      }
    });
    expect(rowOf("r:wf_1")).toBe(row);
    expect(row.querySelector(".entry-mark")).toBeNull();
    expect(openName(row)).toBe("Open feature-pipeline, failed");
    expect(row.querySelector("[data-history-delete]")).not.toBeNull();
  });
});

// A run one of marotte's bounds stopped. Bounds cancel like a person does and KAS has no "cancelled", so both land on
// `aborted`; `end_reason` tells them apart.

describe("history: an overrun reads differently from a cancel", () => {
  beforeEach(resetAll);

  const sub = (row: Element): string => row.querySelector(".entry-sub")?.textContent ?? "";

  it("says a wall-clock overrun stopped the run", async () => {
    await render({
      sessions: [],
      runs: [{ ...runRow, status: "aborted", end_reason: "overran" }],
    });
    expect(sub(rowOf("r:wf_1"))).toContain("ran past its time limit");
  });

  it("says the idle window stopped the run, and settles it as aborted", async () => {
    // `running`, so the verdict comes from the reason: the terminal frame may not have landed yet.
    await render({
      sessions: [],
      runs: [{ ...runRow, status: "running", end_reason: "stalled" }],
    });
    const row = rowOf("r:wf_1");
    expect(sub(row)).toBe("stopped: no activity and no live shell within its idle window");
    expect(openName(row)).toBe("Open feature-pipeline, aborted");
  });

  it("says a restart interrupted the run", async () => {
    // A run cut off by a restart stopped mid-step through the same cancel, so the sentence says so.
    await render({
      sessions: [],
      runs: [{ ...runRow, status: "aborted", end_reason: "orphaned" }],
    });
    const row = rowOf("r:wf_1");
    expect(sub(row)).toContain("the server restarted while it was running");
    expect(openName(row)).toBe("Open feature-pipeline, aborted");
  });

  it("says nothing extra for a user cancel, which is the same status", async () => {
    // The distinguisher is the absence of a reason: a person's abort has no sentence.
    await render({ sessions: [], runs: [runAt("aborted")] });
    const row = rowOf("r:wf_1");
    expect(row.textContent).not.toContain("ran past");
    expect(row.querySelector(".entry-sub")?.textContent).toBe("");
  });

  it("settles a run the terminal frame has not caught up with yet", async () => {
    // A bound cancels at a node boundary, so KAS may still report `running`; the reason outranks the status.
    await render({
      sessions: [],
      runs: [{ ...runRow, status: "running", end_reason: "overran" }],
    });
    const row = rowOf("r:wf_1");
    expect(row.querySelector(".tool-icon")).not.toBeNull();
    expect(row.querySelector(".entry-mark")).toBeNull();
    expect(openName(row)).toBe("Open feature-pipeline, aborted");
  });

  it("states the reason on an agent-parented run too", async () => {
    // The sentence reports what marotte did, so it shows whatever launched the run, beside the glyph.
    await render({
      sessions: [],
      runs: [{ ...runRow, status: "aborted", parent_chat_id: "c-owner", end_reason: "overran" }],
    });
    const row = rowOf("r:wf_1");
    expect(row.querySelector(".tool-icon")).not.toBeNull();
    expect(sub(row)).toContain("ran past its time limit");
  });

  it("ignores an end reason it does not recognise", async () => {
    // An unknown `end_reason` prints no raw enum and does not repaint a completed run as aborted.
    await render({
      sessions: [],
      runs: [{ ...runRow, status: "completed", end_reason: "quiesced" }],
    });
    const row = rowOf("r:wf_1");
    expect(row.textContent).not.toContain("quiesced");
    expect(openName(row)).toBe("Open feature-pipeline, succeeded");
  });
});

// A plain loader, because the toggle-style opener would close the active tab it was fired from.

describe("history: the tab-restore loader", () => {
  beforeEach(resetAll);

  async function restore(): Promise<HTMLElement> {
    return render({ sessions: [chatRow], runs: [] });
  }

  it("fills the page from the activation pair alone", async () => {
    const view = await restore();
    expect(view.querySelectorAll("[data-key]")).toHaveLength(1);
  });

  it("is a reload when fired again, and paints one row rather than two", async () => {
    const view = await restore();
    const { loadHistoryView, refreshHistoryView } = await freshModule();
    loadHistoryView();
    refreshHistoryView();
    await vi.waitFor(() => {
      if (dispatch.mock.calls.length < 2) {
        throw new Error("not reloaded");
      }
    });
    expect(view.querySelectorAll("[data-key]")).toHaveLength(1);
  });

  it("registers the find and fetches NOTHING", async () => {
    // The activation registers and the dispatcher's refresh fetches: one `/api/sessions` per first activation.
    mountView();
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [] });
    const { loadHistoryView } = await freshModule();

    loadHistoryView();

    const { pageFind } = await import("./find-registry.js");
    expect(pageFind("history")).not.toBeUndefined();
    expect(dispatch).not.toHaveBeenCalled();
  });

  it("refreshHistoryView issues exactly one read for both panes", async () => {
    await render({ sessions: [chatRow], runs: [runRow] });
    expect(dispatch).toHaveBeenCalledTimes(1);
    expect(keysIn(chatsPane())).toEqual(["s:sess_chat"]);
    expect(keysIn(runsPane())).toEqual(["r:wf_1"]);
  });

  it("arms no placeholder for an EMPTY list once /api/sessions has answered", async () => {
    // No chats and no runs is an answer; its empty-state row has no `data-key`, so only the flag separates it.
    mountView();
    dispatch.mockResolvedValue({ sessions: [], runs: [] });
    const { loadHistoryView, refreshHistoryView } = await freshModule();
    loadHistoryView();

    refreshHistoryView();
    await vi.waitFor(() => {
      if (chatsPane().querySelector(".list-empty") === null) {
        throw new Error("empty state not painted");
      }
    });
    // The first read had nothing to go on, so it arms once, for both panes.
    expect(skeletonTimingMock).toHaveBeenCalledTimes(1);

    refreshHistoryView();
    await vi.waitFor(() => {
      if (dispatch.mock.calls.length < 2) {
        throw new Error("not re-read");
      }
    });

    expect(skeletonTimingMock).toHaveBeenCalledTimes(1);
  });

  it("tears the page's in-flight work down on close", async () => {
    await restore();
    const { teardownHistoryView } = await freshModule();
    cancelSessions.mockClear();
    teardownHistoryView();
    expect(cancelSessions).toHaveBeenCalled();
  });
});

// Cross-chat search: a second mode over the Chats pane, not a filter of the loaded list.

const match = (over: Record<string, unknown> = {}) => ({
  id: "c-redis",
  name: "Redis migration",
  best: {
    message_id: "m1",
    turn_message_id: "m1",
    excerpt: "we moved the cache to redis",
    role: "user",
    segment_kind: "content",
    turn: 1,
    offset: 22,
    segment_len: 27,
  },
  hits: 3,
  score: 12,
  updated_at: 5000,
  ...over,
});

/** Reached the way Ctrl-F and the toolbar magnifier reach it. */
async function openFind(): Promise<PageFind> {
  const { pageFind } = await import("./find-registry.js");
  const find = pageFind("history");
  if (find === undefined) {
    throw new Error("the history page registered no find");
  }
  // A popup: there is no field until it opens.
  find.open();
  return find;
}

async function search(
  result: unknown,
  query = "redis",
): Promise<{ table: HTMLElement; note: HTMLElement; find: PageFind }> {
  mountView();
  dispatch.mockResolvedValue({ sessions: [], runs: [] });
  searchDispatch.mockResolvedValue(result as never);
  // The page's own loader, which every door reaches through the tab factory's lazy import.
  const { loadHistoryView } = await freshModule();
  loadHistoryView();
  const find = await openFind();

  const input = document.getElementById("hist-search-input") as HTMLInputElement;
  input.value = query;
  input.dispatchEvent(new Event("input"));
  await vi.waitFor(() => {
    if (searchDispatch.mock.calls.length === 0) {
      throw new Error("not searched");
    }
  });
  await vi.waitFor(() => {
    if ((document.getElementById("hist-search-note")?.textContent ?? "") === "") {
      throw new Error("no note yet");
    }
  });
  return {
    table: chatsPane(),
    note: document.getElementById("hist-search-note")!,
    find,
  };
}

describe("history: cross-chat search", () => {
  beforeEach(resetAll);

  it("renders matching CHATS in the row shape, with the hit count in the time slot", async () => {
    const { table } = await search({
      matches: [match()],
      scanned: 12,
      matched: 1,
      truncated: false,
    });
    const row = table.querySelector("[data-search-chat]")!;
    expect(row.getAttribute("data-search-chat")).toBe("c-redis");
    expect(row.classList.contains("entry")).toBe(true);
    expect(row.querySelector(".entry-title")?.textContent).toBe("Redis migration");
    expect(row.querySelector(".entry-sub")?.textContent).toBe("we moved the cache to redis");
    // A count borrows the time slot as text, with no tooltip: it has no absolute form.
    const count = row.querySelector(".entry-time")!;
    expect(count.textContent).toBe("3 matches");
    expect(count.tagName).toBe("SPAN");
    expect(count.hasAttribute("data-tooltip")).toBe(false);
    // Under the Chats pane, not a third container.
    expect(table.id).toBe("history-table");
  });

  // A title-only match has no hit, and must not render an empty excerpt.
  it("explains a title-only match instead of showing an empty excerpt", async () => {
    const { best: _best, ...titleOnly } = match({ hits: 0 });
    const { table } = await search({
      matches: [titleOnly],
      scanned: 4,
      matched: 1,
      truncated: false,
    });
    expect(table.querySelector(".entry-sub")?.textContent).toBe("matches the conversation name");
    expect(table.querySelector(".entry-time")).toBeNull();
  });

  it("opens the matched chat on click and hands its find the query and the best hit", async () => {
    // Without the handoff the reader lands in the chat with nothing marked.
    const { table } = await search({
      matches: [match()],
      scanned: 3,
      matched: 1,
      truncated: false,
    });
    openBtn(table.querySelector("[data-search-chat]")!).click();
    expect(openChatTab).toHaveBeenCalledWith("c-redis", "Redis migration");
    // Search results are existing chats; adopting a session is the other door.
    expect(openPreviousSession).not.toHaveBeenCalled();
    // After the tab resolved: the switch clears the transcript's box, undoing an earlier handoff.
    await vi.waitFor(() => {
      expect(openChatFindAt).toHaveBeenCalledTimes(1);
    });
    expect(openChatFindAt).toHaveBeenCalledWith("redis", match().best);
  });

  it("opens a title-only match without a find, because it has no hit to step to", async () => {
    const { best: _best, ...titleOnly } = match({ hits: 0 });
    const { table } = await search({
      matches: [titleOnly],
      scanned: 4,
      matched: 1,
      truncated: false,
    });
    openBtn(table.querySelector("[data-search-chat]")!).click();
    await vi.waitFor(() => {
      expect(openChatTab).toHaveBeenCalledWith("c-redis", "Redis migration");
    });
    await Promise.resolve();
    expect(openChatFindAt).not.toHaveBeenCalled();
  });

  it("hands nothing to a find when the chat did not open", async () => {
    // Deleted since the search: no transcript for the query, and opening the box would search the active chat.
    openChatTab.mockResolvedValueOnce("not-found");
    const { table } = await search({
      matches: [match()],
      scanned: 3,
      matched: 1,
      truncated: false,
    });
    openBtn(table.querySelector("[data-search-chat]")!).click();
    await vi.waitFor(() => {
      expect(openChatTab).toHaveBeenCalledTimes(1);
    });
    await Promise.resolve();
    expect(openChatFindAt).not.toHaveBeenCalled();
  });

  // The shared grammar over one noun: "conversations" on both axes.
  it("states the cut and the scan's reach in the shared grammar", async () => {
    const { note } = await search({
      matches: [match()],
      scanned: 500,
      matched: 62,
      truncated: true,
    });
    // The cut is `matched > matches.length`; `truncated` means older conversations were not read.
    expect(note.textContent).toBe(
      "1 of 62 conversations shown; 500 conversations scanned, not everything was read",
    );
  });

  it("states a whole answer without a cut clause", async () => {
    const { note } = await search({
      matches: [match()],
      scanned: 12,
      matched: 1,
      truncated: false,
    });
    expect(note.textContent).toBe("1 conversation; 12 conversations scanned");
  });

  // Without saying the scan was capped, "no matches" implies the text is nowhere.
  it("says not everything was searched on an empty answer the scan did not finish", async () => {
    const { note } = await search({ matches: [], scanned: 500, matched: 0, truncated: true });
    expect(note.textContent).toBe(
      "No matches in 500 conversations, but not everything was searched",
    );
  });

  it("says plainly that nothing matched when the scan read everything", async () => {
    const { note } = await search({ matches: [], scanned: 7, matched: 0, truncated: false });
    expect(note.textContent).toBe("No matches");
  });

  it("surfaces a failed search rather than an empty list", async () => {
    const { note } = await search(null);
    expect(note.textContent).toBe("Could not search");
  });

  it("is a role=search landmark carrying the shared field attributes", async () => {
    // A search region, reachable by landmark navigation.
    await search({ matches: [match()], scanned: 3, matched: 1, truncated: false });
    const region = document.getElementById("hist-search");
    expect(region?.getAttribute("role")).toBe("search");
    expect(region?.getAttribute("aria-label")).toBe("Search conversations");
    // The same popup as the transcript's find.
    expect(region?.className).toContain("page-find");
    expect(region?.className).toContain("search-pop");
    expect(region?.className).toContain("uip-popup");
    const input = document.getElementById("hist-search-input") as HTMLInputElement;
    expect(input.getAttribute("autocomplete")).toBe("off");
    expect(input.getAttribute("enterkeyhint")).toBe("search");
    expect(input.placeholder, "the one string the page boxes genuinely differ on").toBe(
      "Search conversations\u2026",
    );
  });

  it("ships NO match-case toggle, because the endpoint would not read it", async () => {
    // GET /api/chats/search takes only `q` and always folds case (chat.searchOneChat), so a case toggle would be wired to
    // nothing.
    await search({ matches: [match()], scanned: 3, matched: 1, truncated: false });
    expect(document.querySelector('#hist-search [aria-label="Match case"]')).toBeNull();
    // The query carries only the text.
    expect(searchDispatch).toHaveBeenLastCalledWith("redis");
  });

  it("carries the MAGNIFIER, because it reaches past what is on screen", async () => {
    // The server reads every chat file, so this finds conversations the list lacks; a funnel would promise narrowing.
    await search({ matches: [match()], scanned: 3, matched: 1, truncated: false });
    expect(document.querySelector("#hist-search .page-find-icon circle")).not.toBeNull();
    expect(document.querySelector("#hist-search .page-find-icon polygon")).toBeNull();
    // The × says which of the two it closes.
    expect(document.querySelector('#hist-search [aria-label="Close search"]')).not.toBeNull();
  });

  it("closes on Escape, and the close is what returns the full list", async () => {
    const { table, find } = await search({
      matches: [match()],
      scanned: 3,
      matched: 1,
      truncated: false,
    });
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [] });
    const input = document.getElementById("hist-search-input") as HTMLInputElement;
    input.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    // The popup hides on transitionend (or its 400ms fallback), and focus leaves the field only then.
    await vi.waitFor(() => {
      expect(find.focused()).toBe(false);
    });
    expect(input.value).toBe("");
    await vi.waitFor(() => {
      if (table.querySelectorAll('[data-key^="s:"]').length === 0) {
        throw new Error("list not restored");
      }
    });
    expect(table.querySelector("[data-search-chat]")).toBeNull();
  });

  it("sends a search as typed, trailing space included", async () => {
    // A search, so the popup's trim rule leaves the text alone; the server splits on whitespace itself.
    await search({ matches: [match()], scanned: 3, matched: 1, truncated: false }, "redis ");
    expect(searchDispatch).toHaveBeenLastCalledWith("redis ");
  });

  it("shows the list, not a search, for a box holding only whitespace", async () => {
    const { table } = await search({
      matches: [match()],
      scanned: 3,
      matched: 1,
      truncated: false,
    });
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [] });
    searchDispatch.mockClear();

    const input = document.getElementById("hist-search-input") as HTMLInputElement;
    input.value = "   ";
    input.dispatchEvent(new Event("input"));
    await vi.waitFor(() => {
      if (table.querySelectorAll('[data-key^="s:"]').length === 0) {
        throw new Error("list not restored");
      }
    });
    expect(searchDispatch).not.toHaveBeenCalled();
    expect(table.querySelector("[data-search-chat]")).toBeNull();
  });

  it("returns to the full list when the box is cleared", async () => {
    const { table } = await search({
      matches: [match()],
      scanned: 3,
      matched: 1,
      truncated: false,
    });
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [] });

    const input = document.getElementById("hist-search-input") as HTMLInputElement;
    input.value = "";
    input.dispatchEvent(new Event("input"));
    await vi.waitFor(() => {
      if (table.querySelectorAll('[data-key^="s:"]').length === 0) {
        throw new Error("list not restored");
      }
    });
    expect(table.querySelector("[data-search-chat]")).toBeNull();
  });

  it("leaves the Runs pane its list while a search is open", async () => {
    // Search is the Chats pane's mode; runs still land and repaint on their own pane.
    await search({ matches: [match()], scanned: 3, matched: 1, truncated: false });
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [runRow] });
    const { refreshHistoryView } = await freshModule();
    refreshHistoryView();
    // The refresh re-runs the search on the chats pane.
    await vi.waitFor(() => {
      expect(searchDispatch.mock.calls.length).toBeGreaterThanOrEqual(2);
    });
    expect(chatsPane().querySelector("[data-search-chat]")).not.toBeNull();
    expect(chatsPane().querySelector('[data-key="s:sess_chat"]')).toBeNull();
  });
});

// The Runs box is a filter over the loaded list: there is no server-side run search, so the funnel.

describe("history: the runs filter", () => {
  beforeEach(resetAll);

  const nightly = {
    workflow_id: "wf_n",
    name: "nightly-sweep",
    status: "completed",
    updated_at: 5,
  };
  const lint = {
    workflow_id: "wf_l",
    name: "lint-all",
    workflow_name: "code-review",
    status: "failed",
    updated_at: 4,
  };

  async function filter(query: string): Promise<{ runs: HTMLElement; find: PageFind }> {
    const view = mountView();
    dispatch.mockResolvedValue({ sessions: [], runs: [nightly, lint] });
    const { forceHistoryTab, loadHistoryView, refreshHistoryView } = await freshModule();
    forceHistoryTab("runs");
    loadHistoryView();
    refreshHistoryView();
    await vi.waitFor(() => {
      if (view.querySelectorAll('[data-key^="r:"]').length < 2) {
        throw new Error("not rendered");
      }
    });
    const find = await openFind();
    const input = document.getElementById("hist-filter-input") as HTMLInputElement;
    input.value = query;
    input.dispatchEvent(new Event("input"));
    await vi.waitFor(() => {
      if (runsPane().querySelectorAll('[data-key^="r:"]').length === 2 && query !== "") {
        throw new Error("not filtered");
      }
    });
    return { runs: runsPane(), find };
  }

  it("is the toolbar's destination on the Runs pane, and it is a filter", async () => {
    const { find } = await filter("");
    expect(find.kind()).toBe("filter");
    const region = document.getElementById("hist-filter");
    expect(region?.getAttribute("role")).toBe("search");
    expect(region?.getAttribute("aria-label")).toBe("Filter workflow runs");
    // The funnel: it narrows what is on screen.
    expect(document.querySelector("#hist-filter .page-find-icon polygon")).not.toBeNull();
    expect(document.querySelector("#hist-filter .page-find-icon circle")).toBeNull();
    // No request: the rows are already here.
    expect(searchDispatch).not.toHaveBeenCalled();
  });

  it("narrows the loaded runs by name and states how many survived", async () => {
    const { runs } = await filter("night");
    expect(keysIn(runs)).toEqual(["r:wf_n"]);
    // The shared grammar over this pane's noun: what matched, then what was read.
    expect(document.getElementById("hist-filter-note")?.textContent).toBe("1 run; 2 runs scanned");
  });

  it("reads the recipe as well as the label", async () => {
    // The subtitle carries the recipe when it differs from the label, and the filter reads what the row shows.
    const { runs } = await filter("code-review");
    expect(keysIn(runs)).toEqual(["r:wf_l"]);
  });

  it("says so when nothing survives, rather than painting an empty card", async () => {
    const { runs } = await filter("zzz");
    expect(keysIn(runs)).toEqual([]);
    expect(runs.textContent).toBe("No workflow runs match the filter.");
    expect(document.getElementById("hist-filter-note")?.textContent).toBe("No matches");
  });

  it("restores the whole list when the box is cleared, without a fetch", async () => {
    const { runs } = await filter("night");
    const before = dispatch.mock.calls.length;
    const input = document.getElementById("hist-filter-input") as HTMLInputElement;
    input.value = "";
    input.dispatchEvent(new Event("input"));
    await vi.waitFor(() => {
      if (runs.querySelectorAll('[data-key^="r:"]').length < 2) {
        throw new Error("list not restored");
      }
    });
    expect(dispatch.mock.calls.length).toBe(before);
    expect(document.getElementById("hist-filter-note")?.textContent).toBe("");
  });

  it("routes the page's find by pane: a search on Chats, a filter on Runs", async () => {
    mountView();
    dispatch.mockResolvedValue({ sessions: [], runs: [] });
    const { forceHistoryTab, loadHistoryView } = await freshModule();
    loadHistoryView();
    const { pageFind } = await import("./find-registry.js");
    const find = pageFind("history")!;
    expect(find.kind()).toBe("search");
    forceHistoryTab("runs");
    expect(find.kind()).toBe("filter");
  });
});

describe("history: the per-row delete", () => {
  beforeEach(() => {
    resetAll();
    confirmMock.mockResolvedValue(true);
    deleteChatDispatch.mockResolvedValue({ ok: true });
    deleteRunDispatch.mockResolvedValue({ ok: true });
  });

  it("gives every settled row a delete button in the actions slot, chats and runs alike", async () => {
    await render({ sessions: [ownedRow], runs: [runRow] });
    for (const key of ["s:sess_owned", "r:wf_1"]) {
      const btn = rowOf(key).querySelector(".entry-actions > [data-history-delete]");
      expect(btn, `${key} has no delete button`).not.toBeNull();
      // Named for the row, so a screen reader announces what it removes.
      expect(btn?.getAttribute("aria-label")).toMatch(/^Delete /);
      expect(btn?.classList.contains("entry-delete")).toBe(true);
    }
  });

  // The run delete cancels a live run before removing it, so on a moving row the trash is a stop control the confirm
  // cannot describe. Stopping is the run page's job.
  it("withholds the delete button from a run that is still moving", async () => {
    await render({
      sessions: [],
      runs: [
        { ...runAt("running"), workflow_id: "wf_live" },
        { ...runAt("paused"), workflow_id: "wf_held" },
        { ...runAt("running"), workflow_id: "wf_agent", parent_chat_id: "c-launcher" },
      ],
    });

    for (const key of ["r:wf_live", "r:wf_held", "r:wf_agent"]) {
      const row = document.querySelector(`[data-key="${key}"]`);
      expect(row, `${key} is not listed`).not.toBeNull();
      expect(
        row?.querySelector("[data-history-delete]"),
        `${key} carries a delete button for a run that is still moving`,
      ).toBeNull();
      expect(row?.querySelector(".entry-actions")).toBeNull();
    }
  });

  // A bound already stopped the run, so a lingering `running` is a frame not yet landed, as in `runVerdict`.
  it("keeps the delete button on a run one of marotte's bounds already stopped", async () => {
    await render({
      sessions: [],
      runs: [{ ...runAt("running"), end_reason: "overran" }],
    });
    expect(rowOf("r:wf_1").querySelector("[data-history-delete]")).not.toBeNull();
  });

  it("keeps the delete button out of the open control, so it is not nested in one", async () => {
    // The open control is a button holding the row's content, the delete button its sibling; neither nests.
    await render({ sessions: [ownedRow], runs: [runRow] });
    for (const key of ["s:sess_owned", "r:wf_1"]) {
      const row = rowOf(key);
      expect(row.getAttribute("role"), `${key} row is a control`).toBe("listitem");
      expect(row.getAttribute("tabindex"), `${key} row is focusable`).toBeNull();
      const open = row.querySelector<HTMLElement>("button.entry-open");
      expect(open, `${key} has no open button`).not.toBeNull();
      expect(open?.getAttribute("aria-label")).toMatch(/^Open /);
      // Two controls, no nesting.
      const controls = [...row.querySelectorAll("button, [role='button'], [tabindex]")];
      expect(controls).toHaveLength(2);
      const del = row.querySelector<HTMLElement>("[data-history-delete]")!;
      expect(open?.contains(del), `${key}'s delete button is inside the open control`).toBe(false);
      // The control holds the row's content, which makes its box the body; history-row-target.test.ts measures it.
      for (const part of [".entry-title", ".entry-time"]) {
        expect(
          open?.querySelector(part),
          `${key}: ${part} is outside the open control`,
        ).not.toBeNull();
      }
    }
  });

  it("deletes a conversation through the chat-delete command and closes no tab itself", async () => {
    await render({ sessions: [ownedRow], runs: [] });
    document.querySelector<HTMLElement>("[data-history-delete]")!.click();
    await vi.waitFor(() => {
      expect(deleteChatDispatch).toHaveBeenCalledWith("c-existing");
    });
    // delete_chat also reaps the KAS session chain. The tab is the membership coordinator's: it closes tabs for a deleted
    // chat under the same lock, so a `close_tab` here would be a second close.
    expect(closeTab).not.toHaveBeenCalled();
    expect(deleteRunDispatch).not.toHaveBeenCalled();
  });

  it("names the runs a conversation's delete also removes", async () => {
    await render({
      sessions: [ownedRow],
      runs: [
        { ...runRow, parent_chat_id: "c-existing" },
        { ...runRow, workflow_id: "wf_2", parent_chat_id: "c-existing" },
        { ...runRow, workflow_id: "wf_other", parent_chat_id: "c-elsewhere" },
      ],
    });
    rowOf("s:sess_owned").querySelector<HTMLElement>("[data-history-delete]")!.click();
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalled();
    });
    expect(confirmMock).toHaveBeenCalledWith(
      'Delete this conversation? "Already open" and its stored history are removed for good. It also deletes the 2 workflow runs it started.',
      "Delete",
      "destructive",
    );
  });

  it("says nothing about runs when the conversation launched none", async () => {
    await render({ sessions: [ownedRow], runs: [runRow] });
    rowOf("s:sess_owned").querySelector<HTMLElement>("[data-history-delete]")!.click();
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalled();
    });
    expect(confirmMock).toHaveBeenCalledWith(
      'Delete this conversation? "Already open" and its stored history are removed for good.',
      "Delete",
      "destructive",
    );
  });

  it("deletes a run through the run-delete endpoint", async () => {
    await render({ sessions: [], runs: [runRow] });
    document.querySelector<HTMLElement>("[data-history-delete]")!.click();
    await vi.waitFor(() => {
      expect(deleteRunDispatch).toHaveBeenCalledWith("wf_1");
    });
    expect(deleteChatDispatch).not.toHaveBeenCalled();
    // A run is not a tab.
    expect(closeTab).not.toHaveBeenCalled();
  });

  it("does not ALSO open the row it is deleting", async () => {
    // Siblings, so a click on one never reaches the other.
    await render({ sessions: [ownedRow], runs: [] });
    document.querySelector<HTMLElement>("[data-history-delete]")!.click();
    await vi.waitFor(() => {
      expect(deleteChatDispatch).toHaveBeenCalled();
    });
    expect(openPreviousSession).not.toHaveBeenCalled();
    expect(openRunView).not.toHaveBeenCalled();
  });

  it("refreshes the list once the delete has landed", async () => {
    await render({ sessions: [ownedRow], runs: [] });
    const before = dispatch.mock.calls.length;
    document.querySelector<HTMLElement>("[data-history-delete]")!.click();
    await vi.waitFor(() => {
      expect(dispatch.mock.calls.length).toBeGreaterThan(before);
    });
  });

  it("deletes nothing when the confirm is declined", async () => {
    confirmMock.mockResolvedValue(false);
    await render({ sessions: [ownedRow], runs: [runRow] });
    rowOf("s:sess_owned").querySelector<HTMLElement>("[data-history-delete]")!.click();
    rowOf("r:wf_1").querySelector<HTMLElement>("[data-history-delete]")!.click();
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalledTimes(2);
    });
    expect(deleteChatDispatch).not.toHaveBeenCalled();
    expect(deleteRunDispatch).not.toHaveBeenCalled();
    expect(closeTab).not.toHaveBeenCalled();
  });

  it("leaves the row in place when the delete failed", async () => {
    // The action returns null on a non-2xx: a failed delete must not refresh, which would drop a row the server still holds.
    deleteChatDispatch.mockResolvedValue(null as never);
    await render({ sessions: [ownedRow], runs: [] });
    const before = dispatch.mock.calls.length;
    document.querySelector<HTMLElement>("[data-history-delete]")!.click();
    await vi.waitFor(() => {
      expect(deleteChatDispatch).toHaveBeenCalled();
    });
    expect(document.querySelector('[data-key="s:sess_owned"]')).not.toBeNull();
    expect(dispatch.mock.calls).toHaveLength(before);
    expect(closeTab).not.toHaveBeenCalled();
  });
});

describe("history: the row's subtitle and time", () => {
  function header(over: Record<string, unknown> = {}): unknown {
    return {
      id: "c-existing",
      name: "Rebuild the rail",
      model: "claude-opus-5",
      current_mode_id: "vibe",
      turn_count: 34,
      usage: { credits: 12.5, context_pct: 0, context_size: 0 },
      ...over,
    };
  }

  const sub = (key: string): string | undefined =>
    rowOf(key).querySelector(".entry-sub")?.textContent ?? undefined;

  beforeEach(() => {
    resetAll();
    storeGet.mockReturnValue(undefined);
  });

  it("states model, mode, turns and credits from the chat record", async () => {
    // Every field comes from /api/chats, already held, so the richer row costs no request. The count is the header's.
    storeGet.mockReturnValue(header());
    await render({ sessions: [ownedRow], runs: [] });
    expect(sub("s:sess_owned")).toBe("claude-opus-5 · Default · 34 turns · 12.50 cr");
  });

  it("omits a zero credit total rather than printing 0.00", async () => {
    // An unmetered chat would carry a cost column that says nothing.
    storeGet.mockReturnValue(header({ turn_count: 1, usage: { credits: 0 } }));
    await render({ sessions: [ownedRow], runs: [] });
    expect(sub("s:sess_owned")).toBe("claude-opus-5 · Default · 1 turn");
  });

  it("prefers the agent's own description to the facts", async () => {
    storeGet.mockReturnValue(header());
    await render({ sessions: [{ ...ownedRow, description: "reading files" }], runs: [] });
    expect(sub("s:sess_owned")).toBe("reading files");
  });

  it("renders an EMPTY subtitle for a chat with no description the store does not know", async () => {
    // Placeholders would read as fact; the line stays so the title keeps its offset.
    storeGet.mockReturnValue(undefined);
    await render({ sessions: [ownedRow], runs: [] });
    expect(sub("s:sess_owned")).toBe("");
  });

  it("states a run's duration, and nothing when it never started", async () => {
    await render({
      sessions: [],
      runs: [
        {
          workflow_id: "wf_timed",
          name: "nightly",
          status: "completed",
          started_at: 1000,
          updated_at: 901000,
        },
        { workflow_id: "wf_untimed", name: "never-ran", status: "failed", updated_at: 2000 },
      ],
    });
    expect(sub("r:wf_timed")).toBe("15m");
    expect(sub("r:wf_untimed")).toBe("");
  });

  it("times a resumed run from its creation, because a resume restamps its start", async () => {
    await render({
      sessions: [],
      runs: [
        {
          workflow_id: "wf_resumed",
          name: "nightly",
          status: "completed",
          created_at: 1000,
          started_at: 841000,
          updated_at: 901000,
        },
      ],
    });
    expect(sub("r:wf_resumed")).toBe("15m");
  });

  it("names the recipe only when the label is not already the recipe", async () => {
    await render({
      sessions: [],
      runs: [
        {
          ...runRow,
          workflow_id: "wf_labelled",
          name: "review-auth",
          workflow_name: "code-review",
        },
        { ...runRow, workflow_id: "wf_bare", name: "code-review", workflow_name: "code-review" },
      ],
    });
    expect(sub("r:wf_labelled")).toBe("code-review");
    expect(sub("r:wf_bare")).toBe("");
  });

  it("names the launching conversation when the store holds it", async () => {
    storeGet.mockImplementation((id: string) =>
      id === "c-launcher" ? header({ id, name: "Rebuild the rail" }) : undefined,
    );
    await render({
      sessions: [],
      runs: [
        { ...runRow, parent_chat_id: "c-launcher", started_at: 1000, updated_at: 61000 },
        { ...runRow, workflow_id: "wf_orphan", parent_chat_id: "c-gone" },
      ],
    });
    expect(sub("r:wf_1")).toBe("1m · from Rebuild the rail");
    expect(sub("r:wf_orphan")).toBe("");
  });

  it("shows the time relative at a glance and absolute on demand", async () => {
    // One vocabulary: relative under a day, a date beyond, the full timestamp as tooltip.
    const recent = Date.now() - 5 * 60_000;
    const old = Date.UTC(2024, 8, 12, 10, 0, 0);
    await render({
      sessions: [
        { ...chatRow, session_id: "sess_recent", updated_at: recent },
        { ...chatRow, session_id: "sess_old", updated_at: old },
      ],
      runs: [],
    });
    const recentTime = rowOf("s:sess_recent").querySelector<HTMLElement>("time.entry-time")!;
    expect(recentTime.textContent).toBe("5 minutes ago");
    expect(recentTime.getAttribute("data-tooltip")).toBe(absoluteTime(recent));
    expect(recentTime.getAttribute("datetime")).toBe(new Date(recent).toISOString());
    const oldTime = rowOf("s:sess_old").querySelector<HTMLElement>("time.entry-time")!;
    expect(oldTime.textContent).toBe(
      new Date(old).toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
        year: "numeric",
      }),
    );
    // Inside the open control, at the trailing end of the title line.
    expect(openBtn(rowOf("s:sess_recent")).contains(recentTime)).toBe(true);
  });
});
