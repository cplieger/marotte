// The sidebar's tab sections over the shipped markup: which headers show, what each header says to
// a screen reader, and how its toggle answers the keyboard.
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
import { dragOwnsStrip, MOUSE_DRAG, REORDER_STILL_MS } from "./tabs-drag.js";
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

  it("shows a hidden editor's unsaved changes on its header, after every other state", async () => {
    await openChats("a");
    await openTab({ kind: "editor", ref: "/workspace/x.go" });
    tabsModule.flushActivationForTest();
    await activateTab(chatID("a"));
    await paint();
    toggle("open").click();
    await paint();
    const dot = (): string | undefined =>
      toggle("open").querySelector<HTMLElement>(".side-section-dot")?.dataset["status"];
    tabsModule.setTabDirty(tabIdFor("editor", "/workspace/x.go"), true);
    await paint();
    expect(dot()).toBe("dirty");
    expect(description(toggle("open"))).toBe("2 tabs, one has unsaved changes");
    await openChats("b");
    await activateTab(chatID("a"));
    setTabStatus(chatID("b"), "done");
    await paint();
    expect(dot(), "a finished chat outranks unsaved changes").toBe("done");
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

  it("moves focus to the next section's stop on Delete of a folded section's only row", async () => {
    await openChats("a", "p");
    await setTabPinned(chatID("p"), true);
    await paint();
    toggle("pinned").click();
    await paint();
    row("p").focus();
    await press("{Delete}");
    expect(section("pinned").hidden).toBe(true);
    expect(document.activeElement).toBe(row("a"));
  });

  it("moves focus to the composer on Delete of the only tab in a folded Open", async () => {
    await openChats("a");
    toggle("open").click();
    await paint();
    row("a").focus();
    await press("{Delete}");
    expect(document.activeElement).toBe(el("prompt-input"));
  });

  it("moves focus to the composer on Delete of the last pinned tab while an empty Open is folded", async () => {
    await openChats("p");
    toggle("open").click();
    await paint();
    await setTabPinned(chatID("p"), true);
    await paint();
    expect(el("tab-list").hasAttribute("data-folded"), "the fold outlives Open's last row").toBe(
      true,
    );
    row("p").focus();
    await press("{Delete}");
    expect(document.activeElement).toBe(el("prompt-input"));
  });

  it("skips a closing parent's own sub-tabs on Delete", async () => {
    await openChats("a");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("a") });
    await openChats("b");
    expect(row("a").nextElementSibling).toBe(row("kid"));
    row("a").focus();
    await press("{Delete}");
    expect(document.activeElement).toBe(row("b"));
  });

  it("walks to the next section on Delete of a folded parent whose hidden sub-tabs are the rest of its section", async () => {
    await openChats("p");
    await setTabPinned(chatID("p"), true);
    await openChats("a");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("a") });
    await activateTab(chatID("a"));
    await paint();
    toggle("open").click();
    await paint();
    expect(hiddenRefs("tab-list")).toEqual(["kid"]);
    row("a").focus();
    await press("{Delete}");
    expect(document.activeElement).toBe(row("p"));
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

describe("focusTabRow", () => {
  beforeEach(() => {
    stubReducedMotion();
  });

  const focusTabRow = (id: string): boolean =>
    (tabsModule as unknown as { focusTabRow: (id: string) => boolean }).focusTabRow(id);
  const rowOf = (ref: string): HTMLElement | null =>
    shell.querySelector<HTMLElement>(`[data-tab-id="${chatID(ref)}"]`);

  it("focuses a shown row and answers true", async () => {
    await openChats("a", "b");
    expect(focusTabRow(chatID("a"))).toBe(true);
    expect(document.activeElement).toBe(rowOf("a"));
  });

  it("draws a queued render first, so a row adopted this task is focused", async () => {
    await openChats("a");
    await openTab({ kind: "chat", ref: "b" });
    expect(focusTabRow(chatID("b"))).toBe(true);
    expect(document.activeElement).toBe(rowOf("b"));
  });

  it("answers false and moves nothing for a row its folded section hides", async () => {
    await openChats("a", "b");
    toggle("open").click();
    await paint();
    expect(rowOf("a")?.hidden).toBe(true);
    const before = document.activeElement;
    expect(focusTabRow(chatID("a"))).toBe(false);
    expect(document.activeElement).toBe(before);
  });

  it("answers false for a tab with no row", async () => {
    await openChats("a");
    const before = document.activeElement;
    expect(focusTabRow("tb_none")).toBe(false);
    expect(document.activeElement).toBe(before);
  });
});

/** A hidden row runs no animation, so its `animationend` never comes; none of these cases sends one. */
describe("a departing row", () => {
  beforeEach(() => {
    stubReducedMotion();
  });

  // By id taken before the close: `tabIdFor` answers "" once the tab has left.
  const nodesOf = (id: string): HTMLElement[] => [
    ...document.querySelectorAll<HTMLElement>(`[data-tab-id="${id}"]`),
  ];

  it("leaves no node for a folded parent's hidden sub-tab closed with it", async () => {
    await openChats("p");
    await setTabPinned(chatID("p"), true);
    await openChats("a");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("a") });
    await activateTab(chatID("a"));
    await paint();
    toggle("open").click();
    await paint();
    expect(hiddenRefs("tab-list")).toEqual(["kid"]);
    const kid = chatID("kid");
    row("a").focus();
    await press("{Delete}");
    expect(nodesOf(kid)).toEqual([]);
  });

  it("empties Pinned when its last row closes, so a later pin shows one row", async () => {
    await openChats("a", "p", "q");
    await setTabPinned(chatID("p"), true);
    await paint();
    await activateTab(chatID("p"));
    await paint();
    const rowsAtHide: number[] = [];
    const watch = new MutationObserver(() => {
      if (section("pinned").hidden) {
        rowsAtHide.push(el("pinned-list").querySelectorAll("[data-tab-id]").length);
      }
    });
    watch.observe(section("pinned"), { attributes: true, attributeFilter: ["hidden"] });
    tabServer.closeRemotely(chatID("p"));
    await paint();
    watch.disconnect();
    expect(new Set(rowsAtHide), "every paint that hides Pinned leaves it no row").toEqual(
      new Set([0]),
    );
    await setTabPinned(chatID("q"), true);
    await paint();
    const rows = [...el("pinned-list").querySelectorAll<HTMLElement>("[data-tab-id]")];
    expect(rows).toHaveLength(1);
    expect(rows[0]?.classList.contains("exiting")).toBe(false);
  });

  it("drops a folded section's hidden row a frame closes, and keeps it gone after an unfold", async () => {
    await openChats("a", "b", "c");
    toggle("open").click();
    await paint();
    expect(hiddenRefs("tab-list")).toEqual(["a", "b"]);
    const a = chatID("a");
    tabServer.closeRemotely(a);
    await paint();
    expect(nodesOf(a)).toEqual([]);
    toggle("open").click();
    await paint();
    expect(nodesOf(a)).toEqual([]);
    expect(hiddenRefs("tab-list")).toEqual([]);
  });

  it("listens once for a visible row's exit however many renders it spans", async () => {
    await openChats("a", "b");
    const leaving = row("a");
    // Held mid-exit, so the second render below still finds it.
    leaving.style.animationPlayState = "paused";
    const listen = vi.spyOn(leaving, "addEventListener");
    tabServer.closeRemotely(chatID("a"));
    await paint();
    expect(leaving.classList.contains("exiting")).toBe(true);
    await openChats("c");
    expect(leaving.isConnected, "still playing its exit").toBe(true);
    expect(listen.mock.calls.filter(([type]) => type === "animationend")).toHaveLength(1);
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

  it("hides every element of a new section class that sets display under [hidden]", async () => {
    await bothSections();
    const setsDisplay = new Set<string>();
    const walk = (rules: CSSRuleList): void => {
      for (const r of rules) {
        if (r instanceof CSSStyleRule) {
          const cls = /^\.(side-[\w-]+)$/.exec(r.selectorText)?.[1];
          if (cls !== undefined && r.style.display !== "" && r.style.display !== "none") {
            setsDisplay.add(cls);
          }
        }
        if (r instanceof CSSGroupingRule) {
          walk(r.cssRules);
        }
      }
    };
    const sheet = style.sheet;
    if (sheet === null) {
      throw new Error("the app stylesheet is not mounted");
    }
    walk(sheet.cssRules);
    expect(setsDisplay.size, "the stylesheet's section classes that set display").toBeGreaterThan(
      0,
    );
    for (const c of setsDisplay) {
      const found = [...el("tab-sections").querySelectorAll<HTMLElement>(`.${c}`)];
      expect(found, c).not.toEqual([]);
      for (const e of found) {
        const was = e.hidden;
        e.hidden = true;
        expect(getComputedStyle(e).display, c).toBe("none");
        e.hidden = was;
      }
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

  it.each(
    (["open", "pinned"] as const).flatMap((key) =>
      (["top", "middle", "end"] as const).flatMap((at) =>
        (["auto", "none"] as const).map((anchor) => ({ key, at, anchor })),
      ),
    ),
  )(
    "keeps the sidebar's offset as $key folds with it scrolled to its $at, overflow-anchor $anchor",
    async ({ key, at, anchor }) => {
      const refs = Array.from({ length: 40 }, (_, i) => `r${String(i)}`);
      await openChats(...refs);
      for (const r of refs.slice(0, 20)) {
        await setTabPinned(chatID(r), true);
      }
      await paint();
      const scroller = el("tab-sections");
      // WebKit ships no scroll anchoring, which in Chromium can put a clamped offset back.
      scroller.style.overflowAnchor = anchor;
      const list = el(key === "open" ? "tab-list" : "pinned-list");
      try {
        const max = scroller.scrollHeight - scroller.clientHeight;
        expect(max, "the sidebar scrolls").toBeGreaterThan(scroller.clientHeight);
        scroller.scrollTop = { top: 0, middle: Math.round(max / 2), end: max }[at];
        const before = scroller.scrollTop;
        toggle(key).click();
        expect(list.getAnimations(), "the list slides shut").not.toHaveLength(0);
        expect(scroller.scrollTop).toBe(before);
      } finally {
        await slideEnd(list);
        scroller.style.removeProperty("overflow-anchor");
      }
    },
  );

  describe("a row's entry", () => {
    const refOf = (e: HTMLElement): string =>
      tabServer.subjects().find((s) => s.id === e.dataset["tabId"])?.ref ?? "";
    const frame = (): Promise<void> =>
      new Promise((r) => {
        requestAnimationFrame(() => {
          r();
        });
      });
    /** Every running animation and transition on the rows, finished or cancelled (a drag's settle
     *  timer cancels its own slide under load), then a frame: a `finished` promise resolves before
     *  its frame dispatches the animation events (Web Animations 1, "update animations"). */
    const settled = async (refs: readonly string[]): Promise<void> => {
      await Promise.allSettled(
        refs.flatMap((r) =>
          row(r)
            .getAnimations()
            .map((a) => a.finished),
        ),
      );
      await frame();
    };
    const pointerAt = (type: string, target: EventTarget, x: number, y: number): void => {
      target.dispatchEvent(
        new PointerEvent(type, {
          bubbles: true,
          cancelable: true,
          isPrimary: true,
          pointerId: 7,
          pointerType: "mouse",
          clientX: x,
          clientY: y,
          buttons: type === "pointerup" ? 0 : 1,
        }),
      );
    };
    const midY = (e: HTMLElement): number => {
      const r = e.getBoundingClientRect();
      return r.top + r.height / 2;
    };
    /** Lift `ref` past the slop, carry it to `toY`, and let the ghost's animation start and report. */
    const lift = async (ref: string, toY: number): Promise<HTMLElement> => {
      const r = row(ref);
      const x = r.getBoundingClientRect().left + 10;
      pointerAt("pointerdown", r, x, midY(r));
      pointerAt("pointermove", r, x, midY(r) + MOUSE_DRAG.slopPx + 2);
      expect(dragOwnsStrip(), "a real drag is live").toBe(true);
      pointerAt("pointermove", r, x, toY);
      await frame();
      await Promise.allSettled(
        (document.querySelector(".tab-drag-ghost")?.getAnimations() ?? []).map((a) => a.ready),
      );
      await frame();
      await frame();
      return r;
    };
    /** Take the slot at `y`: a position report at an unchanged position, after the still window. */
    const slotAt = async (r: HTMLElement, y: number): Promise<void> => {
      await new Promise((res) => {
        setTimeout(res, REORDER_STILL_MS + 20);
      });
      pointerAt("pointermove", r, r.getBoundingClientRect().left + 10, y);
      await frame();
    };

    it.each<{ path: string; lifts: number; run: () => Promise<void> }>([
      {
        path: "a fold and an unfold",
        lifts: 0,
        run: async () => {
          toggle("open").click();
          expect(el("tab-list").getAnimations(), "the list slides shut").not.toHaveLength(0);
          await slideEnd(el("tab-list"));
          toggle("open").click();
          expect(el("tab-list").getAnimations(), "the list slides open").not.toHaveLength(0);
          await slideEnd(el("tab-list"));
        },
      },
      {
        path: "a status repaint",
        lifts: 0,
        run: async () => {
          setTabStatus(chatID("a"), "working");
          await paint();
          setTabStatus(chatID("a"), "idle");
          await paint();
        },
      },
      {
        path: "a pin and an unpin",
        lifts: 0,
        run: async () => {
          await setTabPinned(chatID("a"), true);
          await paint();
          expect(row("a").parentElement?.id).toBe("pinned-list");
          await setTabPinned(chatID("a"), false);
          await paint();
          expect(row("a").parentElement?.id).toBe("tab-list");
        },
      },
      {
        path: "a drop that commits",
        lifts: 1,
        run: async () => {
          const history = el("history-btn");
          const r = await lift("p", midY(history));
          pointerAt("pointerup", r, r.getBoundingClientRect().left + 10, midY(history));
          await paint();
          expect(
            [...el("tab-list").querySelectorAll<HTMLElement>(":scope > [data-tab-id]")].map(refOf),
          ).toEqual(["a", "b", "p", "kid"]);
        },
      },
      {
        path: "a drop released outside the sections",
        lifts: 1,
        run: async () => {
          const top = midY(row("p")) - 2;
          const r = await lift("b", top);
          await slotAt(r, top);
          expect(r.classList.contains("tab-slotted"), "the row took the slot").toBe(true);
          pointerAt("pointerup", document.body, window.innerWidth - 2, top);
          await paint();
        },
      },
      {
        path: "a drag ended by Escape",
        lifts: 1,
        run: async () => {
          const top = midY(row("p")) - 2;
          const r = await lift("b", top);
          await slotAt(r, top);
          window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
          await paint();
        },
      },
    ])("plays once and not again across $path", async ({ lifts, run }) => {
      const starts: string[] = [];
      // An animation that starts and ends within one frame still dispatches its animationstart.
      const record = (e: AnimationEvent): void => {
        if (e.target instanceof HTMLElement) {
          const who = e.target.classList.contains("tab-drag-ghost") ? "ghost" : refOf(e.target);
          starts.push(`${e.animationName} ${who}`);
        }
      };
      document.addEventListener("animationstart", record, { capture: true });
      try {
        await openChats("p");
        await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
        await openChats("a", "b");
        const refs = ["p", "kid", "a", "b"];
        await settled(refs);
        const entered = starts.filter((s) => s.startsWith("vk-slide-in-x"));
        expect(entered.toSorted(), "each new row plays its entry").toEqual(
          refs.map((r) => `vk-slide-in-x ${r}`).toSorted(),
        );
        starts.length = 0;

        await run();
        await settled(refs);
        expect(
          starts.filter((s) => s.startsWith("vk-slide-in-x")),
          "replayed entries",
        ).toEqual([]);
        expect(
          starts.filter((s) => s === "tab-drag-float ghost"),
          "the ghost's bob",
        ).toHaveLength(lifts);
      } finally {
        document.removeEventListener("animationstart", record, { capture: true });
      }
    });

    it("plays no entry for a row opened into a folded section when the unfold reveals it", async () => {
      const starts: string[] = [];
      const record = (e: AnimationEvent): void => {
        if (e.animationName === "vk-slide-in-x" && e.target instanceof HTMLElement) {
          starts.push(refOf(e.target));
        }
      };
      document.addEventListener("animationstart", record, { capture: true });
      try {
        await openChats("a", "b");
        toggle("open").click();
        await slideEnd(el("tab-list"));
        await openTab({ kind: "chat", ref: "c", activate: false });
        await paint();
        expect(hiddenRefs("tab-list")).toEqual(["a", "c"]);
        starts.length = 0;
        toggle("open").click();
        await slideEnd(el("tab-list"));
        await frame();
        expect(hiddenRefs("tab-list")).toEqual([]);
        expect(starts).toEqual([]);
      } finally {
        document.removeEventListener("animationstart", record, { capture: true });
      }
    });

    it("plays each entry once and floats the ghost when a lift lands mid-entry", async () => {
      const starts: string[] = [];
      const record = (e: AnimationEvent): void => {
        if (e.target instanceof HTMLElement && e.target.dataset["tabId"] !== undefined) {
          const who = e.target.classList.contains("tab-drag-ghost") ? "ghost" : refOf(e.target);
          starts.push(`${e.animationName} ${who}`);
        }
      };
      document.addEventListener("animationstart", record, { capture: true });
      const root = document.documentElement.style;
      try {
        await openChats("p", "b");
        await settled(["p", "b"]);
        // A long entry, so the lift, not the clock under load, decides how it stops.
        root.setProperty("--dur-micro", "60s");
        await openTab({ kind: "chat", ref: "kid", parent: chatID("p"), activate: false });
        await openChats("c");
        const entries = ["kid", "c"].map((r) => row(r).getAnimations());
        expect(
          entries.map((list) =>
            list.map((a) => (a instanceof CSSAnimation ? a.animationName : "")),
          ),
          "only the entries run",
        ).toEqual([["vk-slide-in-x"], ["vk-slide-in-x"]]);
        const dot = row("kid").querySelector(".tab-status-dot");
        expect(dot, "the sub-tab's status dot").not.toBeNull();
        dot?.dispatchEvent(
          new AnimationEvent("animationend", { animationName: "vk-dot-beat", bubbles: true }),
        );
        await frame();
        expect(
          row("kid").getAnimations(),
          "a status dot's animation ending leaves the entry",
        ).toEqual(entries[0]);
        await lift("c", midY(row("p")) - 2);
        expect(row("kid").checkVisibility(), "the lift folds the sub-tab away").toBe(false);
        root.removeProperty("--dur-micro");
        window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
        await paint();
        await settled(["kid", "c"]);
        // A slow frame lets the still window pass mid-lift, and the slot's own flash then plays on c.
        const played = starts.filter((s) => !/^(vk-dot-beat|tab-slot-in) /.test(s));
        expect(played.toSorted()).toEqual(
          [
            "vk-slide-in-x p",
            "vk-slide-in-x b",
            "vk-slide-in-x kid",
            "vk-slide-in-x c",
            "tab-drag-float ghost",
          ].toSorted(),
        );
      } finally {
        root.removeProperty("--dur-micro");
        document.removeEventListener("animationstart", record, { capture: true });
      }
    });
  });

  it("paints the count and the Tab stop of a render that lands during an unfold", async () => {
    await openChats("a", "b");
    const list = el("tab-list");
    toggle("open").click();
    await slideEnd(list);
    toggle("open").click();
    expect(list.getAnimations(), "the list slides open").not.toHaveLength(0);
    await openTab({ kind: "chat", ref: "c" });
    tabsModule.flushActivationForTest();
    await paint();
    expect(list.getAnimations(), "the render landed mid-slide").not.toHaveLength(0);
    expect(toggle("open").querySelector(".side-section-count")?.textContent).toBe("3");
    expect(row("c").tabIndex).toBe(0);
    expect(row("b").tabIndex).toBe(-1);
    await slideEnd(list);
  });

  it("shows a hidden row's new state on the header while the section slides shut", async () => {
    await openChats("a", "b");
    const list = el("tab-list");
    toggle("open").click();
    expect(list.getAnimations(), "the list slides shut").not.toHaveLength(0);
    setTabStatus(chatID("a"), "input");
    expect(toggle("open").querySelector<HTMLElement>(".side-section-dot")?.dataset["status"]).toBe(
      "input",
    );
    expect(hiddenRefs("tab-list"), "the slide still owns the rows").toEqual([]);
    await slideEnd(list);
    expect(hiddenRefs("tab-list")).toEqual(["a"]);
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
