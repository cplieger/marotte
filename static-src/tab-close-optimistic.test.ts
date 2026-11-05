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
import { removePendingFor } from "./tabs-sync.js";
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

/** The rows on the strip, departing ones excluded: no stylesheet runs their exit. */
function rowIDs(): string[] {
  return [...document.querySelectorAll<HTMLElement>("#tab-list [data-tab-id]:not(.exiting)")].map(
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
    expect(pointers.active, "the active chat is the MRU successor, not the store's first").toBe(
      "a",
    );
    await frame();
    expect(rowIDs()).not.toContain(c);
    expect(coverUp()).toBe(true);

    await activationRan();
    expect(fx.chat.show.mock.calls).toEqual([["a"]]);
    expect(pointers.painted).toBe("a");
    await done;
  });
});

/** A strip three 20 px rows tall in a 24 px box, so it scrolls; removed after each case. */
function scrollableStrip(): { list: HTMLElement; dispose: () => void } {
  const style = document.createElement("style");
  style.textContent =
    "#tab-list{display:block;height:24px;overflow:auto} #tab-list .tab{display:block;height:20px}";
  document.head.appendChild(style);
  const list = document.getElementById("tab-list");
  if (list === null) {
    throw new Error("no #tab-list");
  }
  return { list, dispose: () => style.remove() };
}

/** End the exit of every departing row, as its animation would. */
function endExits(): void {
  for (const r of document.querySelectorAll<HTMLElement>("#tab-list .exiting")) {
    r.dispatchEvent(new Event("animationend"));
  }
}

describe("a refused close", () => {
  it("puts the row back at its index, pinned and active, with the put-back sentence and no Retry", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    await frame();
    const before = rowIDs();
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
  });

  it("lifts the successor's cover when the refusal lands before the successor showed", async () => {
    const ids = await closing();
    tabServer.failNext("close_tab", 500);

    await closeTab(ids["c"] ?? "");
    await settleTabs();
    await activationRan();

    expect(fx.chat.show, "C's view is still the mounted one").not.toHaveBeenCalled();
    expect(pointers.painted).toBe("c");
    expect(coverUp(), "the cover held for A stood over C's own transcript").toBe(false);
  });

  it("keeps a row whose refusal lands during its exit", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    await frame();
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);

    const done = closeTab(c);
    await frame();
    const node = document.querySelector<HTMLElement>(`#tab-list [data-tab-id="${c}"]`);
    expect(node?.classList.contains("exiting")).toBe(true);
    tabServer.releaseResponses();
    await done;
    await settleTabs();
    await frame();
    node?.dispatchEvent(new Event("animationend"));

    expect(node?.isConnected, "the exit's end removed the restored row").toBe(true);
    expect(node?.classList.contains("exiting")).toBe(false);
    expect(rowIDs()).toContain(c);
  });

  it("restores the strip's scroll offset the removal clamped", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    const { list, dispose } = scrollableStrip();
    try {
      await frame();
      list.scrollTop = list.scrollHeight;
      const bottom = list.scrollTop;
      expect(bottom).toBeGreaterThan(20);
      tabServer.holdResponses();
      tabServer.failNext("close_tab", 500);

      const done = closeTab(c);
      await frame();
      endExits();
      expect(list.scrollTop, "the removal clamped the strip").toBeLessThan(bottom);
      tabServer.releaseResponses();
      await done;
      await settleTabs();
      await frame();

      expect(rowIDs()).toContain(c);
      expect(list.scrollTop, "the strip's scroll offset").toBe(bottom);
    } finally {
      dispose();
    }
  });

  it("leaves the strip where the reader scrolled it while the close was pending", async () => {
    const ids = await closing();
    const { list, dispose } = scrollableStrip();
    try {
      await frame();
      list.scrollTop = list.scrollHeight;
      tabServer.holdResponses();
      tabServer.failNext("close_tab", 500);

      const done = closeTab(ids["c"] ?? "");
      await frame();
      endExits();
      list.dispatchEvent(new WheelEvent("wheel", { deltaY: -40 }));
      list.scrollTop = 0;
      tabServer.releaseResponses();
      await done;
      await settleTabs();
      await frame();

      expect(list.scrollTop).toBe(0);
    } finally {
      dispose();
    }
  });
});

describe("a pending close's view", () => {
  // `app.ts` wires this reader into the renderer's eviction exemption, so a refused close can
  // unpark the view it closed with its scroll and reading state.
  it("is named pending while the close awaits its answer, and not once it is confirmed", async () => {
    const ids = await strip("a", "c");
    // Frames held too: the fake commits at dispatch, and its echo would confirm the close at once.
    tabServer.setMode("manual");
    tabServer.holdResponses();

    const done = closeTab(ids["a"] ?? "");
    expect(removePendingFor("chat", "a")).toBe(true);
    expect(removePendingFor("chat", "c")).toBe(false);
    tabServer.releaseResponses();
    await done;
    tabServer.flushFrames();
    await settleTabs();
    expect(removePendingFor("chat", "a")).toBe(false);
  });
});
