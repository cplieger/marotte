// chat.ts's direct callers of the chat view hooks: none paints inside a click or an event, and
// every refresh promise they start has an owner for its rejection. Real store; chat.ts's other
// collaborators stubbed at the first hop.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as Skeleton from "./skeleton.js";

const h = vi.hoisted(() => ({
  loadMessages: vi.fn(),
  activateTab: vi.fn(() => Promise.resolve()),
  openTab: vi.fn(() => Promise.resolve("opened")),
  getActiveTabId: vi.fn(() => ""),
  tabIdFor: vi.fn((_kind: string, _id: string) => ""),
  refreshActiveView: vi.fn(),
  pointTurnRail: vi.fn(),
}));

vi.mock("./store-load.js", () => ({
  loadList: vi.fn(() => Promise.resolve(true)),
  loadMessages: h.loadMessages,
  loadOlderTurns: vi.fn(),
  confirmChatExists: vi.fn(),
}));
vi.mock("./banner-stack.js", () => ({ ensureBound: vi.fn() }));
vi.mock("./chat-commands.js", () => ({ sendPromptTo: vi.fn() }));
vi.mock("./tabs.js", () => ({
  openTab: h.openTab,
  adoptSubject: vi.fn(),
  activateTab: h.activateTab,
  tabIdFor: h.tabIdFor,
  openChatRefs: vi.fn(() => [] as string[]),
  getActiveTabId: h.getActiveTabId,
  renameTab: vi.fn(),
  setTabStatus: vi.fn(),
  refreshActiveView: h.refreshActiveView,
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
  pointTurnRail: h.pointTurnRail,
  activeTranscriptView: vi.fn(() => null),
  transcriptViewFor: vi.fn(() => null),
  disposeChatView: vi.fn(),
  parkPaintedView: vi.fn(),
}));
vi.mock("./attachments.js", () => ({
  addAttachment: vi.fn(),
  unownedAttachmentPaths: vi.fn(() => []),
}));
vi.mock("./composer-state.js", () =>
  import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.composerStateMock()),
);
vi.mock("./session-context.js", () => ({ setCurrentModel: vi.fn(), getLastModel: () => "auto" }));
vi.mock("./model-switcher.js", () => ({ applyLocalModel: vi.fn() }));
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./submit.js", () => ({ submitPrompt: vi.fn(), submitLabelled: vi.fn() }));
vi.mock("./dom.js", () =>
  import("./__test-helpers__/tab-switch-mocks.js").then((m) => m.domMock()),
);
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
  cancelTurn: { dispatch: vi.fn() },
}));

import * as store from "./store.js";
import * as chat from "./chat.js";
import { onBus } from "./bus.js";
import { makeSession } from "./__test-helpers__/model.js";
import { coverUp, held, mountShell } from "./__test-helpers__/tab-switch-mocks.js";
import type { ResumableSession } from "./types.js";

// Registered once, at chat.ts's import.
const busHandler = vi.mocked(onBus).mock.calls.find(([name]) => name === "activate-chat")?.[1] as
  ((p: { chatID: string; then?: () => void }) => void) | undefined;

function setPainted(id: string): void {
  (store as unknown as { setPainted?: (id: string) => void }).setPainted?.(id);
}

/** B known to the store with turns on the server and none resident: a blank view. */
function blankB(): void {
  store.setSessions([
    makeSession({ id: "a", name: "a" }),
    { ...makeSession({ id: "b", name: "b" }), turn_count: 4, has_more: true },
  ]);
  store.setActive("b");
  setPainted("b");
}

function boxes(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>("#messages .load-error")];
}

beforeEach(() => {
  vi.clearAllMocks();
  h.loadMessages.mockResolvedValue({ kind: "loaded" });
  mountShell();
});

describe("the activate-chat bus event", () => {
  it("runs its continuation after the activation, not inside the event", async () => {
    blankB();
    h.tabIdFor.mockImplementation((_kind, id) => `tb_${id}`);
    const activation = held();
    h.activateTab.mockReturnValueOnce(activation.promise);
    const then = vi.fn();

    expect(busHandler).toBeDefined();
    busHandler?.({ chatID: "b", then });

    expect(then, "the continuation ran inside the event").not.toHaveBeenCalled();
    expect(h.activateTab).toHaveBeenCalledWith("tb_b");
    expect(h.pointTurnRail, "the event showed the chat itself").not.toHaveBeenCalled();
    activation.resolve();
    await activation.promise;
    await Promise.resolve();
    expect(then).toHaveBeenCalledTimes(1);
  });
});

describe("openPreviousSession", () => {
  it("leaves the one activation to openTab for a chat with no tab", async () => {
    blankB();
    const row = { chat_id: "b", title: "b" } as unknown as ResumableSession;

    await expect(chat.openPreviousSession(row)).resolves.toBe("opened");

    expect(h.openTab).toHaveBeenCalledTimes(1);
    expect(h.pointTurnRail, "chat.ts showed the chat a second time").not.toHaveBeenCalled();
    expect(h.loadMessages, "chat.ts loaded the chat a second time").not.toHaveBeenCalled();
  });
});

describe("the load-error Retry", () => {
  it("repaints the box after a second failure and leaves no rejection unhandled", async () => {
    blankB();
    const paint = (chat as unknown as { showBlankLoadFailure?: (id: string) => void })
      .showBlankLoadFailure;
    expect(typeof paint, "chat.showBlankLoadFailure").toBe("function");
    paint?.("b");
    expect(boxes()).toHaveLength(1);
    h.loadMessages.mockResolvedValue({ kind: "failed", status: 503 });

    boxes()[0]?.querySelector("button")?.click();
    await new Promise((r) => setTimeout(r, 0));

    expect(h.loadMessages).toHaveBeenCalledTimes(1);
    expect(boxes()).toHaveLength(1);
    expect(boxes()[0]?.textContent).toContain("Failed to load messages.");
    expect(coverUp(), "the Retry left the cover up").toBe(false);
  });
});
