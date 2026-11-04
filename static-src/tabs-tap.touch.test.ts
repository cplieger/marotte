// A real tap on a tab in the open phone drawer, with a link under the finger. The browser hit-tests
// a tap's `click` after `pointerup`, at the same point, so a row that closed the drawer on
// `pointerup` handed its click to whatever the drawer uncovered (a hook card's file link opened).

import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach, vi } from "vitest";
import { commands, page } from "vitest/browser";

import type * as ApiClient from "./api-client.js";
import type * as Dom from "./dom.js";
import type * as Router from "./router.js";
import type * as RunStore from "./run-store.js";
import type * as Store from "./store.js";
import type * as Toast from "./toast.js";
import type * as Transport from "./transport.js";

// The page chrome a view switch writes (title, toolbar buttons) is not this case's: every element but
// the strip and the drawer is a detached stand-in, and a missing id is made on demand.
vi.mock("./dom.js", async (importOriginal) => {
  const real = await importOriginal<typeof Dom>();
  const ids: Record<string, string> = { tabList: "tab-list", sidebar: "sidebar" };
  return {
    ...real,
    $: new Proxy(
      {},
      {
        get: (_t, prop: string) =>
          (Object.hasOwn(ids, prop) ? document.getElementById(ids[prop] ?? "") : null) ??
          document.createElement("div"),
      },
    ),
    byId: (id: string) => {
      const found = document.getElementById(id);
      if (found !== null) {
        return found;
      }
      const made = document.createElement("span");
      made.id = id;
      made.hidden = true;
      document.documentElement.appendChild(made);
      return made;
    },
  };
});
vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
}));
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Store>()),
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
}));
vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  runLabelOf: vi.fn(() => ""),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  ...(await import("./__test-helpers__/tabs-server.js")).tabTransportMock(),
}));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiBlobOrError: vi.fn(),
  apiGetTyped: (await import("./__test-helpers__/tabs-server.js")).tabListRead(),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

const { openTab, tabIdFor, getActiveTabId, hasTab, _resetForTest } = await import("./tabs.js");
const { registerTabOpeners, _resetTabOpenersForTest } = await import("./tab-materialize.js");
const { ingestTabsChanged, listTabs, _resetTabsSyncForTest } = await import("./tabs-sync.js");
const { resetActionFramework } = await import("./actions/__test-helpers__/action-test-setup.js");
const { bindTabsSync, tabServer } = await import("./__test-helpers__/tabs-server.js");
const { dispatchTouch, frames } = await import("./__test-helpers__/turn-rail-dom.js");
const { TOUCH_DRAG } = await import("./tabs-drag.js");

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

let entry: { readonly width: number; readonly height: number } | null = null;
let linkClicks = 0;

// The drawer's geometry, not the app's stylesheet: the case is the order of the row's dismissal and
// the tap's click, so the drawer only has to cover the link and vanish when `.open` goes.
const LAYOUT = `
  body { margin: 0; }
  #behind { position: fixed; inset: 0; display: block; }
  #sidebar { position: fixed; inset: 0; z-index: 10; background: #fff; display: none; }
  #sidebar.open { display: block; }
  #tab-list .tab { display: flex; align-items: center; block-size: 60px; touch-action: manipulation; }
  #tab-list .tab-close { display: inline-flex; inline-size: 44px; block-size: 44px; }
  #tab-list svg { inline-size: 24px; block-size: 24px; }
`;

beforeAll(async () => {
  entry = { width: window.innerWidth, height: window.innerHeight };
  await page.viewport(390, 844);
  // An instant drawer, as reduced motion makes it: no slide to hold the drawer under the finger
  // until the click lands.
  await commands.emulateA11yMedia({ reducedMotion: "reduce" });
});

afterAll(async () => {
  await commands.emulateA11yMedia({ reducedMotion: "no-preference" });
  if (entry !== null) {
    await page.viewport(entry.width, entry.height);
  }
});

beforeEach(() => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerTabOpeners({
    chat: { show: vi.fn(), refresh: vi.fn(), close: vi.fn(), dot: () => "" },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  linkClicks = 0;
  document.body.innerHTML = `<style>${LAYOUT}</style>
    <a id="behind" href="#opened">Hook fired: hooks.json</a>
    <aside id="sidebar" class="open"><div id="tab-list"></div></aside>`;
  document.getElementById("behind")?.addEventListener("click", (e) => {
    e.preventDefault();
    linkClicks++;
  });
});

afterEach(() => {
  _resetTabsSyncForTest();
  document.body.innerHTML = "";
});

/** The middle of `el` in this page's coordinates. */
function middle(el: Element): { x: number; y: number } {
  const r = el.getBoundingClientRect();
  return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
}

/** A real tap: touch down and up at one point, then the frames the browser's click arrives in. */
async function tap(at: { x: number; y: number }): Promise<void> {
  await dispatchTouch("touchStart", at);
  await dispatchTouch("touchEnd");
  await frames();
  await frames();
}

/** Open two chat tabs, then the drawer over them, as the reader opens ☰ Menu: each open activates its
 *  tab, which dismisses the drawer. */
async function drawerOverTwoTabs(): Promise<void> {
  await openTab({ kind: "chat", ref: "a" });
  await openTab({ kind: "chat", ref: "b" });
  document.getElementById("sidebar")?.classList.add("open");
}

async function rowFor(ref: string): Promise<HTMLElement> {
  const id = tabIdFor("chat", ref);
  await vi.waitFor(() => {
    expect(document.querySelector(`[data-tab-id="${id}"]`)).not.toBeNull();
  });
  return document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`) as HTMLElement;
}

describe("a tap on a tab in the open phone drawer", () => {
  it("activates the tab and closes the drawer without clicking the link behind it", async () => {
    await drawerOverTwoTabs();
    const row = await rowFor("a");
    const at = middle(row);
    expect(document.elementFromPoint(at.x, at.y)?.closest(".tab")).toBe(row);

    await tap(at);

    expect(getActiveTabId()).toBe(tabIdFor("chat", "a"));
    expect(document.getElementById("sidebar")?.classList.contains("open")).toBe(false);
    expect(linkClicks, "the tap's click reached the link the drawer uncovered").toBe(0);
  });

  it("activates a tab held past the lift and released in place without clicking the link", async () => {
    await drawerOverTwoTabs();
    const row = await rowFor("a");
    const at = middle(row);

    await dispatchTouch("touchStart", at);
    // Past the hold, so the row lifts and the list takes the pointer capture.
    await new Promise((r) => setTimeout(r, TOUCH_DRAG.holdMs + 100));
    expect(row.classList.contains("dragging"), "the hold lifted the row").toBe(true);
    await dispatchTouch("touchEnd");
    await frames();
    await frames();

    expect(getActiveTabId()).toBe(tabIdFor("chat", "a"));
    expect(document.getElementById("sidebar")?.classList.contains("open")).toBe(false);
    expect(linkClicks, "the held tap's click reached the link the drawer uncovered").toBe(0);
  });

  it("closes the tab from its × without clicking what slides under the finger", async () => {
    await drawerOverTwoTabs();
    const row = await rowFor("a");
    const x = row.querySelector(".tab-close");
    expect(x).not.toBeNull();
    const rowClicks = vi.fn();
    const below = await rowFor("b");
    below.addEventListener("click", rowClicks);

    const xat = middle(x as Element);
    await tap(xat);

    await vi.waitFor(() => {
      expect(hasTab("chat", "a")).toBe(false);
    });
    await frames();
    expect(getActiveTabId()).toBe(tabIdFor("chat", "b"));
    expect(rowClicks).not.toHaveBeenCalled();
    expect(linkClicks).toBe(0);
  });
});
