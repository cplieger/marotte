import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { settingsPayload } from "./__test-helpers__/settings.js";
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

const m = vi.hoisted(() => ({
  settings: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
// The History page's own content is not this suite's subject; its tab and view are.
vi.mock("./history.js", async (importOriginal) => ({
  ...(await importOriginal<typeof History>()),
  loadHistoryView: vi.fn(),
  refreshHistoryView: vi.fn(),
  teardownHistoryView: vi.fn(),
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
vi.mock("./api-client.js", async (importOriginal) => {
  const tabs = (await import("./__test-helpers__/tabs-server.js")).tabListRead();
  return {
    ...(await importOriginal<typeof ApiClient>()),
    apiGetTyped: vi.fn((path: string) => (path === "/api/settings" ? m.settings() : tabs(path))),
    apiGet: vi.fn(() => Promise.resolve(null)),
  };
});

import { initSidebarHistory } from "./sidebar-history.js";
import { refreshRetention } from "./retention.js";
import { openTab, tabIdFor, getActiveTabId, _resetForTest } from "./tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer } from "./__test-helpers__/tabs-server.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

let shell: HTMLElement;
let style: HTMLStyleElement;

/** The shipped body, so the row, the toolbar and the History view are index.html's own. */
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
  // The History tab imports this lazily and unawaited; loaded here, once the shell exists, its
  // real graph is not still resolving through the mock route when the file ends.
  await import("./history.js");
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
    chat: { show: vi.fn(), refresh: vi.fn(), close: vi.fn(), dot: () => "" },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
});

afterEach(() => {
  _resetTabsSyncForTest();
  const root = document.documentElement;
  delete root.dataset["pointer"];
});

function el<T extends HTMLElement>(id: string): T {
  const e = shell.querySelector<T>(`[id="${id}"]`);
  if (e === null) {
    throw new Error(`no #${id}`);
  }
  return e;
}

/** Let a dispatched command's round trip and the projection's paint run out. */
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await new Promise((r) => {
      setTimeout(r, 0);
    });
  }
}

describe("the sidebar's History button", () => {
  it("leaves an open History tab open and active, and closes the phone drawer", async () => {
    await openTab({ kind: "history" });
    const id = tabIdFor("history");
    expect(getActiveTabId(), "precondition: History is the active tab").toBe(id);
    const sidebar = el("sidebar");
    sidebar.classList.add("open");

    el("history-btn").click();
    expect(sidebar.classList.contains("open")).toBe(false);
    await settle();

    expect(tabIdFor("history")).toBe(id);
    expect(getActiveTabId()).toBe(id);
    expect(el("history-view").classList.contains("hidden")).toBe(false);
    expect(el("history-btn").classList.contains("active")).toBe(true);
  });

  it("is named History and explains itself through the styled tooltip, never a native title", () => {
    const b = el("history-btn");
    expect(b.getAttribute("aria-label")).toBe("History");
    expect(b.getAttribute("data-tooltip")).toBe("History");
    expect(b.hasAttribute("title")).toBe(false);
  });

  it.each(["fine", "coarse"] as const)(
    "is a square level with New chat, right of it, on a %s pointer",
    (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const chat = el("new-chat").getBoundingClientRect();
      const history = el("history-btn").getBoundingClientRect();
      expect(history.height).toBe({ fine: 36, coarse: 44 }[tier]);
      expect(history.height).toBe(chat.height);
      expect(history.width).toBe(history.height);
      expect(history.top).toBe(chat.top);
      expect(history.left).toBeGreaterThan(chat.right);
      // New chat takes the rest of the row.
      expect(chat.width).toBeGreaterThan(4 * history.width);
    },
  );

  it("hides with retention off and returns with it on", async () => {
    m.settings.mockResolvedValue(settingsPayload({ chat_retention_days: 0 }));
    await refreshRetention();
    expect(el("history-btn").classList.contains("hidden")).toBe(true);
    m.settings.mockResolvedValue(settingsPayload({ chat_retention_days: 7 }));
    await refreshRetention();
    expect(el("history-btn").classList.contains("hidden")).toBe(false);
  });
});
