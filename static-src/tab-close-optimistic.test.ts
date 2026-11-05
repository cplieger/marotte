// A close is immediate: the row goes and the successor is selected in the click, its view is shown
// after the frame, and a refusal puts the row back where it was. Real tabs.ts against the fake tab
// server, the chat opener faked.
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

import {
  activateTab,
  closeTab,
  getActiveTabId,
  hasTab,
  registerTabNotice,
  setTabPinned,
} from "./tabs.js";
import { getSessions } from "./store.js";
import * as tabsSync from "./tabs-sync.js";
import { tabServer, settleTabs } from "./__test-helpers__/tabs-server.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import {
  activationRan,
  coverUp,
  frame,
  pointers,
  resident,
} from "./__test-helpers__/tab-switch-mocks.js";
import type { Session } from "./types.js";

installSwitchSuite();

function rowIDs(): string[] {
  return [...document.querySelectorAll<HTMLElement>("#tab-list [data-tab-id]")].map(
    (e) => e.dataset["tabId"] ?? "",
  );
}

/** Strip b, a, c with C active and pinned, MRU C, A, B; the store lists B first. */
async function closing(): Promise<Record<string, string>> {
  const ids = await strip("b", "a", "c");
  await setTabPinned(ids["c"] ?? "", true);
  await settleTabs();
  activateTab(ids["a"] ?? "");
  await activationRan();
  activateTab(ids["c"] ?? "");
  await activationRan();
  pointers.active = "c";
  pointers.painted = "c";
  resident.add("a");
  resident.add("c");
  vi.mocked(getSessions).mockReturnValue(
    ["b", "a", "c"].map((id) => ({ id, name: id }) as unknown as Session),
  );
  vi.clearAllMocks();
  return ids;
}

describe("a close shows at once", () => {
  it("removes the row and selects the MRU successor in the click, and shows it after the frame", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";

    const done = closeTab(c);

    expect(hasTab("chat", "c")).toBe(false);
    expect(getActiveTabId()).toBe(ids["a"]);
    expect(fx.chat.show, "the successor was shown inside the click").not.toHaveBeenCalled();
    expect(pointers.painted).toBe("c");
    expect(pointers.active, "the store's active chat followed store order").toBe("a");
    await frame();
    expect(rowIDs()).not.toContain(c);
    expect(coverUp()).toBe(true);

    await activationRan();
    expect(fx.chat.show.mock.calls).toEqual([["a"]]);
    expect(pointers.painted).toBe("a");
    await done;
  });
});

describe("a refused close", () => {
  it("puts the row back at its index, pinned and active, with the put-back sentence and no Retry", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    await frame();
    const before = rowIDs();
    const list = document.getElementById("tab-list");
    if (list !== null) {
      list.style.cssText = "height:24px;overflow:auto;display:block";
      for (const r of list.querySelectorAll<HTMLElement>("[data-tab-id]")) {
        r.style.cssText = "display:block;height:20px";
      }
      list.scrollTop = 12;
    }
    const scrolled = list?.scrollTop ?? 0;
    expect(scrolled).toBeGreaterThan(0);
    const notices: unknown[][] = [];
    registerTabNotice((...args) => {
      notices.push(args);
    });
    tabServer.failNext("close_tab", 500);

    await closeTab(c);
    await settleTabs();
    await activationRan();

    expect(rowIDs()).toEqual(before);
    expect(tabServer.subjects().find((s) => s.id === c)?.pinned).toBe(true);
    expect(
      document.querySelector(`#tab-list [data-tab-id="${c}"]`)?.classList.contains("tab-pinned"),
    ).toBe(true);
    expect(getActiveTabId()).toBe(c);
    expect(pointers.active).toBe("c");
    expect(notices).toEqual([["c", "c", "Could not close that tab, so it was put back.", "error"]]);
    expect(list?.scrollTop, "the strip's scroll offset").toBe(scrolled);
  });
});

describe("a pending close's view", () => {
  // `app.ts` wires this reader into the renderer's eviction exemption, so a refused close can
  // unpark the view it closed with its scroll and reading state.
  it("is named pending while the close awaits its answer, and not once it is confirmed", async () => {
    const ids = await strip("a", "c");
    const pending = (
      tabsSync as unknown as {
        removePendingFor?: (kind: string, ref: string) => boolean;
      }
    ).removePendingFor;
    expect(typeof pending, "tabsSync.removePendingFor").toBe("function");
    tabServer.holdResponses();

    const done = closeTab(ids["a"] ?? "");
    expect(pending?.("chat", "a")).toBe(true);
    expect(pending?.("chat", "c")).toBe(false);
    tabServer.releaseResponses();
    await done;
    await settleTabs();
    expect(pending?.("chat", "a")).toBe(false);
  });
});
