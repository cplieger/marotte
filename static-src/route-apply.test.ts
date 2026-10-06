import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type * as Router from "./router.js";
import type * as Chat from "./chat.js";
import type * as StoreLoad from "./store-load.js";
import type * as WebView from "./web-view.js";
import type * as EditorOpeners from "./editor-openers.js";
import type * as Files from "./files.js";
import type * as SettingsTabs from "./settings-tabs.js";
import type * as SettingsHighlight from "./settings-highlight.js";
import type * as GitTabs from "./git-tabs.js";
import type * as DeviceView from "./device-view.js";
import type * as Dom from "./dom.js";
import type * as TabsDrag from "./tabs-drag.js";
import type * as Store from "./store.js";
import type * as RunStore from "./run-store.js";
import type * as ComposerState from "./composer-state.js";
import type * as ContextMenu from "./context-menu.js";
import type * as ChatExport from "./chat-export.js";
import type * as Toast from "./toast.js";
import type * as Transport from "./transport.js";
import type * as ApiClient from "./api-client.js";
import type * as TurnRail from "./turn-rail.js";

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
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
vi.mock("./web-view.js", async (importOriginal) => ({
  ...(await importOriginal<typeof WebView>()),
  showWebTab: vi.fn(),
  releaseWebTab: vi.fn(),
}));
vi.mock("./editor-openers.js", async (importOriginal) => ({
  ...(await importOriginal<typeof EditorOpeners>()),
  openFile: vi.fn(),
}));
vi.mock("./files.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Files>()),
  pointFilesTab: vi.fn(),
}));
vi.mock("./settings-tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SettingsTabs>()),
  forceSettingsTab: vi.fn(),
}));
vi.mock("./settings-highlight.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SettingsHighlight>()),
  flushURLHighlight: vi.fn(),
}));
vi.mock("./git-tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof GitTabs>()),
  forceGitTab: vi.fn(),
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
vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Dom>()),
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        if (prop === "tabList") {
          let tl = document.getElementById("tab-list");
          if (tl === null) {
            tl = document.createElement("div");
            tl.id = "tab-list";
            document.body.appendChild(tl);
          }
          return tl;
        }
        return document.createElement("div");
      },
    },
  ),
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
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
  setReprojectCallback: vi.fn(),
  setTapCallback: vi.fn(),
  dragOwnsStrip: vi.fn(() => false),
  exceedsSlop: vi.fn(() => false),
  pointerDragActivation: vi.fn(() => ({ holdMs: 150, slopPx: 8 })),
}));
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
vi.mock("./turn-rail.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TurnRail>()),
  jumpToTurn: vi.fn(() => Promise.resolve(true)),
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: (await import("./__test-helpers__/tabs-server.js")).tabListRead(),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

import { closeTab, tabIdFor, _resetForTest } from "./tabs.js";
import { get, getActiveId } from "./store.js";
import { switchSession } from "./chat.js";
import { jumpToTurn as jumpToTurnFn } from "./turn-rail.js";

const jumpToTurn = vi.mocked(jumpToTurnFn);
import { applyRoute } from "./route-apply.js";
import { replaceRoute } from "./router.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer } from "./__test-helpers__/tabs-server.js";
import type { Route } from "./route-path.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

beforeEach(() => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerTabOpeners({
    chat: { show: vi.fn(), refresh: vi.fn(), close: vi.fn(), dot: () => "" },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  vi.mocked(replaceRoute).mockClear();
  document.body.innerHTML = '<div id="tab-list"></div>';
});

afterEach(() => {
  _resetTabsSyncForTest();
});

/** An open is a server round trip whose frame is what paints, so a check polls. */
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

const page = "/workspace/demo/index.html";
const webRoute: Route = { kind: "web", path: page };

describe("applyRoute for a web preview", () => {
  it("opens the preview tab a deep link names", async () => {
    expect.assertions(1);
    await applyRoute(webRoute, "deeplink");
    await settled(() => tabIdFor("web", page) !== "", "the web tab opened");
  });

  it("activates an open preview tab from a history entry without canonicalizing", async () => {
    expect.assertions(3);
    await applyRoute(webRoute, "deeplink");
    await settled(() => tabIdFor("web", page) !== "", "the web tab opened");
    const id = tabIdFor("web", page);
    await applyRoute(webRoute, "history");
    expect(tabIdFor("web", page)).toBe(id);
    expect(replaceRoute).not.toHaveBeenCalled();
  });

  it("refuses to reopen a closed preview tab from a history entry", async () => {
    expect.assertions(4);
    await applyRoute(webRoute, "deeplink");
    await settled(() => tabIdFor("web", page) !== "", "the web tab opened");
    await closeTab(tabIdFor("web", page));
    await settled(() => tabIdFor("web", page) === "", "the web tab closed");
    await applyRoute(webRoute, "history");
    await new Promise((r) => {
      setTimeout(r, 20);
    });
    expect(tabIdFor("web", page)).toBe("");
    expect(replaceRoute).toHaveBeenCalledTimes(1);
  });
});

describe("applyRoute for a turn permalink", () => {
  it("lands a #turn-<n> through the rail's jump once the chat is active", async () => {
    vi.mocked(get).mockReturnValue({ id: "c1" } as ReturnType<typeof get>);
    vi.mocked(getActiveId).mockReturnValue("c1");
    vi.mocked(switchSession).mockResolvedValue("activated");
    jumpToTurn.mockClear();
    await applyRoute({ kind: "chat", id: "c1", turn: 7 }, "deeplink");
    await settled(() => jumpToTurn.mock.calls.length > 0, "the jump ran");
    expect(jumpToTurn).toHaveBeenCalledWith("c1", 7);
  });

  it("does not jump for a plain chat route", async () => {
    vi.mocked(get).mockReturnValue({ id: "c1" } as ReturnType<typeof get>);
    vi.mocked(getActiveId).mockReturnValue("c1");
    vi.mocked(switchSession).mockResolvedValue("activated");
    jumpToTurn.mockClear();
    await applyRoute({ kind: "chat", id: "c1" }, "deeplink");
    await new Promise((r) => setTimeout(r, 20));
    expect(jumpToTurn).not.toHaveBeenCalled();
  });
});
