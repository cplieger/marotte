import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type * as Router from "./router.js";
import type * as History from "./history.js";
import type * as DeviceView from "./device-view.js";
import type * as Store from "./store.js";
import type * as RunStore from "./run-store.js";
import type * as ComposerState from "./composer-state.js";
import type * as ContextMenu from "./context-menu.js";
import type * as ChatExport from "./chat-export.js";
import type * as Toast from "./toast.js";
import type * as Transport from "./transport.js";
import type * as ApiClient from "./api-client.js";
import type * as Chat from "./chat.js";
import type * as StoreLoad from "./store-load.js";
import type * as RouteApply from "./route-apply.js";
import type * as FindDispatch from "./find-dispatch.js";

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
// The page's own lists are not this suite's subject; where History shows and what it selects are.
vi.mock("./history.js", async (importOriginal) => ({
  ...(await importOriginal<typeof History>()),
  refreshHistoryView: vi.fn(),
}));
vi.mock("./device-view.js", async (importOriginal) => {
  let active = "";
  return {
    ...(await importOriginal<typeof DeviceView>()),
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
});
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Store>()),
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
}));
vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  runLabelOf: vi.fn(() => ""),
}));
vi.mock("./composer-state.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ComposerState>()),
  retargetComposer: vi.fn(),
  adoptUnownedComposer: vi.fn(),
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
vi.mock("./context-menu.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ContextMenu>()),
  showContextMenu: vi.fn(),
}));
vi.mock("./chat-export.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatExport>()),
  downloadChatExport: vi.fn(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  ...(await import("./__test-helpers__/tabs-server.js")).tabTransportMock(),
}));
vi.mock("./chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Chat>()),
  switchSession: vi.fn(),
  resolveUnknownChat: vi.fn(),
}));
vi.mock("./store-load.js", async (importOriginal) => ({
  ...(await importOriginal<typeof StoreLoad>()),
  chatListLoaded: vi.fn(() => true),
  serverMayAnswer: vi.fn(() => true),
}));
vi.mock("./api-client.js", async (importOriginal) => {
  const tabs = (await import("./__test-helpers__/tabs-server.js")).tabListRead();
  return {
    ...(await importOriginal<typeof ApiClient>()),
    apiGetTyped: vi.fn((path: string) => tabs(path)),
    apiGet: vi.fn(() => Promise.resolve(null)),
  };
});

import { initSidebarHistory } from "./sidebar-history.js";
import {
  activateTab,
  adoptSubject,
  closeTab,
  getActiveTabId,
  getActiveTabKind,
  getActiveTabRoute,
  openTab,
  setOnEmpty,
  tabIdFor,
  EMPTY_RESPAWN_MS,
  _resetForTest,
} from "./tabs.js";
import { pushRoute } from "./router.js";
import { maybeEl, queryEl } from "./dom.js";
import { registerFind } from "./find-registry.js";
import type { PageFind } from "./find-registry.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, fakeSubject, tabServer } from "./__test-helpers__/tabs-server.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

let shell: HTMLElement;
let style: HTMLStyleElement;
// Imported once the shell exists: their graphs read the shipped DOM at import.
let applyRoute: typeof RouteApply.applyRoute;
let handleFindKey: typeof FindDispatch.handleFindKey;

beforeAll(async () => {
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  for (const s of doc.querySelectorAll("script")) {
    s.remove();
  }
  shell = document.createElement("div");
  for (const n of [...doc.body.childNodes]) {
    shell.appendChild(document.importNode(n, true));
  }
  document.body.appendChild(shell);
  style = mountAppCSS();
  initSidebarHistory();
  // Loaded once the shell exists, so the lazy import showing History makes resolves at once.
  await import("./history.js");
  ({ applyRoute } = await import("./route-apply.js"));
  ({ handleFindKey } = await import("./find-dispatch.js"));
});

afterAll(() => {
  shell.remove();
  style.remove();
});

beforeEach(() => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerTabOpeners({
    chat: {
      prefetch: vi.fn(),
      select: vi.fn(),
      loadFailed: vi.fn(),
      show: vi.fn(),
      refresh: vi.fn(() => Promise.resolve()),
      close: vi.fn(),
      dot: () => "",
    },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  // A row an earlier case closed may still be playing its exit, still marked selected.
  for (const id of SECTIONS) {
    el(id).replaceChildren();
  }
  vi.mocked(pushRoute).mockClear();
});

afterEach(() => {
  _resetTabsSyncForTest();
});

/** Through dom.ts, which also finds a hidden view parked off the page. */
function el<T extends HTMLElement>(id: string): T {
  const e = maybeEl<T>(id);
  if (e === null) {
    throw new Error(`no #${id}`);
  }
  return e;
}

async function settle(): Promise<void> {
  for (let i = 0; i < 8; i++) {
    await new Promise((r) => {
      setTimeout(r, 0);
    });
  }
  await new Promise((r) => requestAnimationFrame(r));
}

const SECTIONS = ["tab-list", "pinned-list", "recent-list"] as const;

/** Every row the sidebar's sections draw, by its visible name. */
function sectionRowNames(): string[] {
  return SECTIONS.flatMap((id) =>
    [...el(id).querySelectorAll<HTMLElement>(".tab:not(.exiting), li")].map((n) =>
      n.textContent.trim(),
    ),
  );
}

function selectedRows(): string[] {
  return SECTIONS.flatMap((id) =>
    [...el(id).querySelectorAll<HTMLElement>(".tab.active, [aria-selected='true']")].map(
      (n) => n.dataset["tabId"] ?? n.textContent.trim(),
    ),
  );
}

function historyShown(): boolean {
  return !el("history-view").classList.contains("hidden");
}

function footRowSelected(): boolean {
  const row = el("history-btn");
  return row.classList.contains("active") && row.getAttribute("aria-current") === "page";
}

function footRowDeselected(): boolean {
  const row = el("history-btn");
  return !row.classList.contains("active") && !row.hasAttribute("aria-current");
}

function lastRoute(): unknown {
  return vi.mocked(pushRoute).mock.lastCall?.[0];
}

/** History on screen as a destination, with no tab of its own. */
function expectHistoryPane(opens: number, tab: "chats" | "runs" = "chats"): void {
  expect(
    sectionRowNames().filter((n) => n === "History"),
    "no History row",
  ).toEqual([]);
  expect(opensSent(), "no open_tab is sent").toBe(opens);
  expect(selectedRows(), "no section row is drawn selected").toEqual([]);
  expect(historyShown(), "the History view shows").toBe(true);
  expect(footRowSelected(), "the foot row is selected").toBe(true);
  expect(getActiveTabId()).toBe("");
  expect(getActiveTabKind()).toBeNull();
  expect(lastRoute()).toEqual(
    tab === "runs" ? { kind: "history", tab: "runs" } : { kind: "history" },
  );
}

async function openSingletons(): Promise<{ settings: string; git: string }> {
  await openTab({ kind: "settings" });
  await openTab({ kind: "git" });
  await settle();
  return { settings: tabIdFor("settings"), git: tabIdFor("git") };
}

function opensSent(): number {
  return tabServer.sentOfType("open_tab").length;
}

describe("History as the sidebar's foot row, with no tab", () => {
  it("shows the History view from the foot row, adding no row and no tab", async () => {
    const { git } = await openSingletons();
    const opens = opensSent();
    const count = tabServer.subjects().length;
    const sidebar = el("sidebar");
    sidebar.classList.add("open");

    el("history-btn").click();
    expect(sidebar.classList.contains("open"), "the phone drawer closes").toBe(false);
    await settle();

    expectHistoryPane(opens);
    expect(tabServer.subjects()).toHaveLength(count);
    expect(el("history-btn").hasAttribute("aria-pressed"), "a destination, not a toggle").toBe(
      false,
    );
    expect(getActiveTabRoute()).toEqual({ kind: "history", tab: "chats" });
    expect(
      el(`tab-list`).querySelector(`[data-tab-id="${git}"]`),
      "git stays a row",
    ).not.toBeNull();
  });

  it("shows it from the /history route, a deep link and Back alike", async () => {
    await openSingletons();
    const opens = opensSent();

    await applyRoute({ kind: "history" }, "deeplink");
    await settle();
    expectHistoryPane(opens);

    await activateTab(tabIdFor("git"));
    await settle();
    await applyRoute({ kind: "history" }, "history");
    await settle();
    expectHistoryPane(opens);
  });

  it("keeps History on a second press of the foot row", async () => {
    el("history-btn").click();
    await settle();
    el("history-btn").click();
    await settle();

    expect(historyShown()).toBe(true);
    expect(footRowSelected()).toBe(true);
  });

  it("leaves History for the tab under it and for another tab, deselecting the foot row", async () => {
    const { settings, git } = await openSingletons();
    for (const to of [git, settings]) {
      el("history-btn").click();
      await settle();
      expect(historyShown()).toBe(true);

      await activateTab(to);
      await settle();

      expect(historyShown(), `left for ${to}`).toBe(false);
      expect(footRowDeselected()).toBe(true);
      expect(getActiveTabId()).toBe(to);
      expect(selectedRows()).toEqual([to]);
    }
  });

  it("leaves History for a new chat and for an open that activates", async () => {
    await openSingletons();
    el("history-btn").click();
    await settle();

    // New chat's door: chat.ts adopts the minted subject, then activates it. The outgoing view stays
    // up until the chat paints (transcript-cover.ts `deferReveal`), so the selection is the read.
    adoptSubject(fakeSubject("t-new", { kind: "chat", ref: "c-new" }));
    await activateTab("t-new");
    await settle();
    expect(footRowDeselected()).toBe(true);
    expect(selectedRows()).toEqual(["t-new"]);
    expect(getActiveTabId()).toBe("t-new");

    el("history-btn").click();
    await settle();
    await openTab({ kind: "docs" });
    await settle();
    expect(historyShown()).toBe(false);
    expect(footRowDeselected()).toBe(true);
    expect(getActiveTabKind()).toBe("docs");
  });

  it("leaves History on Back to a tab's route", async () => {
    const { settings } = await openSingletons();
    el("history-btn").click();
    await settle();

    await applyRoute({ kind: "settings", tab: "general" }, "history");
    await settle();

    expect(historyShown()).toBe(false);
    expect(footRowDeselected()).toBe(true);
    expect(getActiveTabId()).toBe(settings);
  });

  it("stays up when the tab under it closes, and leaving lands on its successor", async () => {
    const { settings, git } = await openSingletons();
    el("history-btn").click();
    await settle();

    await closeTab(git);
    await settle();

    expect(historyShown(), "a close's successor does not take the view").toBe(true);
    expect(footRowSelected()).toBe(true);
    expect(selectedRows()).toEqual([]);
    expect(getActiveTabId()).toBe("");

    await activateTab(settings);
    await settle();
    expect(historyShown()).toBe(false);
    expect(getActiveTabId()).toBe(settings);
  });

  it("stays up when a refused close puts the tab under it back", async () => {
    const { git } = await openSingletons();
    el("history-btn").click();
    await settle();

    tabServer.failNext("close_tab", 500);
    await closeTab(git);
    await settle();

    expect(tabIdFor("git"), "the tab is back").toBe(git);
    expect(historyShown()).toBe(true);
    expect(footRowSelected()).toBe(true);
    expect(selectedRows()).toEqual([]);
  });

  it("waits to respawn a chat into an emptied strip while History shows", async () => {
    await openTab({ kind: "settings" });
    await settle();
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    el("history-btn").click();
    await settle();

    await closeTab(tabIdFor("settings"));
    await new Promise((r) => setTimeout(r, EMPTY_RESPAWN_MS + 100));

    expect(onEmpty).not.toHaveBeenCalled();
    expect(historyShown()).toBe(true);
    expect(footRowSelected()).toBe(true);
  });

  it("routes its Chats and Runs pills with no tab", async () => {
    const opens = opensSent();
    el("history-btn").click();
    await settle();

    queryEl<HTMLButtonElement>('[data-history-tab="runs"]')?.click();
    await settle();
    expectHistoryPane(opens, "runs");
    expect(getActiveTabRoute()).toEqual({ kind: "history", tab: "runs" });

    queryEl<HTMLButtonElement>('[data-history-tab="chats"]')?.click();
    await settle();
    expectHistoryPane(opens, "chats");

    await applyRoute({ kind: "history", tab: "runs" }, "deeplink");
    await settle();
    expectHistoryPane(opens, "runs");
    const runs = queryEl<HTMLElement>('[data-history-panel="runs"]');
    expect(runs?.classList.contains("hidden")).toBe(false);
  });

  it("sends Ctrl-F to History's find while it shows", async () => {
    el("history-btn").click();
    await settle();
    const open = vi.fn(() => true);
    const stub: PageFind = {
      open,
      toggle: vi.fn(),
      focused: () => false,
      kind: () => "search",
    };
    registerFind("history", stub);

    const e = new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
    handleFindKey(e);

    expect(open).toHaveBeenCalledOnce();
    expect(e.defaultPrevented).toBe(true);
  });

  it("opens with 48 tabs open, taking no slot", async () => {
    for (let i = 0; i < 48; i++) {
      adoptSubject(fakeSubject(`t-${String(i)}`, { kind: "chat", ref: `c-${String(i)}` }));
    }
    tabServer.failNext("open_tab", 409, "48 tabs are open", "tab_cap");
    await settle();
    const opens = opensSent();

    el("history-btn").click();
    await settle();

    expectHistoryPane(opens);
  });
});
