// The sidebar's History section over the shipped markup: its rows drawn from the server's list of
// recent closes, when it shows, its fold, and the More row that ends it.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { page as browser, userEvent } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { loadCSS, manifestSheets, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";
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
// The History page's own content is not this suite's subject; its row is.
vi.mock("./history.js", async (importOriginal) => ({
  ...(await importOriginal<typeof History>()),
  loadHistoryView: vi.fn(),
  refreshHistoryView: vi.fn(),
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

import { adoptRetention } from "./retention.js";
import { formatClock, formatShortDate, formatWeekdayStamp, setClock } from "./locale-time.js";
import { iconEl } from "./icon-el.js";
import { ICON_TAB_CHAT, ICON_TRASH } from "./icons.js";
import { TOUCH_SLOP_PX } from "./context-hold.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";
import {
  closeTab,
  getActiveTabId,
  historyPaneTab,
  openTab,
  setTabStatus,
  tabIdFor,
  _resetForTest,
} from "./tabs.js";
import * as tabsModule from "./tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { removeChat } from "./store.js";
import { error as toastError } from "./toast.js";
import { IDLE_RUN } from "./run-order.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, settleTabs, tabServer } from "./__test-helpers__/tabs-server.js";
import { pressAfterScroll, SCROLLER_RESTED_MS } from "./__test-helpers__/fling.js";
import { until } from "./__test-helpers__/until.js";
import { configureApi } from "@cplieger/reactive/actions";
import { showOnlyView } from "./view-swap.js";

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
const { initHistorySection } = await import("./history-section.js");

let style: HTMLStyleElement;

beforeAll(async () => {
  style = mountAppCSS();
  initHistorySection();
  // Showing History imports this lazily and unawaited; loaded here, once the shell exists.
  await import("./history.js");
});

afterAll(() => {
  shell.remove();
  style.remove();
});

beforeEach(() => {
  vi.setSystemTime(T0);
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
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn(), move: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  adoptRetention(settingsPayload({ chat_retention_days: 7 }));
});

afterEach(async () => {
  vi.useRealTimers();
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
    el("history-list")
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

/** Now in every case, 13:05 local time, so a close minutes or hours before it falls on its day. */
const T0 = new Date(2026, 10, 10, 13, 5).getTime();
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

const section = (): HTMLElement => el("history-section");
const list = (): HTMLElement => el("history-list");
const more = (): HTMLButtonElement => el<HTMLButtonElement>("history-more");
const header = (): HTMLElement => {
  const e = section().querySelector<HTMLElement>(":scope > h2.side-section-h");
  if (e === null) {
    throw new Error("no header in #history-section");
  }
  return e;
};
const toggle = (): HTMLButtonElement => {
  const b = header().querySelector<HTMLButtonElement>("button.side-section-toggle");
  if (b === null) {
    throw new Error("no toggle in #history-section");
  }
  return b;
};
const rows = (): HTMLButtonElement[] => [
  ...list().querySelectorAll<HTMLButtonElement>(":scope > li > button.history-row"),
];
const rowNames = (): string[] => rows().map((r) => r.querySelector("bdi")?.textContent ?? "");
/** The words a reader hears after the name: the elements `aria-describedby` names. */
const description = (b: HTMLElement): string =>
  (b.getAttribute("aria-describedby") ?? "")
    .split(/\s+/)
    .filter((id) => id !== "")
    .map((id) => document.getElementById(id)?.textContent.trim() ?? "")
    .join(" ");
const shown = (e: HTMLElement): boolean => e.getBoundingClientRect().height > 0;
/** Whether the fold hides the rows and More: the list marked folded, More drawn at no height. */
const folded = (): boolean => list().hasAttribute("data-folded") && !shown(more());

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

describe("closed rows", () => {
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
    const dot = first.firstElementChild;
    expect(dot?.matches('span.tab-status-dot[data-status="closed"][aria-hidden="true"]')).toBe(
      true,
    );
    const time = first.querySelector("time");
    expect(time?.textContent).toBe(formatClock(T0 - 60_000));
    expect(time?.dateTime).toBe(new Date(T0 - 60_000).toISOString());
  });

  it("draws a full list of 10 closes, newest first, the server's cap and order", async () => {
    const ten = Array.from({ length: 10 }, (_, i) =>
      entry(`c-${String(i)}`, `Chat ${String(i)}`, i * 30),
    );
    await listed(...ten);
    unfold();
    expect(rowNames()).toEqual(ten.map((e) => e.name));
    expect(toggle().querySelector(".side-section-count")?.textContent).toBe("10");
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

  it("draws again a row a wider window's frame brings back", async () => {
    tabServer.emitRecent([entry("c-b", "Beta", 60)]);
    await settle();
    unfold();
    expect(rowNames()).toEqual(["Beta"]);
    tabServer.emitRecent([entry("c-b", "Beta", 60), entry("c-a", "Alpha", 30 * 60)]);
    await settle();
    expect(rowNames()).toEqual(["Beta", "Alpha"]);
  });
});

// An open chat's row leads with its status dot; a closed row draws that same dot as an empty ring.
describe("a closed row's leading mark", () => {
  const leading = (box: Element): HTMLElement => {
    const d = box.querySelector<HTMLElement>(".tab-status-dot");
    if (d === null) {
      throw new Error("no status dot");
    }
    return d;
  };

  it("draws no chat glyph, only the open row's dot", async () => {
    await listed(entry("c-a", "Alpha", 1));
    unfold();
    const [row] = rows();
    if (row === undefined) {
      throw new Error("no row");
    }
    const chat = iconEl(ICON_TAB_CHAT).innerHTML;
    for (const svg of row.querySelectorAll("svg")) {
      expect(svg.innerHTML, "the chat glyph").not.toBe(chat);
    }
    expect(row.querySelectorAll(".tab-status-dot")).toHaveLength(1);
    expect(row.firstElementChild).toBe(leading(row));
  });

  it("matches an open chat row's dot in size and place, hollow where an open one is filled", async () => {
    tabServer.trackRecent({ "c-a": "Alpha", "c-b": "Beta" });
    await tabServer.seed({ kind: "chat", ref: "c-a" }, { kind: "chat", ref: "c-b" });
    tabServer.closeRemotely(tabServer.idFor("chat", "c-a"));
    await settle();
    unfold();
    const open = tabRow("c-b");
    const [row] = rows();
    if (open === null || row === undefined) {
      throw new Error("no rows");
    }
    setTabStatus(tabIdFor("chat", "c-b"), "done");
    const openDot = leading(open).getBoundingClientRect();
    const closedDot = leading(row).getBoundingClientRect();
    expect(closedDot.width).toBeGreaterThan(0);
    expect(closedDot.width).toBeCloseTo(openDot.width, 2);
    expect(closedDot.height).toBeCloseTo(openDot.height, 2);
    expect(closedDot.left - row.getBoundingClientRect().left, "the same inset").toBeCloseTo(
      openDot.left - open.getBoundingClientRect().left,
      2,
    );
    const filled = getComputedStyle(leading(open));
    const ring = getComputedStyle(leading(row));
    expect(filled.backgroundColor).not.toBe("rgba(0, 0, 0, 0)");
    expect(ring.backgroundColor, "hollow").toBe("rgba(0, 0, 0, 0)");
    expect(ring.borderTopStyle).toBe("solid");
    expect(Number.parseFloat(ring.borderTopWidth)).toBeGreaterThan(0);
  });

  it("says closed in words, not by the ring alone", async () => {
    await listed(entry("c-a", "Alpha", 5));
    unfold();
    const [row] = rows();
    if (row === undefined) {
      throw new Error("no row");
    }
    expect(description(row)).toMatch(/^Closed at /);
  });
});

describe("when the History section shows", () => {
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

  it("is named History by its header alone", async () => {
    await listed(entry("c-a", "A"));
    const named = (toggle().getAttribute("aria-labelledby") ?? "")
      .split(/\s+/)
      .map((id) => document.getElementById(id)?.textContent.trim() ?? "")
      .join(" ");
    expect(named).toBe("History");
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

describe("folding the History section", () => {
  it("starts folded with nothing stored: the header shows, the rows do not", async () => {
    await listed(entry("c-a", "Alpha"));
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
    expect(folded()).toBe(true);
    expect(shown(header())).toBe(true);
  });

  it("stores its fold under the history key of sidebar_folds, keeping the other keys", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ sidebar_folds: { pinned: true } }));
    await listed(entry("c-a", "Alpha"));
    stubReducedMotion();
    toggle().click();
    const stored = (): unknown =>
      (JSON.parse(localStorage.getItem(LS_UI_STATE_KEY) ?? "{}") as Record<string, unknown>)[
        "sidebar_folds"
      ];
    expect(stored()).toEqual({ pinned: true, history: false });
    expect(folded()).toBe(false);
    toggle().click();
    expect(stored()).toEqual({ pinned: true, history: true });
    expect(folded()).toBe(true);
  });

  it("draws the stored unfold when its rows arrive", async () => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ sidebar_folds: { history: false } }));
    await listed(entry("c-a", "Alpha"));
    expect(toggle().getAttribute("aria-expanded")).toBe("true");
    expect(folded()).toBe(false);
  });

  it("hides a row a frame draws while folded, and shows it on the unfold", async () => {
    await listed(entry("c-a", "Alpha"));
    await listed(entry("c-b", "Beta"), entry("c-a", "Alpha"));
    expect(rows().map(shown)).toEqual([false, false]);
    unfold();
    expect(rows().map(shown)).toEqual([true, true]);
  });
});

describe("the More row that ends the History section", () => {
  it("is the list's last row, after the closed rows, named More history and reading More", async () => {
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    unfold();
    expect(more().textContent.trim()).toBe("More");
    const named = browser.getByRole("button", { name: "More history", exact: true }).element();
    expect(named).toBe(more());
    expect(more().closest("ul")).toBe(list());
    expect(list().lastElementChild?.contains(more())).toBe(true);
    const last = rows().at(-1);
    expect(last).toBeDefined();
    expect(more().getBoundingClientRect().top).toBeGreaterThanOrEqual(
      last?.getBoundingClientRect().bottom ?? Infinity,
    );
  });

  it("is hidden with the rows while the section is folded, leaving the header", async () => {
    await listed(entry("c-a", "Alpha"));
    expect(folded()).toBe(true);
    expect(shown(toggle())).toBe(true);
    unfold();
    expect(shown(more())).toBe(true);
  });

  it("shows History with no tab when pressed, and draws as the selected entry", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    const opens = h.sentOpens();
    el("sidebar").classList.add("open");
    more().click();
    expect(el("sidebar").classList.contains("open"), "the phone drawer closes").toBe(false);
    await settle();
    await settleTabs();
    expect(historyPaneTab()).toBe("chats");
    expect(getActiveTabId()).toBe("");
    expect(h.sentOpens(), "History opens no tab").toBe(opens);
    expect(more().classList.contains("active")).toBe(true);
    expect(more().getAttribute("aria-current")).toBe("page");
    expect(more().hasAttribute("aria-pressed"), "a destination, not a toggle").toBe(false);
  });

  it("keeps History on a second press", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    more().click();
    await settle();
    more().click();
    await settle();
    expect(historyPaneTab()).toBe("chats");
    expect(more().getAttribute("aria-current")).toBe("page");
  });
});

describe("no way to History when nothing was closed", () => {
  /** Every sidebar control a reader could take for History. */
  const historyEntries = (): Element[] =>
    [...el("sidebar").querySelectorAll("button, a")].filter((b) => {
      const name = (b.getAttribute("aria-label") ?? b.textContent).trim();
      return /history/i.test(name) && shown(b as HTMLElement);
    });

  it("draws no section and no History entry with no closed chat", async () => {
    await listed();
    expect(section().hidden).toBe(true);
    expect(shown(section())).toBe(false);
    expect(historyEntries()).toEqual([]);
  });

  it("draws no section at Days kept 0 though the server lists closed chats", async () => {
    adoptRetention(settingsPayload({ chat_retention_days: 0 }));
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    expect(tabsModule.recentClosed(), "the entries are held").toHaveLength(2);
    expect(section().hidden).toBe(true);
    expect(historyEntries()).toEqual([]);
  });

  it("ships no standalone History button, in the markup or the styles", () => {
    expect(shell.querySelector("#history-btn")).toBeNull();
    expect(shell.querySelector(".side-history-row, .side-history-label")).toBeNull();
    expect(el("tab-sections").lastElementChild).toBe(section());
    const stale = manifestSheets().filter((sh) =>
      /side(bar)?-history|recent-(row|list)/.test(sh.css),
    );
    expect(stale.map((sh) => sh.name)).toEqual([]);
  });
});

describe("a closed row's box", () => {
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

  it("is its header over the list's top inset while folded, with no padding, border or gap of its own", async () => {
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    expect(folded()).toBe(true);
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
    // A folded Pinned with no active row keeps the same inset: one fold, one closed box.
    const inset = parseFloat(getComputedStyle(list()).paddingBlockStart);
    expect(inset, "the list's top inset").toBeGreaterThan(0);
    expect(list().getBoundingClientRect().height).toBe(inset);
    expect(section().getBoundingClientRect().height).toBe(
      header().getBoundingClientRect().height + inset,
    );
  });

  it.each(["fine", "coarse"])(
    "draws its rows and More at a tab row's height on a %s pointer",
    async (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const tabH = await tabRowHeight();
      expect(tabH).toBeGreaterThan(0);
      await listed(entry("c-a", "Alpha"), entry("c-b", "A much longer chat name that ellipsizes"));
      unfold();
      for (const r of rows()) {
        expect(r.getBoundingClientRect().height, rowNames().join()).toBe(tabH);
      }
      expect(more().getBoundingClientRect().height, "More").toBe(tabH);
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

/** Each chat open then closed by another device: listed in the History section, its Session gone from
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
    throw new Error(`no closed row for ${name}`);
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
const deleteFor = (name: string): HTMLButtonElement => {
  const b = rowFor(name).parentElement?.querySelector<HTMLButtonElement>(
    ":scope > button.history-row-delete",
  );
  if (b === null || b === undefined) {
    throw new Error(`no Delete beside the closed row for ${name}`);
  }
  return b;
};
const sleep = (ms: number): Promise<void> =>
  new Promise((r) => {
    setTimeout(r, ms);
  });
/** The 500 ms touch hold, plus slack for the timer. */
const HOLD = 520;

function touchEvent(type: string, target: Element, pointerId = 7): PointerEvent {
  const box = target.getBoundingClientRect();
  return new PointerEvent(type, {
    pointerType: "touch",
    pointerId,
    isPrimary: true,
    bubbles: true,
    cancelable: true,
    clientX: box.left + 10,
    clientY: box.top + 5,
  });
}

function touch(type: string, target: Element, pointerId = 7): void {
  target.dispatchEvent(touchEvent(type, target, pointerId));
}

function touchAfterFling(row: Element): void {
  pressAfterScroll(el("tab-sections"), row, touchEvent("pointerdown", row));
}

/** A synthetic click carries no pointerdown, as a lift's click and HTML's activation behavior do. */
function clickOn(target: Element): void {
  target.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
}

describe("reopening a closed row", () => {
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

describe("a closed row to a screen reader and the keyboard", () => {
  it("is named Reopen <name> and described by its close time", async () => {
    await listed(entry("c-a", "Alpha", 5));
    unfold();
    const row = browser.getByRole("button", { name: "Reopen Alpha", exact: true }).element();
    expect(row).toBe(rowFor("Alpha"));
    expect(description(row as HTMLElement)).toBe(`Closed at ${formatClock(T0 - 5 * 60_000)}`);
  });

  it("names a close from an earlier day by its date, and the instant to a reader", async () => {
    const yesterday = new Date(2026, 10, 9, 22, 55).getTime();
    await listed({ ref: "c-a", name: "Alpha", closed_at: yesterday });
    unfold();
    const row = rowFor("Alpha");
    const said = `Closed ${formatWeekdayStamp(yesterday)}`;
    expect(row.querySelector("time")?.textContent).toBe(formatShortDate(yesterday));
    expect(row.querySelector("time")?.dateTime).toBe(new Date(yesterday).toISOString());
    expect(description(row)).toBe(said);
    expect(row.dataset["tooltip"]).toBe(`Reopen Alpha\n${said}`);
  });

  it("carries the year on a close from the year before", async () => {
    vi.setSystemTime(new Date(2027, 0, 2, 9, 0));
    const lastYear = new Date(2026, 11, 30, 18, 0).getTime();
    await listed({ ref: "c-a", name: "Alpha", closed_at: lastYear });
    unfold();
    expect(rowFor("Alpha").querySelector("time")?.textContent).toBe(
      formatShortDate(lastYear, { year: true }),
    );
  });

  it("turns a close's clock into its date at midnight, with no new list", async () => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "Date"],
      shouldAdvanceTime: true,
      now: new Date(2026, 10, 12, 23, 58),
    });
    const closed = new Date(2026, 10, 12, 23, 50).getTime();
    await listed({ ref: "c-a", name: "Alpha", closed_at: closed });
    unfold();
    const row = rowFor("Alpha");
    expect(row.querySelector("time")?.textContent).toBe(formatClock(closed));
    vi.advanceTimersByTime(3 * 60_000);
    await settle();
    expect(row.querySelector("time")?.textContent).toBe(formatShortDate(closed));
    expect(description(row)).toBe(`Closed ${formatWeekdayStamp(closed)}`);
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

  it("makes each shown row and its Delete one Tab stop each, then More", async () => {
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    unfold();
    toggle().focus();
    const seen: Element[] = [];
    for (let i = 0; i < 5; i++) {
      await userEvent.keyboard("{Tab}");
      seen.push(document.activeElement ?? document.body);
    }
    expect(seen).toEqual([
      rowFor("Alpha"),
      deleteFor("Alpha"),
      rowFor("Beta"),
      deleteFor("Beta"),
      more(),
    ]);
  });

  it("makes no row and not More a Tab stop while folded: Tab leaves the section from its header", async () => {
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    expect(folded()).toBe(true);
    toggle().focus();
    await userEvent.keyboard("{Tab}");
    expect(section().contains(document.activeElement)).toBe(false);
    expect(document.activeElement).not.toBe(document.body);
  });
});

describe("the closed row menu", () => {
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
  ])("opens on $how with Reopen and Delete", async ({ open }) => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    await open(rowFor("Alpha"));
    await settle();
    expect(menus()).toHaveLength(1);
    expect(menuItems()).toEqual(["Reopen", "Delete"]);
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

  it("paints its row in the hover wash until the menu closes", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    const row = rowFor("Alpha");
    row.focus();
    await userEvent.keyboard("{Shift>}{F10}{/Shift}");
    const box = row.closest("li");
    expect(box, "the row's box is its item").not.toBeNull();
    const probe = document.createElement("div");
    probe.style.background = "var(--c-hover)";
    document.body.append(probe);
    const wash = getComputedStyle(probe).backgroundColor;
    probe.remove();
    for (const a of (box as HTMLElement).getAnimations()) {
      a.finish();
    }
    expect(getComputedStyle(box as HTMLElement).backgroundColor).toBe(wash);
    await userEvent.keyboard("{Escape}");
    expect(row.hasAttribute("data-menu-open")).toBe(false);
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

describe("the touch hold on a closed row", () => {
  it("selects no text", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    expect(getComputedStyle(rowFor("Alpha")).userSelect).toBe("none");
    // WebKit's callout is iOS-only: Chromium computes no such property, so the declaration is read.
    expect(ruleBody(loadCSS("12-tabs.css"), ".history-row")).toMatch(
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

  it("opens no menu from the platform's contextmenu for a touch that caught a fling", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    const row = rowFor("Alpha");
    touchAfterFling(row);
    await sleep(HOLD);
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    await settle();
    expect(menus()).toHaveLength(0);
  });

  it("reopens nothing from a touch that caught a fling and stayed down past the hold", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    const row = rowFor("Alpha");
    touchAfterFling(row);
    await sleep(HOLD);
    touch("pointerup", row);
    clickOn(row);
    await settle();
    expect(menus()).toHaveLength(0);
    expect(opens()).toHaveLength(0);
  });

  it("reopens nothing from a touch that caught a fling and got the platform's contextmenu", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    const row = rowFor("Alpha");
    touchAfterFling(row);
    await sleep(300);
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    touch("pointerup", row);
    clickOn(row);
    await settle();
    expect(menus()).toHaveLength(0);
    expect(opens()).toHaveLength(0);
  });

  it("reopens from a tap that caught a fling", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    const row = rowFor("Alpha");
    touchAfterFling(row);
    touch("pointerup", row);
    clickOn(row);
    await settle();
    expect(opens()).toHaveLength(1);
    // The shell's `#tab-sections` outlives the case: its scroll must not make the next case's touch a fling.
    await sleep(SCROLLER_RESTED_MS);
  });

  it("reopens from the lift of a touch that travelled past the slop", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    const row = rowFor("Alpha");
    touch("pointerdown", row);
    const box = row.getBoundingClientRect();
    row.dispatchEvent(
      new PointerEvent("pointermove", {
        pointerType: "touch",
        pointerId: 7,
        isPrimary: true,
        bubbles: true,
        clientX: box.left + 10 + TOUCH_SLOP_PX + 1,
        clientY: box.top + 5,
      }),
    );
    touch("pointerup", row);
    clickOn(row);
    await settle();
    expect(opens()).toHaveLength(1);
  });

  it("reopens nothing from the lift of a touch that travelled past the slop and got the platform's contextmenu", async () => {
    await closedElsewhere({ "c-a": "Alpha" });
    unfold();
    const row = rowFor("Alpha");
    touch("pointerdown", row);
    const box = row.getBoundingClientRect();
    row.dispatchEvent(
      new PointerEvent("pointermove", {
        pointerType: "touch",
        pointerId: 7,
        isPrimary: true,
        bubbles: true,
        clientX: box.left + 10 + TOUCH_SLOP_PX + 1,
        clientY: box.top + 5,
      }),
    );
    row.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    touch("pointerup", row);
    clickOn(row);
    await settle();
    expect(menus()).toHaveLength(1);
    expect(opens()).toHaveLength(0);
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

/** What `/api/sessions` answers, at the network seam: the runs each chat launched, which its
 *  delete deletes too; null answers 503. */
function stubSessions(runs: { chat: string; id: string }[] | null): void {
  configureApi({
    fetchFn: (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (!url.includes("/api/sessions") || runs === null) {
        return Promise.resolve(new Response("{}", { status: 503 }));
      }
      const body = {
        sessions_state: "ready",
        runs_state: "ready",
        sessions: [],
        runs: runs.map((r) => ({
          workflow_id: r.id,
          name: r.id,
          parent_chat_id: r.chat,
          updated_at: 1,
        })),
      };
      return Promise.resolve(
        new Response(JSON.stringify(body), { headers: { "content-type": "application/json" } }),
      );
    },
  });
}

const ask = (): HTMLDialogElement | null =>
  document.querySelector<HTMLDialogElement>("dialog.uip-ask[open]");
async function askShown(): Promise<HTMLDialogElement> {
  await until(() => ask() !== null, "a confirm to open");
  return ask() as HTMLDialogElement;
}
async function answer(button: "Delete" | "Cancel"): Promise<void> {
  const d = await askShown();
  const b = [...d.querySelectorAll<HTMLButtonElement>("button")].find(
    (x) => x.textContent.trim() === button,
  );
  if (b === undefined) {
    throw new Error(`no ${button} in the confirm`);
  }
  let closed = false;
  d.addEventListener(
    "close",
    () => {
      closed = true;
    },
    { once: true },
  );
  b.click();
  // confirm.ts answers on the close event, a task of its own that a busy page can run after later
  // timers, not when `open` turns false at the end of the close animation.
  await until(() => closed, "the confirm's close event");
  await settle();
  await settleTabs();
}
const deletes = (): readonly { payload: Record<string, unknown> }[] =>
  tabServer.sentOfType("delete_chat");

describe("deleting a closed chat from the History section", () => {
  it("draws a trailing trash button named and titled Delete <name> on each row", async () => {
    await listed(entry("c-a", "Alpha"), entry("c-b", "Beta"));
    unfold();
    for (const name of ["Alpha", "Beta"]) {
      const del = deleteFor(name);
      expect(del.type).toBe("button");
      const named = browser.getByRole("button", { name: `Delete ${name}`, exact: true }).element();
      expect(named).toBe(del);
      expect(del.dataset["tooltip"]).toBe(`Delete ${name}`);
      const glyph = del.querySelector("svg");
      const want = iconEl(ICON_TRASH);
      want.setAttribute("aria-hidden", "true");
      expect(glyph?.outerHTML, "the trash glyph").toBe(want.outerHTML);
      const item = rowFor(name).closest("li");
      expect(item, "the row's box is its item").not.toBeNull();
      const row = (item as HTMLElement).getBoundingClientRect();
      const box = del.getBoundingClientRect();
      expect(box.right, "trailing, inside the row").toBeLessThanOrEqual(row.right);
      expect(box.left).toBeGreaterThan(row.left + row.width / 2);
      expect(box.left, "beside the reopen target, not over it").toBeGreaterThanOrEqual(
        rowFor(name).getBoundingClientRect().right,
      );
    }
  });

  it("asks with the History page's words, then deletes the chat for good and drops the row", async () => {
    stubSessions([
      { chat: "c-a", id: "w-1" },
      { chat: "c-a", id: "w-2" },
      { chat: "c-x", id: "w-3" },
    ]);
    tabServer.trackRecent({ "c-a": "Alpha", "c-b": "Beta" });
    await tabServer.seed({ kind: "chat", ref: "c-a" }, { kind: "chat", ref: "c-b" });
    tabServer.closeRemotely(tabServer.idFor("chat", "c-b"));
    tabServer.closeRemotely(tabServer.idFor("chat", "c-a"));
    await settle();
    unfold();
    deleteFor("Alpha").click();
    const d = await askShown();
    expect(d.getAttribute("role")).toBe("alertdialog");
    expect(d.querySelector(".uip-ask-title")?.textContent).toBe("Delete this conversation?");
    expect(d.querySelector(".uip-ask-msg")?.textContent).toBe(
      '"Alpha" and its stored history are removed for good. It also deletes the 2 workflow runs it started.',
    );
    expect(opens(), "the press reopens nothing").toHaveLength(0);
    await answer("Delete");
    expect(deletes().map((c) => c.payload)).toEqual([{ impact: { chats: [] } }]);
    expect(rowNames()).toEqual(["Beta"]);
    expect(section().hidden).toBe(false);
  });

  it("deletes nothing when the confirm is cancelled", async () => {
    stubSessions([]);
    tabServer.trackRecent({ "c-a": "Alpha" });
    await tabServer.seed({ kind: "chat", ref: "c-a" });
    tabServer.closeRemotely(tabServer.idFor("chat", "c-a"));
    await settle();
    unfold();
    deleteFor("Alpha").click();
    await answer("Cancel");
    expect(deletes()).toHaveLength(0);
    expect(rowNames()).toEqual(["Alpha"]);
    expect(opens()).toHaveLength(0);
  });

  it("asks nothing and deletes nothing when the run list cannot be read", async () => {
    stubSessions(null);
    await listed(entry("c-a", "Alpha"));
    unfold();
    deleteFor("Alpha").click();
    for (let i = 0; i < 10; i++) {
      await settle();
    }
    expect(ask()).toBeNull();
    expect(deletes()).toHaveLength(0);
    expect(rowNames()).toEqual(["Alpha"]);
  });

  it("hides the section once its last row is deleted", async () => {
    stubSessions([]);
    tabServer.trackRecent({ "c-a": "Alpha" });
    await tabServer.seed({ kind: "chat", ref: "c-a" });
    tabServer.closeRemotely(tabServer.idFor("chat", "c-a"));
    await settle();
    unfold();
    deleteFor("Alpha").click();
    await answer("Delete");
    expect(deletes()).toHaveLength(1);
    expect(section().hidden).toBe(true);
  });

  it("deletes from Delete in the row menu", async () => {
    stubSessions([]);
    tabServer.trackRecent({ "c-a": "Alpha", "c-b": "Beta" });
    await tabServer.seed({ kind: "chat", ref: "c-a" }, { kind: "chat", ref: "c-b" });
    tabServer.closeRemotely(tabServer.idFor("chat", "c-b"));
    tabServer.closeRemotely(tabServer.idFor("chat", "c-a"));
    await settle();
    unfold();
    rowFor("Alpha").focus();
    await userEvent.keyboard("{Shift>}{F10}{/Shift}");
    await userEvent.keyboard("{ArrowDown}");
    expect(document.activeElement?.textContent.trim()).toBe("Delete");
    await userEvent.keyboard("{Enter}");
    await answer("Delete");
    expect(deletes()).toHaveLength(1);
    expect(rowNames()).toEqual(["Beta"]);
    expect(opens()).toHaveLength(0);
  });

  it("moves focus to the next row after a delete from the keyboard", async () => {
    stubSessions([]);
    tabServer.trackRecent({ "c-a": "Alpha", "c-b": "Beta" });
    await tabServer.seed({ kind: "chat", ref: "c-a" }, { kind: "chat", ref: "c-b" });
    tabServer.closeRemotely(tabServer.idFor("chat", "c-b"));
    tabServer.closeRemotely(tabServer.idFor("chat", "c-a"));
    await settle();
    unfold();
    deleteFor("Alpha").focus();
    await userEvent.keyboard("{Enter}");
    await answer("Delete");
    expect(rowNames()).toEqual(["Beta"]);
    expect(document.activeElement).toBe(rowFor("Beta"));
  });

  it("moves focus to the row before after a delete of the last row from the keyboard", async () => {
    stubSessions([]);
    tabServer.trackRecent({ "c-a": "Alpha", "c-b": "Beta" });
    await tabServer.seed({ kind: "chat", ref: "c-a" }, { kind: "chat", ref: "c-b" });
    tabServer.closeRemotely(tabServer.idFor("chat", "c-b"));
    tabServer.closeRemotely(tabServer.idFor("chat", "c-a"));
    await settle();
    unfold();
    deleteFor("Beta").focus();
    await userEvent.keyboard("{Enter}");
    await answer("Delete");
    expect(rowNames()).toEqual(["Alpha"]);
    expect(document.activeElement).toBe(rowFor("Alpha"));
  });

  it("opens the confirm from the keyboard on its Delete", async () => {
    stubSessions([]);
    await listed(entry("c-a", "Alpha"));
    unfold();
    rowFor("Alpha").focus();
    await userEvent.keyboard("{Tab}");
    expect(document.activeElement).toBe(deleteFor("Alpha"));
    await userEvent.keyboard("{Enter}");
    await answer("Cancel");
    expect(opens(), "Enter on Delete reopens nothing").toHaveLength(0);
  });

  it("takes a 44px target under a finger and leaves the rest of the row to the reopen", async () => {
    document.documentElement.dataset["pointer"] = "coarse";
    try {
      await listed(entry("c-a", "Alpha"));
      unfold();
      const del = deleteFor("Alpha").getBoundingClientRect();
      const cx = del.left + del.width / 2;
      const cy = del.top + del.height / 2;
      for (const dx of [-21, 21]) {
        expect(document.elementFromPoint(cx + dx, cy)?.closest("button"), `dx ${String(dx)}`).toBe(
          deleteFor("Alpha"),
        );
      }
      for (const dy of [-21, 21]) {
        expect(document.elementFromPoint(cx, cy + dy)?.closest("button"), `dy ${String(dy)}`).toBe(
          deleteFor("Alpha"),
        );
      }
      expect(document.elementFromPoint(cx - 30, cy)?.closest("button")).toBe(rowFor("Alpha"));
    } finally {
      delete document.documentElement.dataset["pointer"];
    }
  });

  it("opens no menu and reopens nothing from a touch hold on its Delete", async () => {
    await listed(entry("c-a", "Alpha"));
    unfold();
    touch("pointerdown", deleteFor("Alpha"));
    await sleep(HOLD);
    touch("pointerup", deleteFor("Alpha"));
    await settle();
    expect(menus()).toHaveLength(0);
    expect(opens()).toHaveLength(0);
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
    await closedElsewhere({ "c-a": "Alpha", "c-b": "Beta" });
    unfold();
    tabServer.holdResponses();
    rowFor("Alpha").focus();
    await userEvent.keyboard("{Enter}");
    await settle();
    more().focus();
    tabServer.releaseResponses();
    await settle();
    expect(tabIdFor("chat", "c-a")).not.toBe("");
    expect(document.activeElement).toBe(more());
  });
});

describe("focus when a frame from elsewhere takes what it was on", () => {
  const pill = (): HTMLElement | null =>
    el("history-view").querySelector<HTMLElement>('[data-history-tab][aria-selected="true"]');
  /** The list as a frame from the server, at its next version, as an expiry or another device sends it. */
  const framed = async (...recent: RecentClose[]): Promise<void> => {
    tabServer.emitRecent(recent);
    await settle();
  };

  it("lands on the shown History page's pill when the last close goes from under More", async () => {
    await framed(entry("c-a", "Alpha"));
    unfold();
    more().click();
    await settle();
    try {
      more().focus();
      await framed();
      expect(section().hidden).toBe(true);
      expect(document.activeElement).not.toBe(document.body);
      expect(document.activeElement).toBe(pill());
    } finally {
      // The chat view back, so the next case's composer can take focus.
      showOnlyView(el("chat-view"));
      await settle();
    }
  });

  it("lands on the next row when the focused row goes, else the one before it", async () => {
    await framed(entry("c-a", "Alpha"), entry("c-b", "Beta", 5), entry("c-c", "Gamma", 10));
    unfold();
    rowFor("Beta").focus();
    await framed(entry("c-a", "Alpha"), entry("c-c", "Gamma", 10));
    expect(document.activeElement).toBe(rowFor("Gamma"));
    await framed(entry("c-a", "Alpha"));
    expect(document.activeElement).toBe(rowFor("Alpha"));
  });

  it("lands on the next row when the focused row's Delete goes", async () => {
    await framed(entry("c-a", "Alpha"), entry("c-b", "Beta", 5));
    unfold();
    deleteFor("Alpha").focus();
    await framed(entry("c-b", "Beta", 5));
    expect(document.activeElement).toBe(rowFor("Beta"));
  });

  it("lands on the composer when Days kept turns 0 under focus with History not shown", async () => {
    await framed(entry("c-a", "Alpha"));
    unfold();
    rowFor("Alpha").focus();
    adoptRetention(settingsPayload({ chat_retention_days: 0 }));
    await settle();
    expect(section().hidden).toBe(true);
    expect(document.activeElement).toBe(prompt());
  });

  it("lands on the composer when the folded section's header goes", async () => {
    await framed(entry("c-a", "Alpha"));
    toggle().focus();
    await framed();
    expect(document.activeElement).toBe(prompt());
  });

  it("leaves focus outside the section where it is", async () => {
    await framed(entry("c-a", "Alpha"));
    unfold();
    prompt().focus();
    await framed();
    expect(document.activeElement).toBe(prompt());
  });
});

describe("focus when the History section changes under it", () => {
  const A = entry("c-a", "Alpha");
  const B = entry("c-b", "Beta", 5);
  const C = entry("c-c", "Gamma", 10);
  type On = "row" | "delete" | "more";
  const focusOn = (on: On): void => {
    ({ row: () => rowFor("Beta"), delete: () => deleteFor("Beta"), more })[on]().focus();
  };
  const framed = async (...recent: RecentClose[]): Promise<void> => {
    tabServer.emitRecent(recent);
    await settle();
  };
  /** The Open section's one row, the landing once the section hides. Its chat is new to each case, since a
   *  case at Days kept 0 closes it as a delete that may land in the next case. */
  let openRef = "";
  let opened = 0;
  const openRow = (): HTMLElement | null => tabRow(openRef);
  /** `/api/sessions` held until `answer()` replies 503, so a frame can land while the delete reads it. */
  const holdSessions = (): { answer: () => void } => {
    let reply = (): void => undefined;
    configureApi({
      fetchFn: () =>
        new Promise<Response>((resolve) => {
          reply = () => {
            resolve(new Response("{}", { status: 503 }));
          };
        }),
    });
    return {
      answer: () => {
        reply();
      },
    };
  };

  const causes: {
    cause: string;
    on: On;
    run: () => Promise<void>;
    want: () => Element | null;
  }[] = [
    ...(["row", "delete"] as const).map((on) => ({
      cause: "a frame drops its row",
      on,
      run: () => framed(A, C),
      want: () => rowFor("Gamma"),
    })),
    {
      cause: "a frame drops another row",
      on: "more",
      run: () => framed(A, C),
      want: more,
    },
    ...(["row", "delete", "more"] as const).map((on) => ({
      cause: "a frame moves its row first",
      on,
      run: () => framed(entry("c-b", "Beta", -1), A, C),
      want: () => ({ row: () => rowFor("Beta"), delete: () => deleteFor("Beta"), more })[on](),
    })),
    ...(["row", "delete", "more"] as const).map((on) => ({
      cause: "a frame empties the list",
      on,
      run: () => framed(),
      want: openRow,
    })),
    ...(["row", "delete", "more"] as const).map((on) => ({
      cause: "Days kept turns 0",
      on,
      run: async () => {
        adoptRetention(settingsPayload({ chat_retention_days: 0 }));
        await settle();
      },
      want: openRow,
    })),
    ...(["row", "delete", "more"] as const).map((on) => ({
      cause: "a fold by a click that moves no focus",
      on,
      run: async () => {
        stubReducedMotion();
        clickOn(toggle());
        vi.unstubAllGlobals();
        await settle();
      },
      want: toggle,
    })),
    {
      cause: "its delete",
      on: "delete",
      run: async () => {
        await userEvent.keyboard("{Enter}");
        await answer("Delete");
      },
      want: () => rowFor("Gamma"),
    },
    {
      cause: "a delete cancelled after a frame dropped its chat",
      on: "delete",
      run: async () => {
        await userEvent.keyboard("{Enter}");
        await askShown();
        await framed(A, C);
        await answer("Cancel");
      },
      want: () => rowFor("Gamma"),
    },
    {
      cause: "a delete the server refuses after a frame dropped its chat",
      on: "delete",
      run: async () => {
        tabServer.failNext("delete_chat", 500);
        await userEvent.keyboard("{Enter}");
        await askShown();
        await framed(A, C);
        await answer("Delete");
      },
      want: () => rowFor("Gamma"),
    },
    {
      cause: "a frame dropping its chat while the delete reads the run list",
      on: "delete",
      run: async () => {
        const sessions = holdSessions();
        await userEvent.keyboard("{Enter}");
        await framed(A, C);
        sessions.answer();
        for (let i = 0; i < 10; i++) {
          await settle();
        }
      },
      want: () => rowFor("Gamma"),
    },
  ];

  it.each(causes)(
    "lands as the rule says after $cause, from its $on",
    async ({ on, run, want }) => {
      stubSessions([]);
      openRef = `c-o${String(++opened)}`;
      await tabServer.seed({ kind: "chat", ref: openRef });
      // The strip paints on an animation frame, which a loaded runner can hold past `settle()`.
      for (let i = 0; i < 40 && openRow() === null; i++) {
        await new Promise((r) => requestAnimationFrame(r));
      }
      await framed(A, B, C);
      unfold();
      focusOn(on);
      const target = want();
      expect(target, "the landing is drawn before the change").not.toBeNull();
      expect(section().contains(document.activeElement), "focus starts inside the section").toBe(
        true,
      );
      await run();
      expect(document.activeElement, "never <body>").not.toBe(document.body);
      expect(document.activeElement).toBe(target);
    },
  );
});
