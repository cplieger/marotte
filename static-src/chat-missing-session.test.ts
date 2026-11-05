// A chat tab naming a chat the store lacks shows "not loaded yet" with Retry on its own, never
// beside another chat's transcript, and drops the cover the switch held, since the box is what the
// reader acts on. Real store, renderer and cover; chat.ts's other collaborators stubbed.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Entry } from "./wire/types.gen.js";

vi.mock("./store-load.js", () => ({
  // Never settles: the heal's re-read is not under test, the box it paints first is.
  loadList: vi.fn(() => new Promise(() => undefined)),
  loadMessages: vi.fn(() => Promise.resolve({ kind: "loaded" })),
  loadOlderTurns: vi.fn(),
  confirmChatExists: vi.fn(),
}));
vi.mock("./banner-stack.js", () => ({ ensureBound: vi.fn() }));
vi.mock("./chat-commands.js", () => ({ sendPromptTo: vi.fn() }));
vi.mock("./tabs.js", () => ({
  openTab: vi.fn(() => Promise.resolve("opened")),
  adoptSubject: vi.fn(),
  activateTab: vi.fn(() => Promise.resolve()),
  tabIdFor: vi.fn(() => ""),
  openChatRefs: vi.fn(() => [] as string[]),
  getActiveTabId: vi.fn(() => ""),
  renameTab: vi.fn(),
  setTabStatus: vi.fn(),
  refreshActiveView: vi.fn(),
}));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./decision-dock.js", () => ({
  hasPendingDecision: vi.fn(() => false),
  dropDecisions: vi.fn(),
}));
vi.mock("./composer-state.js", () =>
  import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.composerStateMock()),
);
vi.mock("./session-context.js", () => ({ setCurrentModel: vi.fn(), getLastModel: () => "auto" }));
vi.mock("./model-switcher.js", () => ({ applyLocalModel: vi.fn() }));
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./submit.js", () => ({ submitPrompt: vi.fn(), submitLabelled: vi.fn() }));
vi.mock("./transport.js", () => ({ newOpID: () => "op-test" }));
vi.mock("./actions/chat.js", () => ({
  deleteChat: { dispatch: vi.fn() },
  restoreChat: { dispatch: vi.fn() },
  setMode: { dispatch: vi.fn() },
  forkChat: { dispatch: vi.fn() },
  createChat: { dispatch: vi.fn() },
  cancelTurn: { dispatch: vi.fn() },
}));

import { loadCSS } from "./__test-helpers__/css-rules.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Session } from "./types.js";

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
const style = document.createElement("style");
style.textContent = loadCSS("13-messages.css") + "#messages-wrap-outer { height: 400px; }";
document.head.appendChild(style);

const store = await import("./store.js");
const messages = await import("./messages.js");
const coverModule = await import("./transcript-cover.js");
const chat = await import("./chat.js");

messages.mountChatView();

function setPainted(id: string): void {
  (store as unknown as { setPainted: (id: string) => void }).setPainted(id);
}

function withTurn(s: Session, reply: string): Session {
  const t = `${s.id}-t1`;
  const e = (at: number, kind: Entry["kind"], payload: unknown): Entry => ({
    id: `${t}-e${String(at)}`,
    turn: t,
    kind,
    seq: at,
    ts: at + 1,
    payload,
  });
  s.turns.set(t, {
    entries: [
      e(0, "turn_open", { prompt: { id: `${t}-p`, text: "go" }, source: "prompt", n: 1 }),
      e(1, "text", { text: reply }),
      e(2, "turn_close", { outcome: "completed" }),
    ],
    openEntries: new Map(),
  });
  s.turn_order.push(t);
  s.turn_count = 1;
  return s;
}

beforeEach(() => {
  coverModule.dropCover();
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  setPainted("");
});

describe("a tab naming a chat the store lacks", () => {
  it("shows the not-loaded box alone, with no other chat's transcript and no cover", async () => {
    store.setSessions([withTurn(makeSession({ id: "ms-a", name: "a" }), "text of a")]);
    store.setActive("ms-a");
    setPainted("ms-a");
    store.bumpMessages("ms-a", "load");
    await Promise.resolve();
    expect(messages.transcriptViewFor("ms-a")?.classList.contains("is-active")).toBe(true);

    // The switch's gesture held the cover over A for the missing chat.
    store.setActive("ms-ghost");
    coverModule.holdCover("ms-ghost");
    const show = (chat as unknown as { showChatView?: (id: string) => void }).showChatView;
    expect(typeof show, "chat.showChatView").toBe("function");
    show?.("ms-ghost");
    await Promise.resolve();

    const root = document.getElementById("messages");
    const box = root?.querySelector(".load-error");
    expect(box?.textContent).toContain("This conversation is not loaded yet.");
    expect(box?.closest(".transcript-view"), "the box sits inside another chat's view").toBeNull();
    expect(
      root?.querySelector(".transcript-view.is-active"),
      "a transcript is still shown",
    ).toBeNull();
    const c = document.getElementById("transcript-cover");
    expect(c === null || c.hidden || c.getAttribute("data-state") === "released").toBe(true);
  });
});
