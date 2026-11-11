// A burst of switches shares one cover and paints only the last tab; an older switch is
// superseded before its view is shown, or aborted mid-load, and never reverts or toasts.
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

import { ActionError } from "@cplieger/reactive/actions";
import { getActiveTabId } from "./tabs.js";
import { pushRoute } from "./router.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import { until } from "./__test-helpers__/until.js";
import {
  activationRan,
  clickRow,
  cover,
  coverUp,
  held,
} from "./__test-helpers__/tab-switch-mocks.js";

installSwitchSuite();

function callsFor(spy: { mock: { calls: unknown[][] } }, id: string): number {
  return spy.mock.calls.filter((c) => c[0] === id).length;
}

describe("A to B to C to D paints only D", () => {
  it("shows only the last of four clicks in one task, under one cover", async () => {
    const ids = await strip("a", "b", "c", "d");

    clickRow(ids["b"] ?? "");
    const first = cover();
    expect(first, "B's click put up no cover").not.toBeNull();
    clickRow(ids["c"] ?? "");
    clickRow(ids["d"] ?? "");
    await activationRan();

    for (const id of ["b", "c"]) {
      expect(callsFor(fx.chat.show, id), `${id} was shown`).toBe(0);
      expect(callsFor(fx.chat.refresh, id), `${id} was loaded`).toBe(0);
    }
    expect(callsFor(fx.chat.show, "d")).toBe(1);
    expect(callsFor(fx.chat.refresh, "d")).toBe(1);
    expect(cover(), "the burst re-created the cover").toBe(first);
    expect(coverUp()).toBe(true);
    expect(vi.mocked(pushRoute).mock.calls).toEqual([[{ kind: "chat", id: "d" }]]);
    expect(getActiveTabId()).toBe(ids["d"]);
    expect(fx.notices).toEqual([]);
  });

  it("lifts B's cover when the reader comes back to the painted A before B showed", async () => {
    const ids = await strip("a", "b");

    clickRow(ids["b"] ?? "");
    expect(coverUp()).toBe(true);
    clickRow(ids["a"] ?? "");
    expect(coverUp(), "A's transcript is still mounted, so nothing stands in for it").toBe(false);
    await activationRan();

    expect(callsFor(fx.chat.show, "b")).toBe(0);
    expect(coverUp()).toBe(false);
    expect(getActiveTabId()).toBe(ids["a"]);
    expect(fx.notices).toEqual([]);
  });

  it("aborts a load already in flight when the next click lands a frame later", async () => {
    const ids = await strip("a", "b", "c");
    const pageB = fx.chat.page("b");

    clickRow(ids["b"] ?? "");
    await activationRan();
    expect(callsFor(fx.chat.show, "b")).toBe(1);
    expect(pageB.signal?.aborted).toBe(false);
    clickRow(ids["c"] ?? "");
    expect(pageB.signal?.aborted, "C's click left B's load running").toBe(true);
    await activationRan();
    pageB.reject(new ActionError("aborted", { code: "aborted" }));
    await activationRan();

    expect(callsFor(fx.chat.show, "c")).toBe(1);
    expect(getActiveTabId()).toBe(ids["c"]);
    expect(fx.notices).toEqual([]);
  });

  it("retries a 503 against the same activation and shows B with no toast", async () => {
    const ids = await strip("a", "b");
    const first = held();
    first.reject(new ActionError("load failed", { status: 503 }));
    fx.chat.page("b", first);
    const second = fx.chat.page("b");
    second.resolve();

    clickRow(ids["b"] ?? "");
    await until(() => fx.chat.refresh.mock.calls.length === 2, "B's second attempt", {
      timeout: 2000,
    });
    await activationRan();

    expect(callsFor(fx.chat.show, "b"), "the retry re-ran the activation").toBe(1);
    expect(callsFor(fx.chat.select, "b"), "the retry re-ran the gesture").toBe(1);
    expect(second.signal?.aborted).toBe(false);
    expect(getActiveTabId()).toBe(ids["b"]);
    expect(fx.notices).toEqual([]);
  });
});
