// Every sidebar section folds the one way: its list slides, never its section, at the same tween,
// marked folded at the press, inert under a drag, instant under reduced motion, and a fold that
// hides the focused row hands focus to the header. The sections are read off the markup, the docked
// Runs among them, so a new one is held to this without a case of its own.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { settingsPayload } from "./__test-helpers__/settings.js";
import type * as Router from "./router.js";
import type * as History from "./history.js";
import type * as Store from "./store.js";
import type * as RunStore from "./run-store.js";
import type * as ComposerState from "./composer-state.js";
import type * as Toast from "./toast.js";
import type * as Transport from "./transport.js";
import type * as ApiClient from "./api-client.js";

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
vi.mock("./history.js", async (importOriginal) => ({
  ...(await importOriginal<typeof History>()),
  loadHistoryView: vi.fn(),
  refreshHistoryView: vi.fn(),
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
    // No run header: a run's row then draws with no state, which no case here asserts.
    apiGetOrError: vi.fn(() =>
      Promise.resolve({ ok: false, status: 404, data: null, error: "not found" }),
    ),
    apiGet: vi.fn(() => Promise.resolve(null)),
  };
});

import { adoptRetention } from "./retention.js";
import { applyLiveRunFrame } from "./run-store.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";
import { closeTab, openTab, setTabPinned, tabIdFor, _resetForTest } from "./tabs.js";
import * as tabsModule from "./tabs.js";
import { dragOwnsStrip, MOUSE_DRAG } from "./tabs-drag.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, settleTabs, tabServer } from "./__test-helpers__/tabs-server.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

// Mounted before the History section's module loads: the chat view's modules read their elements at import.
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
const { initRunsSection } = await import("./runs-section.js");

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  initHistorySection();
  initRunsSection();
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
  vi.unstubAllGlobals();
  window.dispatchEvent(new Event("blur"));
  for (const s of sections()) {
    await Promise.allSettled(
      [s.section, s.list].flatMap((e) => e.getAnimations()).map((a) => a.finished),
    );
  }
  tabServer.emitRecent([]);
  for (const id of [...liveRuns]) {
    liveRun(id, false);
  }
  for (const s of tabServer.subjects()) {
    await closeTab(s.id);
  }
  await paint();
  for (const r of shell.querySelectorAll("[data-tab-id]")) {
    r.remove();
  }
  _resetTabsSyncForTest();
  localStorage.clear();
});

interface Section {
  readonly name: string;
  readonly section: HTMLElement;
  readonly toggle: HTMLButtonElement;
  readonly list: HTMLElement;
}

/** The runs a case made live, by workflow id. */
const liveRuns = new Set<string>();
let runsVersion = 0;

/** One `live_run` frame: the run `id`, parentless, gains or loses its lease. */
function liveRun(id: string, live: boolean): void {
  runsVersion++;
  applyLiveRunFrame(
    { workflow_id: id, live, chat_id: "", executing: live, started_at: live ? runsVersion : 0 },
    { kind: "runs", ref: "", version: String(runsVersion) },
  );
  if (live) {
    liveRuns.add(id);
  } else {
    liveRuns.delete(id);
  }
}

/** Every section the sidebar's markup ships, its header toggle and the list that toggle controls. */
function sections(): Section[] {
  return [
    ...shell.querySelectorAll<HTMLElement>(".sidebar-middle > section, #tab-sections > section"),
  ].map((section) => {
    const toggle = section.querySelector<HTMLButtonElement>(
      ":scope > h2.side-section-h > button.side-section-toggle",
    );
    const controls = toggle?.getAttribute("aria-controls") ?? "";
    const list = shell.querySelector<HTMLElement>(`[id="${controls}"]`);
    if (toggle === null || list === null || !section.contains(list)) {
      throw new Error(`#${section.id} has no header toggle controlling a list inside it`);
    }
    return { name: section.id, section, toggle, list };
  });
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

const chatID = (ref: string): string => tabIdFor("chat", ref);

/** Every section shown with a row its fold hides: Pinned holds p and q, Open holds a and the
 *  active b, History two closed chats, Runs two live runs, and every section starts unfolded. */
async function everySectionShown(): Promise<void> {
  for (const ref of ["p", "q", "a", "b"]) {
    await openTab({ kind: "chat", ref });
    tabsModule.flushActivationForTest();
  }
  await setTabPinned(chatID("p"), true);
  await setTabPinned(chatID("q"), true);
  tabServer.emitRecent([
    { ref: "c-x", name: "Closed X", closed_at: Date.now() - 60_000 },
    { ref: "c-y", name: "Closed Y", closed_at: Date.now() - 120_000 },
  ]);
  liveRun("wf_r1", true);
  liveRun("wf_r2", true);
  await paint();
  stubReducedMotion();
  for (const s of sections()) {
    expect(s.section.hidden, `${s.name} shows`).toBe(false);
    if (s.toggle.getAttribute("aria-expanded") === "false") {
      s.toggle.click();
    }
  }
  vi.unstubAllGlobals();
  await paint();
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

const slideEnd = async (box: HTMLElement): Promise<void> => {
  await Promise.all(box.getAnimations().map((a) => a.finished));
  await new Promise((r) => {
    setTimeout(r, 0);
  });
};

/** The fold's slides on `box`, not the transitions its styles run. */
const slidesOf = (box: HTMLElement): Animation[] =>
  box.getAnimations().filter((a) => !(a instanceof CSSTransition));

const blockSizes = (a: Animation | undefined): unknown[] =>
  ((a?.effect as KeyframeEffect | null)?.getKeyframes() ?? []).map((k) => k["blockSize"]);

/** A row of `s` its fold hides: an inactive tab row, a closed chat's reopen button or a run's link. */
function hideableRow(s: Section): HTMLElement {
  const r =
    s.list.querySelector<HTMLElement>(":scope > [data-tab-id]:not(.active)") ??
    s.list.querySelector<HTMLElement>(":scope > li > button.history-row") ??
    s.list.querySelector<HTMLElement>(":scope > li > a.runs-row");
  if (r === null) {
    throw new Error(`no row in #${s.name} its fold hides`);
  }
  return r;
}

/** Select a row of `s` and return it: a tab row by activating its tab, More by showing History. */
async function selectIn(s: Section): Promise<HTMLElement> {
  const tab = s.list.querySelector<HTMLElement>(":scope > [data-tab-id]");
  const more = s.list.querySelector<HTMLElement>("#history-more");
  if (tab !== null) {
    await tabsModule.activateTab(tab.dataset["tabId"] ?? "");
    tabsModule.flushActivationForTest();
    await paint();
    return tab;
  }
  if (more !== null) {
    tabsModule.showHistoryView();
    await paint();
    return more;
  }
  throw new Error(`#${s.name} has no row to select`);
}

/** Select a tab row outside `s`, so nothing in `s` is selected. */
async function selectElsewhere(s: Section): Promise<void> {
  const other = [...shell.querySelectorAll<HTMLElement>("#tab-sections [data-tab-id]")].find(
    (r) => !s.list.contains(r),
  );
  if (other === undefined) {
    throw new Error(`no tab row outside #${s.name}`);
  }
  await tabsModule.activateTab(other.dataset["tabId"] ?? "");
  tabsModule.flushActivationForTest();
  await paint();
}

describe("every sidebar section's fold", () => {
  it("covers the Open, Pinned, History and Runs sections", () => {
    expect(sections().map((s) => s.name)).toEqual(
      expect.arrayContaining(["open-section", "pinned-section", "history-section", "runs-section"]),
    );
  });

  it("slides the list, never the section, marking the list folded at the press", async () => {
    await everySectionShown();
    for (const s of sections()) {
      s.toggle.click();
      expect(s.toggle.getAttribute("aria-expanded"), s.name).toBe("false");
      expect(s.list.hasAttribute("data-folded"), `${s.name}: the list is marked folded`).toBe(true);
      expect(s.section.getAnimations(), `${s.name}: the section does not slide`).toHaveLength(0);
      const [shut] = slidesOf(s.list);
      expect(shut, `${s.name}: the list slides shut`).toBeDefined();
      expect(hideableRow(s).checkVisibility(), `${s.name}: mid-slide the rows stay`).toBe(true);
      await slideEnd(s.list);
      expect(hideableRow(s).checkVisibility(), `${s.name}: the rows hide at the end`).toBe(false);
      expect(blockSizes(shut).at(-1), `${s.name}: the slide ends at the folded list`).toBe(
        `${String(s.list.getBoundingClientRect().height)}px`,
      );

      s.toggle.click();
      expect(s.toggle.getAttribute("aria-expanded"), s.name).toBe("true");
      expect(s.list.hasAttribute("data-folded"), s.name).toBe(false);
      expect(s.section.getAnimations(), `${s.name}: the section does not slide`).toHaveLength(0);
      expect(slidesOf(s.list), `${s.name}: the list slides open`).not.toHaveLength(0);
      expect(hideableRow(s).checkVisibility(), `${s.name}: an open shows its rows at once`).toBe(
        true,
      );
      await slideEnd(s.list);
    }
  });

  it("runs one tween in every section", async () => {
    await everySectionShown();
    const timings = sections().map((s) => {
      s.toggle.click();
      const t = slidesOf(s.list)[0]?.effect?.getTiming();
      return { name: s.name, duration: t?.duration, easing: t?.easing, fill: t?.fill };
    });
    const root = getComputedStyle(shell.querySelector<HTMLElement>("#tab-sections") ?? shell);
    const token = root.getPropertyValue("--uip-disclosure-duration").trim();
    const want = {
      duration: parseFloat(token) * (token.endsWith("ms") ? 1 : 1000),
      easing: root.getPropertyValue("--uip-disclosure-easing").trim(),
      fill: "forwards",
    };
    expect(want.duration, "the disclosure tween's duration token").toBeGreaterThan(0);
    for (const t of timings) {
      expect({ duration: t.duration, easing: t.easing, fill: t.fill }, t.name).toEqual(want);
    }
    for (const s of sections()) {
      await slideEnd(s.list);
    }
  });

  it("flips at the press with no slide under reduced motion", async () => {
    await everySectionShown();
    stubReducedMotion();
    for (const s of sections()) {
      s.toggle.click();
      expect(s.list.hasAttribute("data-folded"), s.name).toBe(true);
      expect(hideableRow(s).checkVisibility(), `${s.name}: the rows hide at the press`).toBe(false);
      expect([...s.section.getAnimations(), ...slidesOf(s.list)], s.name).toHaveLength(0);
      s.toggle.click();
      expect(s.list.hasAttribute("data-folded"), s.name).toBe(false);
      expect(hideableRow(s).checkVisibility(), s.name).toBe(true);
    }
  });

  it("does nothing while a drag owns the strip", async () => {
    await everySectionShown();
    const list = shell.querySelector<HTMLElement>("#tab-list");
    const row = list?.querySelector<HTMLElement>(`[data-tab-id="${chatID("a")}"]`);
    if (list === null || row === null || row === undefined) {
      throw new Error("no Open row a");
    }
    const captured = new Set<number>();
    list.setPointerCapture = (id: number): void => {
      captured.add(id);
    };
    list.releasePointerCapture = (id: number): void => {
      captured.delete(id);
    };
    list.hasPointerCapture = (id: number): boolean => captured.has(id);
    try {
      const r = row.getBoundingClientRect();
      const at = (type: string, dy: number): void => {
        row.dispatchEvent(
          new PointerEvent(type, {
            bubbles: true,
            cancelable: true,
            isPrimary: true,
            pointerId: 7,
            pointerType: "mouse",
            clientX: r.left + 10,
            clientY: r.top + r.height / 2 + dy,
            buttons: 1,
          }),
        );
      };
      at("pointerdown", 0);
      at("pointermove", MOUSE_DRAG.slopPx + 20);
      expect(dragOwnsStrip(), "a real drag is live").toBe(true);
      const stored = localStorage.getItem(LS_UI_STATE_KEY);
      for (const s of sections()) {
        s.toggle.click();
        expect(s.toggle.getAttribute("aria-expanded"), s.name).toBe("true");
        expect(s.list.hasAttribute("data-folded"), s.name).toBe(false);
        expect([...s.section.getAnimations(), ...slidesOf(s.list)], s.name).toHaveLength(0);
      }
      expect(localStorage.getItem(LS_UI_STATE_KEY), "no fold stored").toBe(stored);
    } finally {
      window.dispatchEvent(new Event("blur"));
      for (const m of ["setPointerCapture", "releasePointerCapture", "hasPointerCapture"]) {
        Reflect.deleteProperty(list, m);
      }
    }
  });

  it("keeps the selected row through a fold and closes the slide on it", async () => {
    await everySectionShown();
    // A run a tab shows leaves Runs, so Runs never holds the row you are on.
    for (const s of sections().filter((x) => x.name !== "runs-section")) {
      const row = await selectIn(s);
      expect(row.classList.contains("active"), `${s.name}: the row is selected`).toBe(true);
      s.toggle.click();
      const [shut] = slidesOf(s.list);
      await slideEnd(s.list);
      expect(row.checkVisibility(), `${s.name}: the selected row stays`).toBe(true);
      expect(hideableRow(s).checkVisibility(), `${s.name}: the other rows hide`).toBe(false);
      const end = parseFloat(String(blockSizes(shut).at(-1)));
      expect(end, `${s.name}: the slide closes on the selected row`).toBeGreaterThanOrEqual(
        row.getBoundingClientRect().height,
      );

      await selectElsewhere(s);
      expect(row.checkVisibility(), `${s.name}: leaving the row hides it under the fold`).toBe(
        false,
      );
      await selectIn(s);
      expect(row.checkVisibility(), `${s.name}: selecting it again shows it`).toBe(true);
      s.toggle.click();
      await slideEnd(s.list);
    }
  });

  it("hands focus to the header when the fold hides the focused row", async () => {
    await everySectionShown();
    stubReducedMotion();
    for (const s of sections()) {
      const r = hideableRow(s);
      r.focus();
      expect(document.activeElement, s.name).toBe(r);
      // A click that moves no focus: a pointer press in WebKit, or a script.
      s.toggle.click();
      expect(r.checkVisibility(), s.name).toBe(false);
      expect(document.activeElement, `${s.name}: focus lands on its header`).toBe(s.toggle);
    }
  });
});
