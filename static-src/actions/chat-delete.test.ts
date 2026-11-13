// The chat delete sends what its confirm named, and a stale confirm's refusal carries the
// server's impact while the optimistic removal rolls back.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as Toast from "../toast.js";
import type * as Transport from "../transport.js";
import type * as ApiClient from "../api-client.js";

vi.mock("../toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
}));

vi.mock("../transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  send: vi.fn(),
}));

vi.mock("../api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiBlobOrError: vi.fn(),
  apiGetOrError: vi.fn(),
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { send as transportSend } from "../transport.js";
import { error as toastError } from "../toast.js";
import { setSessions, get, setActive, setPainted, getActiveId, getPaintedId } from "../store.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { deleteChat } from "./chat.js";
import { serverImpactOf } from "./close-impact.js";
import type { Session } from "../types.js";

const mockSend = vi.mocked(transportSend);

function makeSession(id: string): Session {
  return {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: { context_pct: 0, context_size: 0, credits: 0, last_turn_ms: 0, has_real_data: false },
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

const named = { chats: [{ ref: "d", turns: ["t1"], asks: [], runs: [], steers: [], queued: [] }] };

beforeEach(() => {
  resetActionFramework();
  vi.clearAllMocks();
  setSessions([makeSession("c1"), makeSession("d")]);
  setActive("");
  setPainted("");
});

describe("chat.delete", () => {
  it("sends the impact the confirm named as its payload", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const outcome = await deleteChat.dispatch({ id: "c1", impact: named }).outcome;
    expect(outcome.status).toBe("success");
    expect(mockSend.mock.calls[0]?.[0]).toEqual({
      type: "delete_chat",
      chat_id: "c1",
      payload: { impact: named },
    });
    expect(get("c1")).toBeUndefined();
  });

  it("restores the chat on a close_impact_changed refusal, which carries the server's impact and toasts nothing", async () => {
    const server = {
      chats: [{ ref: "e", turns: [], asks: ["4"], runs: [], steers: [], queued: [] }],
    };
    mockSend.mockResolvedValue({
      ok: false,
      status: 409,
      error: "what closing this tab stops changed; confirm again",
      reason: "close_impact_changed",
      body: { error: "x", reason: "close_impact_changed", impact: server },
    });
    const outcome = await deleteChat.dispatch({ id: "c1", impact: named }).outcome;
    expect(outcome).toMatchObject({
      status: "error",
      error: { code: "close_impact_changed", status: 409 },
    });
    expect(outcome.status === "error" ? serverImpactOf(outcome.error) : undefined).toEqual(server);
    expect(get("c1")).toBeDefined();
    expect(vi.mocked(toastError)).not.toHaveBeenCalled();
  });

  it("carries the server's reason as the code of any other refusal", async () => {
    mockSend.mockResolvedValue({
      ok: false,
      status: 404,
      error: "no such chat",
      reason: "chat_not_found",
    });
    const outcome = await deleteChat.dispatch({ id: "c1", impact: named }).outcome;
    expect(outcome).toMatchObject({
      status: "error",
      error: { code: "chat_not_found", status: 404 },
    });
    expect(get("c1")).toBeDefined();
  });

  it("a refused delete puts back the active chat it cleared, not its paint", async () => {
    setSessions([makeSession("y"), makeSession("x")]);
    setActive("x");
    setPainted("x");
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "nope" });
    const outcome = await deleteChat.dispatch({ id: "x", impact: named }).outcome;
    expect(outcome.status).toBe("error");
    expect(get("x")).toBeDefined();
    expect(getActiveId(), "the refused delete left the reader acting for another chat").toBe("x");
    expect(getPaintedId(), "a chat no activation showed was painted").toBe("");
  });

  it("a refused delete of a chat the reader was not on selects nothing", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "nope" });
    const outcome = await deleteChat.dispatch({ id: "c1", impact: named }).outcome;
    expect(outcome.status).toBe("error");
    expect(get("c1")).toBeDefined();
    expect(getActiveId(), "the rollback moved the reader onto the chat it restored").toBe("");
  });

  it("a refused delete keeps a chat the reader chose while it was pending", async () => {
    setSessions([makeSession("y"), makeSession("x")]);
    setActive("x");
    const refusal = Promise.withResolvers<{ ok: false; status: number; error: string }>();
    mockSend.mockReturnValue(refusal.promise);
    const pending = deleteChat.dispatch({ id: "x", impact: named }).outcome;
    await vi.waitFor(() => {
      expect(mockSend).toHaveBeenCalled();
    });
    expect(getActiveId()).toBe("");
    setActive("y");
    refusal.resolve({ ok: false, status: 500, error: "nope" });
    const outcome = await pending;
    expect(outcome.status).toBe("error");
    expect(get("x")).toBeDefined();
    expect(getActiveId(), "the rollback overrode the reader's later choice").toBe("y");
  });
});
