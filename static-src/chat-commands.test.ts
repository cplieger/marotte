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

vi.mock("./session-context.js", () => ({
  getCurrentModel: () => "claude",
}));
import { sendPromptTo } from "./chat-commands.js";

beforeEach(() => {
  vi.clearAllMocks();
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

  it("forwards explicit opts (model + attachments) to the action", async () => {
    mockSendPromptDispatch.mockResolvedValue("sent");
    const att = [{ path: "foo.ts", name: "foo.ts" }];
    await sendPromptTo("chat1", "hello", { model: "gpt-5.5", attachments: att });
    expect(mockSendPromptDispatch).toHaveBeenCalledWith(
      expect.objectContaining({ model: "gpt-5.5", attachments: att }),
    );
  });

  it("returns 'failed' on null result (action error)", async () => {
    mockSendPromptDispatch.mockResolvedValue(null);
    const result = await sendPromptTo("chat1", "hello");
    expect(result).toBe("failed");
  });
});
