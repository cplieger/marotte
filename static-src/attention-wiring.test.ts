// The attention system's BINDING, against a real browser (the decisions are
// attention-no-dom.node.test.ts's): the title sink composes prefix + a base captured from
// the served document, never reading the title back; every icon link is swapped from its
// ORIGINAL href; the visible-rows rule is geometric (scrolled list, translated drawer);
// and the recompute funnel covers both the tab SET and every dot write, via the real store.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
// EVERY glyph, generated from the module: real-ESM linking needs each name to be a real
// property (a Proxy still fails to link).
vi.mock("./icons.js", () => ({
  ICON_EDIT: "",
  ICON_EDIT_UI: "",
  ICON_CLOSE: "",
  ICON_TRASH: "",
  ICON_TRASH_UI: "",
  ICON_PLUS_UI: "",
  ICON_PIN: "",
  ICON_PIN_FILLED: "",
  ICON_COPY: "",
  ICON_COPY_MD: "",
  ICON_LINK: "",
  ICON_EXTERNAL: "",
  ICON_DOWNLOAD: "",
  ICON_REPO: "",
  ICON_EXPORT: "",
  ICON_DIFF: "",
  ICON_CLOSE_UI: "",
  ICON_PLAY: "",
  ICON_CHEVRON_DOWN: "",
  ICON_CHEVRON_UP: "",
  ICON_SEND: "",
  ICON_SPINNER: "",
  ICON_HOURGLASS: "",
  ICON_CANCEL: "",
  ICON_MODEL: "",
  ICON_MODEL_UI: "",
  ICON_ALERT: "",
  ICON_ARROW_UP: "",
  ICON_GIT_DOWN_ARROW: "",
  ICON_REFRESH: "",
  ICON_FILTER: "",
  ICON_REPO_EMPTY: "",
  ICON_PR_EMPTY: "",
  ICON_GLOBE: "",
  ICON_WARN: "",
  ICON_SCALE: "",
  ICON_SAVE_OK: "",
  ICON_SAVE_FAIL: "",
  ICON_TAB_CHAT: "",
  ICON_TAB_PLAN: "",
  ICON_TAB_SPEC: "",
  ICON_TAB_AGENT: "",
  ICON_SUBAGENT_INTROSPECT: "",
  ICON_SUBAGENT_GATHERER: "",
  ICON_SUBAGENT_TASK: "",
  ICON_SUBAGENT_CREATOR: "",
  ICON_REFUSAL: "",
  ICON_TAB_QUICK_SPEC: "",
  ICON_TAB_BUG: "",
  ICON_TAB_AUTONOMOUS: "",
  ICON_TAB_REVIEW: "",
  ICON_TAB_SUBTAB: "",
  ICON_TAB_SETTINGS: "",
  ICON_TAB_GIT: "",
  ICON_TAB_EDITOR: "",
  ICON_TAB_FILES: "",
  ICON_TAB_HISTORY: "",
  ICON_TAB_DOCS: "",
  ICON_TAB_RUN: "",
  ICON_TAB_WEB: "",
}));
// The tab set is SERVER-owned, so a row lands on the strip through a real round
// trip against the fake collection rather than through a store mutator.
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => ({
    apiGetTyped: m.tabListRead(),
    // tabs.ts reaches api-client.ts through the files-shared.js edge the
    // multi-instance browser added, so the real link needs this name present.
    apiGet: vi.fn(() => Promise.resolve(null)),
  })),
);
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./device-view.js", () => {
  let active = "";
  return {
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
});
// The two leaf stores the tab factory reads for a display NAME. Neither matters
// here: what this suite reads off a row is its DOT.
// One signal per run, so a reply moving only `readPosition` re-runs the fold as the real cell does.
const runCells = vi.hoisted(() => new Map<string, { value: unknown }>());
vi.mock("./run-store.js", async () => {
  const { signal } = await import("@cplieger/reactive");
  return {
    // Inert here; a Browser-Mode mock is linked as real ESM, so a name any module in the graph
    // reaches has to exist on it.
    runLabelOf: vi.fn(() => ""),
    runState: (id: string): unknown => {
      let c = runCells.get(id);
      if (c === undefined) {
        c = signal<unknown>(undefined);
        runCells.set(id, c);
      }
      return c.value;
    },
  };
});
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));
// notify.ts's subscription half, which no case reaches: the retraction under test is the rest.
vi.mock("./persist.js", () => ({ patchSettings: vi.fn(() => Promise.resolve(null)) }));
vi.mock("./actions/notify.js", () => ({
  registerPush: { dispatch: vi.fn(() => Promise.resolve(null)), cancel: vi.fn() },
  unsubscribePush: { dispatch: vi.fn(() => Promise.resolve(null)) },
}));
// Type-only, for the `importOriginal` below.
import type * as TabsDrag from "./tabs-drag.js";
// `exceedsSlop` stays real: `tabs.ts` reads it, so a partial factory would fail
// this whole file at link time.
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
// The sidebar and tab list are the SAME document-attached elements production looks up,
// because the rule under test is geometric.
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        if (prop === "sidebar" || prop === "tabList") {
          const id = prop === "sidebar" ? "sidebar" : "tab-list";
          let node = document.getElementById(id);
          if (node === null) {
            node = document.createElement("div");
            node.id = id;
            document.body.appendChild(node);
          }
          return node;
        }
        return document.createElement("div");
      },
    },
  ),
  // `byId` is reached through this graph and ESM links for real, so it must exist on the
  // mock. Inlined: a `vi.mock` factory is hoisted above imports. Resolve-or-create, because
  // the real `byId` throws.
  setBusy: vi.fn(),
  byId: (id: string): HTMLElement => {
    let el = document.getElementById(id);
    if (el === null) {
      el = document.createElement("span");
      el.id = id;
      document.body.appendChild(el);
    }
    return el;
  },
}));

import { signal, touch } from "@cplieger/reactive";
import {
  browserAttentionEnv,
  createAttention,
  initAttention,
  pageVisible,
  rowsInView,
} from "./attention.js";
import { defaultUsage, setActive, setSessions, upsertHeader } from "./store.js";
import { BUS_PAGE_RESUMED, BUS_RECONCILE, dispatch, emitBus } from "./bus.js";
import {
  _setRegistrationForTest,
  notifyOffScreen,
  setNotificationsEnabled,
  setOnScreen,
} from "./notify.js";
import type { ServerEvent } from "./types.js";
import {
  openTab,
  openEditorView,
  closeTab,
  activateTab,
  setTabStatus,
  setTabDirty,
  setChatSettledProbe,
  tabIdFor,
  _resetForTest,
} from "./tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer } from "./__test-helpers__/tabs-server.js";
import type { TabDotStatus } from "./tab-view.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

function runCell(id: string): { value: unknown } {
  let c = runCells.get(id);
  if (c === undefined) {
    c = signal<unknown>(undefined);
    runCells.set(id, c);
  }
  return c;
}

function rect(top: number, height: number, left = 0, width = 260): DOMRect {
  return {
    top,
    height,
    bottom: top + height,
    left,
    width,
    right: left + width,
    x: left,
    y: top,
    toJSON: () => ({}),
  } as DOMRect;
}

function place(el: Element, r: DOMRect): void {
  el.getBoundingClientRect = (): DOMRect => r;
}

function buildSidebar(rows: readonly { id: string; top: number; height: number }[]): {
  sidebar: HTMLElement;
  tabList: HTMLElement;
} {
  const sidebar = document.createElement("div");
  sidebar.id = "sidebar";
  const tabList = document.createElement("div");
  tabList.id = "tab-list";
  sidebar.appendChild(tabList);
  document.body.appendChild(sidebar);
  // The persistent desktop sidebar: on screen, 260px wide, full height.
  place(sidebar, rect(0, 700));
  // The scrolling list occupies 100..600.
  place(tabList, rect(100, 500));
  for (const row of rows) {
    const el = document.createElement("div");
    el.dataset["tabId"] = row.id;
    place(el, rect(row.top, row.height));
    tabList.appendChild(el);
  }
  return { sidebar, tabList };
}

function hidePage(hidden: boolean): void {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => (hidden ? "hidden" : "visible"),
  });
}

/**
 * The dot the tab FACTORY seeds a chat row with, per chat ref: the boot-restore channel,
 * where a restored chat holding a latch never calls `setTabStatus`.
 */
const seededDots = new Map<string, TabDotStatus>();

function registerOpeners(): void {
  registerTabOpeners({
    chat: {
      show: vi.fn(),
      refresh: vi.fn(),
      close: vi.fn(),
      dot: (chatID: string) => seededDots.get(chatID) ?? "",
    },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    // `TabOpeners` requires `spec`; inert here, since no case opens a spec tab.
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
}

/** Open a chat tab and answer with its OPAQUE id: ids are server-minted, so a
 *  case addresses a row through the projection's own lookup. */
async function openChat(
  ref: string,
  opts: { activate?: boolean; parentRef?: string; owns?: boolean } = {},
): Promise<string> {
  await openTab({
    kind: "chat",
    ref,
    ...(opts.activate === undefined ? {} : { activate: opts.activate }),
    ...(opts.parentRef === undefined ? {} : { parent: tabIdFor("chat", opts.parentRef) }),
    ...(opts.owns === undefined ? {} : { owns: opts.owns }),
  });
  return tabIdFor("chat", ref);
}

/** MutationObserver delivers on a microtask; a transitionend is synchronous. */
async function settle(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

describe("the title sink", () => {
  beforeEach(() => {
    document.head.innerHTML = "";
    document.title = "Whatever The Server Served";
  });

  it("composes the count onto the title the document was SERVED with", () => {
    // Not a constant: the base comes from static/index.html's <title> itself.
    const env = browserAttentionEnv();
    env.titlePrefix("(2) ");
    expect(document.title).toBe("(2) Whatever The Server Served");
  });

  it("restores the exact served title at zero", () => {
    const env = browserAttentionEnv();
    env.titlePrefix("(2) ");
    env.titlePrefix("");
    expect(document.title).toBe("Whatever The Server Served");
  });

  it("cannot compound a prefix, however many times it is written", () => {
    // The bug avoided by holding the base rather than reading document.title
    // back: the current value already carries a prefix.
    const env = browserAttentionEnv();
    for (const n of ["(1) ", "(2) ", "(3) "]) {
      env.titlePrefix(n);
    }
    expect(document.title).toBe("(3) Whatever The Server Served");
  });

  it("is the floor: present even with no badge and no icon links", () => {
    vi.stubGlobal("navigator", {});
    const env = browserAttentionEnv();
    expect(env.setBadge).toBeUndefined();
    expect(env.setIcon).toBeUndefined();
    env.titlePrefix("(5) ");
    expect(document.title).toBe("(5) Whatever The Server Served");
  });
});

describe("the badge sink", () => {
  beforeEach(() => {
    document.head.innerHTML = "";
  });

  it("is absent when the platform has no Badging API", () => {
    // An absent capability is an absent sink, not an error: a browser without
    // the API is a normal state of the world.
    vi.stubGlobal("navigator", {});
    expect(browserAttentionEnv().setBadge).toBeUndefined();
  });

  it("always passes a NUMBER, never the spec's bare flag form", () => {
    // iOS renders nothing at all for setAppBadge() with no argument.
    const setAppBadge = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { setAppBadge });
    browserAttentionEnv().setBadge?.(4);
    expect(setAppBadge).toHaveBeenCalledWith(4);
  });

  it("clears through clearAppBadge where the platform has it", () => {
    const setAppBadge = vi.fn(() => Promise.resolve());
    const clearAppBadge = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { setAppBadge, clearAppBadge });
    browserAttentionEnv().setBadge?.(0);
    expect(clearAppBadge).toHaveBeenCalledTimes(1);
    expect(setAppBadge).not.toHaveBeenCalled();
  });

  it("clears with setAppBadge(0) where clearAppBadge is missing", () => {
    const setAppBadge = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { setAppBadge });
    browserAttentionEnv().setBadge?.(0);
    expect(setAppBadge).toHaveBeenCalledWith(0);
  });

  it("swallows a rejected badge promise", async () => {
    // An OS that will not paint a badge is not an error this page can act on,
    // and an unhandled rejection inside a status sweep surfaces as a page fault.
    const setAppBadge = vi.fn(() => Promise.reject(new Error("unsupported")));
    vi.stubGlobal("navigator", { setAppBadge });
    expect(() => browserAttentionEnv().setBadge?.(2)).not.toThrow();
    await settle();
    expect(setAppBadge).toHaveBeenCalledTimes(1);
  });

  it("swallows a synchronous throw from the badge call", () => {
    const setAppBadge = vi.fn(() => {
      throw new Error("nope");
    });
    vi.stubGlobal("navigator", { setAppBadge });
    expect(() => browserAttentionEnv().setBadge?.(2)).not.toThrow();
  });
});

describe("the icon sink", () => {
  beforeEach(() => {
    vi.stubGlobal("navigator", {});
  });

  it("rewrites EVERY icon link, not the first one a browser might pick", () => {
    document.head.innerHTML =
      '<link rel="icon" type="image/svg+xml" href="/favicon.svg">' +
      '<link rel="icon" sizes="32x32" href="/favicon-32x32.png">';
    browserAttentionEnv().setIcon?.("input");
    const hrefs = [...document.querySelectorAll("link")].map((l) => l.getAttribute("href"));
    expect(hrefs).toEqual(["/favicon-input.svg", "/favicon-input-32x32.png"]);
  });

  it("leaves apple-touch-icon alone, because the OS cached it at install", () => {
    document.head.innerHTML =
      '<link rel="icon" href="/favicon.svg">' +
      '<link rel="apple-touch-icon" href="/apple-touch-icon.png">';
    browserAttentionEnv().setIcon?.("alert");
    expect(document.querySelector('link[rel="apple-touch-icon"]')?.getAttribute("href")).toBe(
      "/apple-touch-icon.png",
    );
  });

  it("leaves it alone even when it is named like a favicon", () => {
    // `rel~="icon"` skips a touch icon even when its file is a sized favicon: the OS caches it
    // at install, so a variant would only 404 the installed app's icon.
    document.head.innerHTML =
      '<link rel="icon" href="/favicon.svg">' +
      '<link rel="apple-touch-icon" sizes="180x180" href="/favicon-180x180.png">';
    browserAttentionEnv().setIcon?.("input");
    expect(document.querySelector('link[rel="apple-touch-icon"]')?.getAttribute("href")).toBe(
      "/favicon-180x180.png",
    );
    expect(document.querySelector('link[rel="icon"]')?.getAttribute("href")).toBe(
      "/favicon-input.svg",
    );
  });

  it("computes every variant from the ORIGINAL, so swaps cannot compound", () => {
    document.head.innerHTML = '<link rel="icon" href="/favicon.svg">';
    const env = browserAttentionEnv();
    env.setIcon?.("input");
    env.setIcon?.("done");
    env.setIcon?.("alert");
    expect(document.querySelector("link")?.getAttribute("href")).toBe("/favicon-alert.svg");
  });

  it("restores the original href on null", () => {
    document.head.innerHTML = '<link rel="icon" href="/favicon.svg">';
    const env = browserAttentionEnv();
    env.setIcon?.("done");
    env.setIcon?.(null);
    expect(document.querySelector("link")?.getAttribute("href")).toBe("/favicon.svg");
  });

  it("is absent when the page declares no icon link at all", () => {
    document.head.innerHTML = "";
    expect(browserAttentionEnv().setIcon).toBeUndefined();
  });

  it("drives the whole chain from a fold value", () => {
    document.head.innerHTML = '<link rel="icon" href="/favicon.svg">';
    document.title = "Marotte for Kiro";
    const surfaces = createAttention(browserAttentionEnv());
    surfaces.apply({ count: 2, worst: "waiting" });
    expect(document.title).toBe("(2) Marotte for Kiro");
    // `waiting` shares the `input` asset: three variants ship, not four.
    expect(document.querySelector("link")?.getAttribute("href")).toBe("/favicon-input.svg");
  });
});

describe("rowsInView", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  it("reports every row fully inside the list's own box", () => {
    const { sidebar, tabList } = buildSidebar([
      { id: "a", top: 110, height: 40 },
      { id: "b", top: 160, height: 40 },
    ]);
    expect(rowsInView(sidebar, tabList)).toEqual(["a", "b"]);
  });

  it("KEEPS a scrolled-out row out of view", () => {
    // The case the whole rule exists for: `#tab-list` is overflow-y:auto, so the
    // forgotten background chat is precisely the one below the fold.
    const { sidebar, tabList } = buildSidebar([
      { id: "a", top: 110, height: 40 },
      { id: "b", top: 900, height: 40 },
    ]);
    expect(rowsInView(sidebar, tabList)).toEqual(["a"]);
  });

  it("excludes a row scrolled off the TOP as well as the bottom", () => {
    const { sidebar, tabList } = buildSidebar([
      { id: "above", top: 20, height: 40 },
      { id: "in", top: 200, height: 40 },
    ]);
    expect(rowsInView(sidebar, tabList)).toEqual(["in"]);
  });

  it("excludes a HALF-clipped row, so the count lingers rather than losing a cue", () => {
    // Fully inside, not partially. The asymmetry is deliberate: a lingering count
    // is dismissible, a blanked cue is not recoverable.
    const { sidebar, tabList } = buildSidebar([
      { id: "half", top: 580, height: 40 },
      { id: "whole", top: 300, height: 40 },
    ]);
    expect(rowsInView(sidebar, tabList)).toEqual(["whole"]);
  });

  it("reports nothing when the sidebar is a drawer parked off the viewport", () => {
    // translateX(-100%) on mobile. checkVisibility() calls such an element fully
    // visible, which is why the geometry is the test.
    const { sidebar, tabList } = buildSidebar([{ id: "a", top: 110, height: 40 }]);
    place(sidebar, rect(0, 700, -1024, 1024));
    expect(rowsInView(sidebar, tabList)).toEqual([]);
  });

  it("reports nothing when the sidebar is hidden by CSS", () => {
    const { sidebar, tabList } = buildSidebar([{ id: "a", top: 110, height: 40 }]);
    (sidebar as unknown as { checkVisibility: () => boolean }).checkVisibility = () => false;
    expect(rowsInView(sidebar, tabList)).toEqual([]);
  });

  it("reports nothing when the sidebar has no box at all", () => {
    const { sidebar, tabList } = buildSidebar([{ id: "a", top: 110, height: 40 }]);
    place(sidebar, rect(0, 0, 0, 0));
    expect(rowsInView(sidebar, tabList)).toEqual([]);
  });

  it("reports nothing when the sidebar has collapsed in either dimension", () => {
    // checkVisibility() calls a zero-width element visible. The box sits INSIDE the viewport,
    // because one at the edge is already rejected by the intersection clauses.
    const { sidebar, tabList } = buildSidebar([{ id: "a", top: 110, height: 40 }]);
    for (const collapsed of [rect(0, 700, 0, 0), rect(0, 700, 100, 0), rect(100, 0, 0, 260)]) {
      place(sidebar, collapsed);
      expect(rowsInView(sidebar, tabList)).toEqual([]);
    }
  });

  it("skips a zero-height row, which is a collapsed one rather than a visible one", () => {
    const { sidebar, tabList } = buildSidebar([
      { id: "folded", top: 200, height: 0 },
      { id: "a", top: 250, height: 40 },
    ]);
    expect(rowsInView(sidebar, tabList)).toEqual(["a"]);
  });

  it("is transform-invariant, so it holds while the drawer animates in", () => {
    // Rows and the clip box share the transformed subtree, so containment holds mid-flight.
    const { sidebar, tabList } = buildSidebar([
      { id: "a", top: 110, height: 40 },
      { id: "b", top: 900, height: 40 },
    ]);
    place(tabList, rect(-400, 500));
    place(tabList.children[0] as Element, rect(-390, 40));
    place(tabList.children[1] as Element, rect(400, 40));
    expect(rowsInView(sidebar, tabList)).toEqual(["a"]);
  });
});

describe("pageVisible", () => {
  beforeEach(() => {
    hidePage(false);
    setActive("");
  });

  it("reads a prerendered or visible page as visible", () => {
    expect(pageVisible()).toBe(true);
  });

  it("reads a hidden page as hidden", () => {
    hidePage(true);
    expect(pageVisible()).toBe(false);
  });
});

describe("initAttention", () => {
  let dispose: (() => void) | null = null;

  /** Place one row per TAB id. Opaque ids, so every case reads them back from the
   *  projection rather than composing one. */
  function rows(entries: readonly { id: string; top: number; height: number }[]): void {
    const tabList = document.getElementById("tab-list");
    if (tabList === null) {
      throw new Error("no tab list");
    }
    tabList.innerHTML = "";
    for (const entry of entries) {
      const el = document.createElement("div");
      el.dataset["tabId"] = entry.id;
      place(el, rect(entry.top, entry.height));
      tabList.appendChild(el);
    }
  }

  function count(): number {
    const match = /^\((\d+)\) /.exec(document.title);
    return match === null ? 0 : Number(match[1]);
  }

  function iconVariant(): string {
    return document.querySelector("link")?.getAttribute("href") ?? "";
  }

  /** The seen marker the SERVER holds for a tab: what every other device folds. */
  function serverSeen(tabID: string): number {
    return tabServer.subjects().find((s) => s.id === tabID)?.seen ?? 0;
  }

  /** The chat's header as the server sends it, at a read position. */
  function header(ref: string, position: number): void {
    upsertHeader({
      id: ref,
      name: ref,
      usage: defaultUsage(),
      turn_count: position,
      created_at: 0,
      updated_at: 0,
      read_position: position,
    });
  }

  /** A chat turn closing: the header's read position moves, then the tab's dot. */
  function finish(tabID: string, ref: string, status: TabDotStatus, position = 1): void {
    header(ref, position);
    setTabStatus(tabID, status);
  }

  /** A run reply landing in the run store, at a read position. */
  function runReply(workflowID: string, position: number): void {
    runCell(workflowID).value = { workflowId: workflowID, readPosition: position };
  }

  beforeEach(() => {
    tabServer.reset();
    _resetTabsSyncForTest();
    _resetTabOpenersForTest();
    seededDots.clear();
    runCells.clear();
    registerOpeners();
    resetActionFramework();
    _resetForTest();
    setSessions([]);
    setActive("");
    hidePage(false);
    document.body.innerHTML = "";
    document.head.innerHTML = '<link rel="icon" href="/favicon.svg">';
    document.title = "Marotte for Kiro";
    vi.stubGlobal("navigator", {});
    // The persistent desktop sidebar, clipping to 100..600; rows are placed per test.
    const sidebar = document.createElement("div");
    sidebar.id = "sidebar";
    const tabList = document.createElement("div");
    tabList.id = "tab-list";
    sidebar.appendChild(tabList);
    document.body.appendChild(sidebar);
    place(sidebar, rect(0, 700));
    place(tabList, rect(100, 500));
    dispose = initAttention();
  });

  afterEach(() => {
    dispose?.();
    dispose = null;
    _resetTabsSyncForTest();
  });

  it("starts silent, leaving the served title alone", () => {
    expect(document.title).toBe("Marotte for Kiro");
    expect(iconVariant()).toBe("/favicon.svg");
  });

  it("raises an unread background chat", async () => {
    await openChat("a");
    const b = await openChat("b", { activate: false });
    finish(b, "b", "done");
    expect(count()).toBe(1);
    expect(iconVariant()).toBe("/favicon-done.svg");
  });

  it("counts nothing for a finished dot whose read position the server has not sent", async () => {
    await openChat("a");
    const b = await openChat("b", { activate: false });
    setTabStatus(b, "done");
    expect(count()).toBe(0);
  });

  // "Watching" is a fact about the TAB: every key here is a tab id.
  it("leaves the watched tab out and acknowledges it on the server", async () => {
    const a = await openChat("a");
    finish(a, "a", "done", 3);
    expect(count()).toBe(0);
    await vi.waitFor(() => {
      expect(serverSeen(a)).toBe(3);
    });
  });

  it("raises the active tab while the page is hidden, and acknowledges nothing", async () => {
    const a = await openChat("a");
    hidePage(true);
    finish(a, "a", "failed");
    expect(count()).toBe(1);
    expect(iconVariant()).toBe("/favicon-alert.svg");
    await settle();
    expect(tabServer.sentOfType("ack_tab")).toHaveLength(0);
  });

  it("counts an open ask on the chat being read until it is answered", async () => {
    const a = await openChat("a");
    setTabStatus(a, "input");
    expect(count()).toBe(1);
    setTabStatus(a, "working");
    expect(count()).toBe(0);
  });

  it("counts each tab once and paints the most severe", async () => {
    const ids: string[] = [];
    for (const ref of ["a", "b", "c"]) {
      ids.push(await openChat(ref, { activate: false }));
    }
    finish(ids[0] ?? "", "a", "done");
    finish(ids[1] ?? "", "b", "failed");
    setTabStatus(ids[2] ?? "", "input");
    expect(count()).toBe(3);
    expect(iconVariant()).toBe("/favicon-input.svg");
  });

  it("raises a cue the SPEC carried in, with no dot write at all", async () => {
    // The boot-restore path: a restored chat holding a latch never calls `setTabStatus`, so
    // the tab-set signal is the only thing that wakes the fold.
    header("a", 1);
    seededDots.set("a", "failed");
    await openChat("a", { activate: false });
    expect(count()).toBe(1);
    expect(iconVariant()).toBe("/favicon-alert.svg");
  });

  it("counts a sub-tab chat, which owns its own bridge and its own cue", async () => {
    await openChat("parent", { activate: false });
    const tangent = await openChat("tangent", { activate: false, parentRef: "parent" });
    finish(tangent, "tangent", "done");
    expect(count()).toBe(1);
  });

  it("does NOT count a view tab watching work another chat owns", async () => {
    // `owns: false` makes a tab a window onto a chat rather than the chat, so
    // counting it would count one chat twice whenever its own tab is open too.
    const a = await openChat("a", { activate: false });
    const view = await openChat("a-view", { activate: false, parentRef: "a", owns: false });
    finish(a, "a", "done");
    finish(view, "a-view", "done");
    expect(count()).toBe(1);
  });

  it("never counts an editor tab's unsaved mark", async () => {
    await openEditorView("/main.go");
    setTabDirty(tabIdFor("editor", "/main.go"), true);
    expect(count()).toBe(0);
  });

  it("clears a chat when the reader switches to it, and the server records the read", async () => {
    await openChat("a");
    const b = await openChat("b", { activate: false });
    finish(b, "b", "done", 2);
    expect(count()).toBe(1);

    await activateTab(b);
    expect(count()).toBe(0);
    await vi.waitFor(() => {
      expect(serverSeen(b)).toBe(2);
    });
  });

  it("acknowledges only the IN-VIEW rows on becoming visible", async () => {
    const a = await openChat("a", { activate: false });
    const b = await openChat("b", { activate: false });
    finish(a, "a", "done");
    finish(b, "b", "done");
    hidePage(true);
    expect(count()).toBe(2);

    // `b` is scrolled below the list's fold: it was never on screen.
    rows([
      { id: a, top: 110, height: 40 },
      { id: b, top: 900, height: 40 },
    ]);
    hidePage(false);
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.waitFor(() => {
      expect(count()).toBe(1);
    });
    expect(serverSeen(b)).toBe(0);
  });

  it("acknowledges nothing while the page is hidden", async () => {
    const a = await openChat("a", { activate: false });
    finish(a, "a", "done");
    rows([{ id: a, top: 110, height: 40 }]);
    hidePage(true);
    document.dispatchEvent(new Event("visibilitychange"));
    await settle();
    expect(tabServer.sentOfType("ack_tab")).toHaveLength(0);
    expect(count()).toBe(1);
  });

  it("acknowledges the in-view rows when the drawer's class changes", async () => {
    // The drawer opening is a class toggle, so it is OBSERVED, covering every opener.
    const a = await openChat("a", { activate: false });
    finish(a, "a", "done");
    rows([{ id: a, top: 110, height: 40 }]);
    expect(count()).toBe(1);

    document.getElementById("sidebar")?.classList.add("open");
    await vi.waitFor(() => {
      expect(count()).toBe(0);
    });
  });

  it("acknowledges the in-view rows once the drawer has settled", async () => {
    // The mutation lands before the transform, so the settled event acknowledges.
    const a = await openChat("a", { activate: false });
    finish(a, "a", "done");
    rows([{ id: a, top: 110, height: 40 }]);
    const sidebar = document.getElementById("sidebar");
    if (sidebar === null) {
      throw new Error("no sidebar");
    }
    place(sidebar, rect(0, 700, -1024, 1024));
    sidebar.classList.add("open");
    await settle();
    expect(tabServer.sentOfType("ack_tab")).toHaveLength(0);

    place(sidebar, rect(0, 700));
    sidebar.dispatchEvent(new Event("transitionend", { bubbles: true }));
    await vi.waitFor(() => {
      expect(count()).toBe(0);
    });
  });

  it("ignores a transition finishing on a tab row rather than the sidebar", async () => {
    // transitionend bubbles, and every row animates its own background on hover.
    const a = await openChat("a", { activate: false });
    finish(a, "a", "done");
    rows([{ id: a, top: 110, height: 40 }]);
    document
      .querySelector("[data-tab-id]")
      ?.dispatchEvent(new Event("transitionend", { bubbles: true }));
    await settle();
    expect(tabServer.sentOfType("ack_tab")).toHaveLength(0);
  });

  it("lights a read chat again when its next turn closes", async () => {
    const a = await openChat("a", { activate: false });
    finish(a, "a", "done");
    rows([{ id: a, top: 110, height: 40 }]);
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.waitFor(() => {
      expect(count()).toBe(0);
    });

    rows([]);
    setTabStatus(a, "working");
    expect(count()).toBe(0);
    finish(a, "a", "done", 2);
    expect(count()).toBe(1);
  });

  it("reaches zero when the last counted chat closes", async () => {
    // The funnel's other leg. `setTabStatus` does not `emit()`, so a recompute
    // hung off the dot alone would leave this count claiming a chat that is gone.
    const a = await openChat("a", { activate: false });
    const b = await openChat("b", { activate: false });
    finish(a, "a", "done");
    setTabStatus(b, "input");
    expect(count()).toBe(2);

    await closeTab(a);
    expect(count()).toBe(1);
    await closeTab(b);
    expect(count()).toBe(0);
    expect(iconVariant()).toBe("/favicon.svg");
  });

  describe("seen state lives on the server, so every device folds the same", () => {
    it("clears here when another device reads the chat", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      finish(b, "b", "done", 2);
      expect(count()).toBe(1);

      tabServer.ackRemotely(b, 2);
      expect(count()).toBe(0);
    });

    it("keeps a newer turn lit when a lagging device acknowledges an older one", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      finish(b, "b", "done", 2);
      tabServer.ackRemotely(b, 1);
      expect(count()).toBe(1);
    });

    it("lights on a header frame that moves only the read position", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      finish(b, "b", "done", 1);
      tabServer.ackRemotely(b, 1);
      expect(count()).toBe(0);

      header("b", 2);
      expect(count()).toBe(1);
    });

    it("unlights a rewound chat on every device at once, the lower header included", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      finish(b, "b", "done", 2);
      tabServer.ackRemotely(b, 1);
      expect(count()).toBe(1);

      // The rewind takes turn 2 out; the server's header now names turn 1, which was read.
      header("b", 1);
      expect(count()).toBe(0);
    });

    it("keeps a read across a reload, because the subject carries it", async () => {
      const a = await openChat("a", { activate: false });
      finish(a, "a", "done");
      rows([{ id: a, top: 110, height: 40 }]);
      document.dispatchEvent(new Event("visibilitychange"));
      await vi.waitFor(() => {
        expect(serverSeen(a)).toBe(1);
      });

      dispose?.();
      _resetForTest();
      registerOpeners();
      document.title = "Marotte for Kiro";
      dispose = initAttention();
      await tabServer.seed();
      setTabStatus(a, "done");
      expect(count()).toBe(0);
    });

    // A 409 is another request holding the idempotency key, whose own outcome is unknown here.
    it.each([
      ["resends after a 409", 409, 2],
      ["does not resend after a 400", 400, 1],
    ])("%s", async (_label, status, sends) => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      finish(b, "b", "done");
      tabServer.failNext("ack_tab", status);
      await activateTab(b);
      await vi.waitFor(() => {
        expect(tabServer.sentOfType("ack_tab")).toHaveLength(1);
      });
      await settle();
      await settle();

      header("b", 1);
      await settle();
      expect(tabServer.sentOfType("ack_tab")).toHaveLength(sends);
    });

    it("listens for no storage event: no window holds seen state", async () => {
      dispose?.();
      const listen = vi.spyOn(window, "addEventListener");
      try {
        dispose = initAttention();
        expect(listen.mock.calls.map(([type]) => type)).not.toContain("storage");
      } finally {
        listen.mockRestore();
      }
    });
  });

  describe("a run tab", () => {
    async function openRun(): Promise<string> {
      await openChat("a");
      await openTab({ kind: "run", ref: "wf-1", owns: false, activate: false });
      return tabIdFor("run", "wf-1");
    }

    it("counts nothing for a terminal dot until a read serves its position", async () => {
      const run = await openRun();
      setTabStatus(run, "failed");
      expect(count()).toBe(0);

      runReply("wf-1", 2);
      expect(count()).toBe(1);
      expect(iconVariant()).toBe("/favicon-alert.svg");
    });

    it("counts a paused run while it waits, with no position", async () => {
      const run = await openRun();
      setTabStatus(run, "waiting");
      expect(count()).toBe(1);
    });

    it("acknowledges a finished run the reader switches to", async () => {
      const run = await openRun();
      runReply("wf-1", 3);
      setTabStatus(run, "done");
      await activateTab(run);
      await vi.waitFor(() => {
        expect(serverSeen(run)).toBe(3);
      });
    });
  });

  // The badge count follows the notification's rule: a chat whose run is still going counts
  // NOTHING until the work is over. The probe is injected, as in production, and backed by a
  // signal so a `cueCandidates` that stopped subscribing fails.
  describe("a chat with outstanding work contributes 0", () => {
    const unsettled = new Set<string>();
    const settleVersion = signal(0);

    function setUnsettled(chatRef: string, on: boolean): void {
      if (on) {
        unsettled.add(chatRef);
      } else {
        unsettled.delete(chatRef);
      }
      settleVersion.value = settleVersion.peek() + 1;
    }

    beforeEach(() => {
      unsettled.clear();
      // Registered after the outer beforeEach's `_resetForTest`, which clears it.
      setChatSettledProbe((chatRef: string) => {
        touch(settleVersion);
        return !unsettled.has(chatRef);
      });
    });

    it("blanks a background chat's done cue while its run is live, and raises it after", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      setUnsettled("b", true);
      finish(b, "b", "done");
      expect(count()).toBe(0);
      expect(iconVariant()).toBe("/favicon.svg");

      setUnsettled("b", false);
      expect(count()).toBe(1);
      expect(iconVariant()).toBe("/favicon-done.svg");
    });

    it("blanks a FAILED cue too, because a failed turn's run is still going", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      setUnsettled("b", true);
      finish(b, "b", "failed");
      expect(count()).toBe(0);

      setUnsettled("b", false);
      expect(count()).toBe(1);
    });

    it("never blanks an unanswered decision", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      setUnsettled("b", true);
      setTabStatus(b, "input");
      expect(count()).toBe(1);
    });

    it("never blanks a chat whose agent is standing by", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      setUnsettled("b", true);
      finish(b, "b", "waiting");
      expect(count()).toBe(1);
    });

    // The RUN's own tab is the surface that reports the run, so blanking it would
    // remove the one cue that is telling the truth.
    it("leaves a RUN tab's own cue alone", async () => {
      await openChat("a");
      await openTab({ kind: "run", ref: "wf-1", owns: false, activate: false });
      const runTab = tabIdFor("run", "wf-1");
      setUnsettled("wf-1", true);
      runReply("wf-1", 1);
      setTabStatus(runTab, "done");
      expect(count()).toBe(1);
    });

    it("keeps the reader's read across the blank", async () => {
      const a = await openChat("a");
      const b = await openChat("b", { activate: false });
      finish(b, "b", "done");
      await activateTab(b);
      await vi.waitFor(() => {
        expect(serverSeen(b)).toBe(1);
      });
      await activateTab(a);

      setUnsettled("b", true);
      setUnsettled("b", false);
      expect(count()).toBe(0);
    });

    // Unregistered suppresses nothing, so a root that forgot the probe degrades safely.
    it("suppresses nothing when no probe is registered", async () => {
      _resetForTest();
      registerOpeners();
      await openChat("a");
      const b = await openChat("b", { activate: false });
      setUnsettled("b", true);
      finish(b, "b", "done");
      expect(count()).toBe(1);
    });
  });

  it("stops touching the surfaces once disposed", async () => {
    const a = await openChat("a", { activate: false });
    finish(a, "a", "done");
    expect(count()).toBe(1);
    dispose?.();
    dispose = null;
    setTabStatus(a, "failed");
    expect(iconVariant()).toBe("/favicon-done.svg");
  });

  it("hands the page's own icon back when the page goes away with a cue lit", async () => {
    // A browser remembers one icon per URL, so a tab closed on a lit cue would leave a variant.
    const a = await openChat("a", { activate: false });
    finish(a, "a", "done");
    expect(iconVariant()).toBe("/favicon-done.svg");

    window.dispatchEvent(new Event("pagehide"));

    expect(iconVariant()).toBe("/favicon.svg");
    expect(document.title).toBe("Marotte for Kiro");
  });

  it("KEEPS the cue when the browser merely freezes a background tab", async () => {
    // A frozen tab is still in the strip showing its icon, so `freeze` must not restore.
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "input");

    document.dispatchEvent(new Event("freeze"));

    expect(iconVariant()).toBe("/favicon-input.svg");
    expect(count()).toBe(1);
  });

  it("repaints the cue when the page comes back from the bfcache", async () => {
    // pagehide fires on bfcache entry too, and that page can return. The sinks
    // are change-gated, so the fold has to be re-run rather than trusted.
    const a = await openChat("a", { activate: false });
    finish(a, "a", "failed");
    window.dispatchEvent(new Event("pagehide"));
    expect(iconVariant()).toBe("/favicon.svg");

    const restored = new Event("pageshow");
    Object.defineProperty(restored, "persisted", { value: true });
    window.dispatchEvent(restored);

    expect(iconVariant()).toBe("/favicon-alert.svg");
    expect(count()).toBe(1);
  });

  it("stops restoring once disposed", async () => {
    const a = await openChat("a", { activate: false });
    finish(a, "a", "done");
    dispose?.();
    dispose = null;
    window.dispatchEvent(new Event("pagehide"));
    expect(iconVariant()).toBe("/favicon-done.svg");
  });

  // The OS badge as WebKit keeps it: assigned by each write that lands, kept until the next one.
  // `dropNext` loses one write after its promise resolved, which the page cannot observe.
  describe("the installed app's badge", () => {
    const os = { badge: 0, dropNext: false };
    const worker = new EventTarget();
    const delivered: string[] = [];
    const closed: string[] = [];

    function write(n: number): Promise<void> {
      if (os.dropNext) {
        os.dropNext = false;
      } else {
        os.badge = n;
      }
      return Promise.resolve();
    }

    beforeEach(() => {
      dispose?.();
      os.badge = 0;
      os.dropNext = false;
      delivered.length = 0;
      closed.length = 0;
      vi.stubGlobal("navigator", {
        setAppBadge: write,
        clearAppBadge: () => write(0),
        serviceWorker: worker,
      });
      _setRegistrationForTest(() =>
        Promise.resolve({
          getNotifications: (filter?: GetNotificationOptions) =>
            Promise.resolve(
              delivered
                .filter((tag) => filter?.tag === undefined || tag === filter.tag)
                .map((tag) => ({ tag, close: () => closed.push(tag) }) as unknown as Notification),
            ),
        }),
      );
      dispose = initAttention();
    });

    afterEach(() => {
      _setRegistrationForTest(null);
    });

    it("lights for a new item and clears when it is read", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      finish(b, "b", "done");
      expect(os.badge).toBe(1);

      await activateTab(b);
      expect(os.badge).toBe(0);
    });

    it("clears when another device reads the chat", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      finish(b, "b", "done");
      expect(os.badge).toBe(1);

      tabServer.ackRemotely(b, 1);
      expect(os.badge).toBe(0);
    });

    /** Two background threads finish; the reader reads the first, and the clear on reading the
     *  second never lands. Answers the thread every case looks at again. */
    async function stuckBadge(): Promise<string> {
      const a = await openChat("a");
      const b = await openChat("b", { activate: false });
      const c = await openChat("c", { activate: false });
      finish(b, "b", "done");
      finish(c, "c", "done");
      await activateTab(b);
      await vi.waitFor(() => {
        expect(serverSeen(b)).toBe(1);
      });
      os.dropNext = true;
      await activateTab(c);
      await vi.waitFor(() => {
        expect(serverSeen(c)).toBe(1);
      });
      expect(count(), "the page believes every thread is read").toBe(0);
      expect(os.badge, "the OS kept the dot").toBe(1);
      // The event under test arrives in a later task than the dropped write.
      await settle();
      return a;
    }

    const syncPoints: readonly (readonly [string, (a: string) => unknown])[] = [
      ["the reader switching to an already-read thread", (a) => activateTab(a)],
      ["the page becoming visible", () => document.dispatchEvent(new Event("visibilitychange"))],
      ["the window taking focus", () => window.dispatchEvent(new Event("focus"))],
      [
        "a fresh pageshow",
        () => window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: false })),
      ],
      [
        "a bfcache pageshow",
        () => window.dispatchEvent(new PageTransitionEvent("pageshow", { persisted: true })),
      ],
      [
        "the stream reconnecting",
        () => {
          emitBus(BUS_RECONCILE, { cause: "epoch", signal: new AbortController().signal });
        },
      ],
      [
        "the page resuming",
        () => {
          emitBus(BUS_PAGE_RESUMED);
        },
      ],
      [
        "the hello",
        () => {
          dispatch({ type: "connected", chat_id: "", payload: {} } as ServerEvent);
        },
      ],
      [
        "the status snapshot",
        () => {
          dispatch({ type: "status_snapshot", chat_id: "", payload: { rows: [] } } as ServerEvent);
        },
      ],
      [
        "the pending snapshot",
        () => {
          dispatch({
            type: "pending_snapshot",
            chat_id: "",
            payload: { items: [] },
          } as ServerEvent);
        },
      ],
      [
        "a push the worker relayed to this page",
        () =>
          worker.dispatchEvent(
            new MessageEvent("message", { data: { type: "push", reason: "arrived", chatId: "" } }),
          ),
      ],
      [
        "a notification click the worker relayed",
        () =>
          worker.dispatchEvent(
            new MessageEvent("message", { data: { type: "push", reason: "clicked", chatId: "" } }),
          ),
      ],
    ];

    it.each(syncPoints)("clears a dot a lost write left lit, on %s", async (_name, fire) => {
      const a = await stuckBadge();
      await fire(a);
      expect(os.badge).toBe(0);
    });

    it("re-asserts the real count, not a blanket clear, on a sync point", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      finish(b, "b", "done");
      await settle();
      os.badge = 0;
      window.dispatchEvent(new Event("focus"));
      expect(os.badge).toBe(1);
    });

    it("ignores a worker message that is not a push", async () => {
      await stuckBadge();
      worker.dispatchEvent(new MessageEvent("message", { data: { type: "other" } }));
      expect(os.badge).toBe(1);
    });

    it("closes this page's own banner for a thread read on another device", async () => {
      // A page-created Notification is invisible to getNotifications(), so only this page can.
      const pageClosed: string[] = [];
      class PageNotification {
        static readonly permission = "granted";
        constructor(
          _title: string,
          private readonly options: NotificationOptions,
        ) {}
        addEventListener(): void {
          /* no click or close in this case */
        }
        close(): void {
          pageClosed.push(this.options.tag ?? "");
        }
      }
      vi.stubGlobal("Notification", PageNotification);
      setNotificationsEnabled(true);
      setOnScreen(() => false);
      try {
        await openChat("a");
        const b = await openChat("b", { activate: false });
        finish(b, "b", "done");
        expect(
          notifyOffScreen({ kind: "agent_finished", chat_id: "b", title: "b", body: "" }),
        ).toBe(true);

        tabServer.ackRemotely(b, 1);
        expect(pageClosed).toEqual(["marotte:b"]);
      } finally {
        setNotificationsEnabled(false);
        setOnScreen(() => document.visibilityState === "visible");
      }
    });

    it("keeps the dot lit when the page goes away with an unread thread", async () => {
      await openChat("a");
      const b = await openChat("b", { activate: false });
      finish(b, "b", "done");
      await settle();

      window.dispatchEvent(new Event("pagehide"));
      expect(os.badge).toBe(1);
      expect(document.title).toBe("Marotte for Kiro");
    });

    it("closes a read chat's delivered notification and no other", async () => {
      delivered.push("marotte:a", "marotte:b", "marotte:c");
      await openChat("a");
      const b = await openChat("b", { activate: false });
      const c = await openChat("c", { activate: false });
      finish(b, "b", "done");
      finish(c, "c", "done");

      await activateTab(b);
      await vi.waitFor(() => {
        expect(closed).toEqual(["marotte:b"]);
      });
      expect(closed).not.toContain("marotte:c");
    });

    it("closes a run's delivered notification once its pause is resumed", async () => {
      delivered.push("marotte:run:wf-1");
      await openChat("a");
      await openTab({ kind: "run", ref: "wf-1", owns: false, activate: false });
      const run = tabIdFor("run", "wf-1");
      setTabStatus(run, "waiting");
      setTabStatus(run, "working");
      await vi.waitFor(() => {
        expect(closed).toEqual(["marotte:run:wf-1"]);
      });
    });

    it("stops listening once disposed", async () => {
      await stuckBadge();
      dispose?.();
      dispose = null;
      window.dispatchEvent(new Event("focus"));
      worker.dispatchEvent(new MessageEvent("message", { data: { type: "push" } }));
      expect(os.badge).toBe(1);
    });
  });
});
