// `tabs.switch`'s own retry loop: a newer switch landing during a back-off supersedes the older
// one, and the time bound firing during a back-off reverts only the switch the reader is still on.
// Real tabs.ts and its real `settle`, through the dispatched action.
import { describe, it, expect, vi, afterEach } from "vitest";
import type * as TabsDrag from "./tabs-drag.js";

vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
vi.mock("./icons.js", () =>
  import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.iconsMock()),
);
vi.mock("./device-view.js", () =>
  import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.deviceViewMock()),
);
vi.mock("./dom.js", () =>
  import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.domMock()),
);
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
vi.mock("./store.js", async () => ({
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
  ...(await import("./__test-helpers__/tab-switch-mocks.js")).storePointerOverrides(),
}));
vi.mock("./run-store.js", () => ({ runLabelOf: vi.fn(() => "") }));
vi.mock("./composer-state.js", () =>
  import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.composerStateMock()),
);
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => ({
    apiBlobOrError: vi.fn(),
    apiGetTyped: m.tabListRead(),
    apiGet: vi.fn(() => Promise.resolve(null)),
  })),
);

import { ActionError } from "@cplieger/ui/actions";
import { getActiveTabId } from "./tabs.js";
import * as tabsModule from "./tabs.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import { until } from "./__test-helpers__/until.js";
import { activationRan, clickRow, held, pointers } from "./__test-helpers__/tab-switch-mocks.js";

installSwitchSuite();

/** The bound on one switch, shortened for a case; the seam restores the default with `null`. */
function bound(ms: number | null): void {
  const seam = (tabsModule as unknown as { setSwitchBoundForTest?: (ms: number | null) => void })
    .setSwitchBoundForTest;
  expect(typeof seam, "tabs.setSwitchBoundForTest").toBe("function");
  seam?.(ms);
}

afterEach(() => {
  const seam = (tabsModule as unknown as { setSwitchBoundForTest?: (ms: number | null) => void })
    .setSwitchBoundForTest;
  seam?.(null);
});

function refuseOnce(chatID: string): void {
  const h = held();
  h.reject(new ActionError("load failed", { status: 503 }));
  fx.chat.page(chatID, h);
}

/** Longer than the first back-off (300 ms), so a second attempt would have run. */
const PAST_BACKOFF_MS = 450;

describe("the switch's back-off", () => {
  it("answers superseded when a newer switch lands during the back-off", async () => {
    const ids = await strip("a", "b", "c");
    refuseOnce("b");

    clickRow(ids["b"] ?? "");
    await activationRan();
    expect(fx.chat.refresh).toHaveBeenCalledTimes(1);
    clickRow(ids["c"] ?? "");
    await new Promise((r) => setTimeout(r, PAST_BACKOFF_MS));

    expect(getActiveTabId()).toBe(ids["c"]);
    expect(
      fx.chat.refresh.mock.calls.filter(([id]) => id === "b"),
      "B's second attempt ran after C was clicked",
    ).toHaveLength(1);
    expect(fx.chat.select.mock.calls.filter(([id]) => id === "b")).toHaveLength(1);
    expect(fx.notices).toEqual([]);
    expect(pointers.active).toBe("c");
  });

  it("neither reverts nor toasts when the bound fires during the back-off of a switch already left", async () => {
    bound(150);
    const ids = await strip("a", "b", "c");
    refuseOnce("b");

    clickRow(ids["b"] ?? "");
    await activationRan();
    clickRow(ids["c"] ?? "");
    await new Promise((r) => setTimeout(r, PAST_BACKOFF_MS));

    expect(getActiveTabId(), "a late timeout reverted over C").toBe(ids["c"]);
    expect(fx.notices).toEqual([]);
  });

  it("reverts once with the timeout sentence when the bound fires during the live switch's back-off", async () => {
    bound(150);
    const ids = await strip("a", "b");
    refuseOnce("b");

    clickRow(ids["b"] ?? "");
    await until(() => fx.notices.length > 0, "the timeout notice", { timeout: 3000 });
    await activationRan();

    expect(getActiveTabId()).toBe(ids["a"]);
    expect(fx.notices.map((n) => n.message)).toEqual([
      "This conversation did not load in time, so you are back on the previous tab.",
    ]);
    expect(fx.notices[0]?.retry).toBeDefined();
  });
});
