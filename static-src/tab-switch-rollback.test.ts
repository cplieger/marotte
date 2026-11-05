// A refused switch reverts to the previous tab with one toast that says what failed and offers
// Retry where one can help. Real tabs.ts against the fake tab server, the chat opener faked.
import { describe, it, expect, vi } from "vitest";
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
import { tabIdFor, getActiveTabId, hasTab, activateTab } from "./tabs.js";
import { removeChat } from "./store.js";
import { tabServer, settleTabs } from "./__test-helpers__/tabs-server.js";
import { until } from "./__test-helpers__/until.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import {
  activationRan,
  clickRow,
  coverUp,
  held,
  pointers,
  rowEl,
} from "./__test-helpers__/tab-switch-mocks.js";

installSwitchSuite();

/** Queue `times` refusals of `chatID`'s page, each answering `err`. */
function refuse(chatID: string, err: ActionError, times: number): void {
  for (let i = 0; i < times; i++) {
    const h = held();
    h.reject(err);
    fx.chat.page(chatID, h);
  }
}

const server503 = (): ActionError => new ActionError("load failed", { status: 503 });
const network = (): ActionError => new ActionError("network", { status: 0, code: "network" });

/** The switch's verdict has been reached: one notice raised. Back-offs total 900 ms. */
async function refused(): Promise<void> {
  await until(() => fx.notices.length > 0, "the switch's refusal notice", { timeout: 5000 });
  await activationRan();
}

describe("a refused switch reverts with the toast", () => {
  it("returns to the previous tab after a 503 on every attempt, with Retry", async () => {
    const ids = await strip("a", "b");
    const a = ids["a"] ?? "";
    const b = ids["b"] ?? "";
    refuse("b", server503(), 3);
    rowEl(b).focus();

    clickRow(b);
    await refused();

    expect(fx.chat.refresh, "the two retries ran").toHaveBeenCalledTimes(3);
    expect(getActiveTabId()).toBe(a);
    expect(rowEl(b).classList.contains("active")).toBe(false);
    expect(pointers.active).toBe("a");
    expect(pointers.painted).toBe("a");
    expect(document.activeElement, "focus followed the revert").toBe(rowEl(a));
    expect(fx.notices).toHaveLength(1);
    const notice = fx.notices[0];
    expect(notice?.level).toBe("error");
    expect(notice?.subject).toBe("b");
    expect(notice?.message).toBe(
      "Could not load this conversation, so you are back on the previous tab.",
    );
    expect(notice?.retry).toBeDefined();

    fx.chat.page("b");
    notice?.retry?.onClick();
    await activationRan();
    expect(getActiveTabId(), "Retry switched to B again").toBe(b);
    expect(fx.chat.refresh).toHaveBeenCalledTimes(4);
  });

  it("words a network loss and offers Retry", async () => {
    const ids = await strip("a", "b");
    refuse("b", network(), 3);

    clickRow(ids["b"] ?? "");
    await refused();

    expect(getActiveTabId()).toBe(ids["a"]);
    expect(fx.notices.map((n) => n.message)).toEqual([
      "Could not reach marotte, so you are back on the previous tab.",
    ]);
    expect(fx.notices[0]?.retry).toBeDefined();
  });

  it("closes a gone chat's tab and drops its row, with no Retry", async () => {
    const ids = await strip("a", "b");
    refuse("b", new ActionError("gone", { status: 404 }), 1);

    clickRow(ids["b"] ?? "");
    await refused();
    await settleTabs();

    expect(fx.chat.refresh, "a 404 was retried").toHaveBeenCalledTimes(1);
    expect(getActiveTabId()).toBe(ids["a"]);
    expect(fx.notices.map((n) => n.message)).toEqual(["This conversation no longer exists."]);
    expect(fx.notices[0]?.retry).toBeUndefined();
    expect(vi.mocked(removeChat)).toHaveBeenCalledWith("b");
    expect(tabServer.sentOfType("close_tab")).toHaveLength(1);
    expect(hasTab("chat", "b")).toBe(false);
  });

  it("falls to the most recent open tab when another device closed the previous one", async () => {
    const ids = await strip("a", "b", "c");
    activateTab(ids["c"] ?? "");
    await activationRan();
    activateTab(ids["a"] ?? "");
    await activationRan();
    refuse("b", server503(), 3);

    clickRow(ids["b"] ?? "");
    tabServer.closeRemotely(ids["a"] ?? "");
    await settleTabs();
    await refused();

    expect(getActiveTabId()).toBe(ids["c"]);
    expect(hasTab("chat", "a"), "the revert reopened a tab another device closed").toBe(false);
    expect(
      tabServer.sentOfType("open_tab").filter((s) => JSON.stringify(s).includes('"a"')),
    ).toEqual([]);
  });

  it("stays on the only tab left, paints the in-view Retry, and Retry loads again", async () => {
    const ids = await strip("a", "b");
    const b = ids["b"] ?? "";
    refuse("b", server503(), 3);

    clickRow(b);
    tabServer.closeRemotely(ids["a"] ?? "");
    await settleTabs();
    await refused();

    expect(getActiveTabId()).toBe(b);
    expect(coverUp(), "the cover stayed over the failed tab").toBe(false);
    expect(fx.chat.loadFailed).toHaveBeenCalledWith("b");
    expect(fx.notices.map((n) => n.message)).toEqual(["Could not load this conversation."]);
    expect(fx.notices[0]?.retry).toBeDefined();

    const page = fx.chat.page("b");
    page.resolve();
    fx.chat.show.mockClear();
    fx.chat.refresh.mockClear();
    fx.notices[0]?.retry?.onClick();
    await activationRan();
    expect(getActiveTabId()).toBe(b);
    expect(fx.chat.show).toHaveBeenCalledWith("b");
    expect(fx.chat.refresh).toHaveBeenCalledTimes(1);
    expect(page.signal?.aborted).toBe(false);
  });
});
