// sendPrompt maps the server's answer:
//   ack                  → its `as`: "sent" (started), "steered" or "queued", and the answer's `run` is held
//   409 "prompt_only"    → "prompt_only", 409 "full" → "full"
//   409 "chat_not_found" → "gone", 409 "chat_closed" → "closed"
//   any other 409        → "starting"
//   anything else        → null
// It writes no optimistic state: the composer and the dock draw the server's run order.

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
  applyRun: vi.fn(),
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
  apiBlobOrError: vi.fn(),
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  // Inert: present only so real-ESM linking succeeds.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { send as transportSend } from "../transport.js";
import { applyRun, setThinking, recordSteerQueued } from "../store.js";
import { IDLE_RUN } from "../run-order.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { compactChat, queuePrompt, sendPrompt } from "./chat.js";

const mockSend = vi.mocked(transportSend);

const args = { chatID: "c1", text: "hello", messageID: "m1", model: "model-1" };

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockGet.mockReturnValue({ id: "c1", model: "m1" });
});

describe("sendPrompt — the ack is the whole POST", () => {
  it.each([
    ["started", "sent"],
    ["steered", "steered"],
    ["queued", "queued"],
  ] as const)(
    "answers the server's %s as '%s' and holds the answer's run order",
    async (as, result) => {
      const run = { ...IDLE_RUN, version: 7 };
      mockSend.mockResolvedValue({
        ok: true,
        status: 200,
        body: { accepted: true, message_id: "m1", as, run },
      });

      expect(await sendPrompt.dispatch(args)).toBe(result);
      expect(mockSend).toHaveBeenCalledTimes(1);
      expect(applyRun).toHaveBeenLastCalledWith("c1", run);
      expect(
        setThinking,
        "no optimistic write: the run order is the server's",
      ).not.toHaveBeenCalled();
    },
  );

  it("dispatches at the standard API timeout — the POST no longer spans the turn", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await sendPrompt.dispatch(args);

    const opts = mockSend.mock.calls[0]?.[1] as { timeoutMs?: number } | undefined;
    expect(opts?.timeoutMs).toBe(30_000);
  });

  it("says prompt_only on the payload only when the text needs a turn of its own", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await sendPrompt.dispatch({ ...args, messageID: "m2", promptOnly: true });
    await sendPrompt.dispatch({ ...args, messageID: "m3" });

    const payload = (i: number): Record<string, unknown> =>
      (mockSend.mock.calls[i]?.[0] as { payload: Record<string, unknown> }).payload;
    expect(payload(0)["prompt_only"]).toBe(true);
    expect(payload(1)["prompt_only"]).toBeUndefined();
  });
});

// The server's prompt refusal reasons, mirroring the Go constants reasonPromptOnly, reasonFull, reasonStarting,
// reasonChatGone and reasonChatClosed in internal/command, with the result each maps to.
const PROMPT_REFUSALS: readonly { reason: string | undefined; result: string }[] = [
  { reason: "prompt_only", result: "prompt_only" },
  { reason: "full", result: "full" },
  { reason: "starting", result: "starting" },
  { reason: "chat_not_found", result: "gone" },
  { reason: "chat_closed", result: "closed" },
  // An unknown 409 is a refusal: the idempotency layer's in-progress 409 carries none.
  { reason: undefined, result: "starting" },
  { reason: "elsewhere", result: "starting" },
];

describe("sendPrompt — each 409 reason has its own arm", () => {
  for (const row of PROMPT_REFUSALS) {
    it(`maps 409 reason ${String(row.reason)} to '${row.result}', keyed on the reason, never the prose`, async () => {
      mockSend.mockResolvedValue({
        ok: false,
        status: 409,
        error: "in-flight",
        ...(row.reason !== undefined ? { reason: row.reason } : {}),
      });

      expect(await sendPrompt.dispatch(args)).toBe(row.result);
      // The action stays a PURE send: the steer chip is written only by the server's frame.
      expect(recordSteerQueued).not.toHaveBeenCalled();
      expect(applyRun).not.toHaveBeenCalled();
    });
  }
});

describe("sendPrompt — a failure that is not a 409", () => {
  it("fails (null) on a pre-ack network death", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, code: "network", error: "unreachable" });
    expect(await sendPrompt.dispatch(args)).toBeNull();
  });

  it("fails (null) on a 5xx — the server spoke, and what it said was not an ack", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" });
    expect(await sendPrompt.dispatch(args)).toBeNull();
  });

  it("fails (null) on a 413", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 413, error: "too large" });
    expect(await sendPrompt.dispatch({ ...args, messageID: "m4" })).toBeNull();
  });
});

// An idle chat's compact POST answers only once the summary commits, so a message sent meanwhile
// must reach the server at once: held behind it, the message showed nowhere for the whole compaction.
describe("a message sent while the chat's compact POST waits", () => {
  it.each([
    ["queued", () => queuePrompt.dispatch({ chatID: "c1", text: "next", messageID: "m7" })],
    ["sent as a prompt", () => sendPrompt.dispatch({ ...args, messageID: "m8" })],
  ])("is %s without waiting for the compaction", async (_how, send) => {
    let finishCompact: (r: { ok: boolean; status: number }) => void = () => undefined;
    mockSend.mockImplementation((cmd: { type?: string }) =>
      cmd.type === "compact"
        ? new Promise((resolve) => {
            finishCompact = resolve;
          })
        : Promise.resolve({ ok: true, status: 200 }),
    );

    const compacting = compactChat.dispatch({ chatID: "c1" });
    await vi.waitFor(() => {
      expect(mockSend).toHaveBeenCalledTimes(1);
    });
    void send();

    try {
      await vi.waitFor(() => {
        expect(mockSend).toHaveBeenCalledTimes(2);
      });
    } finally {
      finishCompact({ ok: true, status: 200 });
      await compacting.outcome;
    }
  });
});
