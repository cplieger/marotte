// A run-end toast's Open button against the real toast DOM: it reaches the chat that launched the
// run, else the run's own tab while the server holds the run, and a click on the toast itself only
// dismisses it.

import { describe, it, expect, beforeAll, beforeEach, afterAll, afterEach, vi } from "vitest";
import { userEvent } from "vitest/browser";
import { http, HttpResponse } from "msw";
import { setupWorker } from "msw/browser";
import type * as RunStore from "../run-store.js";
import type * as Tabs from "../tabs.js";
import type * as RunView from "../run-view.js";
import type * as RunDots from "../run-dots.js";
import type * as DecisionDock from "../decision-dock.js";
import type * as Notify from "../notify.js";
import type * as Transport from "../transport.js";

const { mockActivateTab, mockOpenTab, mockOpenRunView, openTabs, active, realTabs } = vi.hoisted(
  () => ({
    mockActivateTab: vi.fn(),
    mockOpenTab: vi.fn(async (_args: unknown): Promise<string> => "opened"),
    mockOpenRunView: vi.fn((..._args: unknown[]) => Promise.resolve()),
    /** `<kind>:<ref>` to tab id: the strip this page holds. */
    openTabs: new Map<string, string>(),
    active: { id: "" },
    /** The real `openTab`, for a case that drives the open through to the fake tab server. */
    realTabs: { openTab: null as typeof Tabs.openTab | null },
  }),
);
vi.mock("../tabs.js", async (importOriginal) => {
  const real = await importOriginal<typeof Tabs>();
  realTabs.openTab = real.openTab;
  return {
    ...real,
    ...(await import("../__test-helpers__/tabs-mock.js")).tabsMock(),
    activateTab: mockActivateTab,
    openTab: mockOpenTab,
    getActiveTabId: () => active.id,
    tabIdFor: (kind: string, ref = "") => openTabs.get(`${kind}:${ref}`) ?? "",
  };
});
vi.mock("../transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  ...(await import("../__test-helpers__/tabs-server.js")).tabTransportMock(),
}));
vi.mock("../run-view.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunView>()),
  openRunView: mockOpenRunView,
}));
// The store's network reads are cut; the launching-chat record (`noteRunChat`, `runChatID`) is real.
vi.mock("../run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  invalidateRun: vi.fn(),
  invalidateRunControls: vi.fn(),
}));
vi.mock("../run-dots.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunDots>()),
  trackRun: vi.fn(),
}));
vi.mock("../decision-dock.js", async (importOriginal) => ({
  ...(await importOriginal<typeof DecisionDock>()),
  dropRunRequests: vi.fn(),
  dropTurnDecisions: vi.fn(),
}));
vi.mock("../notify.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Notify>()),
  closeRunAsksExcept: vi.fn(() => Promise.resolve()),
}));

import "./run.js";
import { dispatch } from "../bus.js";
import { defaultUsage, setSessions } from "../store.js";
import { _resetForTest } from "../toast.js";
import type { Session } from "../types.js";
import { resetActionFramework } from "../actions/__test-helpers__/action-test-setup.js";
import { tabServer } from "../__test-helpers__/tabs-server.js";

function session(id: string, name: string): Session {
  return {
    id,
    name,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function finish(workflowID: string, status: string, chatID: string): void {
  dispatch({
    type: "run_finished",
    chat_id: chatID,
    payload: { workflow_id: workflowID, status, name: "publish" },
  });
}

function toastNode(): HTMLElement {
  const node = document.querySelector<HTMLElement>(".uip-toast");
  expect(node).not.toBeNull();
  return node as HTMLElement;
}

function openButton(): HTMLButtonElement {
  const btn = toastNode().querySelector<HTMLButtonElement>("button.uip-toast-retry");
  expect(btn).not.toBeNull();
  return btn as HTMLButtonElement;
}

const OPEN_REFUSED = "Could not open that tab";

function toastTexts(): string[] {
  return [...document.querySelectorAll<HTMLElement>(".uip-toast-msg")].map((n) => n.textContent);
}

function navigations(): number {
  return (
    mockActivateTab.mock.calls.length +
    mockOpenTab.mock.calls.length +
    mockOpenRunView.mock.calls.length
  );
}

const worker = setupWorker();

/** Serve `GET /api/runs/<workflowID>` as the run present (200) or gone (404); returns how many reads it answered. */
function runOnServer(workflowID: string, present: boolean): () => number {
  let reads = 0;
  worker.use(
    http.get(`/api/runs/${workflowID}`, () => {
      reads++;
      return present
        ? HttpResponse.json({ state: {} })
        : HttpResponse.json({ error: "workflow not found" }, { status: 404 });
    }),
  );
  return () => reads;
}

beforeAll(async () => {
  await worker.start({ quiet: true });
});

afterAll(() => {
  worker.stop();
});

beforeEach(() => {
  _resetForTest();
  tabServer.reset();
  resetActionFramework();
  vi.clearAllMocks();
  mockOpenTab.mockImplementation(async () => "opened");
  openTabs.clear();
  openTabs.set("chat:c-other", "t-other");
  active.id = "t-other";
  setSessions([session("c-parent", "Fix the parser"), session("c-other", "Write the docs")]);
});

afterEach(() => {
  worker.resetHandlers();
  _resetForTest();
});

describe("a run-end toast opens the chat that launched the run", () => {
  for (const status of ["completed", "failed", "aborted", "cancelled"]) {
    it(`${status}: focuses the parent chat's tab`, async () => {
      openTabs.set("chat:c-parent", "t-parent");
      finish(`wf_tab_${status}`, status, "c-parent");
      const btn = openButton();
      expect(btn.getAttribute("aria-label")).toBe("Open Fix the parser");
      btn.click();
      await vi.waitFor(() => {
        expect(mockActivateTab).toHaveBeenCalledWith("t-parent");
      });
      expect(mockOpenTab).not.toHaveBeenCalled();
      expect(mockOpenRunView).not.toHaveBeenCalled();
    });

    it(`${status}: opens a tab for the parent chat when it has none`, async () => {
      finish(`wf_open_${status}`, status, "c-parent");
      openButton().click();
      await vi.waitFor(() => {
        expect(mockOpenTab).toHaveBeenCalledWith({
          kind: "chat",
          ref: "c-parent",
          name: "Fix the parser",
          quietNotFound: true,
        });
      });
      expect(mockActivateTab).not.toHaveBeenCalled();
      expect(mockOpenRunView).not.toHaveBeenCalled();
    });
  }

  it("is a keyboard-reachable button the Enter key activates", async () => {
    openTabs.set("chat:c-parent", "t-parent");
    finish("wf_keys", "completed", "c-parent");
    const btn = openButton();
    btn.focus();
    expect(document.activeElement).toBe(btn);
    await userEvent.keyboard("{Enter}");
    await vi.waitFor(() => {
      expect(mockActivateTab).toHaveBeenCalledWith("t-parent");
    });
  });

  it("names the chat a run learned from an earlier frame when the end frame carries none", async () => {
    dispatch({
      type: "run_progress",
      chat_id: "c-parent",
      payload: { workflow_id: "wf_learned", kind: "node_start" },
    });
    finish("wf_learned", "completed", "");
    expect(openButton().getAttribute("aria-label")).toBe("Open Fix the parser");
  });

  it("offers no jump onto the parent chat's tab when it is the one on screen", () => {
    openTabs.set("chat:c-parent", "t-parent");
    active.id = "t-parent";
    finish("wf_on_screen", "completed", "c-parent");
    expect(toastNode().querySelector("button")).toBeNull();
  });
});

describe("a run-end toast for a parentless run opens the run's tab", () => {
  it("opens a parentless run's own tab", async () => {
    finish("wf_parentless", "completed", "");
    const btn = openButton();
    expect(btn.getAttribute("aria-label")).toBe("Open publish");
    btn.click();
    await vi.waitFor(() => {
      expect(mockOpenRunView).toHaveBeenCalledWith("wf_parentless", "publish");
    });
    expect(mockOpenTab).not.toHaveBeenCalled();
  });

  it("offers no jump onto a parentless run's tab when it is the one on screen", () => {
    openTabs.set("run:wf_watched", "t-run");
    active.id = "t-run";
    finish("wf_watched", "completed", "");
    expect(toastNode().querySelector("button")).toBeNull();
  });
});

describe("a run-end toast whose parent chat is gone opens the run's tab while the run exists", () => {
  it("opens the run's tab once when the parent was deleted before the run ended", async () => {
    runOnServer("wf_gone", true);
    finish("wf_gone", "failed", "c-deleted");
    const btn = openButton();
    expect(btn.getAttribute("aria-label")).toBe("Open publish");
    btn.click();
    await vi.waitFor(() => {
      expect(mockOpenRunView).toHaveBeenCalledWith("wf_gone", "publish");
    });
    expect(mockOpenRunView).toHaveBeenCalledTimes(1);
    expect(mockActivateTab).not.toHaveBeenCalled();
    expect(mockOpenTab).not.toHaveBeenCalled();
  });

  it("opens the run's tab when the parent is deleted while the toast is up", async () => {
    runOnServer("wf_gone_later", true);
    finish("wf_gone_later", "aborted", "c-parent");
    setSessions([session("c-other", "Write the docs")]);
    openButton().click();
    await vi.waitFor(() => {
      expect(mockOpenRunView).toHaveBeenCalledWith("wf_gone_later", "publish");
    });
    expect(mockOpenRunView).toHaveBeenCalledTimes(1);
    expect(mockActivateTab).not.toHaveBeenCalled();
    expect(mockOpenTab).not.toHaveBeenCalled();
  });

  it("opens nothing and dismisses when the server no longer holds the run", async () => {
    const asked = runOnServer("wf_gone_both", false);
    finish("wf_gone_both", "cancelled", "c-deleted");
    const node = toastNode();
    openButton().click();
    await vi.waitFor(() => {
      expect(asked()).toBe(1);
      expect(node.isConnected && !node.classList.contains("is-leaving")).toBe(false);
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(navigations()).toBe(0);
  });

  it("opens the run's tab when the server answers 404 to the parent chat's open", async () => {
    const realOpenTab = realTabs.openTab;
    expect(realOpenTab).not.toBeNull();
    mockOpenTab.mockImplementation(async (args) =>
      (realOpenTab as typeof Tabs.openTab)(args as Parameters<typeof Tabs.openTab>[0]),
    );
    tabServer.failNext("open_tab", 404, "no such chat");
    runOnServer("wf_404", true);
    finish("wf_404", "completed", "c-parent");
    openButton().click();
    await vi.waitFor(() => {
      expect(mockOpenRunView).toHaveBeenCalledWith("wf_404", "publish");
    });
    expect(mockOpenRunView).toHaveBeenCalledTimes(1);
    expect(tabServer.sentOfType("open_tab")).toHaveLength(1);
    expect(mockActivateTab).not.toHaveBeenCalled();
    expect(toastTexts().some((t) => t.includes(OPEN_REFUSED))).toBe(false);
  });

  it("opens nothing when the parent chat's open answers 404 and the run is gone too", async () => {
    const realOpenTab = realTabs.openTab;
    expect(realOpenTab).not.toBeNull();
    mockOpenTab.mockImplementation(async (args) =>
      (realOpenTab as typeof Tabs.openTab)(args as Parameters<typeof Tabs.openTab>[0]),
    );
    tabServer.failNext("open_tab", 404, "no such chat");
    const asked = runOnServer("wf_404_gone", false);
    finish("wf_404_gone", "completed", "c-parent");
    openButton().click();
    await vi.waitFor(() => {
      expect(asked()).toBe(1);
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(mockOpenRunView).not.toHaveBeenCalled();
    expect(mockActivateTab).not.toHaveBeenCalled();
    expect(toastTexts().some((t) => t.includes(OPEN_REFUSED))).toBe(false);
  });

  it("reports any other refusal of the parent chat's open and opens nothing else", async () => {
    const realOpenTab = realTabs.openTab;
    expect(realOpenTab).not.toBeNull();
    mockOpenTab.mockImplementation(async (args) =>
      (realOpenTab as typeof Tabs.openTab)(args as Parameters<typeof Tabs.openTab>[0]),
    );
    tabServer.failNext("open_tab", 409, "too many tabs");
    const asked = runOnServer("wf_409", true);
    finish("wf_409", "completed", "c-parent");
    openButton().click();
    await vi.waitFor(() => {
      expect(toastTexts()).toContain(
        "Close a tab first, because this workspace has too many open.",
      );
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(asked()).toBe(0);
    expect(mockOpenRunView).not.toHaveBeenCalled();
  });

  it("offers Retry when the parent chat's open keeps failing, and a 404 on retry is reported", async () => {
    const realOpenTab = realTabs.openTab;
    expect(realOpenTab).not.toBeNull();
    mockOpenTab.mockImplementation(async (args) =>
      (realOpenTab as typeof Tabs.openTab)(args as Parameters<typeof Tabs.openTab>[0]),
    );
    for (let i = 0; i < 3; i++) {
      tabServer.failNext("open_tab", 503, "busy");
    }
    const asked = runOnServer("wf_503", true);
    finish("wf_503", "completed", "c-parent");
    openButton().click();
    const liveRefusals = (): HTMLElement[] =>
      [...document.querySelectorAll<HTMLElement>(".uip-toast")].filter(
        (n) =>
          !n.classList.contains("is-leaving") &&
          (n.querySelector(".uip-toast-msg")?.textContent ?? "").includes(OPEN_REFUSED),
      );
    const first = await vi.waitFor(
      () => {
        const [node] = liveRefusals();
        expect(node).toBeDefined();
        return node as HTMLElement;
      },
      { timeout: 5000 },
    );
    expect(tabServer.sentOfType("open_tab")).toHaveLength(3);
    const retry = first.querySelector<HTMLButtonElement>("button.uip-toast-retry");
    expect(retry).not.toBeNull();

    tabServer.failNext("open_tab", 404, "no such chat");
    (retry as HTMLButtonElement).click();
    await vi.waitFor(() => {
      expect(tabServer.sentOfType("open_tab")).toHaveLength(4);
      expect(liveRefusals().some((n) => n !== first)).toBe(true);
    });
    expect(asked()).toBe(0);
    expect(mockOpenRunView).not.toHaveBeenCalled();
  });
});

describe("dismissing a run-end toast", () => {
  it("a click on the toast itself dismisses it without navigating", async () => {
    finish("wf_dismiss", "completed", "c-parent");
    openButton();
    const node = toastNode();
    node.click();
    await vi.waitFor(() => {
      expect(node.isConnected && !node.classList.contains("is-leaving")).toBe(false);
    });
    expect(navigations()).toBe(0);
  });

  it("Escape dismisses it without navigating", async () => {
    finish("wf_escape", "failed", "c-parent");
    const node = toastNode();
    openButton();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await vi.waitFor(() => {
      expect(node.isConnected && !node.classList.contains("is-leaving")).toBe(false);
    });
    expect(navigations()).toBe(0);
  });

  it("the Open button dismisses the toast as it navigates", async () => {
    finish("wf_both", "completed", "");
    const node = toastNode();
    openButton().click();
    await vi.waitFor(() => {
      expect(node.isConnected && !node.classList.contains("is-leaving")).toBe(false);
      expect(mockOpenRunView).toHaveBeenCalledTimes(1);
    });
  });
});
