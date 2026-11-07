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
  claimNavigation,
  closeTab,
  getActiveTabId,
  hasTab,
  navigationHeld,
  registerTabNotice,
  setTabPinned,
} from "./tabs.js";
import { getSessions } from "./store.js";
import { restoreFailedSend } from "./composer-state.js";
import { $ } from "./dom.js";
import { removePendingFor } from "./tabs-sync.js";
import { releaseCover } from "./transcript-cover.js";
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

  it("takes the closed row off the strip and marks the successor active inside the click", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    const a = ids["a"] ?? "";

    const done = closeTab(c);

    expect(rowIDs(), "the closed row left the strip inside the click").not.toContain(c);
    const successor = document.querySelector<HTMLElement>(
      `#tab-list [data-tab-id="${CSS.escape(a)}"]`,
    );
    expect(successor?.classList.contains("active")).toBe(true);
    expect(successor?.getAttribute("aria-selected")).toBe("true");
    await activationRan();
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

  it("shows the closed chat again, uncovered, when the refusal lands inside the successor's show", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    await frame();
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);
    fx.chat.show.mockImplementationOnce((id: string) => {
      pointers.painted = id;
      tabServer.releaseResponses();
    });

    const done = closeTab(c);
    await activationRan();
    await done;
    await settleTabs();
    await activationRan();

    expect(fx.chat.show.mock.calls, "the successor, then the restored chat").toEqual([
      ["a"],
      ["c"],
    ]);
    expect(getActiveTabId()).toBe(c);
    expect(pointers).toEqual({ active: "c", painted: "c" });
    // The fake show runs no transcript pass; C's pass is the cover's releaser, so it must stand for C.
    releaseCover("a");
    expect(coverUp(), "a cover held for the successor").toBe(true);
    releaseCover("c");
    await activationRan();
    expect(coverUp(), "C's paint lifts the cover").toBe(false);
  });

  it("puts the row back without taking the reader off a tab chosen since the close", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    const b = ids["b"] ?? "";
    await frame();
    const before = rowIDs();
    const notices: unknown[][] = [];
    registerTabNotice((...args) => {
      notices.push(args);
    });
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);

    const done = closeTab(c);
    await activationRan();
    activateTab(b);
    await activationRan();
    fx.chat.show.mockClear();
    tabServer.releaseResponses();
    await done;
    await settleTabs();
    await activationRan();

    expect(rowIDs(), "the closed row is back at its index").toEqual(before);
    expect(getActiveTabId()).toBe(b);
    expect(pointers.active, "the composer still acts for the chosen chat").toBe("b");
    expect(fx.chat.show, "the restored chat was shown over the chosen one").not.toHaveBeenCalled();
    expect(notices).toEqual([["c", "c", "Could not close that tab, so it was put back.", "error"]]);
  });

  it("keeps the successor a reader left and came back to", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    const a = ids["a"] ?? "";
    await frame();
    const before = rowIDs();
    const notices: unknown[][] = [];
    registerTabNotice((...args) => {
      notices.push(args);
    });
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);

    const done = closeTab(c);
    await activationRan();
    activateTab(ids["b"] ?? "");
    await activationRan();
    activateTab(a);
    await activationRan();
    fx.chat.show.mockClear();
    tabServer.releaseResponses();
    await done;
    await settleTabs();
    await activationRan();

    expect(rowIDs(), "the closed row is back at its index").toEqual(before);
    expect(getActiveTabId(), "the tab the reader last chose").toBe(a);
    expect(pointers.active).toBe("a");
    expect(fx.chat.show, "the restored chat was shown over the chosen one").not.toHaveBeenCalled();
    expect(notices).toEqual([["c", "c", "Could not close that tab, so it was put back.", "error"]]);
  });

  it("shows the closed tab again when the reader stayed on its successor", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    await frame();
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);

    const done = closeTab(c);
    await activationRan();
    expect(pointers.painted, "the successor showed before the refusal").toBe("a");
    fx.chat.show.mockClear();
    tabServer.releaseResponses();
    await done;
    await settleTabs();
    await activationRan();

    expect(getActiveTabId()).toBe(c);
    expect(pointers).toEqual({ active: "c", painted: "c" });
    expect(fx.chat.show.mock.calls).toEqual([["c"]]);
  });

  it("puts a background tab back in place and leaves the shown tab alone", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    const b = ids["b"] ?? "";
    await frame();
    const before = rowIDs();
    const notices: unknown[][] = [];
    registerTabNotice((...args) => {
      notices.push(args);
    });
    tabServer.failNext("close_tab", 500);

    const done = closeTab(b);
    expect(getActiveTabId(), "a background close moves no view").toBe(c);
    await done;
    await settleTabs();
    await activationRan();

    expect(rowIDs()).toEqual(before);
    expect(getActiveTabId()).toBe(c);
    expect(pointers).toEqual({ active: "c", painted: "c" });
    expect(fx.chat.show).not.toHaveBeenCalled();
    expect(notices).toEqual([["b", "b", "Could not close that tab, so it was put back.", "error"]]);
  });

  it.each([
    { first: "A's", cMs: 4_000, aMs: 2_000 },
    { first: "C's", cMs: 2_000, aMs: 4_000 },
  ])(
    "ends two refused closes on the tab closed last, with $first refusal landing first",
    async ({ cMs, aMs }) => {
      const ids = await closing();
      const c = ids["c"] ?? "";
      const a = ids["a"] ?? "";
      await frame();
      const before = rowIDs();
      const refusal = { ok: false, status: 500, error: "nope" };
      vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
      try {
        tabServer.failNext("close_tab", 500);
        tabServer.answerAfterNext("close_tab", cMs, refusal);
        const closedC = closeTab(c);
        await activationRan();
        expect(getActiveTabId(), "C's successor").toBe(a);
        tabServer.failNext("close_tab", 500);
        tabServer.answerAfterNext("close_tab", aMs, refusal);
        const closedA = closeTab(a);
        await activationRan();

        await vi.advanceTimersByTimeAsync(4_100);
        await Promise.all([closedC, closedA]);
        await settleTabs();
        await activationRan();

        expect(rowIDs()).toEqual(before);
        expect(getActiveTabId(), "A, the tab the reader was on when it last closed one").toBe(a);
        expect(pointers.active).toBe("a");
      } finally {
        vi.useRealTimers();
      }
    },
  );

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

describe("an unanswered close of the active tab", () => {
  it("puts the row back active once the re-list still holds it, with no answer ever arriving", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    await frame();
    const before = rowIDs();
    const notices: unknown[][] = [];
    registerTabNotice((...args) => {
      notices.push(args);
    });
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    tabServer.setMode("manual");
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);
    try {
      void closeTab(c);
      await frame();
      expect(rowIDs()).not.toContain(c);

      await vi.advanceTimersByTimeAsync(5_000 + 1_000);
      await settleTabs();
      await activationRan();

      expect(rowIDs()).toEqual(before);
      expect(getActiveTabId()).toBe(c);
      expect(notices).toEqual([
        ["c", "c", "Close not confirmed, so the tab was restored.", "info"],
      ]);
    } finally {
      vi.useRealTimers();
      tabServer.releaseResponses();
    }
  });

  it("puts the row back without taking the reader off a tab chosen since the close", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    const b = ids["b"] ?? "";
    await frame();
    const before = rowIDs();
    const notices: unknown[][] = [];
    registerTabNotice((...args) => {
      notices.push(args);
    });
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    tabServer.setMode("manual");
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);
    try {
      void closeTab(c);
      await activationRan();
      activateTab(b);
      await activationRan();
      fx.chat.show.mockClear();

      await vi.advanceTimersByTimeAsync(5_000 + 1_000);
      await settleTabs();
      await activationRan();

      expect(rowIDs(), "the closed row is back at its index").toEqual(before);
      expect(getActiveTabId()).toBe(b);
      expect(pointers.active, "the composer still acts for the chosen chat").toBe("b");
      expect(
        fx.chat.show,
        "the restored chat was shown over the chosen one",
      ).not.toHaveBeenCalled();
      expect(notices).toEqual([
        ["c", "c", "Close not confirmed, so the tab was restored.", "info"],
      ]);
    } finally {
      vi.useRealTimers();
      tabServer.releaseResponses();
    }
  });

  it("keeps the successor a reader left and came back to", async () => {
    const ids = await closing();
    const c = ids["c"] ?? "";
    const a = ids["a"] ?? "";
    await frame();
    const before = rowIDs();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    tabServer.setMode("manual");
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);
    try {
      void closeTab(c);
      await activationRan();
      activateTab(ids["b"] ?? "");
      await activationRan();
      activateTab(a);
      await activationRan();
      fx.chat.show.mockClear();

      await vi.advanceTimersByTimeAsync(5_000 + 1_000);
      await settleTabs();
      await activationRan();

      expect(rowIDs(), "the closed row is back at its index").toEqual(before);
      expect(getActiveTabId(), "the tab the reader last chose").toBe(a);
      expect(pointers.active).toBe("a");
      expect(fx.chat.show).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
      tabServer.releaseResponses();
    }
  });

  it("binds the only tab again, with the text typed meanwhile as its draft", async () => {
    const ids = await strip("solo");
    const solo = ids["solo"] ?? "";
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    tabServer.setMode("manual");
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);
    try {
      void closeTab(solo);
      expect(getActiveTabId(), "the strip emptied").toBe("");
      expect(pointers.active, "Send creates a fresh chat while the strip is empty").toBe("");
      $.promptInput.value = "half a thought";

      await vi.advanceTimersByTimeAsync(5_000 + 1_000);
      await settleTabs();
      await activationRan();

      expect(rowIDs()).toEqual([solo]);
      expect(getActiveTabId()).toBe(solo);
      expect(pointers.active, "Send acts for the restored chat").toBe("solo");
      expect(vi.mocked(restoreFailedSend)).toHaveBeenCalledWith("solo", "half a thought");
    } finally {
      vi.useRealTimers();
      tabServer.releaseResponses();
    }
  });
  it("binds the only tab again while a chat opened from the empty strip is still on its way", async () => {
    const ids = await strip("solo");
    const solo = ids["solo"] ?? "";
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    tabServer.setMode("manual");
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);
    try {
      void closeTab(solo);
      // A Send from the empty composer claims the view for the chat it creates.
      const create = claimNavigation();

      await vi.advanceTimersByTimeAsync(5_000 + 1_000);
      await settleTabs();
      await activationRan();

      expect(getActiveTabId()).toBe(solo);
      expect(pointers.active, "the shown tab acts for its chat").toBe("solo");
      expect(navigationHeld(create), "the created chat still takes the view when it lands").toBe(
        true,
      );
    } finally {
      vi.useRealTimers();
      tabServer.releaseResponses();
    }
  });
});

describe("a close's put-back notice lands with the restored chat's content", () => {
  /** Notices, each with the chat painted and the cover's state when it was raised. */
  function atNotice(): { painted: string; covered: boolean }[] {
    fx.chat.show.mockImplementation((id: string) => {
      pointers.painted = id;
      // A resident view's paint, which ends by releasing the cover (messages.ts).
      if (resident.has(id)) {
        releaseCover(id);
      }
    });
    const seen: { painted: string; covered: boolean }[] = [];
    registerTabNotice(() => {
      seen.push({ painted: pointers.painted, covered: coverUp() });
    });
    return seen;
  }

  it("shows the closed chat, uncovered, before a refusal's notice", async () => {
    const ids = await closing();
    const seen = atNotice();
    await frame();
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);

    const done = closeTab(ids["c"] ?? "");
    await activationRan();
    expect(pointers.painted, "the successor showed before the refusal").toBe("a");
    tabServer.releaseResponses();
    await done;

    expect(seen, "the notice says the tab was put back while it was not on screen").toEqual([
      { painted: "c", covered: false },
    ]);
  });

  it("shows the closed chat, uncovered, before an unanswered close's notice", async () => {
    const ids = await closing();
    const seen = atNotice();
    await frame();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"], shouldAdvanceTime: true });
    tabServer.setMode("manual");
    tabServer.holdResponses();
    tabServer.failNext("close_tab", 500);
    try {
      void closeTab(ids["c"] ?? "");
      await activationRan();
      expect(pointers.painted, "the successor showed before the restore").toBe("a");

      await vi.advanceTimersByTimeAsync(5_000 + 1_000);
      await settleTabs();

      expect(seen, "the notice says the tab was restored while it was not on screen").toEqual([
        { painted: "c", covered: false },
      ]);
    } finally {
      vi.useRealTimers();
      tabServer.releaseResponses();
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
