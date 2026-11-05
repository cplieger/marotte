// In a switch's gap the store's active chat is the target and its painted chat is the outgoing one.
// Everything chat.ts does for the reader (send, cancel, attach) must act on the active chat, the one
// the strip and composer show. Real store; chat.ts's other collaborators are stubbed at the first hop.
import { describe, it, expect, vi, beforeEach } from "vitest";

const { submitPromptMock, cancelDispatch, activateTabMock, messagesEl } = vi.hoisted(() => ({
  submitPromptMock: vi.fn(),
  cancelDispatch: vi.fn(() => ({ outcome: Promise.resolve(undefined) })),
  activateTabMock: vi.fn(),
  messagesEl: document.createElement("div"),
}));

vi.mock("./store-load.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  loadList: vi.fn(),
  loadMessages: vi.fn(),
  loadOlderTurns: vi.fn(),
  confirmChatExists: vi.fn(),
}));
vi.mock("./banner-stack.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ensureBound: vi.fn(),
}));
vi.mock("./chat-commands.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  sendPromptTo: vi.fn(),
}));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  openTab: vi.fn(() => Promise.resolve("opened")),
  adoptSubject: vi.fn(),
  activateTab: activateTabMock,
  // One tab per chat, `tb_<chat>`; the strip shows B's.
  tabIdFor: (_kind: string, id: string) => `tb_${id}`,
  openChatRefs: vi.fn(() => [] as string[]),
  refreshActiveView: vi.fn(),
  getActiveTabId: () => "tb_b",
  renameTab: vi.fn(),
  setTabStatus: vi.fn(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  ...(await import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock())),
}));
vi.mock("./decision-dock.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  hasPendingDecision: vi.fn(() => false),
  dropDecisions: vi.fn(),
}));
vi.mock("./picker.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  showModelPicker: vi.fn(),
  hideModelPicker: vi.fn(),
}));
vi.mock("./messages.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  mountChatView: vi.fn(),
  setLoadMore: vi.fn(),
  rebaseLoadMore: vi.fn(),
  loadTurnRail: vi.fn(),
  pointTurnRail: vi.fn(),
  activeTranscriptView: vi.fn(() => null),
  transcriptViewFor: vi.fn(() => null),
  disposeChatView: vi.fn(),
  parkPaintedView: vi.fn(),
}));
vi.mock("./transcript-cover.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  dropCover: vi.fn(),
  holdCover: vi.fn(),
  releaseCover: vi.fn(),
  moveFocusOffTranscript: vi.fn(),
}));
vi.mock("./attachments.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  addAttachment: vi.fn(),
  unownedAttachmentPaths: vi.fn(() => []),
}));
vi.mock("./composer-state.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  retargetComposer: vi.fn(),
  seedComposerState: vi.fn(),
  flushComposerDraft: vi.fn(),
  dropComposerState: vi.fn(),
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
  submitPrompt: submitPromptMock,
  submitLabelled: vi.fn(),
}));
vi.mock("./dom.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  // Total over the registry: a spread original module may read any element at import.
  $: new Proxy(
    { messages: messagesEl, promptInput: { focus: () => undefined } } as Record<string, unknown>,
    {
      get: (known, prop: string) => known[prop] ?? document.createElement("div"),
    },
  ),
  setBusy: vi.fn(),
}));
vi.mock("./retention.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  isRetentionEnabled: vi.fn(() => false),
}));
vi.mock("./bus.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  onBus: vi.fn(),
  onSSE: undefined,
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
  cancelTurn: { dispatch: cancelDispatch },
}));

import * as store from "./store.js";
import * as chat from "./chat.js";
import { addAttachment } from "./attachments.js";
import { makeSession } from "./__test-helpers__/model.js";
import { runAt, turnEntry } from "./__test-helpers__/run-order.js";
import type { Session } from "./types.js";

/** A switch from A to B between its gesture and its activation: B active, A still painted. */
function inTheGap(busy: { a?: boolean; b?: boolean } = {}): void {
  const running = (id: string): Partial<Session> => ({
    thinking: true,
    run: runAt(1, [turnEntry(`t-${id}`)], { cancel: `t-${id}` }),
  });
  store.setSessions([
    makeSession({ id: "a", name: "a", ...(busy.a === true ? running("a") : {}) }),
    makeSession({ id: "b", name: "b", ...(busy.b === true ? running("b") : {}) }),
  ]);
  store.setActive("a");
  store.setPainted("a");
  store.setActive("b");
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("readers in a switch's gap act on the chat the strip shows", () => {
  it("sends the composer's prompt to the target chat", () => {
    inTheGap();
    chat.sendPrompt("hello");
    expect(submitPromptMock).toHaveBeenCalledTimes(1);
    expect(submitPromptMock).toHaveBeenCalledWith("b", "hello");
  });

  it("cancels nothing when the target is idle and the outgoing chat is busy", () => {
    inTheGap({ a: true });
    chat.cancelActiveTurn();
    expect(cancelDispatch).not.toHaveBeenCalled();
  });

  it("cancels the target's turn when the target is busy", () => {
    inTheGap({ b: true });
    chat.cancelActiveTurn();
    expect(cancelDispatch).toHaveBeenCalledTimes(1);
    expect(cancelDispatch).toHaveBeenCalledWith({ chatID: "b", target: "t-b" });
  });

  it("attaches to the target chat without switching back to the painted one", async () => {
    inTheGap();
    await chat.attachPathsToActiveChat(["/w/a.ts"]);
    expect(activateTabMock, "attaching switched the strip away from B").not.toHaveBeenCalled();
    expect(vi.mocked(addAttachment)).toHaveBeenCalledWith("/w/a.ts");
  });
});
