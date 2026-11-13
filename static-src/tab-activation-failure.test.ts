// Every blank chat load that fails has exactly one owner, which drops the cover and paints the
// in-view Retry, and no refresh promise is left unobserved. Real tabs.ts, the chat opener faked;
// the box itself is chat.ts's `showBlankLoadFailure` (chat-open.test.ts).
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

import { ActionError } from "@cplieger/reactive/actions";
import { activateTab, closeTab, getActiveTabId, refreshActiveView } from "./tabs.js";
import { tabServer, settleTabs } from "./__test-helpers__/tabs-server.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import { activationRan, clickRow, coverUp, held } from "./__test-helpers__/tab-switch-mocks.js";

installSwitchSuite();

function refuseOnce(chatID: string): void {
  const h = held();
  h.reject(new ActionError("load failed", { status: 503 }));
  fx.chat.page(chatID, h);
}

describe("refreshActiveView during a pending activation", () => {
  it("issues no load while the activation is pending; the activation's own runs once", async () => {
    const ids = await strip("a", "b");
    const page = fx.chat.page("b");

    clickRow(ids["b"] ?? "");
    refreshActiveView();
    expect(fx.chat.refresh, "the reconcile loaded the pending target").not.toHaveBeenCalled();
    await activationRan();
    expect(fx.chat.refresh).toHaveBeenCalledTimes(1);
    page.resolve();
  });
});

describe("guards", () => {
  /** B's blank load failed and its owner painted the in-view Retry: cover down, no toast. */
  function inViewFailure(): void {
    expect(fx.chat.loadFailed).toHaveBeenCalledWith("b");
    expect(coverUp()).toBe(false);
    expect(fx.notices).toEqual([]);
  }

  it("paints the in-view Retry when a close's successor fails to load", async () => {
    const ids = await strip("b", "c");
    activateTab(ids["c"] ?? "");
    await activationRan();
    vi.clearAllMocks();
    refuseOnce("b");

    await closeTab(ids["c"] ?? "");
    await activationRan();

    expect(getActiveTabId()).toBe(ids["b"]);
    inViewFailure();
  });

  it("paints the in-view Retry when a remote removal's successor fails to load", async () => {
    const ids = await strip("b", "c");
    activateTab(ids["c"] ?? "");
    await activationRan();
    vi.clearAllMocks();
    refuseOnce("b");

    tabServer.closeRemotely(ids["c"] ?? "");
    await settleTabs();
    await activationRan();

    expect(getActiveTabId()).toBe(ids["b"]);
    inViewFailure();
  });

  it("paints the in-view Retry when a refused close's rollback lands on a chat that fails to load", async () => {
    const ids = await strip("b", "c");
    vi.clearAllMocks();
    refuseOnce("b");
    tabServer.failNext("close_tab", 500);

    // B is active; closing it hands the view to C, and the refusal brings B back, blank.
    await closeTab(ids["b"] ?? "");
    await settleTabs();
    await activationRan();

    expect(getActiveTabId()).toBe(ids["b"]);
    expect(fx.chat.loadFailed).toHaveBeenCalledWith("b");
    expect(coverUp()).toBe(false);
  });

  it("paints the in-view Retry when the reconcile's reload of a blank chat fails", async () => {
    await strip("b", "c");
    refuseOnce("b");

    refreshActiveView();
    await activationRan();

    inViewFailure();
  });
});
