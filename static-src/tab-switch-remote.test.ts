// Another device's tab changes during a pending switch: the server's statement wins, the switch
// carries on when its target survives, and nothing reverts or toasts.
// Real tabs.ts against the fake tab server, the chat opener faked.
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

import { getActiveTabId, hasTab } from "./tabs.js";
import { listTabs } from "./tabs-sync.js";
import { tabServer, settleTabs } from "./__test-helpers__/tabs-server.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import {
  activationRan,
  clickRow,
  coverUp,
  frame,
  rowEl,
} from "./__test-helpers__/tab-switch-mocks.js";

installSwitchSuite();

describe("a cross-device change mid-switch reconciles", () => {
  it("returns to the settled tab when another device closes the target, aborting its load", async () => {
    const ids = await strip("a", "b");
    const page = fx.chat.page("b");

    clickRow(ids["b"] ?? "");
    await activationRan();
    tabServer.closeRemotely(ids["b"] ?? "");
    await settleTabs();
    await activationRan();

    expect(getActiveTabId()).toBe(ids["a"]);
    expect(page.signal?.aborted, "the removed target's load kept running").toBe(true);
    page.resolve();
    await activationRan();
    expect(getActiveTabId()).toBe(ids["a"]);
    expect(fx.notices).toEqual([]);
  });

  it("keeps the switch when another device reorders and pins other tabs", async () => {
    const ids = await strip("a", "b", "c");
    const page = fx.chat.page("b");

    clickRow(ids["b"] ?? "");
    const c = tabServer.subjects().find((s) => s.id === ids["c"]);
    expect(c).toBeDefined();
    if (c !== undefined) {
      tabServer.emitRaw({
        changed: { ...c, pinned: true },
        order: [ids["c"] ?? "", ids["b"] ?? "", ids["a"] ?? ""],
        version: tabServer.version() + 1,
      });
    }
    await settleTabs();
    await frame();

    expect(getActiveTabId()).toBe(ids["b"]);
    expect(rowEl(ids["b"] ?? "").classList.contains("active")).toBe(true);
    await activationRan();
    expect(fx.chat.show).toHaveBeenCalledWith("b");
    expect(page.signal?.aborted).toBe(false);
    page.resolve();
    expect(fx.notices).toEqual([]);
  });

  it("keeps the switch across a re-list that keeps the target", async () => {
    const ids = await strip("a", "b");
    const page = fx.chat.page("b");

    clickRow(ids["b"] ?? "");
    await listTabs();
    await settleTabs();
    await activationRan();

    expect(getActiveTabId()).toBe(ids["b"]);
    expect(fx.chat.show.mock.calls.filter(([id]) => id === "b")).toHaveLength(1);
    expect(page.signal?.aborted).toBe(false);
    page.resolve();
  });

  it("keeps the cover up when another device closes the chat the switch is leaving", async () => {
    const ids = await strip("a", "b");
    const page = fx.chat.page("b");

    clickRow(ids["b"] ?? "");
    tabServer.closeRemotely(ids["a"] ?? "");
    await settleTabs();
    await frame();

    expect(hasTab("chat", "a")).toBe(false);
    expect(fx.chat.close).toHaveBeenCalledWith("a");
    expect(coverUp(), "the outgoing chat's close dropped the target's cover").toBe(true);
    await activationRan();
    expect(getActiveTabId()).toBe(ids["b"]);
    expect(fx.chat.show).toHaveBeenCalledWith("b");
    page.resolve();
  });
});
