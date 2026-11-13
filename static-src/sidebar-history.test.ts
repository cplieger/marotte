import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";

import indexHtml from "../static/index.html?raw";
import { manifestSheets, mountAppCSS } from "./__test-helpers__/css-rules.js";
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
vi.mock("./api-client.js", async (importOriginal) => {
  const tabs = (await import("./__test-helpers__/tabs-server.js")).tabListRead();
  return {
    ...(await importOriginal<typeof ApiClient>()),
    apiGetTyped: vi.fn((path: string) => tabs(path)),
    apiGet: vi.fn(() => Promise.resolve(null)),
  };
});

import { initSidebarHistory } from "./sidebar-history.js";
import { adoptRetention } from "./retention.js";
import {
  openTab,
  tabIdFor,
  getActiveTabId,
  toggleDocsView,
  toggleGitView,
  toggleHistoryView,
  toggleSettingsView,
  _resetForTest,
} from "./tabs.js";
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
  it("opens History and marks the button active, closing the phone drawer", async () => {
    const sidebar = el("sidebar");
    sidebar.classList.add("open");

    el("history-btn").click();
    expect(sidebar.classList.contains("open")).toBe(false);
    await settle();

    const id = tabIdFor("history");
    expect(id).not.toBe("");
    expect(getActiveTabId()).toBe(id);
    expect(el("history-view").classList.contains("hidden")).toBe(false);
    expect(el("history-btn").classList.contains("active")).toBe(true);
  });

  it("closes the tab it is showing when pressed again, and drops the active mark", async () => {
    await openTab({ kind: "history" });
    expect(el("history-btn").classList.contains("active")).toBe(true);

    el("history-btn").click();
    await settle();

    expect(tabIdFor("history")).toBe("");
    expect(el("history-btn").classList.contains("active")).toBe(false);
  });

  it("brings an open but hidden History forward instead of closing it", async () => {
    await openTab({ kind: "history" });
    const id = tabIdFor("history");
    await openTab({ kind: "settings" });
    expect(getActiveTabId()).not.toBe(id);
    expect(el("history-btn").classList.contains("active")).toBe(false);

    el("history-btn").click();
    await settle();

    expect(tabIdFor("history")).toBe(id);
    expect(getActiveTabId()).toBe(id);
    expect(el("history-btn").classList.contains("active")).toBe(true);
  });

  it("is a row named by its visible label, with no tooltip", () => {
    const b = el("history-btn");
    expect(b.classList.contains("side-history-row")).toBe(true);
    expect(b.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
    expect(b.querySelector(".side-history-label")?.textContent).toBe("History");
    expect(b.textContent.trim(), "the name from content").toBe("History");
    expect(b.hasAttribute("aria-label")).toBe(false);
    expect(b.hasAttribute("data-tooltip")).toBe(false);
    expect(b.hasAttribute("title")).toBe(false);
  });

  it("sits at the foot of the sections, leaving New chat alone in the actions row", () => {
    const actions = shell.querySelector(".sidebar-actions");
    expect([...(actions?.children ?? [])].map((c) => c.id)).toEqual(["new-chat"]);
    expect(el("tab-sections").lastElementChild).toBe(el("history-btn"));
  });

  it.each(["fine", "coarse"] as const)(
    "is a full-width row at the tab row height on a %s pointer",
    (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const history = el("history-btn").getBoundingClientRect();
      const column = el("tab-sections").getBoundingClientRect();
      // `--tab-row-h` on each tier, 42px on a mouse and 44px under a finger.
      expect(history.height).toBe(tier === "fine" ? 42 : 44);
      expect(history.width).toBeGreaterThan(column.width / 2);
    },
  );

  it("is shown with no tab open, no closed chat and retention off", async () => {
    await settle();
    expect(tabServer.subjects()).toHaveLength(0);
    for (const days of [0, 7]) {
      adoptRetention(settingsPayload({ chat_retention_days: days }));
      const b = el("history-btn");
      expect(b.hidden, `days ${String(days)}`).toBe(false);
      expect(b.classList.contains("hidden"), `days ${String(days)}`).toBe(false);
      expect(b.getBoundingClientRect().height, `days ${String(days)}`).toBeGreaterThan(0);
    }
  });

  it("says in the Days kept hint that 0 deletes on close, never that it hides History", () => {
    const hint = new DOMParser()
      .parseFromString(indexHtml, "text/html")
      .querySelector("#general-retention-section .section-hint");
    expect(hint, "the Days kept hint").not.toBeNull();
    expect(hint?.innerHTML).toContain(
      "Set <strong>0</strong> to delete a chat as soon as you close it.",
    );
    expect(hint?.textContent).not.toMatch(/hide/i);
  });
});

describe("the sidebar's scroll region", () => {
  it("scrolls the sections as a whole, not the Open list", () => {
    expect(getComputedStyle(el("tab-sections")).overflowY).toBe("auto");
    expect(getComputedStyle(el("tab-list")).overflowY).toBe("visible");
    expect(getComputedStyle(el("pinned-list")).overflowY).toBe("visible");
  });

  it("leaves no rule naming the old square History button", () => {
    const stale = manifestSheets().filter((s) => s.css.includes("sidebar-history-btn"));
    expect(stale.map((s) => s.name)).toEqual([]);
  });
});

// The toolbar's page toggles share one contract: lit while their page is the one on screen, and a second
// press closes it. The docs button was the one that never lit.
describe("the page toggles in the toolbar and sidebar", () => {
  const toggles = [
    { button: "docs-btn", kind: "docs", toggle: () => toggleDocsView() },
    { button: "git-btn", kind: "git", toggle: () => toggleGitView() },
    { button: "settings-btn", kind: "settings", toggle: () => toggleSettingsView() },
    { button: "history-btn", kind: "history", toggle: () => toggleHistoryView() },
  ] as const;

  it.each(toggles)(
    "$button lights while $kind is shown and closes it on a second press",
    async (t) => {
      expect(el(t.button).classList.contains("active")).toBe(false);

      await t.toggle();
      await settle();
      expect(el(t.button).classList.contains("active")).toBe(true);
      expect(el(t.button).getAttribute("aria-pressed")).toBe("true");

      await t.toggle();
      await settle();
      expect(el(t.button).classList.contains("active")).toBe(false);
      expect(el(t.button).getAttribute("aria-pressed")).toBe("false");
      expect(tabIdFor(t.kind)).toBe("");
    },
  );

  it("lights only the button of the page on screen", async () => {
    await toggleDocsView();
    await toggleGitView();
    await settle();
    expect(el("git-btn").classList.contains("active")).toBe(true);
    expect(el("docs-btn").classList.contains("active")).toBe(false);
  });
});
