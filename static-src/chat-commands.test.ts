import { describe, it, expect, vi, beforeEach } from "vitest";

const { mockSendPromptDispatch, mockSwitchModelDispatch } = vi.hoisted(() => ({
  mockSendPromptDispatch: vi.fn(),
  mockSwitchModelDispatch: vi.fn(),
}));

vi.mock("./actions/chat.js", () => ({
  sendPrompt: { dispatch: mockSendPromptDispatch },
  switchModel: { dispatch: mockSwitchModelDispatch },
  resolvePendingChange: { dispatch: vi.fn() },
}));

vi.mock("./transport.js", () => ({
  send: vi.fn(),
  newMessageID: vi.fn(() => "m-test-123"),
  // Present-but-inert so real-ESM linking succeeds; no case calls them.
  newOpID: vi.fn(() => "op-test"),
}));

import { sendPromptTo } from "./chat-commands.js";
import { setModel, setSessions } from "./store.js";
import { makeSession } from "./__test-helpers__/model.js";

beforeEach(() => {
  vi.clearAllMocks();
  setSessions([makeSession({ id: "chat1", model: "claude" })]);
});

// sendPromptTo dispatches once and maps the result to sent/queued/starting/failed; what
// each MEANS is submit.ts's job.
describe("sendPromptTo", () => {
  it("returns 'sent' on 2xx and forwards chat/text/model to the action", async () => {
    mockSendPromptDispatch.mockResolvedValue("sent");
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("sent");
    expect(mockSendPromptDispatch).toHaveBeenCalledWith(
      expect.objectContaining({
        chatID: "chat1",
        text: "hello",
        model: "claude",
      }),
    );
    // No attachments passed → the key is omitted (exactOptionalPropertyTypes),
    // not sent as `undefined`.
    expect(mockSendPromptDispatch.mock.calls[0]?.[0]).not.toHaveProperty("attachments");
  });

  it("returns 'queued' on a plain 409 (converting it to a steer is submit's job)", async () => {
    mockSendPromptDispatch.mockResolvedValue("queued");
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("queued");
  });

  it("passes 'starting' through as a value — the caller branches on it, never on prose", async () => {
    mockSendPromptDispatch.mockResolvedValue("starting");
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("starting");
  });

  it("forwards explicit attachments to the action", async () => {
    mockSendPromptDispatch.mockResolvedValue("sent");
    const att = [{ path: "foo.ts", name: "foo.ts" }];
    await sendPromptTo("chat1", "hello", { attachments: att });
    expect(mockSendPromptDispatch).toHaveBeenCalledWith(
      expect.objectContaining({ attachments: att }),
    );
  });

  it("carries the target chat's own model, not one picked in another chat", async () => {
    mockSendPromptDispatch.mockResolvedValue("sent");
    setSessions([
      makeSession({ id: "chat-a", model: "claude" }),
      makeSession({ id: "chat-b", model: "gpt-5.5" }),
    ]);
    setModel("chat-a", "claude-opus");
    await sendPromptTo("chat-b", "hello");
    expect(mockSendPromptDispatch).toHaveBeenCalledWith(
      expect.objectContaining({ chatID: "chat-b", model: "gpt-5.5" }),
    );
  });

  it("returns 'failed' on null result (action error)", async () => {
    mockSendPromptDispatch.mockResolvedValue(null);
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("failed");
  });
});
