// The tab strip as a PROJECTION. No case opens a tab by calling a mutator: each dispatches against
// `__test-helpers__/tabs-server.ts`, and opens/closes are awaited (they resolve once the row is IN
// the projection). Ids are opaque, addressed by `tabIdFor`. Mostly "event-first"; ordering is
// `tabs-projection.test.ts`'s subject.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type { Mock } from "vitest";
import { effect } from "@cplieger/reactive";

// Mock dependencies that tabs.ts imports at module level.
vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
vi.mock("./icons.js", () => ({
  ICON_CLOSE: "",
  ICON_TAB_CHAT: "",
  ICON_TAB_SETTINGS: "",
  ICON_TAB_GIT: "",
  ICON_TAB_FILES: "",
  ICON_TAB_RUN: "",
  ICON_TAB_WEB: "",
  ICON_TAB_AGENT: "",
  // roles.ts is in this graph now (tab-materialize.ts derives a delegate tab's
  // label from it), and Browser Mode links for real rather than reading
  // properties off a namespace object, so every name it imports has to be here.
  ICON_TAB_PLAN: "",
  ICON_TAB_SPEC: "",
  ICON_TAB_QUICK_SPEC: "",
  ICON_TAB_BUG: "",
  ICON_TAB_AUTONOMOUS: "",
  ICON_TAB_REVIEW: "",
  ICON_SUBAGENT_INTROSPECT: "",
  ICON_SUBAGENT_GATHERER: "",
  ICON_SUBAGENT_TASK: "",
  ICON_SUBAGENT_CREATOR: "",
  ICON_TAB_EDITOR: "",
  ICON_TAB_HISTORY: "",
  ICON_TAB_DOCS: "",
  ICON_TAB_SUBTAB: "",
  ICON_SEND: "",
  ICON_SPINNER: "",
  ICON_HOURGLASS: "",
  ICON_ALERT: "",
  ICON_PIN_FILLED: "",
}));
// The active tab is this SCREEN's, so it is written per field to its own module
// rather than folded into any shared document. Stubbed here so the cases below
// can read it back without touching localStorage.
vi.mock("./device-view.js", () => {
  let active = "";
  return {
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
});
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        // tabList and promptInput are stable attached elements (keyboard navigation; last-tab close focuses
        // the composer, and the rollback reads it).
        if (prop === "tabList") {
          let tl = document.getElementById("tab-list");
          if (tl === null) {
            tl = document.createElement("div");
            tl.id = "tab-list";
            document.body.appendChild(tl);
          }
          return tl;
        }
        if (prop === "sidebar") {
          let sidebar = document.getElementById("sidebar");
          if (sidebar === null) {
            sidebar = document.createElement("aside");
            sidebar.id = "sidebar";
            document.body.appendChild(sidebar);
          }
          return sidebar;
        }
        if (prop === "promptInput") {
          let input = document.getElementById("prompt-input");
          if (input === null) {
            input = document.createElement("textarea");
            input.id = "prompt-input";
            document.body.appendChild(input);
          }
          return input;
        }
        return document.createElement("div");
      },
    },
  ),
  // `byId` (page-title.ts, every view switch) must exist for real ESM linking.
  byId: (id: string) => {
    let el = document.getElementById(id);
    if (el === null) {
      el = document.createElement("span");
      el.id = id;
      document.body.appendChild(el);
    }
    return el;
  },
}));
// Type-only, for the `importOriginal` below.
import type * as TabsDrag from "./tabs-drag.js";
// Spreading the original keeps `exceedsSlop` real, so the tap-vs-drag cases below
// measure against the production slop rather than a restated number that could drift.
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
  setTapCallback: vi.fn(),
}));
// The COMPLETE store helper (the factory's name stores, the close's active-pointer writers, and
// what actions/chat.js links): a partial factory fails the file when store.ts grows an export.
vi.mock("./store.js", () =>
  import("./__test-helpers__/store-mock.js").then((m) => ({ ...m.storeMock })),
);
vi.mock("./run-store.js", () => ({
  // Inert here; a Browser-Mode mock is linked as real ESM, so a name any module in the graph
  // reaches has to exist on it.
  runLabelOf: vi.fn(() => ""),
}));
// The composer half of the close gesture: retargeting and the failed-send
// restore are per-chat state this suite does not stage, so both are inert.
vi.mock("./composer-state.js", () => ({
  retargetComposer: vi.fn(),
  restoreFailedSend: vi.fn(),
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  flushComposerDraft: vi.fn(),
  dropComposerState: vi.fn(),
  seedComposerState: vi.fn(),
  adoptRemoteComposerState: vi.fn(),
  noteComposerText: vi.fn(),
  initComposerState: vi.fn(),
  _resetComposerStateForTest: vi.fn(),
}));
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
// The fake collection: `send` answers the four tab commands off it, `newOpID`
// mints the correlation id, and `apiGetTyped` is the boot read.
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => ({
    apiGetTyped: m.tabListRead(),
    // Present-but-inert so real-ESM linking succeeds: tabs.ts imports
    // `normalizeDirPath` from files-shared.js, which imports this name. No case
    // here reaches a listing fetch.
    apiGet: vi.fn(() => Promise.resolve(null)),
  })),
);

import {
  openTab,
  openEditorView,
  openRunTab,
  closeTab,
  activateTab,
  renameTab,
  hasTab,
  tabIdFor,
  tabIdForRoute,
  getActiveTabId,
  getActiveTabKind,
  tabSetVersion,
  activeChatRef,
  setOnEmpty,
  setTabPinned,
  openFilesView,
  toggleFilesView,
  filesTabForRoute,
  refreshActiveView,
  subscribeTabCues,
  registerTabNotice,
  _resetForTest,
} from "./tabs.js";
// The REAL freshness leaf: `viewStale` is what the dispatcher spends, so a case
// that wants a FRESH view records a load the way `loadMessages` does rather than
// faking the verdict. `chat` is the one kind whose ledger can answer "fresh" at all.
import { observeStamp, _resetForTest as _resetFreshnessForTest } from "./subject-versions.js";
import { closeTabCommand } from "./actions/tabs.js";
import { restoreFailedSend, retargetComposer } from "./composer-state.js";
import { info as toastInfo, error as toastErrorFn } from "./toast.js";
import {
  attachDrag,
  pointerDragActivation,
  setReorderCallback,
  setTapCallback,
} from "./tabs-drag.js";
import { $ } from "./dom.js";
import { showContextMenu } from "./context-menu.js";
import type { ContextMenuItem } from "./context-menu.js";
import type { OpenTabOutcome } from "./tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import type { TabOpeners } from "./tab-materialize.js";
import {
  ingestTabsChanged,
  listTabs,
  beginRemove,
  removeCommitted,
  opTimedOut,
  _resetTabsSyncForTest,
} from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer, settleTabs } from "./__test-helpers__/tabs-server.js";
import type { TabKind } from "./types.js";

// The harness takes the sync layer's two entry points by hand: its own module
// cannot import them, because the `api-client.js` mock factory imports the
// harness and that module is what tabs-sync reads the collection through.
bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

// The store's own reorderTabs, captured HERE because tabs.ts registers it once
// at module load and every suite below clears mocks in its beforeEach. Driving it
// is exactly what a completed drag does.
const commitDrop = vi.mocked(setReorderCallback).mock.calls[0]?.[0];
// What a lifted hold released without travel asks the strip to do.
const tapRow = vi.mocked(setTapCallback).mock.calls[0]?.[0];

// `materializeTab` needs openers registered; a spec's `onClose` is the FACTORY's, so a teardown is
// watched through the opener it delegates to.

interface Openers {
  chatShow: Mock<TabOpeners["chat"]["show"]>;
  chatRefresh: Mock<TabOpeners["chat"]["refresh"]>;
  chatClose: Mock<TabOpeners["chat"]["close"]>;
  editorShow: Mock<TabOpeners["editor"]["show"]>;
  editorRefresh: Mock<TabOpeners["editor"]["refresh"]>;
  editorClose: Mock<TabOpeners["editor"]["close"]>;
  runShow: Mock<TabOpeners["run"]["show"]>;
  runRefresh: Mock<TabOpeners["run"]["refresh"]>;
  subagentShow: Mock<TabOpeners["subagent"]["show"]>;
  subagentRefresh: Mock<TabOpeners["subagent"]["refresh"]>;
  specShow: Mock<TabOpeners["spec"]["show"]>;
  specRefresh: Mock<TabOpeners["spec"]["refresh"]>;
}

let openers: Openers;

function registerOpeners(): void {
  openers = {
    chatShow: vi.fn<TabOpeners["chat"]["show"]>(),
    chatRefresh: vi.fn<TabOpeners["chat"]["refresh"]>(),
    chatClose: vi.fn<TabOpeners["chat"]["close"]>(),
    editorShow: vi.fn<TabOpeners["editor"]["show"]>(),
    editorRefresh: vi.fn<TabOpeners["editor"]["refresh"]>(),
    editorClose: vi.fn<TabOpeners["editor"]["close"]>(),
    runShow: vi.fn<TabOpeners["run"]["show"]>(),
    runRefresh: vi.fn<TabOpeners["run"]["refresh"]>(),
    subagentShow: vi.fn<TabOpeners["subagent"]["show"]>(),
    subagentRefresh: vi.fn<TabOpeners["subagent"]["refresh"]>(),
    specShow: vi.fn<TabOpeners["spec"]["show"]>(),
    specRefresh: vi.fn<TabOpeners["spec"]["refresh"]>(),
  };
  registerTabOpeners({
    chat: {
      show: openers.chatShow,
      refresh: openers.chatRefresh,
      close: openers.chatClose,
      dot: () => "",
    },
    editor: {
      show: openers.editorShow,
      refresh: openers.editorRefresh,
      close: openers.editorClose,
    },
    run: { show: openers.runShow, refresh: openers.runRefresh },
    subagent: { show: openers.subagentShow, refresh: openers.subagentRefresh },
    spec: { show: openers.specShow, refresh: openers.specRefresh },
  });
}

// --- Openers the cases below drive ---

/** Open a chat tab for `ref`. Awaited, because the row lands with the frame. */
function openChat(
  ref: string,
  opts: { activate?: boolean; parent?: string; owns?: boolean } = {},
): Promise<OpenTabOutcome> {
  return openTab({ kind: "chat", ref, ...opts });
}

/** The projection's id for a chat, read back rather than composed. */
function chatID(ref: string): string {
  return tabIdFor("chat", ref);
}

async function openChats(...refs: string[]): Promise<Record<string, string>> {
  const ids: Record<string, string> = {};
  for (const ref of refs) {
    await openChat(ref);
    ids[ref] = chatID(ref);
  }
  return ids;
}

/** renderDOM is RAF-deferred, so a DOM assertion has to wait for it. */
function paint(): Promise<void> {
  return new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

function rows(): HTMLElement[] {
  const list = document.getElementById("tab-list");
  return [...(list?.querySelectorAll<HTMLElement>("[data-tab-id]") ?? [])];
}

async function rowIDs(): Promise<string[]> {
  await paint();
  return rows().map((e) => e.dataset["tabId"] ?? "");
}

/** The rendered strip as REFS rather than opaque ids, which is what makes an
 *  order assertion readable. A row whose subject the projection has dropped
 *  answers with its raw id so a stray one is visible rather than blank. */
async function rowRefs(): Promise<string[]> {
  const ids = await rowIDs();
  const byID = new Map(tabServer.subjects().map((s) => [s.id, s.ref === "" ? s.kind : s.ref]));
  return ids.map((id) => byID.get(id) ?? id);
}

beforeEach(() => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerOpeners();
  resetActionFramework();
  _resetForTest();
  _resetFreshnessForTest();
  // Provide minimal DOM for renderDOM subscriber (tab-list element).
  document.body.innerHTML = '<div id="tab-list"></div>';
});

afterEach(() => {
  _resetTabsSyncForTest();
});

describe("openTab", () => {
  it("opens a new tab and activates it", async () => {
    expect.assertions(2);
    await openChat("a");
    expect(hasTab("chat", "a")).toBe(true);
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  it("activates the tab a lifted tap names", async () => {
    expect.assertions(2);
    await openChats("a", "b");
    expect(tapRow).toBeTypeOf("function");
    tapRow?.(chatID("a"));
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  it("opens one tab per distinct subject", async () => {
    expect.assertions(4);
    await openChats("a", "b", "c");
    for (const ref of ["a", "b", "c"]) {
      expect(hasTab("chat", ref)).toBe(true);
    }
    expect(getActiveTabId()).toBe(chatID("c"));
  });

  // `(kind, ref)` uniqueness makes a second open idempotent; `created: false` emits no frame, so
  // openTab resolves from the response.
  it("re-activates rather than opening a second tab for one subject", async () => {
    expect.assertions(3);
    await openChats("a", "b");
    await openChat("a");
    expect(getActiveTabId()).toBe(chatID("a"));
    expect(tabServer.sentOfType("open_tab")).toHaveLength(3);
    expect(await rowRefs()).toEqual(["a", "b"]);
  });

  // `activate: false` is what a bulk restore passes: the strip is the reader's,
  // so restoring the saved set must not move the active tab.
  it("leaves the active tab alone when told not to activate", async () => {
    expect.assertions(2);
    await openChat("a");
    await openChat("b", { activate: false });
    expect(hasTab("chat", "b")).toBe(true);
    expect(getActiveTabId()).toBe(chatID("a"));
  });
});

// Editor tabs are MULTI-INSTANCE: uniqueness is on (kind, ref) and the ref is the path.
describe("openEditorView (multi-instance by path)", () => {
  it("opens one tab per distinct path", async () => {
    expect.assertions(5);
    for (const p of ["src/a.ts", "docs/b.md", "out/shot.png"]) {
      await openEditorView(p);
    }
    for (const p of ["src/a.ts", "docs/b.md", "out/shot.png"]) {
      expect(hasTab("editor", p)).toBe(true);
    }
    expect(getActiveTabId()).toBe(tabIdFor("editor", "out/shot.png"));
    expect(await rowRefs()).toEqual(["src/a.ts", "docs/b.md", "out/shot.png"]);
  });

  // Two pills for one file is the same file: re-activate, never a second tab.
  it("re-activates rather than duplicating when the same path opens twice", async () => {
    expect.assertions(2);
    await openEditorView("src/a.ts");
    await openEditorView("docs/b.md");
    await openEditorView("src/a.ts");
    expect(getActiveTabId()).toBe(tabIdFor("editor", "src/a.ts"));
    expect(await rowRefs()).toEqual(["src/a.ts", "docs/b.md"]);
  });

  // The path travels in the SUBJECT's ref and in the route, never parsed back out
  // of the id.
  it("names the tab by basename while the subject keeps the whole path", async () => {
    expect.assertions(3);
    await openEditorView("a/b/c.ts");
    await paint();
    const id = tabIdFor("editor", "a/b/c.ts");
    const tab = document.querySelector(`[data-tab-id="${id}"]`);
    expect(tab?.textContent).toContain("c.ts");
    expect(tab?.textContent).not.toContain("a/b");
    expect(tabServer.idFor("editor", "a/b/c.ts")).toBe(id);
  });
});

describe("closeTab", () => {
  // The successor is the most recently visited open tab; the first tab is only the fallback. Checked
  // on the boot read and every applied removal.
  it.each([
    {
      desc: "closing the active tab activates the most recently visited open tab",
      setup: ["a", "b", "c"],
      activate: "b",
      close: "b",
      expectActive: "c",
      expectHas: ["a", "c"],
      expectGone: ["b"],
    },
    {
      desc: "activates the previously visited tab when the closed one was last",
      setup: ["a", "b", "c"],
      activate: "c",
      close: "c",
      expectActive: "b",
      expectHas: ["a", "b"],
      expectGone: ["c"],
    },
    {
      desc: "closing an inactive tab preserves active",
      setup: ["a", "b", "c"],
      activate: "c",
      close: "a",
      expectActive: "c",
      expectHas: ["b", "c"],
      expectGone: ["a"],
    },
    {
      desc: "closing the only tab results in empty state",
      setup: ["a"],
      activate: "a",
      close: "a",
      expectActive: "",
      expectHas: [],
      expectGone: ["a"],
    },
  ])("$desc", async ({ setup, activate, close, expectActive, expectHas, expectGone }) => {
    expect.assertions(1 + expectHas.length + expectGone.length);
    await openChats(...setup);
    activateTab(chatID(activate));
    await closeTab(chatID(close));
    expect(getActiveTabId()).toBe(expectActive === "" ? "" : chatID(expectActive));
    for (const ref of expectHas) {
      expect(hasTab("chat", ref)).toBe(true);
    }
    for (const ref of expectGone) {
      expect(hasTab("chat", ref)).toBe(false);
    }
  });

  // Closing an id the collection does not hold is NOT an error: two devices can
  // close one tab, so the answer is an empty `closed` list and no frame.
  it("closing an id nothing holds leaves the strip alone", async () => {
    expect.assertions(3);
    await openChats("a", "b");
    await closeTab("tb_missing");
    expect(getActiveTabId()).toBe(chatID("b"));
    expect(hasTab("chat", "a")).toBe(true);
    expect(hasTab("chat", "b")).toBe(true);
  });

  // The projection REMOVES a tab before tearing it down, so a teardown that closes its own tab (the
  // editor's) finds it gone instead of recursing.
  it("a teardown that closes its own tab does not recurse", async () => {
    expect.assertions(3);
    let closes = 0;
    openers.editorClose.mockImplementation((path: string) => {
      closes++;
      void closeTab(tabIdFor("editor", path));
    });
    await openEditorView("self.ts");
    await closeTab(tabIdFor("editor", "self.ts"));
    expect(closes).toBe(1);
    expect(hasTab("editor", "self.ts")).toBe(false);
    expect(getActiveTabId()).toBe("");
  });

  it("a teardown observes the tab as already gone", async () => {
    expect.assertions(1);
    let presentDuringTeardown = true;
    openers.editorClose.mockImplementation(() => {
      presentDuringTeardown = hasTab("editor", "t.ts");
    });
    await openEditorView("t.ts");
    await closeTab(tabIdFor("editor", "t.ts"));
    expect(presentDuringTeardown).toBe(false);
  });

  it("removes the named tab and only that tab when a cascade precedes the splice", async () => {
    expect.assertions(4);
    await openChat("before");
    await openChat("p");
    await openTab({ kind: "chat", ref: "c", parent: chatID("p") });
    await openChat("after");
    await closeTab(chatID("p"));
    expect(hasTab("chat", "p")).toBe(false);
    expect(hasTab("chat", "c")).toBe(false);
    expect(hasTab("chat", "before")).toBe(true);
    expect(hasTab("chat", "after")).toBe(true);
  });

  // A refused close leaves the strip EXACTLY as it was, because nothing renders
  // optimistically. That is the whole reason the projection paints from the frame
  // rather than from the gesture.
  it("leaves the tab in place when the mutation fails", async () => {
    expect.assertions(3);
    await openChats("a", "b");
    tabServer.failNext("close_tab");
    await closeTab(chatID("b"));
    expect(hasTab("chat", "b")).toBe(true);
    expect(await rowRefs()).toEqual(["a", "b"]);
    expect(openers.chatClose).not.toHaveBeenCalled();
  });
});

// The last-tab close looks like pre-boot state, so the DOM subscriber must not skip it, or the row
// lingers until the empty-state respawn and both animate at once.
describe("closing the last tab", () => {
  it("starts the closed row's exit on close, not on the respawn", async () => {
    expect.assertions(2);
    await openChat("only");
    await paint();
    await closeTab(chatID("only"));
    await paint();
    expect(rows()).toHaveLength(1);
    expect(rows()[0]?.classList.contains("exiting")).toBe(true);
  });

  // The respawn must find an empty strip. No stylesheet, so the removal is driven by hand (the 0.18s
  // exit beats the 500ms empty-state delay).
  it("leaves the strip empty before the empty-state callback fires", async () => {
    expect.assertions(3);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    await openChat("only");
    await paint();
    await closeTab(chatID("only"));
    await paint();
    rows()[0]?.dispatchEvent(new Event("animationend"));
    expect(rows()).toHaveLength(0);
    expect(onEmpty).not.toHaveBeenCalled();
    // Re-opening cancels the pending respawn, so no timer outlives the test.
    await openChat("respawned");
    expect(onEmpty).not.toHaveBeenCalled();
  });

  // A REMOTE close must not mint a chat here, or devices loop minting chats. Provenance is `op_id`
  // correlation: a frame carrying this device's op is local.
  it("does not respawn when the LAST tab was closed remotely", async () => {
    expect.assertions(2);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    await openChat("only");
    tabServer.closeRemotely(chatID("only"));
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();

    // The local close still respawns: an empty strip is a dead end the reader did
    // not ask for when they closed the tab themselves.
    await openChat("mine");
    await closeTab(chatID("mine"));
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).toHaveBeenCalledTimes(1);
  });

  // The respawn DEFERS while a remove is pending; the settlement (here `closed: []`) re-arms it.
  it("defers the respawn while a remove is pending, and re-arms when it settles", async () => {
    expect.assertions(3);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    await openChats("held", "mine");

    // An optimistic close of "held" is in flight (the machine holds its op; the
    // gesture half that empties the strip belongs to the close task — what
    // matters here is the PENDING REMOVE, which is the deferral's whole input).
    const onConfirm = vi.fn();
    beginRemove("op-held", {
      id: chatID("held"),
      capturedTabIDs: [chatID("held")],
      onConfirm,
      rollback: vi.fn(),
    });
    // Another device's close of the same tab lands first: the row leaves the
    // strip, while OUR dispatch stays unanswered (a foreign frame settles no op).
    tabServer.closeRemotely(chatID("held"));
    await settleTabs();

    // The reader closes the last tab. The strip is empty, the respawn is due —
    // and deferred, because "held"'s remove is still pending.
    await closeTab(chatID("mine"));
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();

    // The response finally arrives: closed [] — the other device's close won.
    // Semantic confirmation settles the op and re-arms the respawn.
    removeCommitted("op-held", [], tabServer.version());
    expect(onConfirm).toHaveBeenCalledTimes(1);
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).toHaveBeenCalledTimes(1);
  });

  // VERIFYING arm: an unanswered close keeps the respawn waiting until an authoritative list settles it.
  it("defers the respawn while a remove is verifying, until an authoritative list settles it", async () => {
    expect.assertions(2);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    await openChats("held", "mine");
    beginRemove("op-held", {
      id: chatID("held"),
      capturedTabIDs: [chatID("held")],
      onConfirm: vi.fn(),
      rollback: vi.fn(),
    });
    tabServer.closeRemotely(chatID("held"));
    await settleTabs();
    await closeTab(chatID("mine"));

    // The dispatch times out: no restore, no retire — verifying.
    opTimedOut("op-held");
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();

    // An authoritative snapshot (a gap re-list, a verify tick — one mechanism)
    // no longer holds the row: silent confirmation, and the respawn re-arms.
    await listTabs();
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).toHaveBeenCalledTimes(1);
  });

  // The deferral's other exit: the settlement RESTORED a row, so the strip is
  // not empty and there is nothing to respawn.
  it("drops the deferred respawn when the settlement restores the row", async () => {
    expect.assertions(4);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    await openChats("held", "mine");
    const rollback = vi.fn();
    beginRemove("op-held", {
      id: chatID("held"),
      capturedTabIDs: [chatID("held")],
      onConfirm: vi.fn(),
      rollback,
    });
    // The row leaves the PROJECTION only: a foreign frame this device should not
    // have received (the server still holds the tab). The projection is now a
    // version ahead, so a same-version reopen elsewhere re-aligns them.
    tabServer.emitRaw({
      removed_ids: [chatID("held")],
      order: [chatID("mine")],
      version: tabServer.version() + 1,
    });
    tabServer.openElsewhere({ kind: "chat", ref: "other" });
    await settleTabs();
    await closeTab(chatID("mine"));
    opTimedOut("op-held");
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();

    // The authoritative list still holds "held": restore, not confirm. The strip
    // has rows again, so the deferred respawn is DROPPED rather than re-armed.
    await listTabs();
    expect(rollback).toHaveBeenCalledTimes(1);
    expect(hasTab("chat", "held")).toBe(true);
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();
  });
});

describe("activateTab", () => {
  it.each([
    { desc: "activates existing tab", target: "a", expectActive: "a" },
    { desc: "keeps an already-active tab active", target: "b", expectActive: "b" },
  ])("$desc", async ({ target, expectActive }) => {
    expect.assertions(1);
    await openChats("a", "b");
    activateTab(chatID(target));
    expect(getActiveTabId()).toBe(chatID(expectActive));
  });

  it("activating a tab nothing holds is a no-op", async () => {
    expect.assertions(1);
    await openChats("a", "b");
    activateTab("tb_missing");
    expect(getActiveTabId()).toBe(chatID("b"));
  });

  it("closes the mobile drawer when the active tab is tapped", async () => {
    await openChats("a", "b");
    const sidebar = $.sidebar;
    sidebar.classList.add("open");

    activateTab(chatID("b"));

    expect(getActiveTabId()).toBe(chatID("b"));
    expect(sidebar.classList.contains("open")).toBe(false);
  });

  // A CLOSE IS NOT NAVIGATION: closing a tab from the phone drawer must not dismiss the drawer.
  it.each([
    { desc: "the active tab", close: "b" },
    { desc: "a background tab", close: "a" },
  ])("leaves the mobile drawer open when closing $desc", async ({ close }) => {
    expect.assertions(1);
    await openChats("a", "b");
    const sidebar = $.sidebar;
    sidebar.classList.add("open");

    await closeTab(chatID(close));

    expect(sidebar.classList.contains("open")).toBe(true);
  });
});

// The in-memory history is read through the one decision it drives: which tab a close hands over to.
describe("MRU activation history", () => {
  // A subtree close skips every removed id; the most recent entry is the removed child, and the
  // target is neither it nor position 0.
  it("skips a whole closed subtree, not just the clicked tab", async () => {
    expect.assertions(2);
    await openChats("first", "a");
    await openChat("p");
    await openTab({ kind: "chat", ref: "c", parent: chatID("p") });
    // History: [c, p, a, first]. Re-activating the parent puts it at the head, so
    // the subtree holds the two most recent entries.
    activateTab(chatID("p"));

    await closeTab(chatID("p"));

    expect(getActiveTabId()).toBe(chatID("a"));
    expect(hasTab("chat", "c")).toBe(false);
  });

  // The first-tab rule is the FALLBACK now, not the rule, and it still has to
  // work: a cold boot restores a strip with no history, and `activate: false` opens
  // never claim recency for a tab the reader has not visited.
  it("falls back to the first tab when the history is exhausted", async () => {
    expect.assertions(2);
    await openChat("a");
    await openChat("b", { activate: false });
    await openChat("c", { activate: false });
    // History: [a] alone — neither automatic open activated anything.
    await closeTab(chatID("a"));

    expect(getActiveTabId()).toBe(chatID("b"));
    expect(await rowRefs()).toEqual(["b", "c"]);
  });

  // Four tabs: with three, recency and position agree on the survivor.
  it("closing a non-active tab leaves active alone", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c", "d");
    activateTab(chatID("b")); // history [b, d, c, a]

    await closeTab(chatID("d"));
    expect(getActiveTabId()).toBe(chatID("b"));

    await closeTab(chatID("b"));
    expect(getActiveTabId()).toBe(chatID("c"));
  });

  // A reorder replaces `state.tabs` without changing set membership, so it must
  // not touch recency. The dropped order puts `b` first, so the assertion separates
  // "most recent" from "position 0".
  it("is not affected by a reorder", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c");
    activateTab(chatID("a")); // history [a, c, b]
    commitDrop?.([chatID("b"), chatID("a"), chatID("c")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["b", "a", "c"]);

    await closeTab(chatID("a"));
    expect(getActiveTabId()).toBe(chatID("c"));
  });

  // The `tabs_changed` door (`removed_ids`, `local: false`). The PRUNE is not asserted: stale entries
  // are inert (`hasRow`, never-reused ids), so only the pick is observable.
  it("hands over to the most recent survivor when another device closes the active tab", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c");
    activateTab(chatID("b")); // history [b, c, a]

    tabServer.closeRemotely(chatID("b"));
    await settleTabs();

    expect(hasTab("chat", "b")).toBe(false);
    expect(getActiveTabId()).toBe(chatID("c"));
  });

  // `removed_ids` is a LIST, and a remote close of a parent is where it carries
  // more than one id. The child is the entry behind the head, so a walk that
  // skipped only the id it was handed would land on a row that is gone.
  it("skips every id a remote removal names, not just the first", async () => {
    expect.assertions(2);
    await openChats("first", "a");
    await openChat("p");
    await openTab({ kind: "chat", ref: "c", parent: chatID("p") });
    activateTab(chatID("p")); // history [p, c, a, first]

    tabServer.closeRemotely(chatID("p"));
    await settleTabs();

    expect(hasTab("chat", "c")).toBe(false);
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  // A refused close restores the row under its ORIGINAL id, so its history place must return too.
  it("a refused close restores the closed tab's place in the history", async () => {
    expect.assertions(4);
    await openChats("a", "b", "c");
    activateTab(chatID("b"));
    activateTab(chatID("a")); // history [a, b, c]
    tabServer.failNext("close_tab");

    // Non-active, so the rollback restores the row without moving the active tab.
    await closeTab(chatID("b"));
    expect(hasTab("chat", "b")).toBe(true);
    expect(await rowRefs()).toEqual(["a", "b", "c"]);
    expect(getActiveTabId()).toBe(chatID("a"));

    await closeTab(chatID("a"));
    expect(getActiveTabId()).toBe(chatID("b"));
  });

  // A verify-settled restore: readList adopts the snapshot BEFORE the callback, so nothing is spliced
  // and only the restore can return the place.
  it("a verify-settled restore returns the closed tab's place in the history", async () => {
    expect.assertions(3);
    await openChats("first", "y", "x", "act"); // history [act, x, y, first]
    const doomedTab = chatID("x");

    // No answer at all: the response is held and the dispatch canceled, which is
    // the branch a real 5s timeout takes. Manual mode keeps the echo frame back,
    // or a matching op_id would confirm the op instead.
    tabServer.setMode("manual");
    tabServer.holdResponses();
    const closing = closeTab(doomedTab);
    closeTabCommand.cancel();
    tabServer.releaseResponses();
    await closing;
    expect(hasTab("chat", "x")).toBe(false);

    // The authoritative list still holds the row: the close never committed.
    const held = tabServer
      .subjects()
      .map((s) => ({ ...s }))
      .concat([{ id: doomedTab, kind: "chat", ref: "x", parent: "", pinned: false, owns: true }]);
    tabServer.queueList({ tabs: held, version: tabServer.version() + 1 });
    await listTabs();
    expect(hasTab("chat", "x")).toBe(true);

    // With the slot restored the active tab's close hands over to `x`; without it
    // the history reads [act, y, first] and `y` takes over instead.
    await closeTab(chatID("act"));
    expect(getActiveTabId()).toBe(chatID("x"));
  });

  // The captured slot is an ANCHOR, not an index: the dispatch await can move every entry. History
  // [b,a,c], close `c`, open `d` before the refusal: the anchor gives [d,b,a,c], an index [d,b,c,a].
  it("restores a rank the await window moved, not the index it was captured at", async () => {
    expect.assertions(5);
    await openChat("first", { activate: false });
    await openChats("a", "b", "c"); // history [c, b, a]
    activateTab(chatID("a"));
    activateTab(chatID("b")); // history [b, a, c] — `c` is the tail, behind `a`
    tabServer.failNext("close_tab");
    tabServer.holdResponses();

    // Both dispatches in flight at once, the OPEN released first, so its
    // activation lands inside the close's window and the history reads [d, b, a].
    const opening = openChat("d");
    const closing = closeTab(chatID("c"));

    // The interleaving is what the case DISCRIMINATES on, so it is asserted off the reactive seam.
    const seen: { active: string; back: boolean }[] = [];
    const stop = effect(() => {
      tabSetVersion();
      seen.push({ active: getActiveTabId(), back: hasTab("chat", "c") });
    });

    tabServer.releaseResponses();
    await opening;
    await closing;
    stop();
    // The frame the rollback landed on: `d` already active, so at the head.
    expect(seen.find((s) => s.back)).toEqual({ active: chatID("d"), back: true });
    expect(hasTab("chat", "c")).toBe(true);
    expect(getActiveTabId()).toBe(chatID("d"));

    // The rank, read the only way it is observable: `c` came back BEHIND `a`, so
    // closing `d` then `b` hands over to `a` rather than to the restored tab.
    await closeTab(chatID("d"));
    expect(getActiveTabId()).toBe(chatID("b"));
    await closeTab(chatID("b"));
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  // Adjacent removed entries each anchor on their predecessor, so the restore replays capture order
  // by SLOT; the child is more recent, where slot and parent-first order disagree.
  it("returns adjacent removed siblings in their original relative order", async () => {
    expect.assertions(5);
    await openChat("first", { activate: false });
    await openChat("a");
    await openChat("p");
    await openTab({ kind: "chat", ref: "c", parent: chatID("p") });
    await openChat("b"); // history [b, c, p, a] — the pair, adjacent, mid-history
    await openChat("d", { activate: false });
    tabServer.failNext("close_tab");
    tabServer.holdResponses();

    const closing = closeTab(chatID("p"));
    // The reader's gesture inside the window, synchronous: history [d, b, a].
    activateTab(chatID("d"));
    tabServer.releaseResponses();
    await closing;
    expect(hasTab("chat", "p")).toBe(true);
    expect(hasTab("chat", "c")).toBe(true);

    // [d, b, c, p, a]: the pair is back behind `b`, and `c` still outranks `p`.
    await closeTab(chatID("d"));
    expect(getActiveTabId()).toBe(chatID("b"));
    await closeTab(chatID("b"));
    expect(getActiveTabId()).toBe(chatID("c"));
    await closeTab(chatID("c"));
    expect(getActiveTabId()).toBe(chatID("p"));
  });

  // The anchor itself was closed: fall back to the tail (understating recency beats inverting it).
  it("falls back to the tail when the anchor itself was closed in the window", async () => {
    expect.assertions(4);
    await openChat("first", { activate: false });
    await openChats("a", "c", "s", "b"); // history [b, s, c, a]
    await openChat("d", { activate: false });
    tabServer.failNext("close_tab");
    tabServer.holdResponses();

    const closing = closeTab(chatID("c")); // anchored on `s`
    tabServer.closeRemotely(chatID("s")); // and `s` goes, mid-window
    activateTab(chatID("d")); // history [d, b, a]
    tabServer.releaseResponses();
    await closing;
    expect(hasTab("chat", "c")).toBe(true);
    expect(hasTab("chat", "s")).toBe(false);

    // [d, b, a, c], so `a` outranks the restored tab; the captured index put `c`
    // in front of it.
    await closeTab(chatID("d"));
    expect(getActiveTabId()).toBe(chatID("b"));
    await closeTab(chatID("b"));
    expect(getActiveTabId()).toBe(chatID("a"));
  });
});

// The RESYNC door: an asleep device gets the removal as a `GET /api/tabs` lacking the row, never a
// frame. Characterization: a re-list must not become a union or pin the active row.
describe("a resync that drops the active tab", () => {
  /** The set the server holds minus `gone`, adopted through a real re-list. */
  async function resyncWithout(...gone: readonly string[]): Promise<void> {
    tabServer.queueList({
      tabs: tabServer.subjects().filter((s) => !gone.includes(s.id)),
      version: tabServer.version() + 1,
    });
    await listTabs();
  }

  it("keeps a tab closed while this device was disconnected", async () => {
    expect.assertions(2);
    const ids = await openChats("a", "b");

    await resyncWithout(ids["b"] ?? "");

    // A snapshot is a COMPLETE set, so the row it does not name is gone rather
    // than merged back in.
    expect(hasTab("chat", "b")).toBe(false);
    expect(hasTab("chat", "a")).toBe(true);
  });

  it("hands the view to the most recently visited survivor", async () => {
    // `b` is active and gone. The MRU survivor is `c` while position 0 is `a`, so
    // the assertion separates the two rules rather than agreeing with both.
    expect.assertions(2);
    const ids = await openChats("a", "c", "b"); // history [b, c, a]

    await resyncWithout(ids["b"] ?? "");

    expect(getActiveTabId()).toBe(ids["c"]);
    expect(openers.chatShow).toHaveBeenLastCalledWith("c");
  });

  it("falls back to the first survivor when the history holds none", async () => {
    // The boot-shaped strip: two tabs the reader never visited, so nothing but the
    // departing row has a history entry.
    expect.assertions(1);
    await openChat("a", { activate: false });
    await openChat("z", { activate: false });
    await openChat("b"); // history [b] alone

    await resyncWithout(chatID("b"));

    expect(getActiveTabId()).toBe(chatID("a"));
  });

  it("reaches the empty state and respawns nothing when the whole set went", async () => {
    // `local: false`: a strip emptied by another device must not mint a chat here,
    // which is the shape of the loop that minted one every 1.5s on the live instance.
    expect.assertions(2);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    const ids = await openChats("a", "b");

    await resyncWithout(ids["a"] ?? "", ids["b"] ?? "");

    expect(getActiveTabId()).toBe("");
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();
  });
});

// Read INSIDE AN EFFECT (app.ts's find affordance), so it must track the tab set: BUS_TAB_CHANGED
// is deduped on the active tab id and misses sub-tab switches.
describe("getActiveTabKind is reactive", () => {
  it("re-runs an effect that reads it when the active tab changes", async () => {
    expect.assertions(3);
    await openChat("a");
    await openTab({ kind: "settings" });

    const seen: (TabKind | null)[] = [];
    const stop = effect(() => {
      seen.push(getActiveTabKind());
    });
    expect(seen).toEqual(["settings"]); // the settings tab is active after opening

    activateTab(chatID("a"));
    expect(seen.at(-1)).toBe("chat");

    activateTab(tabIdFor("settings"));
    expect(seen.at(-1)).toBe("settings");
    stop();
  });

  it("re-runs when the active tab is CLOSED and another takes over", async () => {
    expect.assertions(1);
    await openChat("a");
    await openTab({ kind: "settings" });

    const seen: (TabKind | null)[] = [];
    const stop = effect(() => {
      seen.push(getActiveTabKind());
    });

    await closeTab(tabIdFor("settings"));
    expect(seen.at(-1)).toBe("chat");
    stop();
  });
});

// A name override is keyed on the SUBJECT (it arrives before the server mints an id), so the row is
// BUILT with the right label.
describe("renameTab and the name a row renders", () => {
  it("renames an existing tab", async () => {
    expect.assertions(2);
    await openChat("a");
    renameTab(chatID("a"), "Renamed");
    await paint();
    expect(rows()[0]?.querySelector(".tab-name")?.textContent).toBe("Renamed");
    expect(hasTab("chat", "a")).toBe(true);
  });

  it("renaming a tab nothing holds is a no-op", async () => {
    expect.assertions(1);
    await openChat("a");
    expect(() => {
      renameTab("tb_missing", "Whatever");
    }).not.toThrow();
  });

  // The override is applied when the row is BUILT, not after: a caller's name
  // reaches `nameOverrides` before the dispatch, so the first paint carries it.
  it("renders a caller's name from the first frame, never the factory placeholder", async () => {
    expect.assertions(1);
    await openRunTab("wf-1", "Nightly sweep");
    await paint();
    expect(rows()[0]?.querySelector(".tab-name")?.textContent).toBe("Nightly sweep");
  });

  // A re-list rebuilds every row from the factory, so an override that lived only
  // on the row would be lost by any gap or 409.
  it("survives a re-list, which rebuilds every row", async () => {
    expect.assertions(1);
    await openRunTab("wf-1", "Nightly sweep");
    // A version two past local: the sync layer stops applying and re-lists.
    tabServer.emitRaw({ version: tabServer.version() + 2 });
    await settleTabs();
    await paint();
    expect(rows()[0]?.querySelector(".tab-name")?.textContent).toBe("Nightly sweep");
  });
});

describe("the tooltip a row carries", () => {
  /** A title far longer than any strip is wide, so the case cannot pass by the
   *  label happening to fit. */
  const LONG = "Rewrite the streaming parser so a delta extends its own subtask's block";

  it("carries the row's FULL title, past the width the label renders", async () => {
    expect.assertions(3);
    await openChat("a");
    renameTab(chatID("a"), LONG);
    await paint();
    expect(rows()[0]?.dataset["tooltip"]).toBe(LONG);
    // The label is the same string, and the truncation is CSS's; a stylesheet is
    // not loaded here, so the clip itself is 10-shell-app.css's own assertion.
    expect(rows()[0]?.querySelector(".tab-name")?.textContent).toBe(LONG);
    // data-tooltip is the app's tooltip system (tooltip.ts). Nothing gains a
    // native one.
    expect(rows()[0]?.hasAttribute("title")).toBe(false);
  });

  it("repaints on a rename, so the hover cannot name the previous title", async () => {
    expect.assertions(1);
    await openChat("a");
    await paint();
    renameTab(chatID("a"), LONG);
    await paint();
    expect(rows()[0]?.dataset["tooltip"]).toBe(LONG);
  });

  // Every kind, not just chat: an editor tab renders a basename and a files tab a
  // folder, and both truncate through the same rule.
  it("carries a NON-chat row's title too", async () => {
    expect.assertions(2);
    await openEditorView("/workspace/marotte/static-src/messages-blocks.ts");
    await paint();
    const label = rows()[0]?.querySelector(".tab-name")?.textContent ?? "";
    expect(label).not.toBe("");
    expect(rows()[0]?.dataset["tooltip"]).toBe(label);
  });

  it("survives a re-list, which rebuilds every row", async () => {
    expect.assertions(1);
    await openChat("a");
    renameTab(chatID("a"), LONG);
    // A version two past local: the sync layer stops applying and re-lists.
    tabServer.emitRaw({ version: tabServer.version() + 2 });
    await settleTabs();
    await paint();
    expect(rows()[0]?.dataset["tooltip"]).toBe(LONG);
  });

  it("carries no attribute at all for a row with an empty name", async () => {
    expect.assertions(2);
    await openChat("a");
    renameTab(chatID("a"), LONG);
    await paint();
    renameTab(chatID("a"), "");
    await paint();
    // Empty and present would leave `[data-tooltip]` — the selector tooltip.ts
    // resolves a trigger with — standing over nothing to show.
    expect(rows()[0]?.hasAttribute("data-tooltip")).toBe(false);
    expect(rows()[0]?.hasAttribute("title")).toBe(false);
  });
});

// hasTab is keyed by `(kind, ref)` rather than by id: ids are opaque, so a consumer
// holding a chat id or a path cannot construct one.
describe("hasTab", () => {
  it("returns false for an empty projection", () => {
    expect.assertions(1);
    expect(hasTab("chat", "a")).toBe(false);
  });

  it("returns true after open, false after close", async () => {
    expect.assertions(2);
    await openChat("a");
    expect(hasTab("chat", "a")).toBe(true);
    await closeTab(chatID("a"));
    expect(hasTab("chat", "a")).toBe(false);
  });

  it("answers per SUBJECT, so one kind's ref does not satisfy another's", async () => {
    expect.assertions(3);
    await openEditorView("src/a.ts");
    expect(hasTab("editor", "src/a.ts")).toBe(true);
    expect(hasTab("chat", "src/a.ts")).toBe(false);
    // A singleton's ref is empty, which is the one kind whose identity is its
    // kind.
    expect(hasTab("settings")).toBe(false);
  });
});

// What BACK/FORWARD asks first: an entry answering "" is a closed tab, so app.ts redirects instead
// of re-opening it (which would broadcast to every device).
describe("tabIdForRoute", () => {
  it("resolves the route a tab carries to that tab's id", async () => {
    expect.assertions(1);
    await openChat("a");
    expect(tabIdForRoute({ kind: "chat", id: "a" })).toBe(chatID("a"));
  });

  it("answers '' once that tab is closed", async () => {
    expect.assertions(2);
    await openChats("a", "b");
    const id = chatID("a");
    await closeTab(id);
    expect(tabIdForRoute({ kind: "chat", id: "a" })).toBe("");
    // And the neighbour that took over is still resolvable, so the redirect has
    // somewhere honest to land.
    expect(tabIdForRoute({ kind: "chat", id: "b" })).toBe(chatID("b"));
  });

  // The route kind is `file` and the tab kind is `editor`: the one place the two
  // vocabularies differ, and the one this lookup exists to stop a caller
  // re-deriving.
  it("resolves /file/{path} to the editor tab holding that path", async () => {
    expect.assertions(2);
    await openEditorView("src/a.ts");
    expect(tabIdForRoute({ kind: "file", path: "src/a.ts" })).toBe(tabIdFor("editor", "src/a.ts"));
    expect(tabIdForRoute({ kind: "file", path: "src/b.ts" })).toBe("");
  });

  // A singleton's sub-position is not identity, so a sub-tab deep link resolves to the open singleton.
  it("ignores a singleton's sub-position", async () => {
    expect.assertions(2);
    expect(tabIdForRoute({ kind: "settings", tab: "tools" })).toBe("");
    await openTab({ kind: "settings" });
    expect(tabIdForRoute({ kind: "settings", tab: "tools" })).toBe(tabIdFor("settings"));
  });

  // A files route names a FOLDER, so this consults `filesTabForRoute`: the generic path answered ""
  // for an unopened folder and redirected away from an open browser.
  it("resolves a files route to the OPEN browser, not only to an exact ref match", async () => {
    expect.assertions(3);
    // Nothing open: there is no browser to move, so the redirect is correct.
    expect(tabIdForRoute({ kind: "files", path: "/workspace/_ui-qa" })).toBe("");

    await openTab({ kind: "files", ref: "/workspace" });
    const open = tabIdFor("files", "/workspace");
    // The exact folder resolves, as any generic lookup would.
    expect(tabIdForRoute({ kind: "files", path: "/workspace" })).toBe(open);
    // And so does a DIFFERENT folder, which is the arm's reason for existing.
    expect(tabIdForRoute({ kind: "files", path: "/workspace/_ui-qa" })).toBe(open);
  });

  // "/" names no chat, so it resolves to nothing even with chats open. That is
  // what sends a back press onto "/" through the redirect, which canonicalizes it
  // to whatever is on screen — the same thing applyInitialRoute does on load.
  it("answers '' for the default route", async () => {
    expect.assertions(1);
    await openChat("a");
    expect(tabIdForRoute({ kind: "chat", id: "" })).toBe("");
  });
});

describe("keyboard navigation (real tabs.ts handler via rendered tab nodes)", () => {
  async function renderTabs(...refs: string[]): Promise<HTMLElement[]> {
    await openChats(...refs);
    await paint();
    const list = document.getElementById("tab-list");
    if (list === null) {
      throw new Error("tab-list missing");
    }
    return [...list.querySelectorAll<HTMLElement>('[role="tab"]')];
  }

  it("ArrowRight moves focus to the next tab and wraps past the last", async () => {
    expect.assertions(3);
    const nodes = await renderTabs("a", "b", "c");
    expect(nodes).toHaveLength(3);

    nodes[0]?.focus();
    nodes[0]?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(document.activeElement).toBe(nodes[1]);

    nodes[2]?.focus();
    nodes[2]?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(document.activeElement).toBe(nodes[0]);
  });

  it("ArrowLeft moves focus to the previous tab and wraps before the first", async () => {
    expect.assertions(2);
    const nodes = await renderTabs("a", "b", "c");

    nodes[2]?.focus();
    nodes[2]?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(document.activeElement).toBe(nodes[1]);

    nodes[0]?.focus();
    nodes[0]?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(document.activeElement).toBe(nodes[2]);
  });

  it("Home and End jump to the first and last tab", async () => {
    expect.assertions(2);
    const nodes = await renderTabs("a", "b", "c");

    nodes[1]?.focus();
    nodes[1]?.dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    expect(document.activeElement).toBe(nodes[0]);

    nodes[1]?.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    expect(document.activeElement).toBe(nodes[2]);
  });

  it("Enter activates the focused tab", async () => {
    expect.assertions(1);
    const nodes = await renderTabs("a", "b", "c");
    // c is active (last opened); Enter on the first tab's node activates it.
    nodes[0]?.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  // A close is a ROUND TRIP now, so focus has to move before the dispatch: waiting
  // for the removal to land would leave focus on <body> for the whole flight, and
  // the row the keyboard user is standing on is the element being removed.
  it("Delete moves focus to a surviving sibling before the close lands", async () => {
    expect.assertions(2);
    const nodes = await renderTabs("a", "b", "c");
    nodes[1]?.focus();
    tabServer.setMode("manual");
    nodes[1]?.dispatchEvent(new KeyboardEvent("keydown", { key: "Delete", bubbles: true }));
    expect(document.activeElement).toBe(nodes[2]);
    // The strip is untouched until the frame arrives, which is the honest surface
    // for a close that might be refused.
    expect(await rowIDs()).toHaveLength(3);
    tabServer.setMode("event-first");
    tabServer.flushFrames();
    await settleTabs();
  });
});

// `#tab-list` scrolls, so a drag must never activate the row under its first contact; a pan along an
// exhausted axis produces no cancel, so the guard is travelled distance. POINTER events, with
// `isPrimary` stated (it defaults false).
describe("a drag on the strip scrolls it and never activates a row", () => {
  const ORIGIN_X = 40;
  const ORIGIN_Y = 100;

  // `buttons` is stated: a held contact is what a reflow cannot fake (touch reports 1, then 0).
  function ptr(
    type: "pointerdown" | "pointermove" | "pointerup",
    x: number,
    y: number,
    pointerType: string,
  ): PointerEvent {
    return new PointerEvent(type, {
      pointerId: 1,
      pointerType,
      isPrimary: true,
      buttons: type === "pointerup" ? 0 : 1,
      clientX: x,
      clientY: y,
      bubbles: true,
      cancelable: true,
    });
  }

  /** One whole gesture on `target`: press at the origin, travel by `(dx, dy)`,
   *  release there. A zero travel sends no move at all, which is what a stationary
   *  tap looks like on the wire. */
  function gesture(target: HTMLElement, dx: number, dy: number, pointerType = "touch"): void {
    target.dispatchEvent(ptr("pointerdown", ORIGIN_X, ORIGIN_Y, pointerType));
    if (dx !== 0 || dy !== 0) {
      target.dispatchEvent(ptr("pointermove", ORIGIN_X + dx, ORIGIN_Y + dy, pointerType));
    }
    target.dispatchEvent(ptr("pointerup", ORIGIN_X + dx, ORIGIN_Y + dy, pointerType));
  }

  /** Two chats, painted. `b` is the active one, so an activation of `a` is visible
   *  and a suppressed one leaves `b` where it was. */
  async function renderTabs(): Promise<HTMLElement[]> {
    await openChats("a", "b");
    await paint();
    return rows();
  }

  const slop = pointerDragActivation("touch").slopPx;
  const past = slop + 1;

  it.each([
    { desc: "sideways", dx: past, dy: 0 },
    { desc: "vertically", dx: 0, dy: past },
    { desc: "diagonally", dx: past, dy: past },
  ])("leaves the active tab alone when the finger travels $desc", async ({ dx, dy }) => {
    expect.assertions(1);
    const nodes = await renderTabs();
    gesture(nodes[0] as HTMLElement, dx, dy);
    expect(getActiveTabId()).toBe(chatID("b"));
  });

  it("activates the row a stationary tap released on", async () => {
    expect.assertions(1);
    const nodes = await renderTabs();
    gesture(nodes[0] as HTMLElement, 0, 0);
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  // The slop is inclusive: a finger never holds perfectly still, and travel AT the
  // threshold is still the tap the reader meant. Measured as a DISTANCE, so a
  // diagonal wobble whose legs are each under the slop is judged on its length.
  it.each([
    { desc: "at the slop on one axis", dx: slop, dy: 0 },
    { desc: "diagonally inside it", dx: 5, dy: 5 },
  ])("activates the row a tap that wobbled $desc released on", async ({ dx, dy }) => {
    expect.assertions(1);
    const nodes = await renderTabs();
    gesture(nodes[0] as HTMLElement, dx, dy);
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  // The same Euclidean metric as the drag that would start: 4px on each axis is
  // 5.66px of travel, past the mouse's 5px, so this release is a drag's and must
  // not also activate the row.
  it("leaves the active tab alone when a mouse travels past its distance diagonally", async () => {
    expect.assertions(1);
    const nodes = await renderTabs();
    gesture(nodes[0] as HTMLElement, 4, 4, "mouse");
    expect(getActiveTabId()).toBe(chatID("b"));
  });

  it("activates the row a mouse clicked", async () => {
    expect.assertions(1);
    const nodes = await renderTabs();
    gesture(nodes[0] as HTMLElement, 0, 0, "mouse");
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  // A keyboard dismissal shifts client coordinates under a still pointer; read as travel it refuses
  // the activation.
  it("activates the row a click held through a viewport shift released on", async () => {
    expect.assertions(1);
    const nodes = await renderTabs();
    const vv = {
      height: 400,
      offsetTop: 300,
      addEventListener: (): void => undefined,
      removeEventListener: (): void => undefined,
    };
    vi.stubGlobal("visualViewport", vv);
    const row = nodes[0] as HTMLElement;

    row.dispatchEvent(ptr("pointerdown", ORIGIN_X, 320, "mouse"));
    vv.height = 700;
    vv.offsetTop = 0;
    row.dispatchEvent(ptr("pointermove", ORIGIN_X, 20, "mouse"));
    row.dispatchEvent(ptr("pointerup", ORIGIN_X, 20, "mouse"));

    expect(getActiveTabId()).toBe(chatID("a"));
  });

  // The × is the strip's one destructive control; read through the PROJECTION, since an optimistic
  // close leaves the row animating out.
  it("keeps the tab a drag started on the × released over", async () => {
    expect.assertions(2);
    const nodes = await renderTabs();
    gesture(nodes[0]?.querySelector<HTMLElement>(".tab-close") as HTMLElement, past, 0);
    await settleTabs();
    expect(hasTab("chat", "a")).toBe(true);
    expect(tabServer.sentOfType("close_tab")).toHaveLength(0);
  });

  it("closes the tab a stationary tap on the × released on", async () => {
    expect.assertions(1);
    const nodes = await renderTabs();
    gesture(nodes[0]?.querySelector<HTMLElement>(".tab-close") as HTMLElement, 0, 0);
    await settleTabs();
    expect(hasTab("chat", "a")).toBe(false);
  });
});

describe("setTabDirty (editor unsaved indicator)", () => {
  it("shows a steady dirty dot when dirty and clears it when clean", async () => {
    expect.assertions(4);
    const { setTabDirty } = await import("./tabs.js");
    await openEditorView("/a.ts");
    await paint();
    const id = tabIdFor("editor", "/a.ts");
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
    const dot = row?.querySelector<HTMLElement>(".tab-status-dot");
    const sr = row?.querySelector<HTMLElement>(".tab-status-sr");
    if (dot === null || dot === undefined || sr === null || sr === undefined) {
      throw new Error("dot missing");
    }

    setTabDirty(id, true);
    expect(dot.dataset["status"]).toBe("dirty");
    // The announced word rides the same write, so an editor tab is not the one
    // surface where the dot is colour-only.
    expect(sr.textContent).toBe(", unsaved changes");

    setTabDirty(id, false);
    // The attribute is REMOVED rather than emptied: `[data-status]` alone is the
    // CSS reveal condition, so an empty value would leave a clean file's tab
    // showing an idle-styled dot.
    expect(dot.hasAttribute("data-status")).toBe(false);
    expect(sr.textContent).toBe("");
  });

  it("no-ops when the tab is not mounted", async () => {
    expect.assertions(1);
    const { setTabDirty } = await import("./tabs.js");
    expect(() => {
      setTabDirty("tb_missing", true);
    }).not.toThrow();
  });
});

// The dot's AGE reaches a reader only through the tooltip and the screen-reader span, so these read
// those two.

describe("the dot's age", () => {
  /** Five minutes ago, so `relativeTime` gives a stable phrase (a sub-minute "just now" would pass
   *  with the argument dropped). */
  const FIVE_MIN_AGO = Date.now() - 5 * 60 * 1000;

  /** The tooltip and the announced word, which `paintDot` writes from ONE string.
   *  Read together so a case cannot pass on the surface a pointer reveals while
   *  the screen-reader channel says something else. */
  function dotText(id: string): { tooltip: string; announced: string } {
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
    const dot = row?.querySelector<HTMLElement>(".tab-status-dot");
    const sr = row?.querySelector<HTMLElement>(".tab-status-sr");
    return { tooltip: dot?.dataset["tooltip"] ?? "", announced: sr?.textContent ?? "" };
  }

  it("appends an age to the two OUTCOMES and to no other state", async () => {
    expect.assertions(7);
    const { setTabStatus } = await import("./tabs.js");
    await openChat("a");
    await paint();
    const id = chatID("a");

    setTabStatus(id, "done", FIVE_MIN_AGO);
    expect(dotText(id).tooltip, "done").toBe("turn finished · 5 minutes ago");
    setTabStatus(id, "failed", FIVE_MIN_AGO);
    expect(dotText(id).tooltip, "failed").toBe("turn failed · 5 minutes ago");

    // The other five describe NOW, so an age there would date a state that is
    // still true. `withAge` is reached only from the two arms above, which is what
    // keeps `NEUTRAL_PHRASE` total by type with no age term in it.
    for (const status of ["working", "waiting", "input", "idle", "dirty"] as const) {
      setTabStatus(id, status, FIVE_MIN_AGO);
      expect(dotText(id).tooltip, status).not.toMatch(/5 minutes ago/u);
    }
  });

  it("says exactly what it said before the argument existed when no since is supplied", async () => {
    expect.assertions(2);
    const { setTabStatus } = await import("./tabs.js");
    await openChat("a");
    await paint();
    const id = chatID("a");

    // The four production callers that pass nothing (`run-dots`, `turn-teardown`,
    // `subagent-dots`, `setTabDirty`) have to keep their old output byte for byte,
    // or the feature is a change to every dot rather than an addition to two.
    setTabStatus(id, "done");
    expect(dotText(id).tooltip).toBe("turn finished");
    expect(dotText(id).announced).toBe(", turn finished");
  });

  it("recovers the age when the element is rebuilt", async () => {
    expect.assertions(2);
    const { setTabStatus } = await import("./tabs.js");
    await openChat("a");
    await paint();
    const id = chatID("a");
    setTabStatus(id, "done", FIVE_MIN_AGO);
    expect(dotText(id).tooltip, "before the rebuild").toBe("turn finished · 5 minutes ago");

    // A fresh row reads the age back off the ROW (`row.dotSince`), or a finished chat stays ageless.
    document.getElementById("tab-list")?.replaceChildren();
    await openChat("b");
    await paint();

    expect(dotText(id).tooltip, "after the rebuild").toBe("turn finished · 5 minutes ago");
  });

  it("does not move the attention surfaces for a since-only change", async () => {
    expect.assertions(2);
    const { setTabStatus } = await import("./tabs.js");
    await openChat("a");
    await paint();
    const id = chatID("a");
    setTabStatus(id, "done", FIVE_MIN_AGO);

    let runs = 0;
    const stop = subscribeTabCues(() => {
      runs++;
    });
    expect(runs, "the effect's own first run").toBe(1);

    // Same state, newer age: `recordDotStatus` compares `dotStatus` alone, so the favicon/title fold
    // does not wake.
    setTabStatus(id, "done", Date.now() - 60 * 60 * 1000);
    stop();
    expect(runs, "after a since-only write").toBe(1);
  });

  // BOTH omitted-`since` rows, and each alone passes under one of the two wrong
  // readings: delete-always shows no age after a same-status repaint, and
  // preserve-always shows the previous turn's age after a state change.
  it("shows no age when a done repaint with no since follows a DIFFERENT state", async () => {
    expect.assertions(1);
    const { setTabStatus } = await import("./tabs.js");
    await openChat("a");
    await paint();
    const id = chatID("a");

    setTabStatus(id, "done", FIVE_MIN_AGO);
    setTabStatus(id, "working");
    setTabStatus(id, "done");

    // The age belongs to the STATE, so the previous turn's is not this turn's.
    expect(dotText(id).tooltip).toBe("turn finished");
  });

  it("preserves the age when a repaint with no since keeps the SAME state", async () => {
    expect.assertions(1);
    const { setTabStatus } = await import("./tabs.js");
    await openChat("a");
    await paint();
    const id = chatID("a");

    setTabStatus(id, "done", FIVE_MIN_AGO);
    // `turn-teardown.ts` repaints `done` with no `since` and the chat row effect
    // resupplies it a microtask later, so blanking here is half a visible blink.
    setTabStatus(id, "done");

    expect(dotText(id).tooltip).toBe("turn finished · 5 minutes ago");
  });
});

// cueCandidates feeds the favicon and `(N)` title count. The cue is the dot carried off-page, so a
// turn that ENDED raises one whatever its outcome; these pin the projection verbatim.

describe("cueCandidates", () => {
  it("reports every owned chat tab's dot VERBATIM, stopped verdicts included", async () => {
    expect.assertions(2);
    const { setTabStatus, cueCandidates } = await import("./tabs.js");
    await openChat("c-done");
    await openChat("c-failed");

    // `done` is what a cancelled or unreadable turn latches (store.ts
    // `outcomeLatch`), so this row IS the ratified case: it is indistinguishable
    // here from a turn that finished with an answer, deliberately.
    setTabStatus(chatID("c-done"), "done");
    setTabStatus(chatID("c-failed"), "failed");

    expect(cueCandidates()).toEqual([
      { id: chatID("c-done"), status: "done" },
      { id: chatID("c-failed"), status: "failed" },
    ]);

    // And a tab whose dot has never been written reports "" rather than a guess —
    // attention.ts reads that as NO INFORMATION and keeps the acknowledgement,
    // which is what stops a boot restore re-lighting a dismissed cue.
    await openChat("c-fresh");
    expect(cueCandidates()).toContainEqual({ id: chatID("c-fresh"), status: "" });
  });

  it("excludes a VIEW tab, so one chat is never counted twice", async () => {
    expect.assertions(1);
    const { setTabStatus, cueCandidates } = await import("./tabs.js");
    await openChat("c-owned");
    await openChat("c-watched", { owns: false });
    setTabStatus(chatID("c-owned"), "done");
    setTabStatus(chatID("c-watched"), "done");

    expect(cueCandidates()).toEqual([{ id: chatID("c-owned"), status: "done" }]);
  });

  it("reports a RUN tab's dot, which is the one non-chat kind that bears a cue", async () => {
    expect.assertions(1);
    const { setTabStatus, cueCandidates } = await import("./tabs.js");
    // A run tab's dot speaks the same vocabulary (`runStatusFor`), and a run outlives its launching
    // turn, so it is counted.
    await openRunTab("wf_1", "A run");
    setTabStatus(tabIdFor("run", "wf_1"), "done");

    expect(cueCandidates()).toEqual([{ id: tabIdFor("run", "wf_1"), status: "done" }]);
  });

  it("excludes an editor tab, whose dirty mark is not an agent state", async () => {
    expect.assertions(1);
    const { setTabStatus, cueCandidates } = await import("./tabs.js");
    await openEditorView("/a.ts");
    setTabStatus(tabIdFor("editor", "/a.ts"), "dirty");

    expect(cueCandidates()).toEqual([]);
  });

  // The `owns` conjunct is about CHATS: a run tab and its launching chat are two subjects, both counted.
  it("counts a run tab AND its launching chat, because they are two subjects", async () => {
    expect.assertions(1);
    const { setTabStatus, cueCandidates } = await import("./tabs.js");
    await openChat("c-launcher");
    await openRunTab("wf_2", "Its run", { parent: chatID("c-launcher"), owns: false });
    setTabStatus(chatID("c-launcher"), "failed");
    setTabStatus(tabIdFor("run", "wf_2"), "done");

    expect(cueCandidates()).toEqual([
      { id: chatID("c-launcher"), status: "failed" },
      { id: tabIdFor("run", "wf_2"), status: "done" },
    ]);
  });

  // The SERVER's `owns`, arriving as frames: `openTab`'s `owns ?? true` cannot tell a server that
  // omits it from one that states it.
  it("counts a chat tab the server authored as owned, and excludes one it authored as a view", async () => {
    expect.assertions(2);
    const { setTabStatus, cueCandidates } = await import("./tabs.js");
    tabServer.openElsewhere({ kind: "chat", ref: "c-owned-remote", owns: true });
    tabServer.openElsewhere({ kind: "chat", ref: "c-view-remote", owns: false });
    tabServer.flushFrames();
    await settleTabs();
    setTabStatus(chatID("c-owned-remote"), "done");
    setTabStatus(chatID("c-view-remote"), "done");

    expect(cueCandidates()).toContainEqual({
      id: chatID("c-owned-remote"),
      status: "done",
    });
    expect(cueCandidates().map((c) => c.id)).not.toContain(chatID("c-view-remote"));
  });

  // `owns` never changes after open, so a row's `spec` is a SNAPSHOT and a later frame cannot promote
  // a view into the cue.
  it("never lets a later frame change a tab's authority", async () => {
    expect.assertions(2);
    const { setTabStatus, cueCandidates } = await import("./tabs.js");
    const view = tabServer.openElsewhere({ kind: "chat", ref: "c-view", owns: false });
    tabServer.flushFrames();
    await settleTabs();
    setTabStatus(chatID("c-view"), "done");
    expect(cueCandidates()).toEqual([]);

    tabServer.emitRaw({
      changed: { ...view, owns: true },
      version: tabServer.version() + 1,
    });
    await settleTabs();

    expect(cueCandidates()).toEqual([]);
  });
});

// `subagent-dots.ts`'s seam: the TRACKED read is the contract (a delegate tab often arrives after its
// invocation). That suite mocks `tabs.js`, so it is pinned here.
describe("openSubagentRefs", () => {
  it("re-runs an effect that reads it when a subagent tab lands or leaves", async () => {
    expect.assertions(3);
    const { openSubagentRefs, openSubagentTab } = await import("./tabs.js");
    await openChat("c1");

    const seen: string[][] = [];
    const stop = effect(() => {
      seen.push(openSubagentRefs());
    });
    expect(seen).toEqual([[]]);

    await openSubagentTab("c1", "task-1");
    expect(seen.at(-1)).toEqual(["c1/task-1"]);

    await closeTab(tabIdFor("subagent", "c1/task-1"));
    expect(seen.at(-1)).toEqual([]);
    stop();
  });

  it("reports only subagent tabs, whatever else is on the strip", async () => {
    expect.assertions(1);
    const { openSubagentRefs, openSubagentTab } = await import("./tabs.js");
    await openChat("c1");
    await openRunTab("wf_1", "A run");
    await openEditorView("/a.ts");
    await openSubagentTab("c1", "task-1");

    // The COMPOSITE ref verbatim, because that is what carries the two halves the
    // dot needs; an id would say nothing.
    expect(openSubagentRefs()).toEqual(["c1/task-1"]);
  });
});

// `run-dots.ts`'s seam: a cold-restored run tab's arrival is the only signal a fetch is owed.
describe("openRunRefs", () => {
  it("re-runs an effect that reads it when a run tab lands or leaves", async () => {
    expect.assertions(3);
    const { openRunRefs } = await import("./tabs.js");

    const seen: string[][] = [];
    const stop = effect(() => {
      seen.push(openRunRefs());
    });
    expect(seen).toEqual([[]]);

    await openRunTab("wf_seed", "A run");
    expect(seen.at(-1)).toEqual(["wf_seed"]);

    await closeTab(tabIdFor("run", "wf_seed"));
    expect(seen.at(-1)).toEqual([]);
    stop();
  });

  it("reports a run SUB-tab as well as a top-level one, and nothing else", async () => {
    expect.assertions(1);
    const { openRunRefs } = await import("./tabs.js");
    await openChat("c1");
    await openEditorView("/a.ts");
    await openRunTab("wf_top", "Top-level");
    // Nested under its launching chat; both rows are restored from the same kind of
    // persisted subject.
    await openRunTab("wf_child", "Sub-tab", { parent: tabIdFor("chat", "c1") });

    // WORKFLOW ids, not tab ids: the run store is keyed by the ref. Sorted because
    // strip ORDER is not this seam's subject — the seed iterates the whole list, and
    // a sub-tab's position is its parent's (pinned in the sub-tabs block below).
    expect([...openRunRefs()].sort()).toEqual(["wf_child", "wf_top"]);
  });
});

// A sub-tab is a SUBJECT fact (`TabSubject.Parent`); the strip only lays itself out from it.

describe("sub-tabs", () => {
  /** Open a chat nested under the tab already open for `parentRef`. */
  function openChild(ref: string, parentRef: string): Promise<OpenTabOutcome> {
    return openTab({ kind: "chat", ref, parent: chatID(parentRef) });
  }

  it("sorts a child immediately after its parent, not at the end", async () => {
    expect.assertions(1);
    await openChats("p", "other");
    await openChild("c", "p");
    expect(await rowRefs()).toEqual(["p", "c", "other"]);
  });

  it("keeps several children in creation order under one parent", async () => {
    expect.assertions(1);
    await openChats("p", "other");
    await openChild("c1", "p");
    await openChild("c2", "p");
    expect(await rowRefs()).toEqual(["p", "c1", "c2", "other"]);
  });

  it("indents a child and leaves a top-level tab flat", async () => {
    expect.assertions(2);
    await openChat("p");
    await openChild("c", "p");
    await paint();
    const child = rows().find((r) => r.dataset["tabId"] === chatID("c"));
    const parent = rows().find((r) => r.dataset["tabId"] === chatID("p"));
    expect(child?.classList.contains("tab-child")).toBe(true);
    expect(parent?.classList.contains("tab-child")).toBe(false);
  });

  // A tab nobody can see is worse than a tab in the wrong place, which is the
  // server's rule for an unknown parent and the strip's for a parent it does not
  // hold.
  it("falls back to top-level when the parent is not open", async () => {
    expect.assertions(2);
    await openTab({ kind: "chat", ref: "orphan", parent: "tb_missing" });
    expect(hasTab("chat", "orphan")).toBe(true);
    expect(await rowRefs()).toEqual(["orphan"]);
  });

  // A parent and its children go as ONE mutation, so the removal arrives as one
  // frame naming every id.
  it("closes children with their parent, in one frame", async () => {
    expect.assertions(4);
    await openChat("p");
    await openChild("c1", "p");
    await openChild("c2", "p");
    const before = tabServer.version();
    await closeTab(chatID("p"));
    expect(hasTab("chat", "p")).toBe(false);
    expect(hasTab("chat", "c1")).toBe(false);
    expect(hasTab("chat", "c2")).toBe(false);
    expect(tabServer.version()).toBe(before + 1);
  });

  // Children first, so a child's teardown never runs against a parent that has
  // already gone.
  it("tears down children before the parent", async () => {
    expect.assertions(1);
    const order: string[] = [];
    openers.chatClose.mockImplementation((ref: string) => {
      order.push(ref);
    });
    await openChat("p");
    await openChild("c", "p");
    await closeTab(chatID("p"));
    expect(order).toEqual(["c", "p"]);
  });

  it("closing a child leaves the parent alone", async () => {
    expect.assertions(2);
    await openChat("p");
    await openChild("c", "p");
    await closeTab(chatID("c"));
    expect(hasTab("chat", "p")).toBe(true);
    expect(hasTab("chat", "c")).toBe(false);
  });

  // The exit animation is chosen after the row left the projection, so the parent id rides the
  // element (CSS in 10-shell-app.css).
  describe("the exit animation", () => {
    // A sub-tab folds UP into the row it hangs off, where its work came from.
    it("merges a child up into a parent that stays", async () => {
      expect.assertions(2);
      await openChat("p");
      await openChild("c", "p");
      const child = chatID("c");
      await paint();
      await closeTab(child);
      await paint();
      const row = rows().find((r) => r.dataset["tabId"] === child);
      expect(row?.classList.contains("exiting")).toBe(true);
      expect(row?.classList.contains("exiting-merge")).toBe(true);
    });

    // A child cannot merge into a row that is leaving too, so the whole subtree
    // takes the parent's own exit and the group reads as one block departing
    // rather than as N children folding into a vanishing target.
    it("sends a closed parent's children out with it, not into it", async () => {
      expect.assertions(6);
      await openChat("p");
      await openChild("c1", "p");
      await openChild("c2", "p");
      const ids = [chatID("p"), chatID("c1"), chatID("c2")];
      await paint();
      await closeTab(ids[0] ?? "");
      await paint();
      for (const id of ids) {
        const row = rows().find((r) => r.dataset["tabId"] === id);
        expect(row?.classList.contains("exiting")).toBe(true);
        expect(row?.classList.contains("exiting-merge")).toBe(false);
      }
    });

    // An ORPHAN carries a parent its strip does not hold, so it renders indented
    // with nothing above it to merge into. The survivor-set test answers that for
    // free: the parent is not open, so the row takes the sideways exit.
    it("swipes an orphan out rather than merging it into nothing", async () => {
      expect.assertions(2);
      await openTab({ kind: "chat", ref: "orphan", parent: "tb_missing" });
      const id = chatID("orphan");
      await paint();
      await closeTab(id);
      await paint();
      const row = rows().find((r) => r.dataset["tabId"] === id);
      expect(row?.classList.contains("exiting")).toBe(true);
      expect(row?.classList.contains("exiting-merge")).toBe(false);
    });
  });

  // `owns: false` is the VIEW case: dismissing it must not kill the watched work. Asserted on a CHAT,
  // the kind that still has an ownership axis (a run tab is always a view).
  it("does not tear down a tab that owns nothing", async () => {
    expect.assertions(2);
    await openChat("watch", { owns: false });
    await closeTab(chatID("watch"));
    expect(hasTab("chat", "watch")).toBe(false);
    expect(openers.chatClose).not.toHaveBeenCalled();
  });

  it("tears down an owning tab", async () => {
    expect.assertions(2);
    await openChat("mine");
    await closeTab(chatID("mine"));
    expect(hasTab("chat", "mine")).toBe(false);
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
  });

  it("still removes a view child when its parent closes", async () => {
    expect.assertions(1);
    await openChat("p");
    await openRunTab("wf-1", "A view", { parent: chatID("p") });
    await closeTab(chatID("p"));
    expect(hasTab("run", "wf-1")).toBe(false);
  });

  // A cascade reaches every DESCENDANT, not just the direct children, and an
  // owns:false one among them still skips its teardown through its ownership
  // check.
  it("tears down every owning descendant of a closed parent", async () => {
    expect.assertions(5);
    const order: string[] = [];
    openers.chatClose.mockImplementation((ref: string) => {
      order.push(ref);
    });
    await openChat("p");
    await openChild("c", "p");
    await openChild("gc", "c");
    await openRunTab("wf-view", "A view", { parent: chatID("p") });
    await closeTab(chatID("p"));
    expect(order).toEqual(["gc", "c", "p"]);
    // The run sub-tab went with the cascade and tore nothing down, which is what a
    // view child is: removed with its parent, never a teardown of its own.
    expect(hasTab("run", "wf-view")).toBe(false);
    for (const ref of ["p", "c", "gc"]) {
      expect(hasTab("chat", ref)).toBe(false);
    }
  });

  // A reorder names TOP-LEVEL ids and the projection expands them, so a drop on a
  // strip holding a sub-tab is a valid exact set rather than a 409.
  it("expands a drop's top-level order to the exact set the server demands", async () => {
    expect.assertions(2);
    await openChat("p");
    await openChild("c", "p");
    await openChat("other");
    commitDrop?.([chatID("other"), chatID("p")]);
    await settleTabs();
    expect(tabServer.sentOfType("reorder_tabs")).toHaveLength(1);
    expect(await rowRefs()).toEqual(["other", "p", "c"]);
  });

  it("is not independently draggable", async () => {
    expect.assertions(1);
    await openChat("p");
    await openChild("c", "p");
    await paint();
    // attachDrag runs once per DRAGGABLE row; the child must not get a handle,
    // because its position is its parent's rather than its own.
    expect(vi.mocked(attachDrag)).toHaveBeenCalledTimes(1);
  });
});

// `TabSubject.Pinned` is stored server-side; the pinned-first PARTITION is a client rendering rule
// over the stored order, kept in the ARRAY because a drop and the keyboard arrows read DOM order back.

describe("pinned tabs", () => {
  it("moves a pinned tab ahead of every unpinned one", async () => {
    expect.assertions(1);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("c"), true);
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
  });

  it("does not reshuffle the pinned run when another tab joins it", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("c"), true);
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
    await setTabPinned(chatID("a"), true);
    // The partition is stable, so pinning `a` only guarantees it sits above the
    // unpinned tabs — it does not overtake a pin that was already ahead of it.
    // Reordering WITHIN the run is what drag is for.
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
  });

  it("leaves a tab where it is when the pin is removed", async () => {
    expect.assertions(2);
    await openChats("a", "b");
    await setTabPinned(chatID("b"), true);
    expect(await rowRefs()).toEqual(["b", "a"]);
    await setTabPinned(chatID("b"), false);
    expect(await rowRefs()).toEqual(["b", "a"]);
  });

  it("opens a new unpinned tab after the pinned run", async () => {
    expect.assertions(1);
    await openChat("a");
    await setTabPinned(chatID("a"), true);
    await openChat("b");
    expect(await rowRefs()).toEqual(["a", "b"]);
  });

  // A drop is partitioned before it is shown or sent, so an illegal position snaps back; a fully
  // undone drop sends nothing.
  it("cannot be dragged below an unpinned tab", async () => {
    expect.assertions(3);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("a"), true);
    expect(commitDrop).toBeTypeOf("function");
    commitDrop?.([chatID("b"), chatID("c"), chatID("a")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["a", "b", "c"]);
    expect(tabServer.sentOfType("reorder_tabs")).toHaveLength(0);
  });

  // The drag announces and slides from this answer, so a snap-back must not read
  // as a move and a partial partition must report where the tab really went.
  it("answers the top-level order a drop applied, or null when it applied none", async () => {
    expect.assertions(3);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("a"), true);
    expect(commitDrop?.([chatID("b"), chatID("c"), chatID("a")])).toBeNull();
    expect(commitDrop?.([chatID("c"), chatID("b"), chatID("a")])).toEqual([
      chatID("a"),
      chatID("c"),
      chatID("b"),
    ]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["a", "c", "b"]);
  });

  it("sends the server the partitioned order it shows", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("a"), true);
    commitDrop?.([chatID("c"), chatID("b"), chatID("a")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["a", "c", "b"]);
    expect(tabServer.sentOfType("reorder_tabs")[0]?.payload["order"]).toEqual([
      chatID("a"),
      chatID("c"),
      chatID("b"),
    ]);
  });

  it("still honours a drag that reorders within the pinned run", async () => {
    expect.assertions(1);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("a"), true);
    await setTabPinned(chatID("b"), true);
    commitDrop?.([chatID("b"), chatID("a"), chatID("c")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["b", "a", "c"]);
  });

  it("carries a parent's children with it", async () => {
    expect.assertions(2);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await openChat("other");
    await setTabPinned(chatID("p"), true);
    expect(await rowRefs()).toEqual(["p", "kid", "other"]);
    await setTabPinned(chatID("other"), true);
    // `other` is pinned second, so it lands after the whole `p` group rather than
    // between a parent and its child.
    expect(await rowRefs()).toEqual(["p", "kid", "other"]);
  });

  // A tangent can parent on a side chat, so grouping must carry the whole descendant tree.
  it("carries a whole descendant tree, not just direct children", async () => {
    expect.assertions(2);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await openTab({ kind: "chat", ref: "grandkid", parent: chatID("kid") });
    await openChat("other");
    await setTabPinned(chatID("p"), true);
    expect(await rowRefs()).toEqual(["p", "kid", "grandkid", "other"]);
    // The second pin is what exposed it: an orphaned grandchild group sat BETWEEN
    // two pinned groups, so the partition hoisted `other` over it and the
    // grandchild ended up behind a stranger.
    await setTabPinned(chatID("other"), true);
    expect(await rowRefs()).toEqual(["p", "kid", "grandkid", "other"]);
  });

  // Same assumption, second site: a drop names top-level ids only, so the walk
  // that reproduces the strip has to descend.
  it("keeps a nested descendant behind its parent through a reorder", async () => {
    expect.assertions(1);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await openTab({ kind: "chat", ref: "grandkid", parent: chatID("kid") });
    await openChat("other");
    commitDrop?.([chatID("p"), chatID("other")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["p", "kid", "grandkid", "other"]);
  });

  // A sub-tab's position is its parent's, the same rule that denies it a drag
  // handle — and the refusal is LOCAL, before it costs a round trip.
  it("refuses to pin a sub-tab without asking the server", async () => {
    expect.assertions(2);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await openChat("other");
    await setTabPinned(chatID("kid"), true);
    expect(await rowRefs()).toEqual(["p", "kid", "other"]);
    expect(tabServer.sentOfType("pin_tab")).toHaveLength(0);
  });

  // A repeat of the pin already in force sends nothing either: the subject already
  // says so, and the server would commit nothing and emit nothing.
  it("sends nothing when the pin is already what was asked for", async () => {
    expect.assertions(2);
    await openChat("a");
    await setTabPinned(chatID("a"), true);
    expect(tabServer.sentOfType("pin_tab")).toHaveLength(1);
    await setTabPinned(chatID("a"), true);
    expect(tabServer.sentOfType("pin_tab")).toHaveLength(1);
  });

  it("marks the pinned row for the pin glyph", async () => {
    expect.assertions(3);
    await openChat("a");
    await setTabPinned(chatID("a"), true);
    await paint();
    const id = chatID("a");
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
    expect(row?.classList.contains("tab-pinned")).toBe(true);
    // The glyph itself is decorative; the announced state is the .sr-only word.
    expect(row?.querySelector(".tab-pin .sr-only")?.textContent).toBe("Pinned");
    await setTabPinned(id, false);
    await paint();
    expect(
      document
        .querySelector<HTMLElement>(`[data-tab-id="${id}"]`)
        ?.classList.contains("tab-pinned"),
    ).toBe(false);
  });

  // A pin arrives as a `changed` upsert, so the row is REUSED: the subject is
  // replaced wholesale while the spec, the name override and the dot stay. Nothing
  // may re-materialize, or a pin would re-run a tab's activation wiring.
  it("keeps the row's local half across a pin", async () => {
    expect.assertions(2);
    await openChat("a");
    renameTab(chatID("a"), "Renamed");
    await setTabPinned(chatID("a"), true);
    await paint();
    expect(rows()[0]?.querySelector(".tab-name")?.textContent).toBe("Renamed");
    expect(openers.chatShow).toHaveBeenCalledTimes(1);
  });

  // A pin applied on ANOTHER device arrives as the same `changed` frame, so the
  // partition has to move the row with no local gesture behind it.
  it("re-partitions on a pin this device did not make", async () => {
    expect.assertions(1);
    await openChats("a", "b");
    const bID = chatID("b");
    const subject = tabServer.subjects().find((s) => s.id === bID);
    if (subject === undefined) {
      throw new Error("b not in the collection");
    }
    tabServer.emitRaw({
      changed: { ...subject, pinned: true },
      version: tabServer.version() + 1,
    });
    await settleTabs();
    expect(await rowRefs()).toEqual(["b", "a"]);
  });
});

describe("openFilesView vs toggleFilesView", () => {
  // "Go to" is not "toggle": a toggle CLOSES a visible browser, breaking callers that need it shown.
  // The kind is multi-instance, so each takes a folder; `hasTab("files")` with no ref is false.
  const HOME = "/workspace";

  it("shows the browser when it is not open", async () => {
    expect.assertions(2);
    await openChat("c-1");
    await openFilesView(HOME);
    expect(hasTab("files", HOME)).toBe(true);
    expect(getActiveTabId()).toBe(tabIdFor("files", HOME));
  });

  it("activates the browser when it is open but not active", async () => {
    expect.assertions(2);
    await openFilesView(HOME);
    await openChat("c-1");
    expect(getActiveTabId()).toBe(chatID("c-1"));
    await openFilesView(HOME);
    expect(getActiveTabId()).toBe(tabIdFor("files", HOME));
  });

  it("is a NO-OP when the browser is already active, where the toggle closes", async () => {
    expect.assertions(4);
    await openFilesView(HOME);
    expect(getActiveTabId()).toBe(tabIdFor("files", HOME));

    await openFilesView(HOME);
    expect(hasTab("files", HOME), "an open must never close the tab it is asked to open").toBe(
      true,
    );
    expect(getActiveTabId()).toBe(tabIdFor("files", HOME));

    // The contrast is the point: the toolbar button still wants a toggle.
    await toggleFilesView(HOME);
    expect(hasTab("files", HOME)).toBe(false);
  });

  // A show that closed the tab ran the files view's teardown (the factory's
  // `onClose`, which drops the listing and the search bar) against a search the
  // caller was in the middle of opening.
  it("dispatches nothing at all on a second open", async () => {
    expect.assertions(2);
    await openFilesView(HOME);
    const before = tabServer.sent().length;
    await openFilesView(HOME);
    expect(tabServer.sent()).toHaveLength(before);
    expect(tabServer.sentOfType("close_tab")).toHaveLength(0);
  });

  // The toggle's three-way shape. `openAt` decides only the OPEN arm: a browser that
  // is already open is brought forward or closed at whatever folder it holds, which
  // is why the second case's argument is deliberately a folder no tab was opened at.
  it("opens at the folder it is handed when no browser is open", async () => {
    expect.assertions(2);
    await openChat("c-1");
    await toggleFilesView(HOME);
    expect(hasTab("files", HOME)).toBe(true);
    expect(getActiveTabId()).toBe(tabIdFor("files", HOME));
  });

  it("activates the most recent browser rather than opening a second one", async () => {
    expect.assertions(3);
    await openFilesView(HOME);
    await openChat("c-1");
    await toggleFilesView("/elsewhere");
    expect(getActiveTabId()).toBe(tabIdFor("files", HOME));
    expect(hasTab("files", "/elsewhere")).toBe(false);
    expect(tabServer.sentOfType("open_tab")).toHaveLength(2);
  });

  it("closes the ACTIVE browser, and only that one", async () => {
    expect.assertions(3);
    await openFilesView("/a");
    await openTab({ kind: "files", ref: "/b" });
    await settleTabs();
    expect(getActiveTabId()).toBe(tabIdFor("files", "/b"));
    await toggleFilesView(HOME);
    expect(hasTab("files", "/b")).toBe(false);
    expect(hasTab("files", "/a")).toBe(true);
  });
});

describe("the file browser is multi-instance", () => {
  // A folder is content any browser can show, and a tab's
  // ref is where it was OPENED. So two browsers coexist, and a route resolves to ONE
  // of them rather than minting a third.
  it("keeps two browsers open at once", async () => {
    expect.assertions(3);
    await openTab({ kind: "files", ref: "/a" });
    await openTab({ kind: "files", ref: "/b" });
    await settleTabs();
    expect(hasTab("files", "/a")).toBe(true);
    expect(hasTab("files", "/b")).toBe(true);
    expect(tabIdFor("files", "/a")).not.toBe(tabIdFor("files", "/b"));
  });

  it("resolves a route to the browser OPENED at that folder", async () => {
    expect.assertions(2);
    await openTab({ kind: "files", ref: "/a" });
    await openTab({ kind: "files", ref: "/b" });
    await settleTabs();
    // /b is active, so rung 1 has to beat rung 2 for this to answer /a.
    expect(filesTabForRoute("/a")).toEqual({ id: tabIdFor("files", "/a"), ref: "/a" });
    expect(filesTabForRoute("/b")).toEqual({ id: tabIdFor("files", "/b"), ref: "/b" });
  });

  it("normalises its own argument, so a legacy spelling still resolves", async () => {
    expect.assertions(2);
    await openTab({ kind: "files", ref: "/a" });
    await settleTabs();
    expect(filesTabForRoute("a")).toEqual({ id: tabIdFor("files", "/a"), ref: "/a" });
    expect(filesTabForRoute("/a/")).toEqual({ id: tabIdFor("files", "/a"), ref: "/a" });
  });

  it("falls back to the ACTIVE browser for a folder no tab was opened at", async () => {
    expect.assertions(1);
    await openTab({ kind: "files", ref: "/a" });
    await openTab({ kind: "files", ref: "/b" });
    await settleTabs();
    expect(filesTabForRoute("/somewhere/else")).toEqual({
      id: tabIdFor("files", "/b"),
      ref: "/b",
    });
  });

  it("falls back to the MOST RECENT browser when the active tab is not one", async () => {
    expect.assertions(1);
    await openTab({ kind: "files", ref: "/a" });
    await openTab({ kind: "files", ref: "/b" });
    await settleTabs();
    await openChat("c-1");
    expect(filesTabForRoute("/somewhere/else")).toEqual({
      id: tabIdFor("files", "/b"),
      ref: "/b",
    });
  });

  it("answers nothing at all when no browser is open", async () => {
    expect.assertions(1);
    await openChat("c-1");
    expect(filesTabForRoute("/a")).toEqual({ id: "", ref: "" });
  });

  // `admitLocation`: a files route resolves while ANY browser is open, "" when none is, so the router
  // canonicalises instead of opening one.
  it("admits a files route while any browser is open, and refuses one when none is", async () => {
    expect.assertions(3);
    await openChat("c-1");
    expect(tabIdForRoute({ kind: "files", path: "/deep/inside" })).toBe("");
    await openTab({ kind: "files", ref: "/a" });
    await settleTabs();
    expect(tabIdForRoute({ kind: "files", path: "/deep/inside" })).toBe(tabIdFor("files", "/a"));
    expect(tabIdForRoute({ kind: "files", path: "/a" })).toBe(tabIdFor("files", "/a"));
  });
});

// Close provenance is `op_id` alone: it decides WHEN the client-local teardown runs (this device's
// close at confirmation, a remote one at the applied removal), never WHAT it does.

describe("close provenance", () => {
  it("runs the teardown exactly once for a close this device dispatched", async () => {
    expect.assertions(2);
    await openChat("a");
    await closeTab(chatID("a"));
    await settleTabs();
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
    expect(openers.chatClose).toHaveBeenCalledWith("a");
  });

  it("runs the same teardown for a close from another device", async () => {
    expect.assertions(2);
    await openChat("a");
    tabServer.closeRemotely(chatID("a"));
    await settleTabs();
    // The teardown STILL RUNS — that is the point: this device keeps no store
    // row, dock card or composer state for a tab that is gone, whoever closed it.
    expect(openers.chatClose).toHaveBeenCalledWith("a");
    expect(hasTab("chat", "a")).toBe(false);
  });

  it("tears down a remotely-cascaded child too, children first", async () => {
    expect.assertions(1);
    const seen: string[] = [];
    openers.chatClose.mockImplementation((ref: string) => {
      seen.push(ref);
    });
    await openChat("parent");
    await openTab({ kind: "chat", ref: "child", parent: chatID("parent") });
    tabServer.closeRemotely(chatID("parent"));
    await settleTabs();
    expect(seen).toEqual(["child", "parent"]);
  });
});

// ---------------------------------------------------------------------------
// Which mutations are allowed to swap the visible view
// ---------------------------------------------------------------------------

// The view effect runs on every mutation, and a redundant swap replays the entry fade; counting
// `class` mutations is what separates a skip from an idempotent re-run.
describe("the visible view is only swapped when it actually changes", () => {
  let views: HTMLElement[];
  let mutations = 0;
  let mo: MutationObserver | undefined;

  /** Stage the two views showView resolves against, with chat already the shown
   *  one — the state a reader is in whenever a chat tab is active. */
  function stageViews(): void {
    for (const id of ["chat-view", "settings-view"]) {
      const v = document.createElement("div");
      v.id = id;
      v.setAttribute("data-tab-view", "");
      if (id !== "chat-view") {
        v.classList.add("hidden");
      }
      document.body.appendChild(v);
    }
    views = [...document.querySelectorAll<HTMLElement>("[data-tab-view]")];
  }

  function watch(): void {
    mutations = 0;
    mo = new MutationObserver((records) => {
      mutations += records.length;
    });
    for (const v of views) {
      mo.observe(v, { attributes: true, attributeFilter: ["class"] });
    }
  }

  /** MutationObserver delivers in a microtask, so let it flush before counting. */
  async function settle(): Promise<number> {
    await Promise.resolve();
    mo?.takeRecords().forEach(() => {
      mutations += 1;
    });
    return mutations;
  }

  beforeEach(() => {
    stageViews();
  });

  afterEach(() => {
    mo?.disconnect();
    mo = undefined;
  });

  it("does not touch the views when a non-active tab closes", async () => {
    await openChat("a");
    await openChat("b");
    // "b" is active and the chat view is already the shown one.
    watch();

    await closeTab(chatID("a"));

    // The strip lost a row, but which view is visible did not change, so there is
    // nothing to animate and nothing to swap.
    expect(await settle()).toBe(0);
    expect(document.getElementById("chat-view")?.classList.contains("hidden")).toBe(false);
  });

  it("does not touch the views when the active tab moves between two chats", async () => {
    await openChat("a");
    await openChat("b");
    watch();

    // Both rows share one view element, so skipping the swap loses no animation.
    activateTab(chatID("a"));

    expect(await settle()).toBe(0);
  });

  it("still swaps when the active tab needs a different view", async () => {
    await openChat("a");
    watch();

    await openTab({ kind: "settings", ref: "" });

    // A real navigation: settings has its own view element, so the swap has to
    // run — and this is the case the guard must not swallow.
    expect(await settle()).toBeGreaterThan(0);
    expect(document.getElementById("settings-view")?.classList.contains("hidden")).toBe(false);
    expect(document.getElementById("chat-view")?.classList.contains("hidden")).toBe(true);
  });

  it("repairs a view state that drifted, even with the active tab unchanged", async () => {
    await openChat("a");
    // Something outside this module left every view hidden. The guard reads the
    // DOM rather than remembering what it last showed, so the next mutation
    // notices and puts the right one back instead of trusting a cached answer.
    for (const v of views) {
      v.classList.add("hidden");
    }
    watch();

    await openChat("b");

    expect(document.getElementById("chat-view")?.classList.contains("hidden")).toBe(false);
  });
});

// The optimistic close: the gesture applies only what a rollback can undo; every destructive step
// waits for the machine's confirmation. The real closeTab end to end; the transition table is
// tabs-sync.test.ts's.

describe("optimistic close: the reversible gesture", () => {
  it("removes the subtree at the gesture and defers the teardown to the frame", async () => {
    expect.assertions(4);
    tabServer.setMode("manual");
    await openChats("a", "b");
    const doomed = chatID("b");

    const closing = closeTab(doomed);
    // The row left the strip synchronously with the gesture…
    expect(hasTab("chat", "b")).toBe(false);
    await closing;
    // …and the response alone (frames still held) runs NO teardown: the op is
    // confirmed-awaiting-frame, and client-local state must survive a rollback.
    expect(openers.chatClose).not.toHaveBeenCalled();

    tabServer.flushFrames();
    await settleTabs();
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
    expect(openers.chatClose).toHaveBeenCalledWith("b");
  });

  it("runs the teardown exactly once, frame-first", async () => {
    expect.assertions(2);
    tabServer.setMode("event-first");
    await openChats("a", "b");
    await closeTab(chatID("b"));
    await settleTabs();
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
    expect(openers.chatClose).toHaveBeenCalledWith("b");
  });

  it("runs the teardown exactly once, response-first", async () => {
    expect.assertions(2);
    tabServer.setMode("response-first");
    await openChats("a", "b");
    await closeTab(chatID("b"));
    await settleTabs();
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
    expect(openers.chatClose).toHaveBeenCalledWith("b");
  });

  it("dispatches close_tab and nothing else — delete_chat does not exist on this path", async () => {
    expect.assertions(2);
    await openChats("a");
    await closeTab(chatID("a"));
    await settleTabs();
    expect(tabServer.sentOfType("delete_chat")).toHaveLength(0);
    expect(tabServer.sentOfType("close_tab")).toHaveLength(1);
  });

  it("a definitive refusal restores the chat row in place, re-activates it, and toasts", async () => {
    expect.assertions(6);
    await openChats("a", "b", "c");
    activateTab(chatID("b"));
    vi.mocked(openers.chatShow).mockClear();
    tabServer.failNext("close_tab");

    await closeTab(chatID("b"));

    expect(hasTab("chat", "b")).toBe(true);
    expect(await rowRefs()).toEqual(["a", "b", "c"]);
    expect(getActiveTabId()).toBe(chatID("b"));
    // The restored chat was RE-ACTIVATED (its view reloads from retained state)…
    expect(openers.chatShow).toHaveBeenCalledWith("b");
    // …and none of the chat's client state was torn down in between.
    expect(openers.chatClose).not.toHaveBeenCalled();
    expect(vi.mocked(toastErrorFn)).toHaveBeenCalledWith("Could not close that tab");
  });

  it("a definitive refusal names the chat by the row's name at the gesture", async () => {
    await openChats("a", "b");
    renameTab(chatID("b"), "Fix the parser");
    const notices: unknown[][] = [];
    registerTabNotice((...args) => {
      notices.push(args);
    });
    tabServer.failNext("close_tab");

    await closeTab(chatID("b"));

    expect(notices).toEqual([["b", "Fix the parser", "Could not close that tab", "error"]]);
  });

  it("a definitive refusal leaves a dirty editor's state untouched", async () => {
    expect.assertions(3);
    await openChats("a");
    await openEditorView("dirty.ts");
    tabServer.failNext("close_tab");

    await closeTab(tabIdFor("editor", "dirty.ts"));

    // The editor teardown is what deletes its FileState (the unsaved buffer),
    // so its absence IS the dirty state surviving the refused close.
    expect(openers.editorClose).not.toHaveBeenCalled();
    expect(hasTab("editor", "dirty.ts")).toBe(true);
    expect(await rowRefs()).toEqual(["a", "dirty.ts"]);
  });

  it("a reopen inside the window skips the chat-scoped teardown (row-scoped forget only)", async () => {
    expect.assertions(4);
    tabServer.setMode("manual");
    await openChats("a", "keep");

    await closeTab(chatID("a"));
    // Reopened from History before the close settled: the server serialized
    // close-then-open, so the reply mints a NEW tab id for the same chat.
    await openTab({ kind: "chat", ref: "a" });
    expect(hasTab("chat", "a")).toBe(true);

    tabServer.flushFrames();
    await settleTabs();
    // The close confirmed — but the subject is open again, so the chat-scoped
    // teardown is SKIPPED: the reopen re-established the chat's client state.
    expect(openers.chatClose).not.toHaveBeenCalled();
    expect(hasTab("chat", "a")).toBe(true);
    expect(tabServer.idFor("chat", "a")).toBe(tabIdFor("chat", "a"));
  });

  it("burst closes settle independently: one confirms while the other rolls back", async () => {
    expect.assertions(5);
    await openChats("a", "b", "c");
    tabServer.holdResponses();
    const first = closeTab(chatID("a"));
    tabServer.failNext("close_tab");
    const second = closeTab(chatID("b"));
    expect(hasTab("chat", "a")).toBe(false);
    expect(hasTab("chat", "b")).toBe(false);

    tabServer.releaseResponses();
    await Promise.all([first, second]);
    await settleTabs();

    expect(hasTab("chat", "a")).toBe(false);
    expect(hasTab("chat", "b")).toBe(true);
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
  });

  it("children close with their parent and are restored with it, in place", async () => {
    expect.assertions(3);
    await openChat("before");
    await openChat("p");
    await openTab({ kind: "chat", ref: "c", parent: chatID("p") });
    await openChat("after");
    tabServer.failNext("close_tab");

    await closeTab(chatID("p"));

    expect(hasTab("chat", "p")).toBe(true);
    expect(hasTab("chat", "c")).toBe(true);
    expect(await rowRefs()).toEqual(["before", "p", "c", "after"]);
  });
});

describe("optimistic close: timeout, verifying, and authoritative settlement", () => {
  // The 5s deadline (CLOSE_CONFIRM_MS) uses AbortSignal.timeout, which fake timers cannot advance, so
  // a cancellation takes the identical opTimedOut branch.

  /** A close whose dispatch ends with NO answer: response held, then the
   *  in-flight dispatch canceled. The op lands in `verifying`. */
  async function closeIntoVerifying(id: string): Promise<void> {
    tabServer.setMode("manual");
    tabServer.holdResponses();
    const closing = closeTab(id);
    closeTabCommand.cancel();
    tabServer.releaseResponses();
    await closing;
  }

  it("settles a verifying close as CONFIRMED when the authoritative list says absent", async () => {
    expect.assertions(4);
    await openChats("a", "keep");
    await closeIntoVerifying(chatID("a"));
    expect(hasTab("chat", "a")).toBe(false);
    expect(openers.chatClose).not.toHaveBeenCalled();

    // The server's live collection no longer holds the tab (the command
    // committed even though its answer was lost): absent → silent confirm.
    await listTabs();
    expect(openers.chatClose).toHaveBeenCalledWith("a");
    expect(vi.mocked(toastInfo)).not.toHaveBeenCalled();
  });

  it("settles a verifying close as RESTORED when the row is authoritatively present", async () => {
    expect.assertions(4);
    await openChats("a", "keep");
    const doomedTab = chatID("a");
    await closeIntoVerifying(doomedTab);

    // The authoritative list still holds the row: the close never committed.
    const held = tabServer
      .subjects()
      .map((s) => ({ ...s }))
      .concat([{ id: doomedTab, kind: "chat", ref: "a", parent: "", pinned: false, owns: true }]);
    tabServer.queueList({ tabs: held, version: tabServer.version() + 1 });
    await listTabs();

    expect(hasTab("chat", "a")).toBe(true);
    expect(openers.chatClose).not.toHaveBeenCalled();
    // Restored with a notice — this arm is the machine's, not a server error's,
    // so the ordinary failure toast stays silent.
    expect(vi.mocked(toastInfo)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(toastErrorFn)).not.toHaveBeenCalled();
  });
});

describe("optimistic close: the last tab and the empty-state surface", () => {
  /** The two views the empty surface resolves against, chat hidden behind the
   *  settings view — the state a reader is in when a singleton is active. */
  function stageViews(): { chatView: HTMLElement; settingsView: HTMLElement } {
    const chatView = document.createElement("div");
    chatView.id = "chat-view";
    chatView.setAttribute("data-tab-view", "");
    const settingsView = document.createElement("div");
    settingsView.id = "settings-view";
    settingsView.setAttribute("data-tab-view", "");
    document.body.append(chatView, settingsView);
    return { chatView, settingsView };
  }

  it("renders the empty chat surface when the strip empties, without disposing the closed view", async () => {
    expect.assertions(3);
    const { chatView, settingsView } = stageViews();
    await openTab({ kind: "settings" });
    expect(settingsView.classList.contains("hidden")).toBe(false);

    await closeTab(tabIdFor("settings"));
    await settleTabs();

    // The empty-state surface is the CHAT view (empty transcript + composer,
    // whose Send creates a fresh chat); the departed view is hidden, not torn
    // down — every dispose belongs to confirmed teardown.
    expect(chatView.classList.contains("hidden")).toBe(false);
    expect(settingsView.classList.contains("hidden")).toBe(true);
  });

  it("keeps create-vs-send on CREATE while the strip is empty", async () => {
    expect.assertions(3);
    await openChat("solo");
    expect(activeChatRef()).toBe("solo");
    tabServer.setMode("manual");
    const closing = closeTab(chatID("solo"));
    // The projection's active subject is what app.ts keys the decision on: with
    // the strip empty (close still pending), Send must create a fresh chat, not
    // send into the chat being closed — whose STORE row is still retained.
    expect(activeChatRef()).toBe("");
    await closing;
    expect(activeChatRef()).toBe("");
  });

  it("rollback of a last-tab close restores the view and the empty-state composer text", async () => {
    expect.assertions(5);
    await openChat("solo");
    const solo = chatID("solo");
    vi.mocked(openers.chatShow).mockClear();
    tabServer.failNext("close_tab");

    const closing = closeTab(solo);
    // Typed into the EMPTY-STATE composer while the close was in flight: with
    // no live chat this text is parked nowhere but the box. (Read through the
    // dom mock's getter, which mints the element on first access.)
    const input = $.promptInput;
    input.value = "half a thought";
    await closing;

    // The subtree is back, re-activated, and the typed text was filed as the
    // restored chat's draft through the failed-send primitive (which writes the
    // draft map and only ever an EMPTY box).
    expect(hasTab("chat", "solo")).toBe(true);
    expect(getActiveTabId()).toBe(solo);
    expect(openers.chatShow).toHaveBeenCalledWith("solo");
    expect(vi.mocked(restoreFailedSend)).toHaveBeenCalledWith("solo", "half a thought");
    expect(vi.mocked(retargetComposer)).not.toHaveBeenCalled();
  });

  it("keyboard close of the last tab moves focus to the composer", async () => {
    expect.assertions(1);
    await openChat("solo");
    await paint();
    const row = rows()[0];
    row?.focus();
    row?.dispatchEvent(new KeyboardEvent("keydown", { key: "Delete", bubbles: true }));
    const input = document.getElementById("prompt-input");
    expect(document.activeElement).toBe(input);
  });
});

// `refreshRow` is private, so its two doors drive it; `editor` is always stale, and a `chat` with a
// current-epoch ledger record is "fresh".
describe("the freshness dispatcher", () => {
  it("refreshes the row an activation activated, after its onShow", async () => {
    await openEditorView("/w/a.ts");
    expect(openers.editorRefresh).toHaveBeenCalledWith("/w/a.ts");
    // The kind's VIEW activation first, then the data half: a refresh that ran
    // ahead of the show would fetch into a surface pointing at another subject.
    expect(openers.editorShow.mock.invocationCallOrder[0]).toBeLessThan(
      openers.editorRefresh.mock.invocationCallOrder[0] ?? 0,
    );
  });

  it("refreshes nothing when the view is already fresh", async () => {
    observeStamp({ kind: "chat", ref: "a", version: "1" });
    await openChat("a");
    expect(openers.chatShow).toHaveBeenCalledWith("a");
    expect(openers.chatRefresh).not.toHaveBeenCalled();
  });

  it("the already-active early return runs neither half", async () => {
    await openEditorView("/w/a.ts");
    openers.editorShow.mockClear();
    openers.editorRefresh.mockClear();
    activateTab(tabIdFor("editor", "/w/a.ts"));
    expect(openers.editorShow).not.toHaveBeenCalled();
    expect(openers.editorRefresh).not.toHaveBeenCalled();
  });

  it("refreshActiveView refreshes the active row and no other", async () => {
    await openEditorView("/w/a.ts");
    await openEditorView("/w/b.ts");
    openers.editorRefresh.mockClear();
    refreshActiveView();
    expect(openers.editorRefresh.mock.calls).toEqual([["/w/b.ts"]]);
  });

  // It is not a mutation: it writes no projection state, so nothing re-renders and
  // no route is pushed. `pushRoute` is the router mock, which is the one channel a
  // URL could move through from here.
  it("refreshActiveView emits nothing and pushes no route", async () => {
    await openEditorView("/w/a.ts");
    await paint();
    const version = tabSetVersion();
    const painted = $.tabList.innerHTML;
    const { pushRoute } = await import("./router.js");
    vi.mocked(pushRoute).mockClear();

    refreshActiveView();
    await paint();

    expect(tabSetVersion()).toBe(version);
    expect($.tabList.innerHTML).toBe(painted);
    expect(vi.mocked(pushRoute)).not.toHaveBeenCalled();
  });

  // `activateSuccessor` leaves `state.active` empty on an empty strip, and a gap
  // arriving in that window has no view to refresh. Ruled rather than guarded:
  // reaching for the row unchecked would throw here.
  it("refreshActiveView is a no-op on an empty strip", () => {
    expect(() => {
      refreshActiveView();
    }).not.toThrow();
    expect(openers.chatRefresh).not.toHaveBeenCalled();
  });

  // `forgetRow` deliberately does NOT drop the ledger record: closing a tab does not
  // destroy its subject, so a reopened chat keeps its loaded window and costs zero
  // message fetches. Adding a `forgetView` call there turns this red.
  it("a closed chat tab reopens without refetching", async () => {
    observeStamp({ kind: "chat", ref: "a", version: "1" });
    await openChat("a");
    await closeTab(chatID("a"));
    openers.chatRefresh.mockClear();

    await openChat("a");

    expect(openers.chatShow).toHaveBeenCalledWith("a");
    expect(openers.chatRefresh).not.toHaveBeenCalled();
  });
});

// A drop shows its order at once; a pending reorder in tabs-sync keeps it until the server answers.

describe("a drop is shown before the server confirms it", () => {
  async function threeChatsHeld(): Promise<void> {
    await openChats("a", "b", "c");
    tabServer.setMode("manual");
  }

  it("shows a dropped order before the server's frame lands", async () => {
    expect.assertions(2);
    await threeChatsHeld();
    commitDrop?.([chatID("c"), chatID("a"), chatID("b")]);
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
    expect(tabServer.pendingCount(), "the frame is still held").toBe(1);
  });

  it("keeps the dropped order through a render between the drop and the frame", async () => {
    expect.assertions(1);
    await threeChatsHeld();
    commitDrop?.([chatID("c"), chatID("a"), chatID("b")]);
    renameTab(chatID("a"), "renamed");
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
  });

  it("keeps the dropped order through a re-list that predates it", async () => {
    expect.assertions(1);
    await threeChatsHeld();
    const before = {
      tabs: tabServer.subjects().map((s) => ({ ...s })),
      version: tabServer.version(),
    };
    commitDrop?.([chatID("c"), chatID("a"), chatID("b")]);
    await settleTabs();
    tabServer.queueList(before);
    await listTabs();
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
  });

  it("changes nothing on screen when the confirming frame lands", async () => {
    expect.assertions(2);
    await threeChatsHeld();
    commitDrop?.([chatID("c"), chatID("a"), chatID("b")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
    tabServer.flushFrames();
    await settleTabs();
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
  });

  // The re-list here is unreachable, so only the rollback can put the strip back.
  it("rolls a refused drop back and re-lists on a 409", async () => {
    expect.assertions(3);
    await openChats("a", "b", "c");
    const listsBefore = tabServer.listCalls();
    tabServer.failNext("reorder_tabs", 409, "set moved");
    tabServer.queueList(null);
    commitDrop?.([chatID("c"), chatID("a"), chatID("b")]);
    await settleTabs();
    expect(tabServer.sentOfType("reorder_tabs")).toHaveLength(1);
    expect(tabServer.listCalls()).toBe(listsBefore + 1);
    expect(await rowRefs()).toEqual(["a", "b", "c"]);
  });

  it("rolls a failed drop back", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c");
    tabServer.failNext("reorder_tabs", 500, "boom");
    commitDrop?.([chatID("c"), chatID("a"), chatID("b")]);
    await settleTabs();
    expect(tabServer.sentOfType("reorder_tabs")).toHaveLength(1);
    expect(await rowRefs()).toEqual(["a", "b", "c"]);
  });
});

// Move up/down moves a GROUP within its pin partition, committed like a drop.

describe("Move up / Move down in the chat menu", () => {
  /** Open the context menu on `ref`'s row and answer with the items it offered. */
  async function menuFor(ref: string): Promise<ContextMenuItem[]> {
    await paint();
    const row = rows().find((r) => r.dataset["tabId"] === chatID(ref));
    if (row === undefined) {
      throw new Error(`no row for ${ref}`);
    }
    vi.mocked(showContextMenu).mockClear();
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    return vi.mocked(showContextMenu).mock.lastCall?.[0] ?? [];
  }

  function item(items: ContextMenuItem[], label: string): ContextMenuItem {
    const found = items.find((i) => i.label === label);
    if (found === undefined) {
      throw new Error(`no ${label}`);
    }
    return found;
  }

  function announced(): Promise<string> {
    return new Promise((resolve) => {
      setTimeout(() => {
        resolve(document.querySelector('[role="status"][aria-live="polite"]')?.textContent ?? "");
      }, 150);
    });
  }

  it("offers both after Pin and before the exports, on every chat row", async () => {
    expect.assertions(2);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    expect((await menuFor("p")).map((i) => i.label)).toEqual([
      "Pin",
      "Move up",
      "Move down",
      "Rename\u2026",
      "Export as Markdown",
      "Export as JSON",
      "Download Kiro session",
    ]);
    expect((await menuFor("kid")).map((i) => i.label)).toEqual([
      "Move up",
      "Move down",
      "Rename\u2026",
      "Export as Markdown",
      "Export as JSON",
      "Download Kiro session",
    ]);
  });

  it("disables Move up on the first group and Move down on the last", async () => {
    expect.assertions(4);
    await openChats("a", "b");
    expect(item(await menuFor("a"), "Move up").disabled).toBe(true);
    expect(item(await menuFor("a"), "Move down").disabled).toBe(false);
    expect(item(await menuFor("b"), "Move up").disabled).toBe(false);
    expect(item(await menuFor("b"), "Move down").disabled).toBe(true);
  });

  it("will not cross the pin partition in either direction", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("a"), true);
    expect(item(await menuFor("b"), "Move up").disabled, "first unpinned").toBe(true);
    expect(item(await menuFor("a"), "Move down").disabled, "last pinned").toBe(true);
  });

  it("disables both on a sub-tab, whose position is its parent's", async () => {
    expect.assertions(2);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await openChat("other");
    const items = await menuFor("kid");
    expect(item(items, "Move up").disabled).toBe(true);
    expect(item(items, "Move down").disabled).toBe(true);
  });

  it("carries a parent's sub-tab with it, in one exact-set reorder", async () => {
    expect.assertions(3);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await openChat("other");
    item(await menuFor("other"), "Move up").action();
    await settleTabs();
    expect(await rowRefs()).toEqual(["other", "p", "kid"]);
    expect(tabServer.sentOfType("reorder_tabs")).toHaveLength(1);
    expect(tabServer.sentOfType("reorder_tabs")[0]?.payload["order"]).toEqual([
      chatID("other"),
      chatID("p"),
      chatID("kid"),
    ]);
  });

  it("shows the move before the server's frame lands", async () => {
    expect.assertions(1);
    await openChats("a", "b");
    tabServer.setMode("manual");
    item(await menuFor("a"), "Move down").action();
    expect(await rowRefs()).toEqual(["b", "a"]);
  });

  it("announces the move with the drag's own phrase", async () => {
    expect.assertions(1);
    await openChats("a", "b");
    renameTab(chatID("b"), "Bee");
    item(await menuFor("b"), "Move up").action();
    expect(await announced()).toBe("Moved Bee to position 1");
  });

  it("sends nothing from a disabled item", async () => {
    expect.assertions(2);
    await openChats("a", "b");
    const up = item(await menuFor("a"), "Move up");
    expect(up.disabled).toBe(true);
    up.action();
    await settleTabs();
    expect(tabServer.sentOfType("reorder_tabs")).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// In-place rename of a chat row: double-click, F2 or the menu's Rename…
// opens a field over the name; Enter or blur sends `rename_chat`, Escape sends
// nothing. The label itself repaints from the server's frame, not from here.
// ---------------------------------------------------------------------------

describe("in-place chat rename", () => {
  async function rowFor(ref: string): Promise<HTMLElement> {
    await paint();
    const row = rows().find((r) => r.dataset["tabId"] === chatID(ref));
    if (row === undefined) {
      throw new Error(`no row for ${ref}`);
    }
    return row;
  }

  function field(row: HTMLElement): HTMLInputElement {
    const input = row.querySelector<HTMLInputElement>(".tab-name-input");
    if (input === null) {
      throw new Error("no rename field");
    }
    return input;
  }

  function renames(): unknown[] {
    return tabServer.sentOfType("rename_chat").map((c) => c.payload);
  }

  it("opens a bounded, labelled field on double-click", async () => {
    expect.assertions(3);
    await openChat("a");
    const row = await rowFor("a");
    row.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    const input = field(row);
    expect(input.maxLength).toBe(128);
    expect(input.getAttribute("aria-label")).toBe("Chat name");
    expect(document.activeElement).toBe(input);
  });

  it("sends the trimmed name on Enter", async () => {
    expect.assertions(2);
    await openChat("a");
    const row = await rowFor("a");
    row.dispatchEvent(new KeyboardEvent("keydown", { key: "F2", bubbles: true }));
    const input = field(row);
    input.value = "  Release notes  ";
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settleTabs();
    expect(renames()).toEqual([{ name: "Release notes" }]);
    expect(row.querySelector(".tab-name-input")).toBeNull();
  });

  it("sends nothing on Escape and puts the name back", async () => {
    expect.assertions(3);
    await openChat("a");
    const row = await rowFor("a");
    row.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    const input = field(row);
    input.value = "Something else";
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    await settleTabs();
    expect(renames()).toEqual([]);
    expect(row.querySelector(".tab-name-input")).toBeNull();
    expect(row.querySelector<HTMLElement>(".tab-name")?.hidden).toBe(false);
  });

  it("sends nothing for an empty or unchanged name", async () => {
    expect.assertions(1);
    await openChat("a");
    const row = await rowFor("a");
    row.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    field(row).dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    row.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    const input = field(row);
    input.value = "   ";
    input.blur();
    await settleTabs();
    expect(renames()).toEqual([]);
  });

  it("opens from the context menu", async () => {
    expect.assertions(1);
    await openChat("a");
    const row = await rowFor("a");
    vi.mocked(showContextMenu).mockClear();
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    const items = vi.mocked(showContextMenu).mock.lastCall?.[0] ?? [];
    items.find((i) => i.label === "Rename\u2026")?.action();
    expect(row.querySelector(".tab-name-input")).not.toBeNull();
  });
});
