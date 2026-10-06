// The three asks share one answer path; "somebody else answered first" (409
// {"error":"already_answered"}, internal/command/validate.go) is not a failure and raises no
// toast, because decision-dock.ts already explains it.

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

// The TOTAL store mock (real ESM linking needs every name), plus the reader this file drives.
vi.mock("../store.js", async () => ({
  ...(await import("../__test-helpers__/store-mock.js")).storeMock,
  get: () => ({ id: "c1", model: "m1" }),
  hasMessage: () => false,
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
import { error as toastError } from "../toast.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { respondPermission, respondElicitation, respondUserInput } from "./chat.js";

const mockSend = vi.mocked(transportSend);
const mockToastError = vi.mocked(toastError);

/** One dispatch per ask kind, so a rule proved for one is proved for all three. */
const asks = [
  {
    name: "permission",
    dispatch: () => respondPermission.dispatch({ chatID: "c1", requestID: 7, optionID: "allow" }),
  },
  {
    name: "elicitation",
    dispatch: () => respondElicitation.dispatch({ chatID: "c1", requestID: 7, action: "decline" }),
  },
  {
    name: "user input",
    dispatch: () => respondUserInput.dispatch({ chatID: "c1", requestID: 7, action: "dismissed" }),
  },
] as const;

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockToastError.mockReset();
});

describe("answering an ask: the three outcomes", () => {
  for (const ask of asks) {
    it(`${ask.name}: a 409 already_answered is 'superseded' and raises NO error toast`, async () => {
      mockSend.mockResolvedValue({ ok: false, status: 409, error: "already_answered" });
      const result = await ask.dispatch();
      expect(result).toBe("superseded");
      // The dock owns this explanation, so this layer is silent.
      expect(mockToastError).not.toHaveBeenCalled();
    });

    it(`${ask.name}: a real failure yields null and DOES raise an error toast`, async () => {
      mockSend.mockResolvedValue({ ok: false, status: 500, error: "bridge died" });
      // A failed dispatch resolves null and toasts, so "no toast" is the only difference here.
      await expect(ask.dispatch()).resolves.toBeNull();
      expect(mockToastError).toHaveBeenCalled();
    });

    it(`${ask.name}: a landed answer is 'answered'`, async () => {
      mockSend.mockResolvedValue({ ok: true, status: 200 });
      await expect(ask.dispatch()).resolves.toBe("answered");
      expect(mockToastError).not.toHaveBeenCalled();
    });
  }

  // The idempotency middleware's 409 "request already in progress" is a different condition, so a
  // status-only match must not swallow it.
  it("a 409 with a different body is still a failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "request already in progress" });
    await expect(
      respondPermission.dispatch({ chatID: "c1", requestID: 8, optionID: "allow" }),
    ).resolves.toBeNull();
    expect(mockToastError).toHaveBeenCalled();
  });

  it("does not report send-state: that surface belongs to the prompt button", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await respondPermission.dispatch({ chatID: "c1", requestID: 9, optionID: "allow" });
    const opts = mockSend.mock.calls[0]?.[1];
    expect(opts?.reportSendState).toBe(false);
  });
});
