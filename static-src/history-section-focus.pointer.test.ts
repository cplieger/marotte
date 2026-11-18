// A delete from the History section asked by a click: after its confirm closes and the frame takes the row out,
// focus lands on the next row with no ring (WebKit matches `:focus-visible` for a focus scripted after a dialog).
// Real mouse, so it runs in the pointer projects, WebKit and Firefox included.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { RING, outlineOf } from "./__test-helpers__/focus-ring.js";
import { settingsPayload } from "./__test-helpers__/settings.js";
import type { Signal } from "@cplieger/reactive";
import type { Session } from "./types.js";
import type { ChatHeader } from "./wire/types.gen.js";
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
import { closeTab, _resetForTest } from "./tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { removeChat } from "./store.js";
import { error as toastError } from "./toast.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, settleTabs, tabServer } from "./__test-helpers__/tabs-server.js";
import { until } from "./__test-helpers__/until.js";
import { configureApi } from "@cplieger/reactive/actions";

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
    el("history-section")
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

const section = (): HTMLElement => el("history-section");
const list = (): HTMLElement => el("history-list");
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

const rowFor = (name: string): HTMLButtonElement => {
  const r = rows().find((b) => b.querySelector("bdi")?.textContent === name);
  if (r === undefined) {
    throw new Error(`no closed row for ${name}`);
  }
  return r;
};
const deleteFor = (name: string): HTMLButtonElement => {
  const b = rowFor(name).parentElement?.querySelector<HTMLButtonElement>(
    ":scope > button.history-row-delete",
  );
  if (b === null || b === undefined) {
    throw new Error(`no Delete beside the closed row for ${name}`);
  }
  return b;
};

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

/** Open the delete's confirm with `open` and press its `button` with the mouse; resolves once it has closed and
 *  the frame that drops the row has run. */
async function deleteBy(open: () => Promise<void>, button: "Delete" | "Cancel"): Promise<void> {
  await open();
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
  await userEvent.click(b);
  // confirm.ts answers on the close event, a task of its own that a busy page can run after later
  // timers, not when `open` turns false at the end of the close animation.
  await until(() => closed, "the confirm's close event");
  await settle();
  await settleTabs();
}

async function twoClosed(): Promise<void> {
  stubSessions([]);
  tabServer.trackRecent({ "c-a": "Alpha", "c-b": "Beta" });
  await tabServer.seed({ kind: "chat", ref: "c-a" }, { kind: "chat", ref: "c-b" });
  tabServer.closeRemotely(tabServer.idFor("chat", "c-b"));
  tabServer.closeRemotely(tabServer.idFor("chat", "c-a"));
  await settle();
  unfold();
}

describe("the focus after a delete from the History section", () => {
  it("lands on the next row with no ring when a click asked for it", async () => {
    await twoClosed();

    await deleteBy(() => userEvent.click(deleteFor("Alpha")), "Delete");

    expect(rowNames()).toEqual(["Beta"]);
    expect(document.activeElement).toBe(rowFor("Beta"));
    expect(outlineOf(rowFor("Beta"))).toBe("none");
  });

  it("lands on the next row with the ring when the keyboard asked for it", async () => {
    await twoClosed();

    await deleteBy(async () => {
      deleteFor("Alpha").focus();
      await userEvent.keyboard("{Enter}");
    }, "Delete");

    expect(document.activeElement).toBe(rowFor("Beta"));
    expect(outlineOf(rowFor("Beta"))).toBe(RING);
  });
});
