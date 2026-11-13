// The chat tab row's menu over the shipped markup and the real menu: where focus goes when it closes
// and after the item it ran.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type * as Router from "./router.js";
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
    apiGet: vi.fn(() => Promise.resolve(null)),
  };
});

import { openTab, closeTab, setTabPinned, tabIdFor, _resetForTest } from "./tabs.js";
import * as tabsModule from "./tabs.js";
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
  if (menu() !== null) {
    menu()?.querySelector<HTMLElement>("[role=menuitem]")?.focus();
    await userEvent.keyboard("{Escape}");
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

async function openChats(...refs: string[]): Promise<void> {
  for (const ref of refs) {
    await openTab({ kind: "chat", ref });
    tabsModule.flushActivationForTest();
  }
  await paint();
}

const row = (ref: string): HTMLElement => {
  const r = shell.querySelector<HTMLElement>(`[data-tab-id="${tabIdFor("chat", ref)}"]`);
  if (r === null) {
    throw new Error(`no row for ${ref}`);
  }
  return r;
};
const menu = (): HTMLElement | null => document.querySelector<HTMLElement>(".tab-context-menu");
const prompt = (): HTMLElement => el("prompt-input");

/** Open the focused row's menu from the keyboard and walk to `label`. */
async function menuTo(ref: string, label: string): Promise<void> {
  row(ref).focus();
  await userEvent.keyboard("{ContextMenu}");
  const labels = [...(menu()?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? [])].map((i) =>
    i.textContent.trim(),
  );
  const at = labels.indexOf(label);
  expect(at, `${label} in ${labels.join(", ")}`).toBeGreaterThanOrEqual(0);
  for (let i = 0; i < at; i++) {
    await userEvent.keyboard("{ArrowDown}");
  }
  expect(document.activeElement?.textContent.trim()).toBe(label);
}

describe("closing the tab row menu", () => {
  it("returns focus to its tab on Escape", async () => {
    await openChats("a", "b");
    row("a").focus();
    await userEvent.keyboard("{ContextMenu}");
    expect(menu()?.contains(document.activeElement)).toBe(true);
    await userEvent.keyboard("{Escape}");
    expect(menu()).toBeNull();
    expect(document.activeElement).toBe(row("a"));
  });

  it("leaves focus in the composer after an outside click into it", async () => {
    await openChats("a");
    row("a").focus();
    await userEvent.keyboard("{ContextMenu}");
    await userEvent.click(prompt());
    expect(menu()).toBeNull();
    expect(document.activeElement).toBe(prompt());
  });

  it("leaves focus on another tab row an outside click pressed", async () => {
    await openChats("a", "b");
    // The menu opens below the row, so the row above it is the one left to press.
    row("b").focus();
    await userEvent.keyboard("{ContextMenu}");
    await userEvent.click(row("a"));
    expect(menu()).toBeNull();
    expect(document.activeElement).toBe(row("a"));
  });
});

describe("focus after a tab row menu item", () => {
  it("puts focus in the rename field for Rename", async () => {
    await openChats("a");
    await menuTo("a", "Rename\u2026");
    await userEvent.keyboard("{Enter}");
    const field = row("a").querySelector(".tab-name-input");
    expect(field).not.toBeNull();
    expect(document.activeElement).toBe(field);
  });

  it("keeps focus on a row Pin moved into Pinned", async () => {
    await openChats("a", "b");
    await menuTo("a", "Pin");
    await userEvent.keyboard("{Enter}");
    await paint();
    expect(row("a").parentElement?.id).toBe("pinned-list");
    expect(document.activeElement).toBe(row("a"));
  });

  it("keeps focus on a row Unpin moved back into Open", async () => {
    await openChats("a", "b");
    await setTabPinned(tabIdFor("chat", "a"), true);
    await paint();
    await menuTo("a", "Unpin");
    await userEvent.keyboard("{Enter}");
    await paint();
    expect(row("a").parentElement?.id).toBe("tab-list");
    expect(document.activeElement).toBe(row("a"));
  });

  it("keeps focus on a row Move up moved", async () => {
    await openChats("a", "b");
    await menuTo("b", "Move up");
    await userEvent.keyboard("{Enter}");
    await paint();
    expect(row("b").nextElementSibling, "the move happened").toBe(row("a"));
    expect(document.activeElement).toBe(row("b"));
  });
});
