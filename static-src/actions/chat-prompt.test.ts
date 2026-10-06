// sendPrompt maps the admission ack and the 409 split:
//   ack            → "sent" (thinking stays true; SSE owns the turn)
//   plain 409      → "queued" (a steerable turn is in flight; submit.ts steers)
//   409 "starting" → "starting" (thinking retracted, so the retry is a PROMPT)
//   anything else  → null (rollback retracts thinking)
// `thinking` is all the optimistic write risks: the tab dot reads `last_turn_outcome`, which no
// client path writes.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../transport.js", () => ({
  send: vi.fn(),
  // Inert: present only so real-ESM linking succeeds.
  newOpID: vi.fn(() => "op-test"),
}));

const { mockGet } = vi.hoisted(() => ({
  mockGet: vi.fn(() => ({ id: "c1", model: "m1" }) as Record<string, unknown> | undefined),
}));
// The TOTAL store mock (real ESM linking needs every name); each case sets `get`, the existence
// check every path takes before it writes.
vi.mock("../store.js", async () => ({
  ...(await import("../__test-helpers__/store-mock.js")).storeMock,
  get: mockGet,
  setThinking: vi.fn(),
  recordSteerQueued: vi.fn(),
  setModel: vi.fn(),
  setSupervisedMode: vi.fn(),
  setChatInterruptMode: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
  // Inert: present only so real-ESM linking succeeds.
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

vi.mock("../api-client.js", () => ({
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  // Inert: present only so real-ESM linking succeeds.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { send as transportSend } from "../transport.js";
import { setThinking, recordSteerQueued } from "../store.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { sendPrompt } from "./chat.js";

const mockSend = vi.mocked(transportSend);

const args = { chatID: "c1", text: "hello", messageID: "m1", model: "model-1" };

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockGet.mockReturnValue({ id: "c1", model: "m1" });
});

describe("sendPrompt — the ack is the whole POST", () => {
  it("returns 'sent' on the admission ack alone, with thinking left on for SSE to clear", async () => {
    // Ack-only: the turn runs server-side and nothing else arrives on this POST.
    mockSend.mockResolvedValue({
      ok: true,
      status: 200,
      body: { accepted: true, message_id: "m1" },
    });

    const result = await sendPrompt.dispatch(args);

    expect(result).toBe("sent");
    expect(mockSend).toHaveBeenCalledTimes(1);
    // turn_closed (SSE) clears thinking, so success must not touch it after the optimistic set.
    const calls = vi.mocked(setThinking).mock.calls;
    expect(calls).toContainEqual(["c1", true]);
    expect(calls).not.toContainEqual(["c1", false]);
  });

  it("dispatches at the standard API timeout — the POST no longer spans the turn", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await sendPrompt.dispatch(args);

    const opts = mockSend.mock.calls[0]?.[1] as { timeoutMs?: number } | undefined;
    expect(opts?.timeoutMs).toBe(30_000);
  });

  it("sets thinking optimistically", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await sendPrompt.dispatch({ ...args, messageID: "m2" });
    expect(setThinking).toHaveBeenCalledWith("c1", true);
  });
});

describe("sendPrompt — the three-way 409 split", () => {
  it("returns 'queued' on a plain 409 WITHOUT enqueuing (steering is submit.ts's job)", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "in-flight" });
    const result = await sendPrompt.dispatch(args);
    expect(result).toBe("queued");
    // The action stays a PURE send: submit.ts owns what a busy chat means, and the steer chip is
    // written only by the server's frame, so a chip here could show a message KAS may yet refuse.
    expect(recordSteerQueued).not.toHaveBeenCalled();
    expect(vi.mocked(setThinking).mock.calls).not.toContainEqual(["c1", false]);
  });

  it("returns 'starting' on 409 reason:'starting' — a VALUE, never error-prose matching", async () => {
    // Same prose as the plain 409: only the lifted `reason` distinguishes them (the contract).
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "in-flight", reason: "starting" });

    const result = await sendPrompt.dispatch(args);

    expect(result).toBe("starting");
    expect(recordSteerQueued).not.toHaveBeenCalled();
  });

  it("retracts the optimistic thinking on 'starting', so the retry is a prompt and not a steer", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "busy", reason: "starting" });

    await sendPrompt.dispatch(args);

    expect(vi.mocked(setThinking).mock.calls).toContainEqual(["c1", true]);
    expect(setThinking).toHaveBeenLastCalledWith("c1", false);
  });

  it("returns 'gone' on 409 reason:'chat_not_found' and retracts the optimistic thinking", async () => {
    // Only the lifted `reason` decides it. A tombstoned chat has no turn, so `thinking` left true would
    // offer a steer into nothing.
    mockSend.mockResolvedValue({
      ok: false,
      status: 409,
      error: "in-flight",
      reason: "chat_not_found",
    });

    const result = await sendPrompt.dispatch(args);

    expect(result).toBe("gone");
    expect(recordSteerQueued).not.toHaveBeenCalled();
    expect(setThinking).toHaveBeenLastCalledWith("c1", false);
  });

  it("treats a 409 with any OTHER reason as the plain queue signal", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "busy", reason: "elsewhere" });
    expect(await sendPrompt.dispatch(args)).toBe("queued");
  });
});

// On every failure class the rollback retracts `setThinking(chatID, true)`, leaving the chat
// promptable.
describe("sendPrompt — rollback retracts the optimistic thinking", () => {
  it("fails (null) on a pre-ack network death — the POST is short now, so no echo rescue", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, code: "network", error: "unreachable" });

    expect(await sendPrompt.dispatch(args)).toBeNull();
    // submit.ts owns the text-restore and id-reuse on this result.
    expect(vi.mocked(setThinking).mock.calls).toContainEqual(["c1", false]);
  });

  it("fails (null) on a 5xx — the server spoke, and what it said was not an ack", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" });
    expect(await sendPrompt.dispatch(args)).toBeNull();
  });

  it("retracts the thinking a 400 refused, in that order", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 400, error: "bad request" });

    await sendPrompt.dispatch({ ...args, messageID: "m3" });

    // The ORDER too: a rollback that never ran would pass on the last call alone.
    expect(setThinking).toHaveBeenCalledWith("c1", true);
    expect(setThinking).toHaveBeenLastCalledWith("c1", false);
  });

  it("retracts it on a 413 too — the class is every non-ack answer, not one status", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 413, error: "too large" });

    await sendPrompt.dispatch({ ...args, messageID: "m4" });

    expect(setThinking).toHaveBeenLastCalledWith("c1", false);
  });

  it("leaves thinking ON when the send succeeded — the turn is running", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });

    await sendPrompt.dispatch({ ...args, messageID: "m6" });

    // Negative control: an unconditional rollback would satisfy every case above.
    expect(vi.mocked(setThinking).mock.calls).not.toContainEqual(["c1", false]);
  });
});
