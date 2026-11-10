// A switch paints the target tab at once: the click writes the strip, the device view, the active
// chat and the shared cover, and nothing else; the target's view is shown in a task after that
// frame paints. Real tabs.ts against the fake tab server, the chat opener's halves faked.
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

import { tabIdFor, getActiveTabId, closeTab } from "./tabs.js";
import { observeStamp } from "./subject-versions.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import {
  activationRan,
  clickRow,
  cover,
  coverUp,
  emptyChats,
  frame,
  pointers,
  resident,
  rowEl,
} from "./__test-helpers__/tab-switch-mocks.js";

installSwitchSuite();

describe("a switch paints the target tab at once", () => {
  it("writes the strip, the active chat and the cover in the click, and shows the view after the frame", async () => {
    const ids = await strip("a", "b");
    const b = ids["b"] ?? "";
    const page = fx.chat.page("b");
    document.getElementById("in-transcript")?.focus();

    clickRow(b);

    // Inside the click: the gesture half alone.
    expect(fx.chat.show, "the view was shown inside the click").not.toHaveBeenCalled();
    expect(fx.chat.refresh, "the load started inside the click").not.toHaveBeenCalled();
    expect(fx.chat.select).toHaveBeenCalledWith("b");
    expect(pointers.active).toBe("b");
    expect(pointers.painted).toBe("a");
    expect(getActiveTabId()).toBe(b);
    expect(rowEl(b).classList.contains("active"), "the strip moved inside the click").toBe(true);
    expect(rowEl(b).getAttribute("aria-selected")).toBe("true");

    await frame();
    expect(coverUp(), "the cover is up in the gesture's frame").toBe(true);
    expect(cover()?.querySelector(".skeleton-rows")).not.toBeNull();
    const wrap = document.getElementById("messages-wrap");
    expect(wrap?.getAttribute("aria-busy")).toBe("true");
    expect(wrap?.getAttribute("aria-hidden"), "the covered transcript is read").toBe("true");
    expect(wrap?.inert, "inert restyles every resident turn").toBe(false);
    expect(document.activeElement?.id, "focus left the covered transcript").toBe("prompt-input");

    await activationRan();
    expect(wrap?.inert, "inert restyles every resident turn").toBe(false);
    expect(fx.chat.show).toHaveBeenCalledTimes(1);
    expect(fx.chat.show).toHaveBeenCalledWith("b");
    expect(fx.chat.refresh).toHaveBeenCalledTimes(1);
    expect(page.signal?.aborted).toBe(false);
    // The page is still held: nothing has put B's turns on screen, so the cover stays.
    expect(coverUp()).toBe(true);
    page.resolve();
  });

  it("covers a switch to a parked, fresh chat and issues no load", async () => {
    observeStamp({ kind: "chat", ref: "b", version: "1" });
    await strip("a", "b");
    resident.add("b");

    clickRow(tabIdFor("chat", "b"));
    await frame();
    expect(coverUp(), "the cover is up in the gesture's frame").toBe(true);

    await activationRan();
    expect(fx.chat.show).toHaveBeenCalledWith("b");
    expect(fx.chat.refresh, "a fresh view issued a load").not.toHaveBeenCalled();
  });

  it("covers a switch to an evicted chat whose window is empty", async () => {
    await strip("a", "e");

    clickRow(tabIdFor("chat", "e"));
    await frame();
    expect(coverUp(), "A's cards showed under no cover").toBe(true);
  });

  it("shows an empty chat's own page inside the click, with no cover", async () => {
    await strip("a", "n");
    emptyChats.add("n");

    clickRow(tabIdFor("chat", "n"));

    expect(fx.chat.show, "the empty page waited for a frame").toHaveBeenCalledWith("n");
    expect(pointers.painted).toBe("n");
    expect(coverUp(), "an empty chat flashed the cover").toBe(false);
    await activationRan();
    expect(coverUp()).toBe(false);
    expect(fx.chat.show).toHaveBeenCalledTimes(1);
  });

  it("lifts a burst's cover when the burst ends on an empty chat", async () => {
    await strip("a", "b", "n");
    emptyChats.add("n");
    fx.chat.page("b");

    clickRow(tabIdFor("chat", "b"));
    await frame();
    expect(coverUp()).toBe(true);
    clickRow(tabIdFor("chat", "n"));

    expect(coverUp(), "B's cover stayed over N's empty page").toBe(false);
    await activationRan();
    expect(
      fx.chat.show.mock.calls.map((c) => c[0]),
      "B, superseded, was shown",
    ).toEqual(["n"]);
    expect(fx.chat.refresh.mock.calls.map((c) => c[0])).toEqual(["n"]);
  });

  it("dispatches no second switch for a re-click on the tab being shown", async () => {
    const ids = await strip("a", "b");
    const b = ids["b"] ?? "";
    const page = fx.chat.page("b");

    clickRow(b);
    clickRow(b);
    await activationRan();
    clickRow(b);
    await activationRan();

    expect(fx.chat.show).toHaveBeenCalledTimes(1);
    expect(fx.chat.refresh).toHaveBeenCalledTimes(1);
    expect(page.signal?.aborted, "the re-click restarted the load").toBe(false);
    page.resolve();
  });
});

describe("the activation sends a stale target's page request before mounting its view", () => {
  function sentBeforeMount(ref: string): void {
    expect(fx.chat.prefetch).toHaveBeenCalledExactlyOnceWith(ref, expect.any(AbortSignal));
    expect(fx.chat.refresh.mock.calls[0]?.[1], "the refresh did not join the started page").toBe(
      fx.chat.prefetch.mock.calls[0]?.[1],
    );
    const sent = fx.chat.prefetch.mock.invocationCallOrder[0] ?? Infinity;
    const mounted = fx.chat.show.mock.invocationCallOrder[0] ?? -Infinity;
    expect(sent, "the page request waited for the view to mount").toBeLessThan(mounted);
  }

  it("on a reader's switch, after the gesture's frame", async () => {
    const ids = await strip("a", "b");
    fx.chat.page("b").resolve();

    clickRow(ids["b"] ?? "");
    expect(fx.chat.prefetch, "a superseded target's page left in the click").not.toHaveBeenCalled();
    await activationRan();

    sentBeforeMount("b");
  });

  it("on a close that hands the view to its successor", async () => {
    const ids = await strip("a", "b");
    fx.chat.page("b").resolve();

    void closeTab(ids["a"] ?? "");
    await activationRan();

    sentBeforeMount("b");
  });

  it("not for a parked chat whose window is fresh", async () => {
    observeStamp({ kind: "chat", ref: "b", version: "1" });
    const ids = await strip("a", "b");
    resident.add("b");

    clickRow(ids["b"] ?? "");
    await activationRan();

    expect(fx.chat.show).toHaveBeenCalledWith("b");
    expect(fx.chat.prefetch).not.toHaveBeenCalled();
  });
});
