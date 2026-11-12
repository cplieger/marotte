// A switch between view kinds (a chat and Settings). Toward a chat with nothing resident the click
// lays out no incoming transcript and never leaves the view area empty: the chat view shows under
// the held cover with its transcript out of layout, and the activation lays it out. A chat that can
// show its own turns is tab-switch-resident.test.ts's. Toward any other view the click shows that
// view, its entry fade starting there, and hides the chat view. Real tabs.ts and view-swap.ts
// against the fake tab server.
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

import { openTab, tabIdFor, getActiveTabId, closeTab } from "./tabs.js";
import { markBootDone, _resetForTest as resetViewSwap } from "./view-swap.js";
import { listTabs } from "./tabs-sync.js";
import { tabServer, settleTabs } from "./__test-helpers__/tabs-server.js";
import { loadCSS } from "./__test-helpers__/css-rules.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import {
  activationRan,
  clickRow,
  cover,
  coverUp,
  frame,
} from "./__test-helpers__/tab-switch-mocks.js";

installSwitchSuite();

const style = document.createElement("style");
style.textContent = loadCSS("40-a11y.css");
document.head.appendChild(style);

/** The shell with both view kinds: the chat view holds the transcript scroller, tall enough to
 *  scroll, and the composer; Settings is a sibling view, hidden. */
function mountViews(): void {
  document.body.innerHTML =
    '<aside id="sidebar"><div id="tab-list"></div></aside>' +
    '<div id="chat-view" data-tab-view style="display:flex;flex-direction:column;height:400px">' +
    '<div id="messages-wrap-outer" style="position:relative;flex:1;min-height:0">' +
    '<div id="messages-wrap" style="height:100%;overflow:auto">' +
    '<div id="messages"><div style="height:3000px"></div><button id="in-transcript">copy</button></div>' +
    "</div></div>" +
    '<textarea id="prompt-input"></textarea>' +
    "</div>" +
    '<div id="settings-view" class="hidden" data-tab-view><p>settings</p></div>';
  mounted.clear();
  for (const el of document.querySelectorAll<HTMLElement>("[id]")) {
    mounted.set(el.id, el);
  }
}

/** The shell's elements by id, kept because a hidden view is parked off the page (dom.ts `park`). */
const mounted = new Map<string, HTMLElement>();

function view(id: string): HTMLElement {
  const el = mounted.get(id);
  if (el === undefined) {
    throw new Error(`no #${id}`);
  }
  return el;
}

/** Laid out: the element has a box. */
function rendered(id: string): boolean {
  return view(id).getClientRects().length > 0;
}

/** The view's WAAPI entry fade, if one is running. */
function entryFade(id: string): Animation | undefined {
  return view(id)
    .getAnimations()
    .find((a) => a.playState === "running");
}

/** Chats `refs` open with the first active and painted, the transcript scrolled to 600, then
 *  Settings opened and shown. Answers the tab ids. */
async function onSettings(...refs: string[]): Promise<Record<string, string>> {
  const ids = await strip(...refs);
  view("messages-wrap").scrollTop = 600;
  await openTab({ kind: "settings", ref: "" });
  await activationRan();
  expect(getActiveTabId()).toBe(tabIdFor("settings", ""));
  expect(rendered("settings-view")).toBe(true);
  expect(rendered("chat-view")).toBe(false);
  vi.clearAllMocks();
  return { ...ids, settings: tabIdFor("settings", "") };
}

beforeEach(() => {
  mountViews();
  markBootDone();
});

afterEach(() => {
  resetViewSwap();
});

describe("a switch from Settings to a chat", () => {
  it("keeps Settings up for an unpainted chat until the cover shows, then shows the chat view with its transcript laid out", async () => {
    const ids = await onSettings("a", "b");
    const page = fx.chat.page("b");

    clickRow(ids["b"] ?? "");
    expect(rendered("settings-view"), "the click emptied the view area").toBe(true);
    await frame();
    expect(rendered("settings-view"), "the gesture's frame emptied the view area").toBe(true);
    await activationRan();
    expect(fx.chat.show).toHaveBeenCalledWith("b");
    expect(rendered("settings-view"), "the activation emptied the view area").toBe(true);
    expect(cover()?.hidden, "the cover showed inside its window").toBe(true);

    await new Promise((r) => setTimeout(r, 320));
    expect(cover()?.hidden, "the chat still loading shows no skeleton").toBe(false);
    expect(rendered("settings-view")).toBe(false);
    expect(rendered("chat-view")).toBe(true);
    expect(rendered("messages-wrap"), "the swap left the transcript out of layout").toBe(true);
    expect(coverUp()).toBe(true);
    page.resolve();
  });

  it("never shows a superseded chat view: Settings, B, Settings shows Settings from its click", async () => {
    const ids = await onSettings("a", "b");

    clickRow(ids["b"] ?? "");
    clickRow(ids["settings"] ?? "");

    expect(rendered("settings-view"), "the Settings click left the covered chat view up").toBe(
      true,
    );
    expect(rendered("chat-view")).toBe(false);
    expect(cover()?.hidden, "the cover outlived the click that left the chat").toBe(true);
    await activationRan();

    expect(fx.chat.show, "the superseded chat was shown").not.toHaveBeenCalled();
    expect(rendered("settings-view")).toBe(true);
    expect(rendered("chat-view")).toBe(false);

    clickRow(ids["a"] ?? "");
    await activationRan();
    expect(rendered("messages-wrap"), "a later switch left the transcript out of layout").toBe(
      true,
    );
    expect(view("messages-wrap").scrollTop).toBe(600);
  });

  it("shows the transcript when the strip empties under a pending switch", async () => {
    const ids = await onSettings("a", "b");

    clickRow(ids["b"] ?? "");
    for (const id of [ids["settings"], ids["a"], ids["b"]]) {
      void closeTab(id ?? "");
    }
    await activationRan();
    await activationRan();

    expect(getActiveTabId()).toBe("");
    expect(rendered("chat-view")).toBe(true);
    expect(rendered("messages-wrap"), "the empty surface kept the transcript out of layout").toBe(
      true,
    );
  });
});

describe("a switch from a chat to Settings", () => {
  it("shows Settings, fading in, and hides the chat view in the click", async () => {
    await strip("a");
    view("messages-wrap").scrollTop = 600;
    await openTab({ kind: "settings", ref: "", activate: false });
    const settings = tabIdFor("settings", "");
    await activationRan();
    expect(rendered("messages-wrap")).toBe(true);
    vi.clearAllMocks();

    clickRow(settings);

    expect(getActiveTabId()).toBe(settings);
    expect(rendered("settings-view"), "the click left Settings hidden").toBe(true);
    expect(
      rendered("chat-view"),
      "the click left the chat up under the selected Settings tab",
    ).toBe(false);
    expect(entryFade("settings-view"), "no entry fade from the click").toBeDefined();
    expect(coverUp()).toBe(false);

    await activationRan();
    expect(rendered("settings-view")).toBe(true);
    expect(rendered("chat-view")).toBe(false);
    view("chat-view").classList.remove("hidden");
    expect(view("messages-wrap").scrollTop, "the chat's place was lost").toBe(600);
  });
});

/** Chats a and b shown in turn (a the last), then Settings shown; every spy cleared. The chat
 *  opener's close clears each store pointer naming its chat, as the store's removal does. */
async function settingsAfterChats(): Promise<{ a: string; b: string; settings: string }> {
  const ids = await strip("a", "b");
  const a = ids["a"] ?? "";
  const b = ids["b"] ?? "";
  clickRow(b);
  await activationRan();
  clickRow(a);
  await activationRan();
  await openTab({ kind: "settings", ref: "" });
  await activationRan();
  const settings = tabIdFor("settings", "");
  expect(getActiveTabId()).toBe(settings);
  expect(pointers.active).toBe("a");
  vi.clearAllMocks();
  fx.chat.close.mockImplementation((id: string) => {
    if (pointers.active === id) {
      pointers.active = "";
    }
    if (pointers.painted === id) {
      pointers.painted = "";
    }
  });
  return { a, b, settings };
}

describe("a chat removed while Settings shows", () => {
  it("another device's close of the chat under Settings acts for the chat last shown before it", async () => {
    const ids = await settingsAfterChats();

    tabServer.closeRemotely(ids.a);
    await settleTabs();

    expect(fx.chat.close).toHaveBeenCalledWith("a");
    expect(fx.chat.select, "the strip picked no chat to act for").toHaveBeenCalledWith("b");
    expect(pointers.active).toBe("b");
    expect(getActiveTabId(), "the removal moved the reader off Settings").toBe(ids.settings);
    expect(fx.chat.show).not.toHaveBeenCalled();
  });

  it("a re-list that drops the chat under Settings acts for the chat last shown before it", async () => {
    const ids = await settingsAfterChats();

    tabServer.closeElsewhere(ids.a);
    await listTabs();
    await settleTabs();

    expect(fx.chat.close).toHaveBeenCalledWith("a");
    expect(fx.chat.select, "the strip picked no chat to act for").toHaveBeenCalledWith("b");
    expect(pointers.active).toBe("b");
    expect(getActiveTabId(), "the re-list moved the reader off Settings").toBe(ids.settings);
    expect(fx.chat.show).not.toHaveBeenCalled();
  });

  it("closing a chat the store already dropped, under Settings, acts for the chat last shown before it", async () => {
    const ids = await settingsAfterChats();
    // The state a gone chat's revert leaves: the store dropped the chat before its tab closes.
    pointers.active = "";
    pointers.painted = "";

    const closing = closeTab(ids.a);

    expect(fx.chat.select, "the strip picked no chat to act for").toHaveBeenCalledWith("b");
    expect(pointers.active).toBe("b");
    expect(getActiveTabId()).toBe(ids.settings);
    await closing;
  });

  it("shows a reopened chat again, though it was the last chat shown before Settings", async () => {
    const ids = await settingsAfterChats();
    tabServer.closeRemotely(ids.a);
    await settleTabs();
    vi.clearAllMocks();

    await openTab({ kind: "chat", ref: "a" });
    await activationRan();

    expect(fx.chat.show, "the activation took the reopened chat as mounted").toHaveBeenCalledWith(
      "a",
    );
  });
});
