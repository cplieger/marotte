// A singleton's sub-tab, end to end through the real strip, factory, router arms and segmented
// bars: the panel on screen, the selected pill, the loader that ran and the URL must name one
// sub-tab, whichever door opened the row.

import { describe, it, expect, beforeAll, beforeEach, afterEach, vi } from "vitest";
import type * as Dom from "./dom.js";
import type * as ApiClient from "./api-client.js";
import type { GitTab, Route, SettingsTab } from "./route-path.js";

const spies = vi.hoisted(() => ({
  loaders: {
    general: vi.fn(),
    tools: vi.fn(),
    permissions: vi.fn(),
    instructions: vi.fn(),
  },
  loadGitRepos: vi.fn(),
  refreshGitView: vi.fn(),
}));

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
vi.mock("./deep-link.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  admitLocation: vi.fn(() => "opens"),
  settleDeepLinkedChat: vi.fn(),
}));
vi.mock("./chat.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  switchSession: vi.fn(),
}));
vi.mock("./editor-openers.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  openFile: vi.fn(),
}));
vi.mock("./files.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  pointFilesTab: vi.fn(),
}));
vi.mock("./settings-highlight.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  flushURLHighlight: vi.fn(),
}));
// The git page's fetches are what a sub-tab change must not start; its panels are git-tabs.ts's.
vi.mock("./git.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  loadGitRepos: spies.loadGitRepos,
  refreshGitView: spies.refreshGitView,
}));
vi.mock("./git-prs-tab.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  requestPRFocus: vi.fn(),
}));
vi.mock("./icons.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js")).iconsMock(),
}));
vi.mock("./device-view.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js")).deviceViewMock(),
}));
// One element per `$` name, kept in the document, so the Settings bar the page wired at init is the
// bar a case clicks.
vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Dom>()),
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        const id = prop === "tabList" ? "tab-list" : prop;
        let node = document.getElementById(id);
        if (node === null) {
          node = document.createElement("div");
          node.id = id;
          document.body.appendChild(node);
        }
        return node;
      },
    },
  ),
  setBusy: vi.fn(),
  byId: (id: string) => {
    let node = document.getElementById(id);
    if (node === null) {
      node = document.createElement("span");
      node.id = id;
      document.body.appendChild(node);
    }
    return node;
  },
}));
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
}));
vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  runLabelOf: vi.fn(() => ""),
}));
vi.mock("./composer-state.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js")).composerStateMock(),
}));
vi.mock("./context-menu.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  showContextMenu: vi.fn(),
}));
vi.mock("./chat-export.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  downloadChatExport: vi.fn(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tabs-server.js")).tabTransportMock(),
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiBlobOrError: vi.fn(),
  apiGetTyped: (await import("./__test-helpers__/tabs-server.js")).tabListRead(),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

import {
  activateTab,
  closeTab,
  flushActivationForTest,
  getActiveTabRoute,
  openSettingsView,
  tabIdFor,
  toggleGitView,
  toggleSettingsView,
  _resetForTest,
} from "./tabs.js";
import { applyRoute } from "./route-apply.js";
import { pushRoute } from "./router.js";
import { initSettingsTabs, TABS as SETTINGS_TABS } from "./settings-tabs.js";
import { initGitTabs } from "./git-tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer } from "./__test-helpers__/tabs-server.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

const GIT_TABS: readonly GitTab[] = ["changes", "prs", "sources"];

beforeAll(async () => {
  // The factory reaches git.js through a dynamic import: loading its graph here keeps that cold
  // load out of the first Git case's wait.
  await import("./git.js");
  const settingsBar = document.createElement("nav");
  settingsBar.id = "settingsTabBar";
  const gitBar = document.createElement("nav");
  gitBar.id = "git-tab-bar";
  const panels = document.createElement("div");
  for (const tab of SETTINGS_TABS) {
    settingsBar.insertAdjacentHTML("beforeend", `<button data-settings-tab="${tab}"></button>`);
    panels.insertAdjacentHTML("beforeend", `<div data-settings-panel="${tab}"></div>`);
  }
  for (const tab of GIT_TABS) {
    gitBar.insertAdjacentHTML("beforeend", `<button data-git-tab="${tab}"></button>`);
    panels.insertAdjacentHTML("beforeend", `<div data-git-panel="${tab}"></div>`);
  }
  document.body.append(settingsBar, gitBar, panels);
  initSettingsTabs(spies.loaders);
  initGitTabs();
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
  clearSpies();
});

afterEach(() => {
  _resetTabsSyncForTest();
});

function clearSpies(): void {
  for (const fn of Object.values(spies.loaders)) {
    fn.mockClear();
  }
  spies.loadGitRepos.mockClear();
  spies.refreshGitView.mockClear();
  vi.mocked(pushRoute).mockClear();
}

/** The settings sub-tabs whose loader ran since the last clear, in bar order. */
function loaded(): SettingsTab[] {
  return SETTINGS_TABS.filter((t) => spies.loaders[t].mock.calls.length > 0);
}

/** The one panel of `attr` not hidden, or "" when none or several are shown. */
function shownPanel(attr: "settings" | "git"): string {
  const shown = [...document.querySelectorAll<HTMLElement>(`[data-${attr}-panel]`)].filter(
    (p) => !p.classList.contains("hidden"),
  );
  return shown.length === 1 ? (shown[0]?.getAttribute(`data-${attr}-panel`) ?? "") : "";
}

function selectedPill(attr: "settings" | "git"): string {
  return (
    document
      .querySelector<HTMLElement>(`[data-${attr}-tab].active`)
      ?.getAttribute(`data-${attr}-tab`) ?? ""
  );
}

function click(attr: "settings" | "git", tab: string): void {
  document.querySelector<HTMLElement>(`[data-${attr}-tab="${tab}"]`)?.click();
}

function lastPush(): Route | undefined {
  return vi.mocked(pushRoute).mock.calls.at(-1)?.[0];
}

/** The refresh reaches its loader through a dynamic import, so a load is polled for. */
async function settled(check: () => boolean, what: string): Promise<void> {
  for (let i = 0; i < 50; i++) {
    if (check()) {
      break;
    }
    await new Promise((r) => {
      setTimeout(r, 1);
    });
  }
  expect(check(), what).toBe(true);
}

/** Give a load that should not happen the time a real one takes to land. */
async function quiet(): Promise<void> {
  for (let i = 0; i < 10; i++) {
    await new Promise((r) => {
      setTimeout(r, 1);
    });
  }
}

/** Open Settings with the gear, then pick `tab` in its bar. */
async function settingsOn(tab: SettingsTab): Promise<void> {
  await toggleSettingsView();
  flushActivationForTest();
  await settled(() => loaded().length > 0, "the first Settings load");
  click("settings", tab);
}

/** Open Git with its toggle, then pick `tab` in its bar. */
async function gitOn(tab: GitTab): Promise<void> {
  await toggleGitView();
  flushActivationForTest();
  await settled(() => spies.refreshGitView.mock.calls.length > 0, "the Git activation's fetch");
  click("git", tab);
}

describe("a reopened singleton lands on the panel its URL names", () => {
  it("the gear after Settings closed on Tools shows General", async () => {
    await settingsOn("tools");
    await closeTab(tabIdFor("settings"));
    clearSpies();

    await toggleSettingsView();
    flushActivationForTest();
    await settled(() => loaded().length > 0, "the reopen's load");

    expect(shownPanel("settings")).toBe("general");
    expect(selectedPill("settings")).toBe("general");
    expect(loaded()).toEqual(["general"]);
    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "general" });
    expect(lastPush()).toEqual({ kind: "settings", tab: "general" });
  });

  it("the gear on a background Settings keeps the sub-tab it shows", async () => {
    await settingsOn("tools");
    await gitOn("changes");

    clearSpies();

    await toggleSettingsView();
    flushActivationForTest();
    await settled(() => loaded().length > 0, "the activation's load");

    expect(shownPanel("settings")).toBe("tools");
    expect(loaded()).toEqual(["tools"]);
    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "tools" });
  });

  it("the Git toggle after Git closed on Sources shows Changes", async () => {
    await gitOn("sources");
    await closeTab(tabIdFor("git"));
    clearSpies();

    await toggleGitView();
    flushActivationForTest();
    await settled(() => spies.refreshGitView.mock.calls.length > 0, "the Git activation's fetch");

    expect(shownPanel("git")).toBe("changes");
    expect(selectedPill("git")).toBe("changes");
    expect(getActiveTabRoute()).toEqual({ kind: "git", tab: "changes" });
  });

  it("a Settings row another device opens shows General", async () => {
    await settingsOn("tools");
    await closeTab(tabIdFor("settings"));
    clearSpies();

    tabServer.openElsewhere({ kind: "settings" });
    tabServer.flushFrames();
    await activateTab(tabIdFor("settings"));
    flushActivationForTest();
    await settled(() => loaded().length > 0, "the remote row's load");

    expect(shownPanel("settings")).toBe("general");
    expect(loaded()).toEqual(["general"]);
    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "general" });
  });
});

describe("a door's sub-tab", () => {
  it("a door into a closed Settings loads only the panel it names, once", async () => {
    await settingsOn("tools");
    await closeTab(tabIdFor("settings"));
    clearSpies();

    await openSettingsView("permissions");
    flushActivationForTest();
    await settled(() => loaded().length > 0, "the door's load");
    await quiet();

    expect(shownPanel("settings")).toBe("permissions");
    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "permissions" });
    expect(lastPush()).toEqual({ kind: "settings", tab: "permissions" });
    expect(spies.loaders.permissions).toHaveBeenCalledTimes(1);
    expect(spies.loaders.general).not.toHaveBeenCalled();
    expect(vi.mocked(pushRoute).mock.calls).not.toContainEqual([
      { kind: "settings", tab: "general" },
    ]);
  });

  it("Back onto a background Settings shows and loads the panel the URL names", async () => {
    await settingsOn("tools");
    await gitOn("changes");
    clearSpies();

    await applyRoute({ kind: "settings", tab: "permissions" }, "history");
    await settled(() => getActiveTabRoute()?.kind === "settings", "the Settings activation");
    flushActivationForTest();
    await settled(() => loaded().length > 0, "the popstate's load");
    await quiet();

    expect(shownPanel("settings")).toBe("permissions");
    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "permissions" });
    expect(spies.loaders.permissions).toHaveBeenCalledTimes(1);
    expect(loaded()).toEqual(["permissions"]);
  });

  it("a failed door leaves no sub-tab for the next Settings row", async () => {
    tabServer.failNext("open_tab");
    await openSettingsView("tools");

    tabServer.openElsewhere({ kind: "settings" });
    tabServer.flushFrames();
    await activateTab(tabIdFor("settings"));
    flushActivationForTest();
    await settled(() => loaded().length > 0, "the remote row's load");

    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "general" });
    expect(shownPanel("settings")).toBe("general");
    expect(loaded()).toEqual(["general"]);
  });
});

describe("a pill click", () => {
  it("on the active Git points the row and the URL at the sub-tab", async () => {
    await gitOn("prs");

    expect(shownPanel("git")).toBe("prs");
    expect(getActiveTabRoute()).toEqual({ kind: "git", tab: "prs" });
    expect(lastPush()).toEqual({ kind: "git", tab: "prs" });

    click("git", "changes");

    expect(shownPanel("git")).toBe("changes");
    expect(lastPush()).toEqual({ kind: "git", tab: "changes" });
  });

  it("on the active Settings loads that panel once and pushes it", async () => {
    await settingsOn("tools");
    await quiet();

    expect(shownPanel("settings")).toBe("tools");
    expect(spies.loaders.tools).toHaveBeenCalledTimes(1);
    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "tools" });
    expect(lastPush()).toEqual({ kind: "settings", tab: "tools" });
  });
});

describe("a close repaints nothing", () => {
  it("closing Git on Sources fetches nothing and leaves its panel as it was", async () => {
    await gitOn("sources");
    clearSpies();

    await closeTab(tabIdFor("git"));
    await quiet();

    expect(spies.refreshGitView).not.toHaveBeenCalled();
    expect(spies.loadGitRepos).not.toHaveBeenCalled();
    expect(shownPanel("git")).toBe("sources");
    expect(selectedPill("git")).toBe("sources");
  });

  it("closing Settings on Tools runs no loader", async () => {
    await settingsOn("tools");
    clearSpies();

    await closeTab(tabIdFor("settings"));
    await quiet();

    expect(loaded()).toEqual([]);
    expect(shownPanel("settings")).toBe("tools");
  });
});

describe("a door's sub-tab while the row leaves and returns during its open", () => {
  /** Start the door with its response held, and wait for its dispatch to reach the server. Boxed:
   *  an async function returning the door's promise would wait for it. */
  async function heldDoor(tab: SettingsTab): Promise<{ door: Promise<void> }> {
    tabServer.holdResponses();
    const door = openSettingsView(tab);
    await settled(() => tabServer.sentOfType("open_tab").length > 0, "the door's dispatch");
    return { door };
  }

  async function landed(door: Promise<void>): Promise<void> {
    tabServer.releaseResponses();
    await door;
    flushActivationForTest();
    await settled(() => loaded().length > 0, "the rebuilt row's load");
  }

  it("an open Settings removed and rebuilt by the response shows the door's sub-tab", async () => {
    await settingsOn("tools");
    const { door } = await heldDoor("permissions");
    tabServer.closeRemotely(tabIdFor("settings"));
    expect(tabIdFor("settings")).toBe("");
    clearSpies();

    await landed(door);

    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "permissions" });
    expect(shownPanel("settings")).toBe("permissions");
  });

  it("a row a frame built, removed and rebuilt by the response shows the door's sub-tab", async () => {
    const { door } = await heldDoor("permissions");
    expect(tabIdFor("settings"), "the open's frame built the row").not.toBe("");
    tabServer.closeRemotely(tabIdFor("settings"));
    expect(tabIdFor("settings")).toBe("");

    await landed(door);

    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "permissions" });
    expect(shownPanel("settings")).toBe("permissions");
  });

  it("a refused close's restored row shows the door's sub-tab", async () => {
    await settingsOn("tools");
    tabServer.failNext("close_tab", 500);
    tabServer.answerAfterNext("close_tab", 20, { ok: false, status: 500, error: "nope" });
    const closing = closeTab(tabIdFor("settings"));
    expect(tabIdFor("settings"), "the close removed the row at the gesture").toBe("");
    const { door } = await heldDoor("permissions");
    await closing;
    expect(tabIdFor("settings"), "the refusal restored the row").not.toBe("");
    clearSpies();

    await landed(door);

    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "permissions" });
    expect(shownPanel("settings")).toBe("permissions");
  });

  it("a pill clicked on the row the door pointed keeps its sub-tab when the response lands", async () => {
    await settingsOn("tools");
    const { door } = await heldDoor("permissions");
    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "permissions" });
    click("settings", "instructions");

    tabServer.releaseResponses();
    await door;

    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "instructions" });
    expect(shownPanel("settings")).toBe("instructions");
  });
});
