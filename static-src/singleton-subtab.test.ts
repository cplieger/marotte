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

vi.mock("./router.js", () => ({ pushRoute: vi.fn(), replaceRoute: vi.fn() }));
// No-ops rather than absent: Browser Mode links ESM for real.
vi.mock("./deep-link.js", () => ({
  admitLocation: vi.fn(() => "opens"),
  settleDeepLinkedChat: vi.fn(),
}));
vi.mock("./chat.js", () => ({ switchSession: vi.fn() }));
vi.mock("./editor-openers.js", () => ({ openFile: vi.fn() }));
vi.mock("./files.js", () => ({ pointFilesTab: vi.fn() }));
vi.mock("./settings-highlight.js", () => ({ flushURLHighlight: vi.fn() }));
// The git page's fetches are what a sub-tab change must not start; its panels are git-tabs.ts's.
vi.mock("./git.js", () => ({
  loadGitRepos: spies.loadGitRepos,
  refreshGitView: spies.refreshGitView,
}));
vi.mock("./git-prs-tab.js", () => ({ requestPRFocus: vi.fn() }));
vi.mock("./icons.js", () => ({
  ICON_MODEL_UI: "",
  ICON_CLOSE: "",
  ICON_TAB_CHAT: "",
  ICON_TAB_SETTINGS: "",
  ICON_TAB_GIT: "",
  ICON_TAB_FILES: "",
  ICON_TAB_RUN: "",
  ICON_TAB_WEB: "",
  ICON_TAB_AGENT: "",
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
vi.mock("./device-view.js", () => {
  let active = "";
  return {
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
});
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
vi.mock("./tabs-drag.js", () => ({
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
  setReprojectCallback: vi.fn(),
  dragOwnsStrip: vi.fn(() => false),
  exceedsSlop: vi.fn(() => false),
  pointerDragActivation: vi.fn(() => ({ holdMs: 150, slopPx: 8 })),
}));
vi.mock("./store.js", () =>
  import("./__test-helpers__/store-mock.js").then((m) => ({ ...m.storeMock })),
);
vi.mock("./run-store.js", () => ({ runLabelOf: vi.fn(() => "") }));
vi.mock("./composer-state.js", () => ({
  adoptUnownedComposer: vi.fn(),
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
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
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

beforeAll(() => {
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

    await toggleSettingsView();
    flushActivationForTest();

    expect(shownPanel("settings")).toBe("tools");
    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "tools" });
  });

  it("the Git toggle after Git closed on Sources shows Changes", async () => {
    await gitOn("sources");
    await closeTab(tabIdFor("git"));

    await toggleGitView();
    flushActivationForTest();

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

    expect(getActiveTabRoute()).toEqual({ kind: "settings", tab: "general" });
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
