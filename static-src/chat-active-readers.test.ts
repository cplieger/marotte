// In a switch's gap the store's active chat is the target and its painted chat is the outgoing one.
// Everything chat.ts does for the reader (send, cancel, attach) must act on the active chat, the one
// the strip and composer show. Real store; chat.ts's other collaborators are stubbed at the first hop.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as Skeleton from "./skeleton.js";

const { submitPromptMock, cancelDispatch, activateTabMock, messagesEl } = vi.hoisted(() => ({
  submitPromptMock: vi.fn(),
  cancelDispatch: vi.fn(() => ({ outcome: Promise.resolve(undefined) })),
  activateTabMock: vi.fn(),
  messagesEl: document.createElement("div"),
}));

vi.mock("./store-load.js", () => ({
  loadList: vi.fn(),
  loadMessages: vi.fn(),
  loadOlderTurns: vi.fn(),
  confirmChatExists: vi.fn(),
}));
vi.mock("./banner-stack.js", () => ({ ensureBound: vi.fn() }));
vi.mock("./chat-commands.js", () => ({ sendPromptTo: vi.fn() }));
vi.mock("./tabs.js", () => ({
  openTab: vi.fn(() => Promise.resolve("opened")),
  adoptSubject: vi.fn(),
  activateTab: activateTabMock,
  // One tab per chat, `tb_<chat>`; the strip shows B's.
  tabIdFor: (_kind: string, id: string) => `tb_${id}`,
  openChatRefs: vi.fn(() => [] as string[]),
  getActiveTabId: () => "tb_b",
  renameTab: vi.fn(),
  setTabStatus: vi.fn(),
}));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./decision-dock.js", () => ({
  hasPendingDecision: vi.fn(() => false),
  dropDecisions: vi.fn(),
}));
vi.mock("./skeleton.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Skeleton>()),
  chatSkeleton: vi.fn(() => document.createElement("div")),
}));
vi.mock("@cplieger/ui/skeleton", () => ({
  skeletonTiming: vi.fn(() => ({ commit: vi.fn(), cancel: vi.fn() })),
}));
vi.mock("./picker.js", () => ({ showModelPicker: vi.fn(), hideModelPicker: vi.fn() }));
vi.mock("./messages.js", () => ({
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
vi.mock("./attachments.js", () => ({
  addAttachment: vi.fn(),
  unownedAttachmentPaths: vi.fn(() => []),
}));
vi.mock("./composer-state.js", () => ({
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  retargetComposer: vi.fn(),
  seedComposerState: vi.fn(),
  flushComposerDraft: vi.fn(),
  dropComposerState: vi.fn(),
}));
vi.mock("./session-context.js", () => ({ setCurrentModel: vi.fn(), getLastModel: () => "auto" }));
vi.mock("./model-switcher.js", () => ({ applyLocalModel: vi.fn() }));
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./submit.js", () => ({ submitPrompt: submitPromptMock, submitLabelled: vi.fn() }));
vi.mock("./dom.js", () => ({
  $: { messages: messagesEl, promptInput: { focus: () => undefined } },
  setBusy: vi.fn(),
}));
vi.mock("./retention.js", () => ({ isRetentionEnabled: vi.fn(() => false) }));
vi.mock("./bus.js", () => ({
  onBus: vi.fn(),
  onSSE: undefined,
  BUS_ACTIVATE_CHAT: "activate-chat",
}));
vi.mock("./transport.js", () => ({ newOpID: () => "op-test" }));
vi.mock("./actions/chat.js", () => ({
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

// The seams this file needs, read off the namespaces so a missing one fails its own case only.
function setPainted(id: string): void {
  const fn = (store as unknown as { setPainted?: (id: string) => void }).setPainted;
  expect(typeof fn, "store.setPainted").toBe("function");
  fn?.(id);
}
function cancelActiveTurn(): void {
  const fn = (chat as unknown as { cancelActiveTurn?: () => void }).cancelActiveTurn;
  expect(typeof fn, "chat.cancelActiveTurn").toBe("function");
  fn?.();
}

/** A switch from A to B between its gesture and its activation: B active, A still painted. */
function inTheGap(busy: { a?: boolean; b?: boolean } = {}): void {
  store.setSessions([
    makeSession({ id: "a", name: "a", thinking: busy.a === true }),
    makeSession({ id: "b", name: "b", thinking: busy.b === true }),
  ]);
  store.setActive("a");
  setPainted("a");
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
    cancelActiveTurn();
    expect(cancelDispatch).not.toHaveBeenCalled();
  });

  it("cancels the target's turn when the target is busy", () => {
    inTheGap({ b: true });
    cancelActiveTurn();
    expect(cancelDispatch).toHaveBeenCalledTimes(1);
    expect(cancelDispatch).toHaveBeenCalledWith({ chatID: "b" });
  });

  it("attaches to the target chat without switching back to the painted one", async () => {
    inTheGap();
    await chat.attachPathsToActiveChat(["/w/a.ts"]);
    expect(activateTabMock, "attaching switched the strip away from B").not.toHaveBeenCalled();
    expect(vi.mocked(addAttachment)).toHaveBeenCalledWith("/w/a.ts");
  });
});
