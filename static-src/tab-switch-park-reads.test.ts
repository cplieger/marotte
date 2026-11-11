// The click takes the outgoing chat's park reads: through the real tabs.ts, a chat-to-chat switch
// whose composer area changes height forces no layout in the click, only the restore's write in
// the activation, and brings the parked chat back where the reader left it. Real tabs, store, chat
// opener, renderer, scroller and cover over the fake tab server; layouts from `layout-spy.ts`.
import { describe, it, expect, vi, afterEach } from "vitest";
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
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
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

import { makeSession, makeTurn } from "./__test-helpers__/model.js";
import type { Session } from "./types.js";
import { loadCSS } from "./__test-helpers__/css-rules.js";
import { installLayoutSpy, type LayoutSpy } from "./__test-helpers__/layout-spy.js";
import { activationRan, clickRow, frame } from "./__test-helpers__/tab-switch-mocks.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";

document.body.innerHTML =
  '<span id="titlebar-title"></span><span id="titlebar-subtitle"></span><div id="banner-stack"></div><aside id="sidebar"><div id="tab-list"></div></aside>' +
  '<button id="settings-btn"></button><button id="git-btn"></button>' +
  '<button id="files-btn"></button><button id="history-btn"></button>' +
  '<button id="docs-btn"></button>' +
  '<div id="chat-view"><div id="messages-wrap-outer"><div id="messages-wrap">' +
  '<div id="messages"></div></div></div><div id="scroll-bottom"><span></span></div></div>' +
  '<button id="send-btn"></button><textarea id="prompt-input"></textarea>';
const style = document.createElement("style");
style.textContent =
  loadCSS("01-tokens.css") +
  loadCSS("13-messages.css") +
  loadCSS("29-turns.css") +
  loadCSS("40-a11y.css") +
  "#messages-wrap-outer { height: 400px; }";
document.head.appendChild(style);

const store = await import("./store.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");
const tabs = await import("./tabs.js");
const { registerTabOpeners } = await import("./tab-materialize.js");
const { selectChat, showChatView } = await import("./chat.js");
const { ingestTabsChanged, listTabs } = await import("./tabs-sync.js");
const { bindTabsSync, settleTabs } = await import("./__test-helpers__/tabs-server.js");
const outer = document.getElementById("messages-wrap-outer") as HTMLElement;

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });
resetActionFramework();
messages.mountChatView();
// `chat.ts`'s own halves, minus the page fetch. The composer retargets in `select`, where y's
// queued prompt shows its steer row and takes 50 px from the scroller.
registerTabOpeners({
  chat: {
    select: (id: string) => {
      selectChat(id);
      outer.style.height = id === "pl-y" ? "350px" : "400px";
    },
    show: showChatView,
    prefetch: vi.fn(),
    refresh: () => Promise.resolve(),
    loadFailed: vi.fn(),
    close: vi.fn(),
    dot: () => "",
  },
  editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
  run: { show: vi.fn(), refresh: vi.fn() },
  subagent: { show: vi.fn(), refresh: vi.fn() },
  spec: { show: vi.fn(), refresh: vi.fn() },
} as unknown as Parameters<typeof registerTabOpeners>[0]);

function chatOf(id: string, n: number): Session {
  const turns = Array.from({ length: n }, (_, i) =>
    makeTurn({
      id: `${id}-t${String(i)}`,
      n: i + 1,
      trigger: { id: `${id}-t${String(i)}-p`, text: `prompt ${String(i)}` },
      body: [
        { kind: "text", payload: { text: `reply ${String(i)} `.repeat(12) } },
        { kind: "turn_close", payload: { outcome: "completed" } },
      ],
    }),
  );
  return makeSession({ id, name: id, turns });
}

async function frames(): Promise<void> {
  await frame();
  await frame();
}

/** Shown, loaded and settled: the turns' first-frame work (the footer fits, the entry
 *  transitions) is over. */
async function loaded(id: string): Promise<void> {
  store.bumpMessages(id, "load");
  await Promise.resolve();
  await frames();
  await new Promise((r) => setTimeout(r, 400));
}

let spy: LayoutSpy | null = null;

afterEach(() => {
  spy?.dispose();
  spy = null;
});

describe("a chat-to-chat switch through the strip whose composer area changes height", () => {
  it("reads the park in the click, so the activation forces only the restore's write", async () => {
    store.setSessions([chatOf("pl-x", 30), chatOf("pl-y", 30)]);
    await tabs.openTab({ kind: "chat", ref: "pl-x", activate: true });
    await tabs.openTab({ kind: "chat", ref: "pl-y", activate: false });
    await settleTabs();
    await activationRan();
    const x = tabs.tabIdFor("chat", "pl-x");
    const y = tabs.tabIdFor("chat", "pl-y");
    expect(tabs.getActiveTabId()).toBe(x);
    await loaded("pl-x");
    const scroller = scroll.getScrollEl();
    expect(scroller.scrollHeight).toBeGreaterThan(scroller.clientHeight * 3);
    scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
    scroller.scrollTo({ top: 600, behavior: "instant" });
    scroller.dispatchEvent(new Event("scroll"));
    await frames();
    expect(scroll.readingState()).toBe("reading");
    const parked = scroller.scrollTop;

    clickRow(y);
    await activationRan();
    expect(store.getPaintedId()).toBe("pl-y");
    await loaded("pl-y");

    spy = installLayoutSpy();
    spy.begin();
    clickRow(x);
    const click = spy.end();
    expect(tabs.getActiveTabId()).toBe(x);
    expect(click.forced, "layouts forced by the click").toBe(0);
    await frame();

    spy.begin();
    tabs.flushActivationForTest();
    await Promise.resolve();
    const task = spy.end();
    expect(store.getPaintedId()).toBe("pl-x");
    expect(
      task.log.filter((a) => a.forced).map((a) => `${a.kind}:${a.what}`),
      "the activation laid the outgoing transcript out again to read its park",
    ).toEqual(["write:scrollTo"]);
    await frames();
    expect(scroll.readingState()).toBe("reading");
    expect(
      Math.abs(scroller.scrollTop - parked),
      "the parked chat came back away from where the reader left it",
    ).toBeLessThanOrEqual(1);
  });
});
