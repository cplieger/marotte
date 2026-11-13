// The sidebar's Recently closed section over the shipped markup: its rows drawn from the server's
// list, when it shows, its fold, and the History row under it.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { page as browser, userEvent } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { loadCSS, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";
import { settingsPayload } from "./__test-helpers__/settings.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Signal } from "@cplieger/reactive";
import type { Session } from "./types.js";
import type { ChatHeader, RecentClose } from "./wire/types.gen.js";
import type * as Router from "./router.js";
import type * as History from "./history.js";
import type * as Store from "./store.js";
import type * as RunStore from "./run-store.js";
import type * as ComposerState from "./composer-state.js";
import type * as ChatExport from "./chat-export.js";
import type * as Toast from "./toast.js";
import type * as Transport from "./transport.js";
import type * as ApiClient from "./api-client.js";

const h = vi.hoisted(() => ({
  // The chat headers this page holds, by id; a real signal from the store factory below.
  headers: null as unknown as Signal<Map<string, Session>>,
  /** What `/api/chats` answers; `null` is a failed read. */
  chats: [] as ChatHeader[] | null,
  /** The `open_tab` commands already sent at each `/api/chats` read. */
  opensAtRead: [] as number[],
  sentOpens: (): number => 0,
}));

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
// The History page's own content is not this suite's subject; its tab is.
vi.mock("./history.js", async (importOriginal) => ({
  ...(await importOriginal<typeof History>()),
  loadHistoryView: vi.fn(),
  refreshHistoryView: vi.fn(),
  teardownHistoryView: vi.fn(),
}));
vi.mock("./store.js", async (importOriginal) => {
  const { signal } = await import("@cplieger/reactive");
  h.headers = signal(new Map<string, Session>());
  return {
    ...(await importOriginal<typeof Store>()),
    ...(await import("./__test-helpers__/store-mock.js")).storeMock,
    get: (id: string) => h.headers.peek().get(id),
    getSessions: () => [...h.headers.peek().values()],
    // The chat-list read's commit.
    setSessions: vi.fn((list: readonly Session[]) => {
      h.headers.value = new Map(list.map((s) => [s.id, s]));
    }),
    watchSession: (id: string) => h.headers.value.get(id),
    // `closeChatTab`'s store write: the closed chat's Session goes, whatever retention says.
    removeChat: vi.fn((id: string) => {
      const next = new Map(h.headers.peek());
      next.delete(id);
      h.headers.value = next;
    }),
  };
});
vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  runLabelOf: vi.fn(() => ""),
}));
vi.mock("./composer-state.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ComposerState>()),
  retargetComposer: vi.fn(),
  adoptUnownedComposer: vi.fn(),
  restoreFailedSend: vi.fn(),
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  flushComposerDraft: vi.fn(),
  dropComposerState: vi.fn(),
  seedComposerState: vi.fn(),
  adoptRemoteComposerState: vi.fn(),
  noteComposerText: vi.fn(),
  initComposerState: vi.fn(),
  _resetComposerStateForTest: vi.fn(),
}));
vi.mock("./chat-export.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatExport>()),
  downloadChatExport: vi.fn(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  ...(await import("./__test-helpers__/tabs-server.js")).tabTransportMock(),
}));
vi.mock("./api-client.js", async (importOriginal) => {
  const tabs = (await import("./__test-helpers__/tabs-server.js")).tabListRead();
  return {
    ...(await importOriginal<typeof ApiClient>()),
    apiGetTyped: vi.fn((path: string) => {
      if (path !== "/api/chats") {
        return tabs(path);
      }
      h.opensAtRead.push(h.sentOpens());
      return Promise.resolve(h.chats === null ? null : { chats: h.chats.map((c) => ({ ...c })) });
    }),
    apiGet: vi.fn(() => Promise.resolve(null)),
  };
});

import { initSidebarHistory } from "./sidebar-history.js";
import { adoptRetention } from "./retention.js";
import { formatClock, setClock } from "./locale-time.js";
import { iconEl } from "./icon-el.js";
import { ICON_TAB_CHAT } from "./icons.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { closeTab, getActiveTabId, openTab, tabIdFor, _resetForTest } from "./tabs.js";
import * as tabsModule from "./tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { removeChat } from "./store.js";
import { error as toastError } from "./toast.js";
import { IDLE_RUN } from "./run-order.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, settleTabs, tabServer } from "./__test-helpers__/tabs-server.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });
h.sentOpens = () => tabServer.sentOfType("open_tab").length;

// Mounted before the module loads: the chat view's modules read their elements at import.
const shell = document.createElement("div");
{
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  for (const s of doc.querySelectorAll("script")) {
    s.remove();
  }
  for (const n of [...doc.body.childNodes]) {
    shell.appendChild(document.importNode(n, true));
  }
  shell.querySelector("#tab-strip-skeleton")?.remove();
  document.body.appendChild(shell);
}
const { initRecentClosed } = await import("./recent-closed.js");

let style: HTMLStyleElement;

beforeAll(async () => {
  style = mountAppCSS();
  initSidebarHistory();
  initRecentClosed();
  // The History tab imports this lazily and unawaited; loaded here, once the shell exists.
  await import("./history.js");
});

afterAll(() => {
  shell.remove();
  style.remove();
});

beforeEach(() => {
  localStorage.clear();
  h.headers.value = new Map();
  h.chats = [];
  h.opensAtRead = [];
  vi.mocked(toastError).mockClear();
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerTabOpeners({
    chat: {
      prefetch: vi.fn(),
      select: vi.fn(),
      loadFailed: vi.fn(),
      show: vi.fn(),
      refresh: vi.fn(() => Promise.resolve()),
      close: (ref: string) => {
        removeChat(ref);
      },
      dot: () => "",
    },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  adoptRetention(settingsPayload({ chat_retention_days: 7 }));
});

afterEach(async () => {
  vi.unstubAllGlobals();
  if (document.querySelector(".tab-context-menu") !== null) {
    document.querySelector<HTMLElement>(".tab-context-item")?.focus();
    await userEvent.keyboard("{Escape}");
  }
  // A hold a case left pressed: its lift listener goes with the pointer.
  window.dispatchEvent(new PointerEvent("pointercancel", { pointerId: 7, pointerType: "touch" }));
  document.querySelector("#sidebar")?.classList.remove("open");
  tabServer.releaseResponses();
  tabServer.setMode("event-first");
  tabServer.flushFrames();
  for (const s of tabServer.subjects()) {
    await closeTab(s.id);
  }
  await settle();
  for (const r of shell.querySelectorAll("[data-tab-id]")) {
    r.remove();
  }
  await Promise.all(
    el("recent-section")
      .getAnimations()
      .map((a) => a.finished),
  );
  _resetTabsSyncForTest();
  adoptRetention(settingsPayload({ chat_retention_days: 7 }));
  localStorage.clear();
});

function el<T extends HTMLElement>(id: string): T {
  const e = shell.querySelector<T>(`[id="${id}"]`);
  if (e === null) {
    throw new Error(`no #${id}`);
  }
  return e;
}

/** Let a frame's apply, the effects over it and a dispatched command's round trip run out. */
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await new Promise((r) => {
      setTimeout(r, 0);
    });
  }
}

const T0 = Date.UTC(2026, 10, 10, 13, 5);
const entry = (ref: string, name: string, minutesAgo = 0): RecentClose => ({
  ref,
  name,
  closed_at: T0 - minutesAgo * 60_000,
});

/** Adopt `recent` the way boot does: one `GET /api/tabs`. */
async function listed(...recent: RecentClose[]): Promise<void> {
  tabServer.queueList({ tabs: [], recent, version: 1 });
  await listTabs();
  await settle();
}

function holdHeader(id: string, name: string): void {
  const next = new Map(h.headers.peek());
  next.set(id, makeSession({ id, name }));
  h.headers.value = next;
}

const section = (): HTMLElement => el("recent-section");
const list = (): HTMLElement => el("recent-list");
const historyBtn = (): HTMLButtonElement => el<HTMLButtonElement>("history-btn");
const header = (): HTMLElement => {
  const e = section().querySelector<HTMLElement>(":scope > h2.side-section-h");
  if (e === null) {
    throw new Error("no header in #recent-section");
  }
  return e;
};
const toggle = (): HTMLButtonElement => {
  const b = header().querySelector<HTMLButtonElement>("button.side-section-toggle");
  if (b === null) {
    throw new Error("no toggle in #recent-section");
  }
  return b;
};
const rows = (): HTMLButtonElement[] => [
  ...list().querySelectorAll<HTMLButtonElement>(":scope > li > button.recent-row"),
];
const rowNames = (): string[] => rows().map((r) => r.querySelector("bdi")?.textContent ?? "");
/** The words a reader hears after the name: the elements `aria-describedby` names. */
const description = (b: HTMLElement): string =>
  (b.getAttribute("aria-describedby") ?? "")
    .split(/\s+/)
    .filter((id) => id !== "")
    .map((id) => document.getElementById(id)?.textContent.trim() ?? "")
    .join(" ");
const historyLabel = (): string => historyBtn().textContent.trim();
const shown = (e: HTMLElement): boolean => e.getBoundingClientRect().height > 0;

/** Unfold through the header at reduced motion, so the open state is there at the press. */
function unfold(): void {
  stubReducedMotion();
  if (toggle().getAttribute("aria-expanded") === "false") {
    toggle().click();
  }
  vi.unstubAllGlobals();
}

function stubReducedMotion(): void {
  vi.stubGlobal(
    "matchMedia",
    (q: string): MediaQueryList =>
      ({
        matches: q.includes("prefers-reduced-motion"),
        media: q,
        onchange: null,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
        addListener: () => undefined,
        removeListener: () => undefined,
        dispatchEvent: () => false,
      }) as MediaQueryList,
  );
}

describe("Recently closed rows", () => {
  it("draws the server's list in its order, newest first, each row from its own entry", async () => {
    await listed(entry("c-b", "Beta", 1), entry("c-a", "Alpha", 30), entry("c-c", "Gamma", 90));
    unfold();
    expect(rowNames()).toEqual(["Beta", "Alpha", "Gamma"]);
    const [first] = rows();
    if (first === undefined) {
      throw new Error("no row");
    }
    expect(first.parentElement?.tagName).toBe("LI");
    expect(first.type).toBe("button");
    const glyph = first.querySelector("svg");
    expect(glyph?.getAttribute("aria-hidden")).toBe("true");
    const want = iconEl(ICON_TAB_CHAT);
    want.setAttribute("aria-hidden", "true");
    expect(glyph?.outerHTML, "the chat glyph").toBe(want.outerHTML);
    const time = first.querySelector("time");
    expect(time?.textContent).toBe(formatClock(T0 - 60_000));
    expect(time?.dateTime).toBe(new Date(T0 - 60_000).toISOString());
  });

  it("names an entry with an empty name Untitled chat", async () => {
    await listed(entry("c-a", ""));
    unfold();
    expect(rowNames()).toEqual(["Untitled chat"]);
  });

  it("draws a held header's newer name, and follows a rename that arrives later", async () => {
    holdHeader("c-a", "Renamed");
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    unfold();
    expect(rowNames()).toEqual(["Renamed", "Beta"]);
    holdHeader("c-b", "Beta, renamed");
    await settle();
    expect(rowNames()).toEqual(["Renamed", "Beta, renamed"]);
  });

  it("shows a chat closed on this device at once, although the close removed its Session", async () => {
    tabServer.trackRecent({ "c-a": "Alpha" });
    holdHeader("c-a", "Alpha");
    await openTab({ kind: "chat", ref: "c-a" });
    await settle();
    expect(section().hidden).toBe(true);

    await closeTab(tabIdFor("chat", "c-a"));
    await settle();
    expect(h.headers.peek().has("c-a"), "the close removed the Session").toBe(false);
    expect(section().hidden).toBe(false);
    unfold();
    expect(rowNames()).toEqual(["Alpha"]);
  });

  it("shows a chat another device closed at once, from that device's frame", async () => {
    tabServer.trackRecent({ "c-a": "Alpha" });
    await tabServer.seed({ kind: "chat", ref: "c-a" });
    holdHeader("c-a", "Alpha");
    await settle();
    tabServer.closeRemotely(tabServer.idFor("chat", "c-a"));
    await settle();
    expect(h.headers.peek().has("c-a")).toBe(false);
    unfold();
    expect(rowNames()).toEqual(["Alpha"]);
  });

  it("drops a row once a frame takes its entry out", async () => {
    tabServer.trackRecent({ "c-a": "Alpha", "c-b": "Beta" });
    await tabServer.seed({ kind: "chat", ref: "c-a" }, { kind: "chat", ref: "c-b" });
    tabServer.closeRemotely(tabServer.idFor("chat", "c-a"));
    tabServer.closeRemotely(tabServer.idFor("chat", "c-b"));
    await settle();
    unfold();
    expect(rowNames()).toEqual(["Beta", "Alpha"]);
    await openTab({ kind: "chat", ref: "c-b" });
    await settle();
    expect(rowNames()).toEqual(["Alpha"]);
  });
});

describe("when Recently closed shows", () => {
  it("is hidden with no row drawn and shown with one", async () => {
    await listed();
    expect(section().hidden).toBe(true);
    await listed(entry("c-a", "Alpha"));
    expect(section().hidden).toBe(false);
    expect(shown(header())).toBe(true);
    await listed();
    expect(section().hidden).toBe(true);
  });

  it("counts the drawn rows in its header, the words for a screen reader", async () => {
    await listed(entry("c-a", "A"), entry("c-b", "B"), entry("c-c", "C"));
    expect(toggle().querySelector(".side-section-count")?.textContent).toBe("3");
    expect(description(toggle())).toBe("3 chats");
    await listed(entry("c-a", "A"));
    expect(toggle().querySelector(".side-section-count")?.textContent).toBe("1");
    expect(description(toggle())).toBe("1 chat");
  });

  it("is named Recently closed by its header alone", async () => {
    await listed(entry("c-a", "A"));
    const named = (toggle().getAttribute("aria-labelledby") ?? "")
      .split(/\s+/)
      .map((id) => document.getElementById(id)?.textContent.trim() ?? "")
      .join(" ");
    expect(named).toBe("Recently closed");
  });

  it("hides while retention is off with entries held, and shows again when it is back on", async () => {
    await listed(entry("c-a", "Alpha"));
    expect(section().hidden).toBe(false);
    adoptRetention(settingsPayload({ chat_retention_days: 0 }));
    await settle();
    expect(section().hidden).toBe(true);
    expect(tabsModule.recentClosed(), "the entries are still held").toHaveLength(1);
    adoptRetention(settingsPayload({ chat_retention_days: -1 }));
    await settle();
    expect(section().hidden).toBe(false);
  });
});

describe("folding Recently closed", () => {
  const slideEnd = async (box: HTMLElement): Promise<void> => {
    await Promise.all(box.getAnimations().map((a) => a.finished));
    await settle();
  };

  it("starts folded with nothing stored: the header shows, the rows do not", async () => {
    await listed(entry("c-a", "Alpha"));
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
    expect(list().hidden).toBe(true);
    expect(shown(header())).toBe(true);
  });

  it("stores its fold under the recent key of sidebar_folds, keeping the other keys", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ sidebar_folds: { pinned: true } }));
    await listed(entry("c-a", "Alpha"));
    stubReducedMotion();
    toggle().click();
    const stored = (): unknown =>
      (JSON.parse(localStorage.getItem(LS_UI_STATE_KEY) ?? "{}") as Record<string, unknown>)[
        "sidebar_folds"
      ];
    expect(stored()).toEqual({ pinned: true, recent: false });
    expect(list().hidden).toBe(false);
    toggle().click();
    expect(stored()).toEqual({ pinned: true, recent: true });
    expect(list().hidden).toBe(true);
  });

  it("draws the stored unfold when its rows arrive", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ sidebar_folds: { recent: false } }));
    await listed(entry("c-a", "Alpha"));
    expect(toggle().getAttribute("aria-expanded")).toBe("true");
    expect(list().hidden).toBe(false);
  });

  it("slides the section open, never the list, the rows shown at the press", async () => {
    await listed(entry("c-a", "A"), entry("c-b", "B"), entry("c-c", "C"));
    toggle().click();
    expect(toggle().getAttribute("aria-expanded")).toBe("true");
    expect(list().hidden).toBe(false);
    expect(section().getAnimations(), "the section slides").not.toHaveLength(0);
    expect(list().getAnimations(), "the list does not").toHaveLength(0);
    await slideEnd(section());
  });

  it("keeps the rows rendered while it slides shut, to the header's height", async () => {
    await listed(entry("c-a", "A"), entry("c-b", "B"), entry("c-c", "C"));
    unfold();
    const headerHeight = header().getBoundingClientRect().height;
    toggle().click();
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
    const anims = section().getAnimations();
    expect(anims, "the section slides shut").not.toHaveLength(0);
    expect(list().getAnimations()).toHaveLength(0);
    expect(list().hidden, "mid-slide").toBe(false);
    const frames = (anims[0]?.effect as KeyframeEffect | null)?.getKeyframes() ?? [];
    expect(frames.at(-1)?.["blockSize"], "the closed height is the header's").toBe(
      `${String(headerHeight)}px`,
    );
    await slideEnd(section());
    expect(list().hidden).toBe(true);
    expect(section().getBoundingClientRect().height).toBe(headerHeight);
  });

  it("flips at the press under reduced motion, with no animation left", async () => {
    await listed(entry("c-a", "A"), entry("c-b", "B"));
    stubReducedMotion();
    toggle().click();
    expect(list().hidden).toBe(false);
    expect(section().getAnimations()).toHaveLength(0);
    toggle().click();
    expect(list().hidden).toBe(true);
    expect(section().getAnimations()).toHaveLength(0);
    expect(list().getAnimations()).toHaveLength(0);
  });
});

describe("the History row under Recently closed", () => {
  it("reads More history, its name too, and sits after the closed rows while the section shows", async () => {
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    unfold();
    expect(historyLabel()).toBe("More history");
    expect(historyBtn().hasAttribute("aria-label")).toBe(false);
    expect(historyBtn().previousElementSibling).toBe(section());
    const last = rows().at(-1);
    expect(last).toBeDefined();
    expect(historyBtn().getBoundingClientRect().top).toBeGreaterThanOrEqual(
      last?.getBoundingClientRect().bottom ?? Infinity,
    );
  });

  it("stays shown and focusable with the header while the section is folded", async () => {
    await listed(entry("c-a", "Alpha"));
    expect(list().hidden).toBe(true);
    expect(historyLabel()).toBe("More history");
    for (const b of [toggle(), historyBtn()]) {
      expect(shown(b)).toBe(true);
      b.focus();
      expect(document.activeElement).toBe(b);
    }
  });

  it("reads History with no row drawn, and with retention off while entries are held", async () => {
    await listed();
    expect(historyLabel()).toBe("History");
    await listed(entry("c-a", "Alpha"));
    expect(historyLabel()).toBe("More history");
    adoptRetention(settingsPayload({ chat_retention_days: 0 }));
    await settle();
    expect(historyLabel()).toBe("History");
    adoptRetention(settingsPayload({ chat_retention_days: 7 }));
    await settle();
    expect(historyLabel()).toBe("More history");
  });

  it.each([
    { state: "with no row", arrange: () => listed() },
    { state: "folded", arrange: () => listed(entry("c-a", "Alpha")) },
    {
      state: "unfolded",
      arrange: async () => {
        await listed(entry("c-a", "Alpha"));
        unfold();
      },
    },
    {
      state: "with retention off",
      arrange: async () => {
        await listed(entry("c-a", "Alpha"));
        adoptRetention(settingsPayload({ chat_retention_days: 0 }));
        await settle();
      },
    },
  ])("opens the History tab when pressed $state", async ({ arrange }) => {
    await arrange();
    historyBtn().click();
    await settle();
    await settleTabs();
    const id = tabIdFor("history");
    expect(id).not.toBe("");
    expect(getActiveTabId()).toBe(id);
  });
});

describe("a Recently closed row's box", () => {
  afterEach(() => {
    delete document.documentElement.dataset["pointer"];
  });

  /** An open chat tab's drawn row, the height both rows below must meet. */
  async function tabRowHeight(): Promise<number> {
    holdHeader("c-open", "Open one");
    await openTab({ kind: "chat", ref: "c-open" });
    await settle();
    await settleTabs();
    await new Promise((r) => requestAnimationFrame(r));
    const tab = shell.querySelector<HTMLElement>(`[data-tab-id="${tabIdFor("chat", "c-open")}"]`);
    expect(tab, "the open tab's row is drawn").not.toBeNull();
    return tab?.getBoundingClientRect().height ?? 0;
  }

  it("is as tall as its header while the list is folded, with no padding, border or gap of its own", async () => {
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    expect(list().hidden).toBe(true);
    const cs = getComputedStyle(section());
    for (const p of [
      "padding-block-start",
      "padding-block-end",
      "border-block-start-width",
      "border-block-end-width",
    ]) {
      expect(cs.getPropertyValue(p), p).toBe("0px");
    }
    expect(["normal", "0px"]).toContain(cs.rowGap);
    expect(section().getBoundingClientRect().height).toBe(header().getBoundingClientRect().height);
  });

  it.each(["fine", "coarse"])(
    "draws its rows and the History row at a tab row's height on a %s pointer",
    async (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const tabH = await tabRowHeight();
      expect(tabH).toBeGreaterThan(0);
      await listed(entry("c-a", "Alpha"), entry("c-b", "A much longer chat name that ellipsizes"));
      unfold();
      for (const r of rows()) {
        expect(r.getBoundingClientRect().height, rowNames().join()).toBe(tabH);
      }
      expect(historyBtn().getBoundingClientRect().height, "More history").toBe(tabH);
    },
  );
});

function chatHeader(id: string, name: string): ChatHeader {
  return {
    id,
    name,
    run: IDLE_RUN,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    created_at: 0,
    updated_at: 0,
    turn_count: 0,
  };
}

/** Each chat open then closed by another device: listed in Recently closed, its Session gone from
 *  this page, and named by the chat list's read. `pinned` names the chats closed while pinned. */
async function closedElsewhere(
  names: Readonly<Record<string, string>>,
  pinned: readonly string[] = [],
): Promise<void> {
  tabServer.trackRecent(names);
  const refs = Object.keys(names);
  await tabServer.seed(
    ...refs.map((ref) => ({ kind: "chat" as const, ref, pinned: pinned.includes(ref) })),
  );
  for (const ref of [...refs].reverse()) {
    tabServer.closeRemotely(tabServer.idFor("chat", ref));
  }
  h.chats = refs.map((ref) => chatHeader(ref, names[ref] ?? ""));
  await settle();
}

const rowFor = (name: string): HTMLButtonElement => {
  const r = rows().find((b) => b.querySelector("bdi")?.textContent === name);
  if (r === undefined) {
    throw new Error(`no Recently closed row for ${name}`);
  }
  return r;
};
const tabRow = (ref: string): HTMLElement | null =>
  shell.querySelector<HTMLElement>(`[data-tab-id="${tabIdFor("chat", ref)}"]`);
const menus = (): HTMLElement[] => [...document.querySelectorAll<HTMLElement>(".tab-context-menu")];
const menuItems = (): string[] =>
  menus().flatMap((m) =>
    [...m.querySelectorAll<HTMLElement>("[role=menuitem]")].map((i) => i.textContent.trim()),
  );
const opens = (): readonly { payload: Record<string, unknown> }[] =>
  tabServer.sentOfType("open_tab");
const prompt = (): HTMLTextAreaElement => el<HTMLTextAreaElement>("prompt-input");
const sleep = (ms: number): Promise<void> =>
  new Promise((r) => {
    setTimeout(r, ms);
  });
/** The design's touch hold, plus slack for the timer. */
const HOLD = 520;

function touch(type: string, target: Element, pointerId = 7): void {
  const box = target.getBoundingClientRect();
  target.dispatchEvent(
    new PointerEvent(type, {
      pointerType: "touch",
      pointerId,
      isPrimary: true,
      bubbles: true,
      cancelable: true,
      clientX: box.left + 10,
      clientY: box.top + 5,
    }),
  );
}

/** A synthetic click carries no pointerdown, as a lift's click and HTML's activation behavior do. */
function clickOn(target: Element): void {
  target.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
}

describe("reopening a Recently closed row", () => {
  it("dispatches one open_tab that owns the chat with no parent, busy until it lands, and activates it", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    el("sidebar").classList.add("open");
    const row = rowFor("Alpha");
    row.click();
    expect(row.getAttribute("aria-busy")).toBe("true");
    row.click();
    await settle();
    await settleTabs();

    expect(opens()).toHaveLength(1);
    const payload = opens()[0]?.payload ?? {};
    expect(payload["kind"]).toBe("chat");
    expect(payload["ref"]).toBe("c-a");
    expect(payload["owns"]).toBe(true);
    expect(payload["parent"] ?? "", "no parent").toBe("");
    expect(h.opensAtRead, "the second press started no second reopen").toHaveLength(1);
    expect(getActiveTabId()).toBe(tabIdFor("chat", "c-a"));
    expect(el("sidebar").classList.contains("open"), "the phone drawer is dismissed").toBe(false);
  });

  it("reads the chat list before the open_tab for a chat this page's close removed", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    expect(h.headers.peek().has("c-a")).toBe(false);
    unfold();
    rowFor("Alpha").click();
    await settle();
    expect(h.opensAtRead, "one read, with no open_tab sent yet").toEqual([0]);
    expect(opens()).toHaveLength(1);
    expect(h.headers.peek().get("c-a")?.name).toBe("Alpha");
  });

  it("reads nothing for a chat the store still holds", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    holdHeader("c-a", "Alpha");
    unfold();
    rowFor("Alpha").click();
    await settle();
    expect(h.opensAtRead).toEqual([]);
    expect(opens()).toHaveLength(1);
  });

  it("still sends the open_tab when the chat list read fails", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    h.chats = null;
    unfold();
    rowFor("Alpha").click();
    await settle();
    expect(h.opensAtRead).toHaveLength(1);
    expect(opens()).toHaveLength(1);
    expect(getActiveTabId()).toBe(tabIdFor("chat", "c-a"));
  });

  it("keeps the row until the frame takes it out", async () => {
    await closedElsewhere({ "c-a": "Alpha", "c-b": "Beta" });
    unfold();
    tabServer.setMode("manual");
    rowFor("Alpha").click();
    await settle();
    expect(opens()).toHaveLength(1);
    expect(rowNames(), "answered, with its frame withheld").toEqual(["Alpha", "Beta"]);
    tabServer.flushFrames();
    await settle();
    expect(rowNames()).toEqual(["Beta"]);
  });

  it("names the remedy at 48 tabs and keeps the row", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    tabServer.failNext("open_tab", 409, "too many tabs", "tabs_full");
    const row = rowFor("Alpha");
    row.click();
    await settle();
    expect(vi.mocked(toastError).mock.calls.map((c) => c[0])).toEqual([
      "Close a tab first, because this workspace has too many open.",
    ]);
    expect(rowNames()).toEqual(["Alpha"]);
    expect(row.hasAttribute("aria-busy")).toBe(false);
    expect(tabIdFor("chat", "c-a")).toBe("");
  });

  it("shows the open's not-found notice for a chat deleted meanwhile", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    tabServer.failNext("open_tab", 404, "no such chat");
    rowFor("Alpha").click();
    await settle();
    expect(vi.mocked(toastError).mock.calls.map((c) => c[0])).toEqual(["Could not open that tab"]);
    expect(tabIdFor("chat", "c-a")).toBe("");
  });

  it("reopens on Enter and on Space", async () => {
    await closedElsewhere({ "c-a": "Alpha", "c-b": "Beta" });
    unfold();
    rowFor("Alpha").focus();
    await userEvent.keyboard("{Enter}");
    await settle();
    expect(tabIdFor("chat", "c-a")).not.toBe("");
    rowFor("Beta").focus();
    await userEvent.keyboard(" ");
    await settle();
    expect(tabIdFor("chat", "c-b")).not.toBe("");
    expect(opens()).toHaveLength(2);
  });
});

describe("a Recently closed row to a screen reader and the keyboard", () => {
  it("is named Reopen <name> and described by its close time", async () => {
    await listed(entry("c-a", "Alpha", 5));
    unfold();
    const row = browser.getByRole("button", { name: "Reopen Alpha", exact: true }).element();
    expect(row).toBe(rowFor("Alpha"));
    expect(description(row as HTMLElement)).toBe(`Closed at ${formatClock(T0 - 5 * 60_000)}`);
  });

  it("redraws its time, description and tooltip on a Clock change", async () => {
    await listed(entry("c-a", "Alpha", 5));
    unfold();
    const row = rowFor("Alpha");
    const drawn = (): string[] => [
      row.querySelector("time")?.textContent ?? "",
      description(row),
      row.dataset["tooltip"] ?? "",
    ];
    try {
      setClock("h23");
      await settle();
      const h23 = drawn();
      expect(h23[0]).toMatch(/^\d{1,2}:\d{2}$/);
      setClock("h12");
      await settle();
      const h12 = drawn();
      const clock12 = formatClock(T0 - 5 * 60_000);
      expect(clock12).not.toBe(h23[0]);
      expect(h12).toEqual([clock12, `Closed at ${clock12}`, `Reopen Alpha\nClosed at ${clock12}`]);
    } finally {
      setClock("auto");
    }
  });

  it("makes each shown row one Tab stop, then the History row", async () => {
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    unfold();
    toggle().focus();
    const seen: Element[] = [];
    for (let i = 0; i < 3; i++) {
      await userEvent.keyboard("{Tab}");
      seen.push(document.activeElement ?? document.body);
    }
    expect(seen).toEqual([rowFor("Alpha"), rowFor("Beta"), historyBtn()]);
  });

  it("makes no row a Tab stop while folded: Tab goes from the header to the History row", async () => {
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    expect(list().hidden).toBe(true);
    toggle().focus();
    await userEvent.keyboard("{Tab}");
    expect(document.activeElement).toBe(historyBtn());
  });
});

describe("the Recently closed row menu", () => {
  it.each([
    {
      how: "a right-click",
      open: (row: HTMLElement): Promise<void> => userEvent.click(row, { button: "right" }),
    },
    {
      how: "the context-menu key",
      open: async (row: HTMLElement): Promise<void> => {
        row.focus();
        await userEvent.keyboard("{ContextMenu}");
      },
    },
    {
      how: "Shift+F10",
      open: async (row: HTMLElement): Promise<void> => {
        row.focus();
        await userEvent.keyboard("{Shift>}{F10}{/Shift}");
      },
    },
    {
      how: "a 500 ms touch hold",
      open: async (row: HTMLElement): Promise<void> => {
        touch("pointerdown", row);
        await sleep(HOLD);
      },
    },
  ])("opens on $how with Reopen its only item", async ({ open }) => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    await open(rowFor("Alpha"));
    await settle();
    expect(menus()).toHaveLength(1);
    expect(menuItems()).toEqual(["Reopen"]);
    expect(opens(), "opening the menu reopens nothing").toHaveLength(0);
  });

  it("reopens from its Reopen", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    rowFor("Alpha").focus();
    await userEvent.keyboard("{Shift>}{F10}{/Shift}");
    await userEvent.keyboard("{Enter}");
    await settle();
    expect(opens()).toHaveLength(1);
    expect(menus()).toHaveLength(0);
    expect(getActiveTabId()).toBe(tabIdFor("chat", "c-a"));
  });

  it("returns focus to the row's button on Escape", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    const row = rowFor("Alpha");
    row.focus();
    await userEvent.keyboard("{Shift>}{F10}{/Shift}");
    expect(menus()[0]?.contains(document.activeElement)).toBe(true);
    await userEvent.keyboard("{Escape}");
    expect(menus()).toHaveLength(0);
    expect(document.activeElement).toBe(row);
  });

  it("leaves focus in the composer after an outside click into it", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    rowFor("Alpha").focus();
    await userEvent.keyboard("{Shift>}{F10}{/Shift}");
    await userEvent.click(prompt());
    expect(menus()).toHaveLength(0);
    expect(document.activeElement).toBe(prompt());
  });

  it("leaves focus on another tab row an outside click pressed", async () => {
    await tabServer.seed({ kind: "chat", ref: "c-open" });
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    rowFor("Alpha").focus();
    await userEvent.keyboard("{Shift>}{F10}{/Shift}");
    const other = tabRow("c-open");
    expect(other).not.toBeNull();
    await userEvent.click(other as HTMLElement);
    expect(menus()).toHaveLength(0);
    expect(document.activeElement).toBe(other);
  });

  it("leaves focus on <body> after an outside click on a spot that takes no focus", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    const row = rowFor("Alpha");
    row.focus();
    await userEvent.keyboard("{Shift>}{F10}{/Shift}");
    const logo = shell.querySelector<HTMLElement>(".sidebar-header .logo-name");
    expect(logo).not.toBeNull();
    await userEvent.click(logo as HTMLElement);
    expect(menus()).toHaveLength(0);
    expect(document.activeElement).toBe(document.body);
  });
});

describe("the touch hold on a Recently closed row", () => {
  it("selects no text", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    expect(getComputedStyle(rowFor("Alpha")).userSelect).toBe("none");
    // WebKit's callout is iOS-only: Chromium computes no such property, so the declaration is read.
    expect(ruleBody(loadCSS("12-tabs.css"), ".recent-row")).toMatch(
      /(?:^|[;{\s])-webkit-touch-callout:\s*none;/,
    );
  });

  it("keeps its menu open through the lift, whose click reopens nothing", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    const row = rowFor("Alpha");
    touch("pointerdown", row);
    await sleep(HOLD);
    touch("pointerup", row);
    clickOn(row);
    await settle();
    expect(menus()).toHaveLength(1);
    expect(opens()).toHaveLength(0);
  });

  it("builds one menu when the platform's contextmenu follows the hold", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    const row = rowFor("Alpha");
    touch("pointerdown", row);
    await sleep(HOLD);
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    await settle();
    expect(menus()).toHaveLength(1);
  });

  it("builds one menu when the platform's contextmenu comes before the hold's timer", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    const row = rowFor("Alpha");
    touch("pointerdown", row);
    await sleep(100);
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    await sleep(HOLD);
    expect(menus()).toHaveLength(1);
  });

  it("lets the next tap on the row reopen it", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    const row = rowFor("Alpha");
    touch("pointerdown", row);
    await sleep(HOLD);
    touch("pointerup", row);
    clickOn(row);
    await sleep(100);
    touch("pointerdown", row, 8);
    touch("pointerup", row, 8);
    clickOn(row);
    await settle();
    expect(opens()).toHaveLength(1);
  });

  it.each([
    {
      how: "Enter on its focused Reopen",
      choose: async (): Promise<void> => {
        await userEvent.keyboard("{Enter}");
      },
    },
    {
      how: "a click on Reopen with no pointerdown",
      choose: (): Promise<void> => {
        const item = menus()[0]?.querySelector<HTMLElement>("[role=menuitem]") ?? null;
        expect(item).not.toBeNull();
        clickOn(item as HTMLElement);
        return Promise.resolve();
      },
    },
  ])("reopens from $how 100 ms after a lift that sent no click", async ({ choose }) => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    const row = rowFor("Alpha");
    touch("pointerdown", row);
    await sleep(HOLD);
    touch("pointerup", row);
    await sleep(100);
    expect(menus()[0]?.contains(document.activeElement)).toBe(true);
    await choose();
    await settle();
    expect(opens()).toHaveLength(1);
  });
});

describe("focus after a reopen", () => {
  it.each(["event-first", "response-first"] as const)(
    "lands on the reopened tab's row in its section, the frame %s",
    async (mode) => {
      await closedElsewhere({ "c-a": "Alpha" });
      unfold();
      tabServer.setMode(mode);
      rowFor("Alpha").focus();
      await userEvent.keyboard("{Enter}");
      await settle();
      const row = tabRow("c-a");
      expect(row).not.toBeNull();
      expect(row?.parentElement?.id).toBe("tab-list");
      expect(document.activeElement).toBe(row);
    },
  );

  it("lands on the reopened row in Pinned for a chat closed while pinned", async () => {
    await closedElsewhere({ "c-p": "Pinned one" }, ["c-p"]);
    unfold();
    rowFor("Pinned one").focus();
    await userEvent.keyboard("{Enter}");
    await settle();
    const row = tabRow("c-p");
    expect(row?.parentElement?.id).toBe("pinned-list");
    expect(document.activeElement).toBe(row);
  });

  it.each(["event-first", "response-first"] as const)(
    "lands on the reopened tab's row after the menu's Reopen, the frame %s",
    async (mode) => {
      await closedElsewhere({ "c-a": "Alpha" });
      unfold();
      tabServer.setMode(mode);
      rowFor("Alpha").focus();
      await userEvent.keyboard("{ContextMenu}");
      await userEvent.keyboard("{Enter}");
      await settle();
      expect(document.activeElement).toBe(tabRow("c-a"));
    },
  );

  it("lands on the composer with the phone layout", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    await browser.viewport(390, 844);
    try {
      el("sidebar").classList.add("open");
      rowFor("Alpha").focus();
      await userEvent.keyboard("{Enter}");
      await settle();
      expect(el("sidebar").classList.contains("open")).toBe(false);
      expect(document.activeElement).toBe(prompt());
    } finally {
      await browser.viewport(1280, 720);
    }
  });

  it("leaves focus where the person moved it while the reopen ran", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    tabServer.holdResponses();
    rowFor("Alpha").focus();
    await userEvent.keyboard("{Enter}");
    await settle();
    historyBtn().focus();
    tabServer.releaseResponses();
    await settle();
    expect(tabIdFor("chat", "c-a")).not.toBe("");
    expect(document.activeElement).toBe(historyBtn());
  });
});
