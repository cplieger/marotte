// The FOURTH 409 class, "chat_not_found": a tombstoned chat answers "gone", a refusal with the text
// handed back; never "sent", never a steer. Real store; send primitive and steer action mocked.

import { describe, it, expect, vi, beforeEach } from "vitest";

const {
  mockSendPromptTo,
  mockSteer,
  mockTakeAttachments,
  mockAddAttachmentTo,
  mockAttachmentGeneration,
  mockTypedCommand,
  mockInvokesCatalog,
  mockClearAgentDown,
  mockReportSendRefused,
  mockRestoreFailedSend,
  mockChatNotice,
} = vi.hoisted(() => ({
  mockSendPromptTo: vi.fn(),
  mockSteer: vi.fn(),
  mockTakeAttachments: vi.fn(() => [] as unknown[]),
  mockAddAttachmentTo: vi.fn(),
  mockAttachmentGeneration: vi.fn(() => 0),
  mockTypedCommand: vi.fn(() => false),
  mockInvokesCatalog: vi.fn(() => false),
  mockClearAgentDown: vi.fn(),
  mockReportSendRefused: vi.fn(),
  mockRestoreFailedSend: vi.fn(),
  mockChatNotice: vi.fn(),
}));

vi.mock("./chat-commands.js", () => ({ sendPromptTo: mockSendPromptTo }));
vi.mock("./notice-subject.js", () => ({ chatNotice: mockChatNotice }));
vi.mock("./actions/chat.js", () => ({
  steerChat: { dispatch: mockSteer },
  // Present-but-undefined so real-ESM linking succeeds; no case here is in Queue mode.
  queuePrompt: undefined,
}));
vi.mock("./typed-commands.js", () => ({ handleTypedCommand: mockTypedCommand }));
vi.mock("./slash-menu.js", () => ({ invokesCatalogCommand: mockInvokesCatalog }));
vi.mock("./attachments.js", () => ({
  takeAttachments: mockTakeAttachments,
  hasAttachments: vi.fn(() => false),
  addAttachmentTo: mockAddAttachmentTo,
  attachmentGeneration: mockAttachmentGeneration,
}));
vi.mock("./send-state.js", () => ({
  setAgentDown: undefined,
  setSSEStatus: undefined,
  clearAgentDown: mockClearAgentDown,
  reportSendRefused: mockReportSendRefused,
}));
vi.mock("./composer-state.js", () => ({
  restoreFailedSend: mockRestoreFailedSend,
  retargetComposer: vi.fn(),
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

import { submitPrompt } from "./submit.js";
import { setSessions, setActive } from "./store.js";
import type { Session } from "./types.js";

function makeSession(id: string): Session {
  return {
    id,
    name: "test",
    model: "claude",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function resetStore(id: string): void {
  setSessions([makeSession(id)]);
  setActive(id);
}

beforeEach(() => {
  vi.clearAllMocks();
  mockTakeAttachments.mockReturnValue([]);
  mockAttachmentGeneration.mockReturnValue(0);
  mockTypedCommand.mockReturnValue(false);
  mockSendPromptTo.mockResolvedValue("gone");
});

describe("submitPrompt on a 409 chat_not_found refusal", () => {
  it("reports failure rather than falling through to sent", async () => {
    resetStore("c1");
    expect(await submitPrompt("c1", "hello")).toBe("failed");
  });

  it("never attempts a steer, because there is no turn to join", async () => {
    resetStore("c1");
    await submitPrompt("c1", "hello");
    expect(mockSteer).not.toHaveBeenCalled();
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
  });

  it("renders the chat-gone face through send-state's error surface", async () => {
    resetStore("c1");
    await submitPrompt("c1", "hello");
    expect(mockReportSendRefused).toHaveBeenCalledTimes(1);
    expect(String(mockReportSendRefused.mock.calls[0]?.[0])).toMatch(/no longer exists/);
  });

  it("hands the text and the attachments back, because nothing was persisted", async () => {
    resetStore("c1");
    mockTakeAttachments.mockReturnValue([{ path: "a.ts" }]);
    await submitPrompt("c1", "hello");
    expect(mockRestoreFailedSend).toHaveBeenCalledWith("c1", "hello");
    expect(mockAddAttachmentTo).toHaveBeenCalledWith("c1", "a.ts", 0);
  });
});
