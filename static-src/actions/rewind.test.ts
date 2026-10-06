// rewind reverts the chat it is in.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../transport.js", () => ({ send: vi.fn() }));

// The refetch is the success path's point, so the loader is a spy.
vi.mock("../store-load.js", () => ({ loadMessages: vi.fn(() => Promise.resolve(true)) }));

import { send as transportSend } from "../transport.js";
import { loadMessages } from "../store-load.js";
import * as toast from "../toast.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

const mockSend = vi.mocked(transportSend);
const mockLoadMessages = vi.mocked(loadMessages);

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockLoadMessages.mockClear();
});

describe("rewind.revert", () => {
  it("sends a rewind_chat command addressing a MESSAGE, not a turn index", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { rewindChat } = await import("./rewind.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    expect(mockSend).toHaveBeenCalledTimes(1);
    const cmd = mockSend.mock.calls[0]?.[0] as {
      type: string;
      chat_id: string;
      payload: { message_id: string };
    };
    expect(cmd.type).toBe("rewind_chat");
    expect(cmd.chat_id).toBe("c-1");
    // KAS's revert verb addresses a user message id (shared because marotte sends it on session/prompt).
    expect(cmd.payload.message_id).toBe("m-abc");
    expect(cmd.payload).not.toHaveProperty("turn_index");
  });

  it("toasts an error when the server rejects", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" });
    const { rewindChat } = await import("./rewind.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    // The server's reason is appended, so a refusal KAS explained in-band reaches the user.
    expect(toast.error).toHaveBeenCalledWith("Could not rewind chat: boom", undefined);
  });

  // A retry that reverts twice cuts again from a truncated transcript. The key must be at the
  // command's TOP level under the framework's field name, where transport.send reads it.
  it("carries a framework idempotency key at the top level", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { rewindChat } = await import("./rewind.js");
    const { IDEMPOTENCY_COMMAND_FIELD } = await import("./index.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    const cmd = mockSend.mock.calls[0]?.[0] as Record<string, unknown>;
    expect(cmd[IDEMPOTENCY_COMMAND_FIELD]).toBeTypeOf("string");
    // The server's envelope has no request_id.
    expect(cmd["request_id"]).toBeUndefined();
  });

  it("exports no promote or discard action", async () => {
    const mod = await import("./rewind.js");
    expect(Object.keys(mod)).toEqual(["rewindChat"]);
  });

  // Nothing on the wire says messages were REMOVED (chat_updated is header-only and upsertHeader
  // merges the count as Math.max), so without this refetch the reader keeps the dropped turns.
  it("refetches the chat's messages after a successful rewind", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { rewindChat } = await import("./rewind.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    expect(mockLoadMessages).toHaveBeenCalledTimes(1);
    expect(mockLoadMessages).toHaveBeenCalledWith("c-1");
  });

  // A refusal left the record untouched, so the reader's transcript is still the truth.
  it("does not refetch when the rewind was refused", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "no bridge" });
    const { rewindChat } = await import("./rewind.js");

    await rewindChat.dispatch({ chatID: "c-1", messageID: "m-abc" });

    expect(mockLoadMessages).not.toHaveBeenCalled();
  });
});
