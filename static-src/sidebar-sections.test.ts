// The sidebar's tab sections over the shipped markup (`design.md` §16.1, §16.8): which headers
// show, what each header says to a screen reader, and how its toggle answers the keyboard.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import type * as Router from "./router.js";
import type * as Store from "./store.js";
import type * as RunStore from "./run-store.js";
import type * as ComposerState from "./composer-state.js";
import type * as ContextMenu from "./context-menu.js";
import type * as Toast from "./toast.js";
import type * as Transport from "./transport.js";
import type * as ApiClient from "./api-client.js";

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
vi.mock("./store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Store>()),
  ...(await import("./__test-helpers__/store-mock.js")).storeMock,
}));
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
vi.mock("./context-menu.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ContextMenu>()),
  showContextMenu: vi.fn(),
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
    apiGetTyped: vi.fn((path: string) => tabs(path)),
    apiGet: vi.fn(() => Promise.resolve(null)),
  };
});

import {
  openTab,
  closeTab,
  activateTab,
  getActiveTabId,
  tabIdFor,
  setTabPinned,
  setTabStatus,
  _resetForTest,
} from "./tabs.js";
// The namespaces, for exports this build adds: a named import of an export a build lacks fails the
// whole file's linking in Browser Mode rather than only the case that calls it.
import * as tabsModule from "./tabs.js";
import * as deviceView from "./device-view.js";
import { showContextMenu } from "./context-menu.js";
import { dragOwnsStrip, MOUSE_DRAG } from "./tabs-drag.js";
import type { ContextMenuItem } from "./context-menu.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, settleTabs, tabServer } from "./__test-helpers__/tabs-server.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

let shell: HTMLElement;
let style: HTMLStyleElement;

beforeAll(() => {
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  for (const s of doc.querySelectorAll("script")) {
    s.remove();
  }
  shell = document.createElement("div");
  for (const n of [...doc.body.childNodes]) {
    shell.appendChild(document.importNode(n, true));
  }
  // Boot removes the strip's skeleton once the tab set is adopted.
  shell.querySelector("#tab-strip-skeleton")?.remove();
  document.body.appendChild(shell);
  style = mountAppCSS();
});

afterAll(() => {
  shell.remove();
  style.remove();
});

beforeEach(() => {
  localStorage.clear();
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
      close: vi.fn(),
      dot: () => "",
    },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
});

afterEach(async () => {
  vi.unstubAllGlobals();
  for (const s of tabServer.subjects()) {
    await closeTab(s.id);
  }
  await paint();
  // Ids restart with the fake server, so a departing row left playing its exit would answer for
  // the next case's tab of the same id.
  for (const r of shell.querySelectorAll("[data-tab-id]")) {
    r.remove();
  }
  _resetTabsSyncForTest();
  localStorage.clear();
});

function el<T extends HTMLElement>(id: string): T {
  const e = shell.querySelector<T>(`[id="${id}"]`);
  if (e === null) {
    throw new Error(`no #${id}`);
  }
  return e;
}

/** renderDOM is RAF-deferred: two frames let a queued render and its follow-up both run. */
async function paint(): Promise<void> {
  await settleTabs();
  for (let i = 0; i < 2; i++) {
    await new Promise<void>((r) => {
      requestAnimationFrame(() => {
        r();
      });
    });
  }
}

/** Open each chat in turn, the last one active. */
async function openChats(...refs: string[]): Promise<void> {
  for (const ref of refs) {
    await openTab({ kind: "chat", ref });
    tabsModule.flushActivationForTest();
  }
  await paint();
}

const chatID = (ref: string): string => tabIdFor("chat", ref);
const section = (key: string): HTMLElement => el(`${key}-section`);
const header = (key: string): HTMLElement => {
  const h = section(key).querySelector<HTMLElement>(":scope > h2.side-section-h");
  if (h === null) {
    throw new Error(`no header in #${key}-section`);
  }
  return h;
};
const toggle = (key: string): HTMLButtonElement => {
  const b = header(key).querySelector<HTMLButtonElement>("button.side-section-toggle");
  if (b === null) {
    throw new Error(`no toggle in #${key}-section`);
  }
  return b;
};
/** The words a reader hears after the name: the elements `aria-describedby` names. */
const description = (b: HTMLElement): string =>
  (b.getAttribute("aria-describedby") ?? "")
    .split(/\s+/)
    .filter((id) => id !== "")
    .map((id) => document.getElementById(id)?.textContent.trim() ?? "")
    .join(" ");
/** The name `aria-labelledby` computes. */
const accessibleName = (b: HTMLElement): string =>
  (b.getAttribute("aria-labelledby") ?? "")
    .split(/\s+/)
    .filter((id) => id !== "")
    .map((id) => document.getElementById(id)?.textContent.trim() ?? "")
    .join(" ");

describe("which sections show", () => {
  it("lays Open out before Pinned, and the History row last", () => {
    const column = el("tab-sections");
    const kids = [...column.children].map((c) => c.id);
    expect(kids.indexOf("open-section")).toBeLessThan(kids.indexOf("pinned-section"));
    expect(column.lastElementChild?.id).toBe("history-btn");
    expect(el("tab-list").parentElement?.id).toBe("open-section");
    expect(el("pinned-list").parentElement?.id).toBe("pinned-section");
  });

  it("hides Pinned with no pinned tab and shows it with one", async () => {
    await openChats("a", "b");
    expect(section("pinned").hidden).toBe(true);
    await setTabPinned(chatID("b"), true);
    await paint();
    expect(section("pinned").hidden).toBe(false);
    await setTabPinned(chatID("b"), false);
    await paint();
    expect(section("pinned").hidden).toBe(true);
  });

  it("hides the Open header while the set is empty or still loading, and shows it with a row", async () => {
    expect(header("open").hidden, "the shipped markup, before any render").toBe(true);
    await paint();
    expect(header("open").hidden, "an empty set").toBe(true);
    await openChats("a");
    expect(header("open").hidden).toBe(false);
    await closeTab(chatID("a"));
    await paint();
    expect(header("open").hidden, "the last Open tab closed").toBe(true);
  });

  it("keeps the Open header hidden while every tab is pinned, though its list stays", async () => {
    await openChats("a");
    await setTabPinned(chatID("a"), true);
    await paint();
    expect(header("open").hidden).toBe(true);
    expect(section("open").hidden, "the list holds the boot skeleton").toBe(false);
  });
});

describe("a section's header", () => {
  it.each(["open", "pinned"] as const)(
    "is a toggle button inside an h2 controlling its list (%s)",
    (key) => {
      const b = toggle(key);
      expect(b.parentElement?.tagName).toBe("H2");
      expect(b.type).toBe("button");
      expect(b.getAttribute("aria-expanded")).toBe("true");
      expect(b.getAttribute("aria-controls")).toBe(key === "open" ? "tab-list" : "pinned-list");
    },
  );

  it("is named by the section's name alone and described by its count in words", async () => {
    await openChats("a", "b", "c");
    await setTabPinned(chatID("a"), true);
    await setTabPinned(chatID("b"), true);
    await paint();
    expect(accessibleName(toggle("pinned"))).toBe("Pinned");
    expect(description(toggle("pinned"))).toBe("2 tabs");
    expect(accessibleName(toggle("open"))).toBe("Open");
    expect(description(toggle("open"))).toBe("1 tab");
  });

  it("shows the count as a bare digit hidden from screen readers", async () => {
    await openChats("a", "b");
    const count = toggle("open").querySelector(".side-section-count");
    expect(count?.textContent).toBe("2");
    expect(count?.getAttribute("aria-hidden")).toBe("true");
  });

  it("counts top-level groups, so a pinned chat with a sub-tab reads 1 tab", async () => {
    await openChats("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await setTabPinned(chatID("p"), true);
    await paint();
    expect(el("pinned-list").querySelectorAll("[data-tab-id]")).toHaveLength(2);
    expect(toggle("pinned").querySelector(".side-section-count")?.textContent).toBe("1");
    expect(description(toggle("pinned"))).toBe("1 tab");
  });

  it.each(["{Enter}", " "])("toggles on %s and follows the fold in aria-expanded", async (key) => {
    await openChats("a", "b");
    const b = toggle("open");
    b.focus();
    await userEvent.keyboard(key);
    expect(b.getAttribute("aria-expanded")).toBe("false");
    await userEvent.keyboard(key);
    expect(b.getAttribute("aria-expanded")).toBe("true");
  });
});

/** Under reduced motion `slideBox` applies at once, so a fold's end state is readable at the press. */
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

const row = (ref: string): HTMLElement => {
  const r = document.querySelector<HTMLElement>(`[data-tab-id="${chatID(ref)}"]:not(.exiting)`);
  if (r === null) {
    throw new Error(`no row for ${ref}`);
  }
  return r;
};
const hiddenRefs = (listID: string): string[] => {
  const byID = new Map(tabServer.subjects().map((s) => [s.id, s.ref]));
  return [...el(listID).querySelectorAll<HTMLElement>("[data-tab-id]")]
    .filter((r) => r.hidden)
    .map((r) => byID.get(r.dataset["tabId"] ?? "") ?? "");
};
const storedFolds = (): unknown => {
  const raw = localStorage.getItem(LS_UI_STATE_KEY);
  return raw === null ? undefined : (JSON.parse(raw) as Record<string, unknown>)["sidebar_folds"];
};
async function press(key: string): Promise<void> {
  await userEvent.keyboard(key);
  await paint();
}

describe("the stored folds", () => {
  it("reads every tab section open when nothing is stored", () => {
    expect(deviceView.sectionFolded("open")).toBe(false);
    expect(deviceView.sectionFolded("pinned")).toBe(false);
  });

  it("reads back what it wrote, per key", () => {
    deviceView.setSectionFolded("pinned", true);
    expect(deviceView.sectionFolded("pinned")).toBe(true);
    expect(deviceView.sectionFolded("open")).toBe(false);
    deviceView.setSectionFolded("pinned", false);
    expect(deviceView.sectionFolded("pinned")).toBe(false);
  });

  it.each([
    { desc: "a non-boolean entry", folds: { open: "yes", pinned: 1 } },
    { desc: "a map that is not an object", folds: "folded" },
    { desc: "an array", folds: [true, true] },
    { desc: "null", folds: null },
  ])("reads $desc as the default", ({ folds }) => {
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ sidebar_folds: folds }));
    expect(deviceView.sectionFolded("open")).toBe(false);
    expect(deviceView.sectionFolded("pinned")).toBe(false);
  });

  it("keeps a key this build does not know, and the blob's other fields, on a write", () => {
    localStorage.setItem(
      LS_UI_STATE_KEY,
      JSON.stringify({ shell_h: 200, sidebar_folds: { bots: true, recent: false } }),
    );
    deviceView.setSectionFolded("open", true);
    expect(storedFolds()).toEqual({ bots: true, recent: false, open: true });
    expect(deviceView.loadDeviceView().shell_h).toBe(200);
  });
});

describe("folding a tab section", () => {
  beforeEach(() => {
    stubReducedMotion();
  });

  it("hides every row but the active one and marks the list folded", async () => {
    await openChats("a", "b", "c");
    expect(getActiveTabId()).toBe(chatID("c"));
    toggle("open").click();
    await paint();
    expect(hiddenRefs("tab-list")).toEqual(["a", "b"]);
    expect(el("tab-list").hasAttribute("data-folded")).toBe(true);
    expect(toggle("open").getAttribute("aria-expanded")).toBe("false");
    toggle("open").click();
    await paint();
    expect(hiddenRefs("tab-list")).toEqual([]);
    expect(el("tab-list").hasAttribute("data-folded")).toBe(false);
    expect(toggle("open").getAttribute("aria-expanded")).toBe("true");
  });

  it("keeps the active row shown as the active tab moves", async () => {
    await openChats("a", "b", "c");
    toggle("open").click();
    await activateTab(chatID("a"));
    await paint();
    expect(hiddenRefs("tab-list")).toEqual(["b", "c"]);
  });

  it("hides every row of a folded section that does not hold the active tab", async () => {
    await openChats("p", "q", "a");
    await setTabPinned(chatID("p"), true);
    await setTabPinned(chatID("q"), true);
    await paint();
    toggle("pinned").click();
    await paint();
    expect(hiddenRefs("pinned-list")).toEqual(["p", "q"]);
    expect(section("pinned").hidden, "its header stays").toBe(false);
  });

  it("is written only by the toggle, and a render seats rows from it", async () => {
    await openChats("a", "b");
    expect(storedFolds(), "a render writes nothing").toBeUndefined();
    toggle("open").click();
    expect(storedFolds()).toEqual({ open: true });
    toggle("open").click();
    expect(storedFolds()).toEqual({ open: false });
    deviceView.setSectionFolded("open", true);
    await openChats("c");
    expect(hiddenRefs("tab-list"), "the stored fold, read at the render").toEqual(["a", "b"]);
    expect(toggle("open").getAttribute("aria-expanded")).toBe("false");
  });

  it("shows on its header the first hidden row state in the dot's precedence", async () => {
    await openChats("a", "b", "c", "d");
    toggle("open").click();
    await paint();
    const dot = (): string | undefined =>
      toggle("open").querySelector<HTMLElement>(".side-section-dot")?.dataset["status"];
    expect(dot(), "every hidden row idle").toBeUndefined();
    expect(description(toggle("open"))).toBe("4 tabs");
    setTabStatus(chatID("a"), "done");
    setTabStatus(chatID("b"), "working");
    await paint();
    expect(dot()).toBe("working");
    setTabStatus(chatID("c"), "input");
    await paint();
    expect(dot()).toBe("input");
    expect(description(toggle("open"))).toBe("4 tabs, one needs a decision");
    setTabStatus(chatID("d"), "failed");
    await paint();
    expect(dot(), "the active row is not hidden, so it does not count").toBe("input");
    setTabStatus(chatID("a"), "failed");
    await paint();
    expect(dot(), "a decision outranks a failure").toBe("input");
    for (const ref of ["a", "b", "c"]) {
      setTabStatus(chatID(ref), "idle");
    }
    await paint();
    expect(dot()).toBeUndefined();
    expect(description(toggle("open"))).toBe("4 tabs");
  });

  it("shows no dot on an unfolded header", async () => {
    await openChats("a", "b");
    setTabStatus(chatID("a"), "input");
    await paint();
    expect(
      toggle("open").querySelector<HTMLElement>(".side-section-dot")?.dataset["status"],
    ).toBeUndefined();
  });
});

describe("the keyboard across sections", () => {
  beforeEach(() => {
    stubReducedMotion();
  });

  const stops = (listID: string): string[] => {
    const byID = new Map(tabServer.subjects().map((s) => [s.id, s.ref]));
    return [...el(listID).querySelectorAll<HTMLElement>("[data-tab-id]")]
      .filter((r) => r.tabIndex === 0)
      .map((r) => byID.get(r.dataset["tabId"] ?? "") ?? "");
  };

  it("gives an unfolded list one stop: its active row, else its first row", async () => {
    await openChats("p", "q", "a", "b");
    await setTabPinned(chatID("p"), true);
    await setTabPinned(chatID("q"), true);
    await paint();
    expect(stops("tab-list")).toEqual(["b"]);
    expect(stops("pinned-list")).toEqual(["p"]);
  });

  it("gives a folded list its active row as its stop, and otherwise none", async () => {
    await openChats("p", "q", "a", "b");
    await setTabPinned(chatID("p"), true);
    await setTabPinned(chatID("q"), true);
    toggle("open").click();
    toggle("pinned").click();
    await paint();
    expect(stops("tab-list")).toEqual(["b"]);
    expect(stops("pinned-list")).toEqual([]);
  });

  it("tabs through Open's header and stop, Pinned's header and stop, then the History row", async () => {
    await openChats("p", "a");
    await setTabPinned(chatID("p"), true);
    await paint();
    toggle("open").focus();
    const seen: (Element | null)[] = [];
    for (let i = 0; i < 4; i++) {
      await userEvent.keyboard("{Tab}");
      seen.push(document.activeElement);
    }
    expect(seen).toEqual([row("a"), toggle("pinned"), row("p"), el("history-btn")]);
  });

  it("tabs from a folded header with no row stop straight past its section", async () => {
    await openChats("p", "a");
    await setTabPinned(chatID("p"), true);
    await paint();
    toggle("pinned").click();
    await paint();
    toggle("pinned").focus();
    await userEvent.keyboard("{Tab}");
    expect(document.activeElement).toBe(el("history-btn"));
  });

  it("keeps the arrows, Home and End inside the list", async () => {
    await openChats("p", "a", "b");
    await setTabPinned(chatID("p"), true);
    await paint();
    row("b").focus();
    await press("{ArrowDown}");
    expect(document.activeElement, "wraps to Open's first row, never into Pinned").toBe(row("a"));
    await press("{ArrowUp}");
    expect(document.activeElement).toBe(row("b"));
    await press("{Home}");
    expect(document.activeElement).toBe(row("a"));
    await press("{End}");
    expect(document.activeElement).toBe(row("b"));
  });

  it("keeps focus on the active row of a folded section for every arrow, Home and End", async () => {
    await openChats("a", "b", "c");
    toggle("open").click();
    await paint();
    row("c").focus();
    for (const key of ["{ArrowDown}", "{ArrowUp}", "{Home}", "{End}"]) {
      await press(key);
      expect(document.activeElement, key).toBe(row("c"));
    }
  });

  it("moves focus to a visible successor on Delete, as today", async () => {
    await openChats("a", "b", "c");
    row("b").focus();
    await press("{Delete}");
    expect(document.activeElement).toBe(row("c"));
  });

  it("moves focus to a folded section's header on Delete of its active row", async () => {
    await openChats("a", "b", "c");
    toggle("open").click();
    await paint();
    row("c").focus();
    await press("{Delete}");
    expect(document.activeElement).toBe(toggle("open"));
  });

  it("moves focus to the next section's stop on Delete of Open's only row", async () => {
    await openChats("p", "a");
    await setTabPinned(chatID("p"), true);
    await paint();
    row("a").focus();
    await press("{Delete}");
    expect(document.activeElement).toBe(row("p"));
  });

  it("walks back to the nearest earlier section's stop on Delete of the last pinned row", async () => {
    await openChats("p", "a");
    await setTabPinned(chatID("p"), true);
    await paint();
    row("p").focus();
    await press("{Delete}");
    expect(document.activeElement).toBe(row("a"));
  });

  it("moves focus to the composer on Delete of the last tab", async () => {
    await openChats("a");
    row("a").focus();
    await press("{Delete}");
    expect(document.activeElement).toBe(el("prompt-input"));
  });

  it.each([
    {
      desc: "pinned",
      act: (): Promise<void> => setTabPinned(chatID("b"), true),
      list: "pinned-list",
    },
    {
      desc: "unpinned",
      act: async (): Promise<void> => {
        await setTabPinned(chatID("b"), true);
        await paint();
        row("b").focus();
        await setTabPinned(chatID("b"), false);
      },
      list: "tab-list",
    },
  ])("keeps focus on a focused row a frame $desc", async ({ act, list }) => {
    await openChats("a", "b", "c");
    row("b").focus();
    await act();
    await paint();
    expect(row("b").parentElement?.id).toBe(list);
    expect(document.activeElement).toBe(row("b"));
  });

  it("keeps focus on a focused row its menu's Move up re-seats", async () => {
    await openChats("a", "b");
    row("b").focus();
    vi.mocked(showContextMenu).mockClear();
    row("b").dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    const items: ContextMenuItem[] = vi.mocked(showContextMenu).mock.lastCall?.[0] ?? [];
    items.find((i) => i.label === "Move up")?.action();
    await paint();
    expect(row("b").previousElementSibling, "the move happened").toBeNull();
    expect(document.activeElement).toBe(row("b"));
  });

  it("hands focus to the folded Pinned header when a focused row is pinned into it", async () => {
    await openChats("p", "a", "b");
    await setTabPinned(chatID("p"), true);
    await paint();
    toggle("pinned").click();
    await paint();
    row("a").focus();
    await setTabPinned(chatID("a"), true);
    await paint();
    expect(row("a").hidden).toBe(true);
    expect(document.activeElement).toBe(toggle("pinned"));
  });
});

describe("a header's box and motion", () => {
  /** Synthetic pointers have no capture to take, so the lists record it instead. */
  function stubPointerCapture(list: HTMLElement): void {
    const captured = new Set<number>();
    list.setPointerCapture = (id: number): void => {
      captured.add(id);
    };
    list.releasePointerCapture = (id: number): void => {
      captured.delete(id);
    };
    list.hasPointerCapture = (id: number): boolean => captured.has(id);
  }
  const mouse = (type: string, target: HTMLElement, dy = 0): void => {
    const r = target.getBoundingClientRect();
    target.dispatchEvent(
      new PointerEvent(type, {
        bubbles: true,
        cancelable: true,
        isPrimary: true,
        pointerId: 7,
        pointerType: "mouse",
        clientX: r.left + 10,
        clientY: r.top + r.height / 2 + dy,
        buttons: type === "pointerup" ? 0 : 1,
      }),
    );
  };
  const ghost = (): Element | null => document.querySelector(".tab-drag-ghost");
  const chevron = (key: string): HTMLElement => {
    const c = toggle(key).querySelector<HTMLElement>(".side-section-chevron");
    if (c === null) {
      throw new Error(`no chevron in #${key}-section`);
    }
    return c;
  };
  const slideEnd = async (box: HTMLElement): Promise<void> => {
    await Promise.all(box.getAnimations().map((a) => a.finished));
    await new Promise((r) => {
      setTimeout(r, 0);
    });
  };

  beforeEach(() => {
    for (const id of ["tab-list", "pinned-list"]) {
      stubPointerCapture(el(id));
    }
  });

  afterEach(() => {
    // Ends a drag a failed case left live, and outlasts its 80 ms click suppression.
    window.dispatchEvent(new Event("blur"));
    for (const id of ["tab-list", "pinned-list"]) {
      for (const m of ["setPointerCapture", "releasePointerCapture", "hasPointerCapture"]) {
        Reflect.deleteProperty(el(id), m);
      }
    }
    delete document.documentElement.dataset["pointer"];
  });

  async function bothSections(): Promise<void> {
    await openChats("p", "a");
    await setTabPinned(chatID("p"), true);
    await paint();
  }

  it.each([
    { tier: "fine", px: 32 },
    { tier: "coarse", px: 44 },
  ])("stands at --row-tap-dense, $px px, on a $tier pointer", async ({ tier, px }) => {
    await bothSections();
    document.documentElement.dataset["pointer"] = tier;
    for (const key of ["open", "pinned"]) {
      expect(header(key).getBoundingClientRect().height, key).toBe(px);
      expect(toggle(key).getBoundingClientRect().height, key).toBe(px);
    }
  });

  it("hides every element of a new section class under [hidden]", async () => {
    await bothSections();
    const styled = [...el("tab-sections").querySelectorAll<HTMLElement>("*")].filter((e) =>
      [...e.classList].some((c) => c.startsWith("side-")),
    );
    const classes = new Set(styled.flatMap((e) => [...e.classList]));
    for (const c of ["side-section", "side-section-h", "side-section-toggle", "side-history-row"]) {
      expect(classes.has(c), c).toBe(true);
    }
    for (const e of styled) {
      const was = e.hidden;
      e.hidden = true;
      expect(getComputedStyle(e).display, e.className).toBe("none");
      e.hidden = was;
    }
  });

  it("keeps a folding section's rows rendered until its slide ends, the fold marked at the press", async () => {
    await openChats("a", "b", "c");
    const list = el("tab-list");
    toggle("open").click();
    expect(toggle("open").getAttribute("aria-expanded")).toBe("false");
    expect(list.hasAttribute("data-folded")).toBe(true);
    expect(list.getAnimations(), "the list slides").not.toHaveLength(0);
    await paint();
    expect(hiddenRefs("tab-list"), "mid-slide").toEqual([]);
    await slideEnd(list);
    expect(hiddenRefs("tab-list")).toEqual(["a", "b"]);

    toggle("open").click();
    expect(toggle("open").getAttribute("aria-expanded")).toBe("true");
    expect(list.hasAttribute("data-folded")).toBe(false);
    expect(hiddenRefs("tab-list"), "an opening slide shows its rows at once").toEqual([]);
    await slideEnd(list);
  });

  it("arms no drag from a press on a row that is sliding shut", async () => {
    await openChats("a", "b", "c");
    toggle("open").click();
    expect(el("tab-list").getAnimations()).not.toHaveLength(0);
    mouse("pointerdown", row("a"));
    mouse("pointermove", row("a"), MOUSE_DRAG.slopPx + 20);
    expect(dragOwnsStrip()).toBe(false);
    expect(ghost()).toBeNull();
    mouse("pointerup", row("a"));
    await slideEnd(el("tab-list"));
  });

  it("hides the rows at the press under reduced motion", async () => {
    stubReducedMotion();
    await openChats("a", "b", "c");
    toggle("open").click();
    expect(hiddenRefs("tab-list")).toEqual(["a", "b"]);
    expect(el("tab-list").getAnimations()).toHaveLength(0);
  });

  it("does nothing while a drag owns the strip", async () => {
    await openChats("a", "b", "c");
    mouse("pointerdown", row("a"));
    mouse("pointermove", row("a"), MOUSE_DRAG.slopPx + 20);
    expect(dragOwnsStrip(), "a real drag is live").toBe(true);
    toggle("open").click();
    expect(toggle("open").getAttribute("aria-expanded")).toBe("true");
    expect(el("tab-list").hasAttribute("data-folded")).toBe(false);
    expect(storedFolds()).toBeUndefined();
  });

  it("turns its chevron at --dur-micro as the fold changes", async () => {
    await bothSections();
    const chev = chevron("pinned");
    const micro = getComputedStyle(document.documentElement).getPropertyValue("--dur-micro").trim();
    expect(getComputedStyle(chev).transitionDuration).toBe(micro);
    const open = getComputedStyle(chev).transform;
    stubReducedMotion();
    toggle("pinned").click();
    // A running transition reads its start value, so the turn is read at its end.
    for (const a of chev.getAnimations()) {
      a.finish();
    }
    expect(getComputedStyle(chev).transform, "the turn follows the fold").not.toBe(open);
  });

  describe("under reduced motion", () => {
    beforeAll(async () => {
      await emulateA11yMedia({ reducedMotion: "reduce" });
    });
    afterAll(async () => {
      await resetA11yMedia();
    });

    it("turns its chevron instantly", async () => {
      await bothSections();
      expect(
        Number.parseFloat(getComputedStyle(chevron("pinned")).transitionDuration),
      ).toBeLessThan(0.001);
    });
  });
});
