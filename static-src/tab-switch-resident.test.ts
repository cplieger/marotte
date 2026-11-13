// A switch to a chat that can show its own turns at once (a parked view, or the mounted chat coming
// back from another tab kind) raises no cover: the outgoing view stays until the activation shows
// the chat's real content. A chat with nothing resident is held under the cover, which shows only
// once its show window passes. Real tabs.ts, view-swap.ts and transcript-cover.ts against the fake
// tab server, the chat opener and the transcript owner faked.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
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
vi.mock("./settings-tabs.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  refreshSettingsPanel: vi.fn(),
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
  activateRestoredTab,
  openTab,
  paintProvisionalTabs,
  tabIdFor,
  getActiveTabId,
  closeTab,
} from "./tabs.js";
import { markBootDone, _resetForTest as resetViewSwap } from "./view-swap.js";
import { registerTranscriptOwner, releaseCover, swapTranscript } from "./transcript-cover.js";
import { loadCSS } from "./__test-helpers__/css-rules.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import { TAB_SECTIONS_HTML } from "./__test-helpers__/tab-sections-markup.js";
import {
  activationRan,
  clickRow,
  coverUp,
  frame,
  pointers,
  resident,
  residentViews,
  transcriptOwnerFake,
} from "./__test-helpers__/tab-switch-mocks.js";

installSwitchSuite();

const style = document.createElement("style");
style.textContent = loadCSS("40-a11y.css");
document.head.appendChild(style);

/** The cover's show window. */
const DELAY_MS = 250;

/** Past the cover's show window. */
const PAST_DELAY_MS = 320;

/** The shell's elements by id, kept because a hidden view is parked off the page (dom.ts `park`). */
const mounted = new Map<string, HTMLElement>();

/** The chat view holding a scrollable transcript and the composer, and Settings, hidden. */
function mountViews(): void {
  document.body.innerHTML =
    `<aside id="sidebar">${TAB_SECTIONS_HTML}</aside>` +
    '<div id="chat-view" data-tab-view style="display:flex;flex-direction:column;height:400px">' +
    '<div id="messages-wrap-outer" style="position:relative;flex:1;min-height:0">' +
    '<div id="messages-wrap" style="height:100%;overflow:auto">' +
    '<div id="messages"><div style="height:3000px"></div></div>' +
    "</div></div>" +
    '<textarea id="prompt-input"></textarea>' +
    "</div>" +
    '<div id="settings-view" class="hidden" data-tab-view><p>settings</p></div>';
  mounted.clear();
  for (const el of document.querySelectorAll<HTMLElement>("[id]")) {
    mounted.set(el.id, el);
  }
}

function view(id: string): HTMLElement {
  const el = mounted.get(id);
  if (el === undefined) {
    throw new Error(`no #${id}`);
  }
  return el;
}

function rendered(id: string): boolean {
  return view(id).getClientRects().length > 0;
}

/** The cover is on screen and not lifting. */
function coverShown(): boolean {
  const c = document.getElementById("transcript-cover");
  return c !== null && !c.hidden && c.getAttribute("data-state") !== "released";
}

async function pastShowWindow(): Promise<void> {
  await new Promise((r) => setTimeout(r, PAST_DELAY_MS));
}

/** Make `ref` a parked chat: its view and its turns resident. */
function park(ref: string): void {
  resident.add(ref);
  residentViews.add(ref);
}

beforeEach(() => {
  mountViews();
  markBootDone();
});

afterEach(() => {
  resetViewSwap();
});

describe("a switch to a parked chat", () => {
  it("raises no cover and keeps the outgoing chat on screen until the activation shows the target", async () => {
    const ids = await strip("a", "b");
    park("b");
    let busyAtShow: boolean | undefined;
    fx.chat.show.mockImplementation((id: string) => {
      busyAtShow = coverUp();
      pointers.painted = id;
    });

    clickRow(ids["b"] ?? "");

    expect(getActiveTabId()).toBe(ids["b"]);
    expect(coverUp(), "the switch to a parked chat held the cover").toBe(false);
    expect(rendered("messages-wrap"), "the outgoing transcript left the screen").toBe(true);
    await activationRan();
    expect(fx.chat.show).toHaveBeenCalledWith("b");
    expect(busyAtShow, "the parked chat's paint ran under a cover").toBe(false);
    await pastShowWindow();
    expect(coverShown(), "a skeleton showed over the parked chat's turns").toBe(false);
  });

  it("drops a cover an earlier switch of the same burst held for a chat with nothing resident", async () => {
    const ids = await strip("a", "b", "c");
    park("b");

    clickRow(ids["c"] ?? "");
    expect(coverUp(), "the premise: the chat with nothing resident is held").toBe(true);
    clickRow(ids["b"] ?? "");
    expect(coverUp(), "the parked chat inherited the burst's cover").toBe(false);
    await activationRan();
    await pastShowWindow();
    expect(coverShown()).toBe(false);
  });

  it("keeps a slow switch's shown skeleton over a return to the parked chat until its paint lifts it", async () => {
    const ids = await strip("a", "b");
    const page = fx.chat.page("b");
    clickRow(ids["b"] ?? "");
    await activationRan();
    await pastShowWindow();
    expect(coverShown(), "the premise: the slow switch's skeleton shows").toBe(true);
    let shownAtPaint: boolean | undefined;
    fx.chat.show.mockImplementation((id: string) => {
      shownAtPaint = coverShown();
      pointers.painted = id;
      releaseCover(id);
    });

    clickRow(ids["a"] ?? "");

    expect(coverShown(), "going back lifted the skeleton over the abandoned chat's view").toBe(
      true,
    );
    await activationRan();
    expect(fx.chat.show).toHaveBeenCalledWith("a");
    expect(shownAtPaint, "the skeleton lifted before the parked chat painted its turns").toBe(true);
    expect(coverShown(), "the parked chat's paint left the skeleton up").toBe(false);
    page.resolve();
  });

  it("drops the park reads its gesture took when a later gesture supersedes it", async () => {
    const log: string[] = [];
    registerTranscriptOwner({
      ...transcriptOwnerFake(),
      take: () => log.push("take"),
      drop: () => log.push("drop"),
    });
    const ids = await strip("a", "b");
    park("b");
    log.length = 0;

    clickRow(ids["b"] ?? "");
    clickRow(ids["a"] ?? "");

    expect(log, "the superseded gesture's reads outlived it").toEqual(["take", "drop"]);
  });

  it("re-reads the outgoing transcript at the activation's start, before the target is shown", async () => {
    const log: string[] = [];
    registerTranscriptOwner({
      ...transcriptOwnerFake(),
      take: () => log.push(`take at ${String(view("messages-wrap").scrollTop)}`),
      drop: () => log.push("drop"),
    });
    fx.chat.show.mockImplementation((id: string) => {
      log.push(`show ${id}`);
      pointers.painted = id;
    });
    const ids = await strip("a", "b");
    park("b");
    view("messages-wrap").scrollTop = 100;
    log.length = 0;

    clickRow(ids["b"] ?? "");
    // The uncovered transcript stays in reach for the frame between the halves.
    view("messages-wrap").scrollTop = 200;
    await activationRan();

    expect(log.slice(-2), "the park read a position the reader had left").toEqual([
      "take at 200",
      "show b",
    ]);
  });
});

describe("a switch to a chat with nothing resident", () => {
  it("holds the cover at once but shows it only after the window, while the page is out", async () => {
    const ids = await strip("a", "b");
    const page = fx.chat.page("b");

    clickRow(ids["b"] ?? "");

    expect(coverUp(), "the switching transcript is not marked busy").toBe(true);
    expect(coverShown(), "the cover showed in the switch's own frame").toBe(false);
    await activationRan();
    expect(coverShown(), "the cover showed before the window passed").toBe(false);
    await pastShowWindow();
    expect(coverShown(), "a chat still loading after the window shows no skeleton").toBe(true);
    page.resolve();
  });
});

describe("a return to the mounted chat from Settings", () => {
  /** Chats `refs` open with the first active and painted, scrolled to 600, then Settings shown. */
  async function onSettings(...refs: string[]): Promise<Record<string, string>> {
    const ids = await strip(...refs);
    view("messages-wrap").scrollTop = 600;
    await openTab({ kind: "settings", ref: "" });
    await activationRan();
    expect(rendered("settings-view")).toBe(true);
    vi.clearAllMocks();
    return { ...ids, settings: tabIdFor("settings", "") };
  }

  it("keeps Settings up in the click and shows the chat's own transcript at the activation, no cover", async () => {
    const ids = await onSettings("a");

    clickRow(ids["a"] ?? "");

    expect(getActiveTabId()).toBe(ids["a"]);
    expect(coverUp(), "the mounted chat was covered").toBe(false);
    expect(rendered("settings-view"), "the click emptied the view area").toBe(true);
    await frame();
    expect(rendered("settings-view"), "the gesture's frame emptied the view area").toBe(true);

    await activationRan();
    expect(rendered("chat-view")).toBe(true);
    expect(rendered("messages-wrap")).toBe(true);
    expect(rendered("settings-view")).toBe(false);
    expect(
      view("chat-view").getAnimations().length,
      "the chat view showed with no entry fade",
    ).toBeGreaterThan(0);
    expect(view("messages-wrap").scrollTop, "the reader's place was lost").toBe(600);
    expect(fx.chat.show, "the mounted chat was shown again").not.toHaveBeenCalled();
    await pastShowWindow();
    expect(coverShown()).toBe(false);
  });

  it("does the same when closing Settings hands the view back to the chat", async () => {
    const ids = await onSettings("a");

    void closeTab(ids["settings"] ?? "");

    expect(getActiveTabId()).toBe(ids["a"]);
    expect(coverUp()).toBe(false);
    await activationRan();
    expect(rendered("messages-wrap")).toBe(true);
    expect(view("messages-wrap").scrollTop).toBe(600);
    await pastShowWindow();
    expect(coverShown()).toBe(false);
  });
});

describe("a switch from Settings to a chat with nothing resident", () => {
  it("keeps Settings on screen through the window, then shows the chat under the shown cover", async () => {
    const ids = await strip("a", "b");
    await openTab({ kind: "settings", ref: "" });
    await activationRan();
    const page = fx.chat.page("b");

    const t0 = performance.now();
    clickRow(ids["b"] ?? "");
    await activationRan();
    expect(rendered("settings-view"), "the activation emptied the view area").toBe(true);
    await new Promise((r) => setTimeout(r, 150));
    // Read only while the window is surely still open: a late frame on a loaded runner can end it.
    if (performance.now() - t0 < DELAY_MS) {
      expect(rendered("settings-view"), "the view area emptied inside the window").toBe(true);
      expect(coverShown()).toBe(false);
    }

    await new Promise((r) => setTimeout(r, Math.max(0, t0 + PAST_DELAY_MS - performance.now())));
    expect(coverShown(), "the chat still loading shows no skeleton").toBe(true);
    expect(rendered("chat-view")).toBe(true);
    expect(rendered("settings-view")).toBe(false);
    page.resolve();
  });

  it("swaps straight from Settings to the chat when its page answers inside the window", async () => {
    const ids = await strip("a", "b");
    await openTab({ kind: "settings", ref: "" });
    await activationRan();
    const page = fx.chat.page("b");

    clickRow(ids["b"] ?? "");
    await activationRan();
    // The paint of the answer, which the fake opener does not run.
    page.resolve();
    resident.add("b");
    swapTranscript();
    releaseCover("b");

    expect(rendered("chat-view"), "the answer left Settings up").toBe(true);
    expect(rendered("messages-wrap")).toBe(true);
    expect(rendered("settings-view")).toBe(false);
    await pastShowWindow();
    expect(coverShown(), "a skeleton showed over the answered chat").toBe(false);
  });
});

describe("the boot's restore of a chat with nothing resident", () => {
  it("shows the skeleton at once when the page has already waited past the window", async () => {
    expect(performance.now(), "the page is not past the cover's window").toBeGreaterThan(
      PAST_DELAY_MS,
    );
    paintProvisionalTabs([
      { id: "tab-a", kind: "chat", ref: "a", parent: "", pinned: false, owns: false },
    ]);
    const page = fx.chat.page("a");

    activateRestoredTab();
    expect(getActiveTabId()).toBe("tab-a");
    expect(coverUp(), "the restore held no cover over a chat with nothing resident").toBe(true);
    expect(coverShown(), "a boot already past the window waited for a timer").toBe(true);
    expect(rendered("chat-view"), "the cover stands over no chat view").toBe(true);
    page.resolve();
  });
});
