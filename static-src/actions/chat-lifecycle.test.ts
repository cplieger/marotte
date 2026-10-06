import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../transport.js", () => ({
  send: vi.fn(),
  // Inert: present only so real-ESM linking succeeds.
  newOpID: vi.fn(() => "op-test"),
}));

vi.mock("../api-client.js", () => ({
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
  // Inert: present only so real-ESM linking succeeds.
  apiGetTyped: vi.fn(),
}));
import { send as transportSend } from "../transport.js";
import { setSessions, get, setActive } from "../store.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import type { Session } from "../types.js";

const mockSend = vi.mocked(transportSend);
const mockFetch = vi.fn();

function makeSession(id: string, extra?: Partial<Session>): Session {
  return {
    id,
    name: "test",
    model: "claude-4",
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
    ...extra,
  };
}

beforeEach(() => {
  resetActionFramework();
  vi.clearAllMocks();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
  setSessions([makeSession("c1"), makeSession("c2")]);
  setActive("c1");
});

describe("chat.set_supervised", () => {
  it("sends set_supervised_mode command and applies optimistic update", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { setSupervised } = await import("./chat.js");
    await setSupervised.dispatch({ chatID: "c1", enabled: true });
    expect(get("c1")!.supervised_mode).toBe(true);
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({ type: "set_supervised_mode", chat_id: "c1" }),
      expect.anything(),
    );
  });

  it("rolls back on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const { setSupervised } = await import("./chat.js");
    await setSupervised.dispatch({ chatID: "c1", enabled: true });
    expect(get("c1")!.supervised_mode).toBe(false);
  });
});

describe("chat.rename", () => {
  it("sends rename_chat with the name as its whole payload", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { renameChat } = await import("./chat.js");
    await renameChat.dispatch({ chatID: "c1", name: "Release notes" });
    const [cmd] = mockSend.mock.calls.at(-1) ?? [];
    expect(cmd).toEqual(
      expect.objectContaining({
        type: "rename_chat",
        chat_id: "c1",
        payload: { name: "Release notes" },
      }),
    );
  });

  it("leaves the chat's name to the server's frame", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 400, error: "too long" });
    const before = get("c1")?.name;
    const { renameChat } = await import("./chat.js");
    await renameChat.dispatch({ chatID: "c1", name: "Other" });
    expect(get("c1")?.name).toBe(before);
  });
});

describe("chat.switch_model", () => {
  it("applies optimistic model change and sends via transport", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { switchModel } = await import("./chat.js");
    const r = await switchModel.dispatch({ chatID: "c1", model: "opus" });
    expect(r).toBe(true);
    expect(get("c1")!.model).toBe("opus");
  });

  it("rolls back model on failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "fail" });
    const { switchModel } = await import("./chat.js");
    await switchModel.dispatch({ chatID: "c1", model: "opus" });
    expect(get("c1")!.model).toBe("claude-4");
  });
});

describe("chat.cancel_turn", () => {
  it("sends cancel command via transport", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { cancelTurn } = await import("./chat.js");
    await cancelTurn.dispatch({ chatID: "c1" });
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({ type: "cancel", chat_id: "c1" }),
      expect.anything(),
    );
    const [cmd] = mockSend.mock.calls[0] ?? [];
    expect((cmd as { payload?: unknown }).payload).toBeUndefined();
  });

  // The send-now arrow's row rides the cancel, so the turn-end resend can order it first.
  it("names the lead row in the payload when one is given", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { cancelTurn } = await import("./chat.js");
    await cancelTurn.dispatch({ chatID: "c1", lead: "steer-m2" });
    const [cmd] = mockSend.mock.calls[0] ?? [];
    expect((cmd as { payload?: unknown }).payload).toEqual({ lead: "steer-m2" });
  });
});

describe("chat.load_sessions", () => {
  /** The reply as the server sends it. The per-list verdicts are REQUIRED by the generated decoder
   *  (no omitempty in Go), so an absent verdict cannot read as success. */
  const empty = { sessions: [], runs: [], sessions_state: "ready", runs_state: "ready" };

  it("GETs /api/sessions and dedupes concurrent calls", async () => {
    vi.useFakeTimers();
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => {
            r(new Response(JSON.stringify(empty), { status: 200 }));
          }, 50),
        ),
    );
    const { loadSessions } = await import("./chat.js");
    const p1 = loadSessions.dispatch(undefined);
    const p2 = loadSessions.dispatch(undefined);
    await vi.advanceTimersByTimeAsync(50);
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toEqual(empty);
    expect(r1).toEqual(r2);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it("reports a reply missing a read verdict as a failure, not as an empty list", async () => {
    // Without the decoder "the read failed" and "nothing to resume" are the same empty 200.
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ sessions: [], runs: [] })));
    const { loadSessions } = await import("./chat.js");

    expect(await loadSessions.dispatch(undefined)).toBe(null);
  });
});

// The minting command's wire contract: no chat id in the envelope, the op id in the payload, and
// the reply's chat is what the caller opens.
describe("chat.create", () => {
  const header = {
    id: "c-minted",
    name: "New conversation",
    model: "claude-opus-5",
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    created_at: 0,
    updated_at: 0,
    turn_count: 0,
  };

  it("sends create_chat with the op id and NO chat id", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { createChat } = await import("./chat.js");
    await createChat.dispatch({ opID: "op-1", model: "claude-opus-5" });

    const sent = mockSend.mock.calls.at(-1)?.[0] as { chat_id?: string; payload: unknown };
    expect(sent).toMatchObject({
      type: "create_chat",
      payload: { op_id: "op-1", model: "claude-opus-5" },
    });
    expect(sent.chat_id).toBeUndefined();
  });

  // The framework's per-dispatch key dedupes a retry inside the server's 5-minute cache; the op id
  // covers the fall-through past that TTL, so both must travel.
  it("carries the framework's idempotency key alongside the op id", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { createChat } = await import("./chat.js");
    const { IDEMPOTENCY_COMMAND_FIELD } = await import("./index.js");
    await createChat.dispatch({ opID: "op-1" });

    const sent = mockSend.mock.calls.at(-1)?.[0] as Record<string, unknown>;
    expect(typeof sent[IDEMPOTENCY_COMMAND_FIELD]).toBe("string");
  });

  it("returns the chat the server minted, with the tab it opened and the version", async () => {
    mockSend.mockResolvedValue({
      ok: true,
      status: 200,
      body: {
        ok: true,
        chat: header,
        subject: {
          id: "tb_1",
          kind: "chat",
          ref: "c-minted",
          parent: "",
          pinned: false,
          owns: true,
        },
        version: 7,
      },
    });
    const { createChat } = await import("./chat.js");
    const got = await createChat.dispatch({ opID: "op-1" });

    expect(got?.chat.id).toBe("c-minted");
    expect(got?.chat.model).toBe("claude-opus-5");
    // The subject and committed version reach the caller, which the adoption path paints from.
    expect(got?.subject?.id).toBe("tb_1");
    expect(got?.version).toBe(7);
  });

  it("carries no subject when the reply omits it", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { createChat } = await import("./chat.js");
    const got = await createChat.dispatch({ opID: "op-1" });
    expect(got?.chat.id).toBe("c-minted");
    expect(got?.subject).toBeUndefined();
  });

  // A 200 with no readable chat is a FAILURE: there is nothing to open.
  it("fails when the reply names no chat", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true } });
    const { createChat } = await import("./chat.js");
    expect(await createChat.dispatch({ opID: "op-1" })).toBeNull();
  });

  it("fails when the command itself fails", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" });
    const { createChat } = await import("./chat.js");
    expect(await createChat.dispatch({ opID: "op-1" })).toBeNull();
  });

  // The server defaults an absent name and model; sending "" would read as a choice.
  it("omits an unset name and model rather than sending empty strings", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { createChat } = await import("./chat.js");
    await createChat.dispatch({ opID: "op-1", name: "", model: "" });

    const sent = mockSend.mock.calls.at(-1)?.[0] as { payload: Record<string, unknown> };
    expect(sent.payload).toEqual({ op_id: "op-1" });
  });
});

// The server mints the new chat's id.
describe("chat.resume_session", () => {
  const header = {
    id: "c-minted",
    name: "Earlier work",
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    created_at: 0,
    updated_at: 0,
    turn_count: 0,
  };

  it("sends the session id, the title and an op id, and NO chat id", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { resumeSession } = await import("./chat.js");
    await resumeSession.dispatch({
      opID: "op-1",
      sessionID: "sess_abc-123",
      name: "Earlier work",
    });
    const sent = mockSend.mock.calls.at(-1)?.[0] as { chat_id?: string; payload: unknown };
    expect(sent).toMatchObject({
      type: "resume_session",
      payload: { session_id: "sess_abc-123", name: "Earlier work", op_id: "op-1" },
    });
    expect(sent.chat_id).toBeUndefined();
  });

  it("returns the chat the server created, so the caller can open it", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, chat: header } });
    const { resumeSession } = await import("./chat.js");
    const got = await resumeSession.dispatch({
      opID: "op-2",
      sessionID: "sess_abc-123",
      name: "Earlier work",
    });
    expect(got?.chat.id).toBe("c-minted");
  });

  // A 200 with no readable chat is a FAILURE: the session was adopted into an unaddressable chat.
  it("fails when the reply names no chat", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true } });
    const { resumeSession } = await import("./chat.js");
    const got = await resumeSession.dispatch({
      opID: "op-3",
      sessionID: "sess_abc-123",
      name: "Earlier work",
    });
    expect(got).toBeNull();
  });
});

// No chat.resolve_pending_change test: a turn's writes are approved through
// chat.respond_permission, the reply KAS uses for every permission.
describe("chat.exports", () => {
  it("exposes no pending-change resolver", async () => {
    const mod = await import("./chat.js");
    for (const name of [
      "resolvePendingChange",
      "resolveAllPending",
      "trustPending",
      "clearPendingTrust",
    ]) {
      expect(mod).not.toHaveProperty(name);
    }
  });
});

describe("chat.respond_permission", () => {
  it("sends permission_response with request_id and option_id", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { respondPermission } = await import("./chat.js");
    await respondPermission.dispatch({ chatID: "c1", requestID: 42, optionID: "allow_once" });
    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "permission_response",
        chat_id: "c1",
        payload: expect.objectContaining({ request_id: 42, option_id: "allow_once" }),
      }),
      expect.anything(),
    );
  });

  it("sends a deny note as rejection_reason", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { respondPermission } = await import("./chat.js");
    await respondPermission.dispatch({
      chatID: "c1",
      requestID: 43,
      optionID: "reject_once",
      rejectionReason: "wrong directory",
    });
    expect(mockSend.mock.calls[0]?.[0]).toMatchObject({
      payload: { request_id: 43, option_id: "reject_once", rejection_reason: "wrong directory" },
    });
  });

  it("omits rejection_reason when no note was written", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    const { respondPermission } = await import("./chat.js");
    await respondPermission.dispatch({ chatID: "c1", requestID: 44, optionID: "reject_once" });
    // The inner payload is matched exactly, so a stray rejection_reason key fails.
    expect(mockSend.mock.calls[0]?.[0]).toEqual(
      expect.objectContaining({ payload: { request_id: 44, option_id: "reject_once" } }),
    );
  });
});
