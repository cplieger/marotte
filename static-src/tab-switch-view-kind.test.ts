// A switch between view kinds (a chat and Settings) splits like a chat switch: the click lays out
// no incoming view and never leaves the view area empty. A chat target shows under the cover with
// its transcript out of layout; a non-chat target leaves the outgoing view up. The activation shows
// the incoming view, with its entry fade starting there, before the chat's own show. Real tabs.ts
// and view-swap.ts against the fake tab server.
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
import { loadCSS } from "./__test-helpers__/css-rules.js";
import { fx, installSwitchSuite, strip } from "./__test-helpers__/tab-switch-suite.js";
import {
  activationRan,
  clickRow,
  cover,
  coverUp,
  frame,
  pointers,
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
  it("paints the cover over a painted chat in the click, then lifts it over the transcript the activation lays out", async () => {
    const ids = await onSettings("a");
    const a = ids["a"] ?? "";

    clickRow(a);

    expect(getActiveTabId()).toBe(a);
    expect(rendered("settings-view"), "Settings stayed up in the click").toBe(false);
    expect(rendered("chat-view"), "the click left the view area empty").toBe(true);
    expect(rendered("messages-wrap"), "the click laid out the transcript").toBe(false);
    await frame();
    expect(coverUp(), "the gesture's frame painted no skeleton over the chat").toBe(true);
    expect(rendered("messages-wrap"), "the gesture's frame laid out the transcript").toBe(false);
    expect(entryFade("chat-view"), "the covered view showed with no fade").toBeDefined();

    await activationRan();
    expect(rendered("messages-wrap")).toBe(true);
    expect(view("messages-wrap").scrollTop, "the reader's place was lost").toBe(600);
    expect(coverUp(), "the cover stayed up over the mounted transcript").toBe(false);
    expect(fx.chat.show, "the mounted chat was shown again").not.toHaveBeenCalled();
  });

  it("closing Settings shows the chat under it the same way: cover first, transcript from the activation", async () => {
    const ids = await onSettings("a");

    void closeTab(ids["settings"] ?? "");

    expect(getActiveTabId()).toBe(ids["a"]);
    expect(rendered("chat-view"), "the close left the view area empty").toBe(true);
    expect(rendered("messages-wrap"), "the close laid out the transcript").toBe(false);
    await frame();
    expect(coverUp(), "the close's frame painted no skeleton over the chat").toBe(true);

    await activationRan();
    expect(rendered("messages-wrap")).toBe(true);
    expect(view("messages-wrap").scrollTop).toBe(600);
    expect(coverUp()).toBe(false);
  });

  it("paints the cover in the click for an unpainted chat, with the transcript under it out of layout", async () => {
    const ids = await onSettings("a", "b");
    const b = ids["b"] ?? "";
    let shownOver = false;
    fx.chat.show.mockImplementation((id: string) => {
      shownOver = rendered("messages-wrap");
      pointers.painted = id;
    });
    const page = fx.chat.page("b");

    clickRow(b);

    expect(rendered("settings-view")).toBe(false);
    expect(rendered("chat-view"), "the cover's view stayed hidden").toBe(true);
    expect(rendered("messages-wrap"), "the click laid out the transcript under the cover").toBe(
      false,
    );
    await frame();
    expect(coverUp(), "the cover is up in the gesture's frame").toBe(true);
    expect(rendered("messages-wrap"), "the gesture's frame laid out the transcript").toBe(false);
    expect(entryFade("chat-view"), "the covered view showed with no fade").toBeDefined();

    await activationRan();
    expect(fx.chat.show).toHaveBeenCalledWith("b");
    expect(shownOver, "the chat was shown into a scroller with no box").toBe(true);
    expect(rendered("messages-wrap")).toBe(true);
    expect(coverUp(), "the cover lifted before B's page").toBe(true);
    page.resolve();
  });

  it("never shows a superseded chat view: Settings, B, Settings ends on Settings with the transcript held for nothing", async () => {
    const ids = await onSettings("a", "b");

    clickRow(ids["b"] ?? "");
    clickRow(ids["settings"] ?? "");
    await frame();
    expect(coverUp(), "the Settings click lifted the cover off a transcript with no box").toBe(
      true,
    );
    expect(rendered("messages-wrap")).toBe(false);
    await activationRan();

    expect(fx.chat.show, "the superseded chat was shown").not.toHaveBeenCalled();
    expect(rendered("settings-view")).toBe(true);
    expect(rendered("chat-view")).toBe(false);
    expect(
      cover()?.hidden,
      "the cover lifted under a hidden view kept its fade-out for the next show",
    ).toBe(true);

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
  it("keeps the chat view up through the click's frame and shows Settings, fading in, from the activation", async () => {
    await strip("a");
    await openTab({ kind: "settings", ref: "", activate: false });
    const settings = tabIdFor("settings", "");
    await activationRan();
    expect(rendered("messages-wrap")).toBe(true);
    vi.clearAllMocks();

    clickRow(settings);

    expect(getActiveTabId()).toBe(settings);
    expect(rendered("settings-view"), "the click laid out Settings").toBe(false);
    await frame();
    expect(rendered("settings-view"), "the gesture's frame laid out Settings").toBe(false);
    expect(rendered("chat-view"), "the gesture's frame showed no view").toBe(true);

    await activationRan();
    expect(rendered("settings-view")).toBe(true);
    expect(rendered("chat-view")).toBe(false);
    expect(entryFade("settings-view"), "no entry fade from the activation's unhide").toBeDefined();
  });
});
