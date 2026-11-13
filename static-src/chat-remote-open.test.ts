// A chat tab that opens on this device for a chat the store does not hold (another device reopened
// it, after this one's close removed its Session) is named and shown through one chat-list read.
// Real store, chat-list loader, renderer and chat.ts; the tab projection is a signal-backed fake.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as ApiClient from "./api-client.js";
import type * as DecisionDock from "./decision-dock.js";
import type { ChatHeader } from "./wire/types.gen.js";

const h = vi.hoisted(() => ({
  /** The `/api/chats` reads, counted; each answers the chats in `listed`, `null` a failed read. */
  chatReads: 0,
  listed: [] as ChatHeader[] | null,
  renameTab: vi.fn(),
  openTab: vi.fn(),
  // Replaced with a real signal by the async tabs factory below.
  openRefs: { value: [] as string[] },
}));

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: vi.fn((path: string) => {
    if (path !== "/api/chats") {
      return Promise.resolve(null);
    }
    h.chatReads++;
    return Promise.resolve(h.listed === null ? null : { chats: h.listed.map((c) => ({ ...c })) });
  }),
}));
vi.mock("./store-load.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  // The page read is not under test; the chat-list read is.
  loadMessages: vi.fn(() => Promise.resolve({ kind: "loaded" })),
  loadOlderTurns: vi.fn(),
  confirmChatExists: vi.fn(),
}));
vi.mock("./tabs.js", async (importOriginal) => {
  const { signal } = await import("@cplieger/reactive");
  const { tabsMock } = await import("./__test-helpers__/tabs-mock.js");
  h.openRefs = signal<string[]>([]);
  return {
    ...(await importOriginal<Record<string, unknown>>()),
    ...tabsMock(),
    openChatRefs: () => h.openRefs.value,
    tabIdFor: (_kind: string, ref = "") => (h.openRefs.value.includes(ref) ? `tb_${ref}` : ""),
    getActiveTabId: () => "",
    renameTab: h.renameTab,
    openTab: h.openTab,
  };
});
vi.mock("./banner-stack.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ensureBound: vi.fn(),
}));
vi.mock("./chat-commands.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  sendPromptTo: vi.fn(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock())),
}));
vi.mock("./decision-dock.js", async (importOriginal) => ({
  ...(await importOriginal<typeof DecisionDock>()),
  hasPendingDecision: vi.fn(() => false),
  dropDecisions: vi.fn(),
}));
vi.mock("./composer-state.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.composerStateMock())),
}));
vi.mock("./session-context.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  setCurrentModel: vi.fn(),
  getLastModel: () => "auto",
}));
vi.mock("./model-switcher.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  applyLocalModel: vi.fn(),
}));
vi.mock("./context-ui.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  refreshContextUI: vi.fn(),
}));
vi.mock("./submit.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  submitPrompt: vi.fn(),
  submitLabelled: vi.fn(),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  newOpID: () => "op-test",
}));
vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  deleteChat: { dispatch: vi.fn() },
  restoreChat: { dispatch: vi.fn() },
  setMode: { dispatch: vi.fn() },
  forkChat: { dispatch: vi.fn() },
  createChat: { dispatch: vi.fn() },
  cancelTurn: { dispatch: vi.fn() },
}));

import { IDLE_RUN } from "./run-order.js";
import { makeSession } from "./__test-helpers__/model.js";

for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  if (id === "messages-wrap") {
    document.getElementById("messages-wrap-outer")?.appendChild(d);
  } else if (id === "messages") {
    document.getElementById("messages-wrap")?.appendChild(d);
  } else {
    document.body.appendChild(d);
  }
}
for (const id of ["send-btn", "prompt-input"]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "button");
  d.id = id;
  document.body.appendChild(d);
}

const store = await import("./store.js");
const messages = await import("./messages.js");
const chat = await import("./chat.js");

function header(id: string, name: string): ChatHeader {
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

/** The flush is a microtask and the read's settle a few more; one macrotask covers them. */
async function tick(): Promise<void> {
  await new Promise((r) => setTimeout(r, 0));
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  store.setPainted("");
  h.openRefs.value = [];
  h.chatReads = 0;
  h.listed = [header("ms-a", "Held"), header("ms-b", "Reopened elsewhere")];
  h.renameTab.mockReset();
  h.openTab.mockReset();
  chat.installStoreSubscribers();
});

describe("a chat tab another device opened, for a chat this store lacks", () => {
  it("runs one chat-list read, names its row from it and shows its view with no load error", async () => {
    store.setSessions([makeSession({ id: "ms-a", name: "Held" })]);

    // The frame's `changed` lands the tab; nothing else names the chat.
    h.openRefs.value = ["ms-a", "ms-b"];
    await tick();

    expect(h.chatReads).toBe(1);
    expect(h.renameTab).toHaveBeenCalledWith("tb_ms-b", "Reopened elsewhere");
    expect(h.renameTab).not.toHaveBeenCalledWith("tb_ms-b", "New conversation");

    store.setActive("ms-b");
    chat.showChatView("ms-b");
    await Promise.resolve();
    expect(document.querySelector("#messages .load-error")).toBeNull();
  });
});

describe("reopenChat", () => {
  it("runs one chat-list read in all on the reopening device, the frame's tab included", async () => {
    h.openTab.mockImplementation(() => {
      // event-first: the frame lands the tab before the answer.
      h.openRefs.value = [...h.openRefs.value, "ms-b"];
      return Promise.resolve("opened");
    });

    await expect(chat.reopenChat("ms-b", "Reopened elsewhere")).resolves.toBe("opened");
    await tick();

    expect(h.chatReads).toBe(1);
    expect(h.openTab).toHaveBeenCalledTimes(1);
    expect(h.renameTab).toHaveBeenCalledWith("tb_ms-b", "Reopened elsewhere");
  });
});

describe("reopenChat for a chat this store lacks", () => {
  it("reads the chat list before the open, so the activated view shows no load error", async () => {
    let readsAtOpen = -1;
    h.openTab.mockImplementation((args: { ref: string; name: string }) => {
      readsAtOpen = h.chatReads;
      h.openRefs.value = [...h.openRefs.value, args.ref];
      // The open's activation paints the chat view.
      store.setActive(args.ref);
      chat.showChatView(args.ref);
      return Promise.resolve("opened");
    });

    await expect(chat.reopenChat("ms-b", "Listed name")).resolves.toBe("opened");

    expect(readsAtOpen).toBe(1);
    expect(h.openTab.mock.calls[0]?.[0]).toMatchObject({
      kind: "chat",
      ref: "ms-b",
      name: "Reopened elsewhere",
    });
    expect(document.querySelector("#messages .load-error")).toBeNull();
  });

  it("still opens, under the listed name, when the chat list read fails", async () => {
    h.listed = null;
    h.openTab.mockResolvedValue("opened");
    await expect(chat.reopenChat("ms-b", "Listed name")).resolves.toBe("opened");
    expect(h.chatReads).toBe(1);
    expect(h.openTab).toHaveBeenCalledTimes(1);
    expect(h.openTab.mock.calls[0]?.[0]).toMatchObject({ ref: "ms-b", name: "Listed name" });
  });

  it("reads nothing for a chat the store holds, and answers the open's outcome", async () => {
    store.setSessions([makeSession({ id: "ms-a", name: "Held" })]);
    h.openTab.mockResolvedValue("not-found");
    await expect(chat.reopenChat("ms-a", "Listed name")).resolves.toBe("not-found");
    expect(h.chatReads).toBe(0);
    expect(h.openTab.mock.calls[0]?.[0]).toMatchObject({ ref: "ms-a", name: "Held" });
  });
});
