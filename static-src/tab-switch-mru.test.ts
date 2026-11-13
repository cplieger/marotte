// The most-recent-tab history records a tab when its activation settles, so a close or a
// remote removal during a burst hands the view to the last tab that loaded, and a non-chat tab
// in a burst leaves the active chat on the painted one. Real tabs.ts, the chat opener faked.
import { describe, it, expect, vi } from "vitest";
import type * as TabsDrag from "./tabs-drag.js";

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  pushRoute: vi.fn(),
}));
vi.mock("./icons.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.iconsMock())),
}));
vi.mock("./device-view.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.deviceViewMock())),
}));
vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.domMock())),
}));
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
  ...(await import("./__test-helpers__/tab-switch-mocks.js")).storePointerOverrides(),
}));
vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  runLabelOf: vi.fn(() => ""),
}));
vi.mock("./composer-state.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.composerStateMock())),
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
  ...(await import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock())),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock())),
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tabs-server.js").then((m) => ({
    apiBlobOrError: vi.fn(),
    apiGetTyped: m.tabListRead(),
    apiGet: vi.fn(() => Promise.resolve(null)),
  }))),
}));

import { activateTab, closeTab, getActiveTabId, openTab, tabIdFor } from "./tabs.js";
import { settleTabs } from "./__test-helpers__/tabs-server.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import { activationRan, clickRow, pointers } from "./__test-helpers__/tab-switch-mocks.js";

installSwitchSuite();

describe("MRU records at settle", () => {
  it("hands a close to the settled successor, then the next close to the one before", async () => {
    const ids = await strip("a", "b", "c");
    for (const ref of ["b", "c"]) {
      activateTab(ids[ref] ?? "");
      await activationRan();
    }

    await closeTab(ids["c"] ?? "");
    await activationRan();
    expect(getActiveTabId()).toBe(ids["b"]);
    await closeTab(ids["b"] ?? "");
    await activationRan();
    expect(getActiveTabId()).toBe(ids["a"]);
  });

  it("hands a close during a burst to the last settled tab, not an intermediate one", async () => {
    const ids = await strip("a", "b", "c", "d");
    const page = fx.chat.page("d");

    clickRow(ids["b"] ?? "");
    clickRow(ids["c"] ?? "");
    clickRow(ids["d"] ?? "");
    await activationRan();
    await closeTab(ids["d"] ?? "");
    await activationRan();

    expect(getActiveTabId(), "the close went to a tab that never loaded").toBe(ids["a"]);
    page.resolve();
  });

  it("keeps the active chat on the painted one across a burst through a non-chat tab", async () => {
    const ids = await strip("a", "b");
    await openTab({ kind: "settings", ref: "", activate: false });
    await settleTabs();
    await activationRan();
    const settings = tabIdFor("settings", "");
    vi.clearAllMocks();

    clickRow(ids["a"] ?? "");
    clickRow(ids["b"] ?? "");
    clickRow(settings);
    expect(pointers.active, "the Settings click left the active chat on B").toBe("a");
    clickRow(ids["a"] ?? "");
    await activationRan();

    expect(getActiveTabId()).toBe(ids["a"]);
    expect(fx.chat.show, "a chat that was never unpainted was shown again").not.toHaveBeenCalled();
    expect(fx.chat.show.mock.calls.filter(([id]) => id === "b")).toHaveLength(0);
  });
});
