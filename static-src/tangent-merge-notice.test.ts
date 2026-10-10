import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as Chat from "./chat.js";
import type * as Tabs from "./tabs.js";
import type * as Toast from "./toast.js";
import type * as Transport from "./transport.js";

// The real chat.js the spreads below load reads these app-shell elements at module load.
vi.hoisted(() => {
  for (const id of [
    "messages",
    "messages-wrap",
    "messages-wrap-outer",
    "chat-view",
    "scroll-bottom",
  ]) {
    const el = document.createElement("div");
    el.id = id;
    document.body.append(el);
  }
});
const { mockSend, mockFetch } = vi.hoisted(() => ({ mockSend: vi.fn(), mockFetch: vi.fn() }));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  send: mockSend,
  newOpID: vi.fn(() => "op-test"),
}));
vi.mock("./chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Chat>()),
  switchSession: vi.fn(),
}));

const toast = await import("./toast.js");
const store = await import("./store.js");
const { resetActionFramework } = await import("./actions/__test-helpers__/action-test-setup.js");
const { configureApi } = await import("@cplieger/actions");
const { mergeTangentChat, noteTangentMerged, noteTangentMergeFailed } =
  await import("./tangent-merge.js");
const { reportFailure, _resetForTest: resetFailureNotices } = await import("./failure-notice.js");

/** A tangent_merge_failed frame as both of its SSE handlers take it: the error route's report
 *  (handlers/turn.ts) and the merge's settlement (handlers/chat.ts). */
function frameFailed(opID: string, message: string): void {
  reportFailure("c-tangent", message, undefined, false);
  noteTangentMergeFailed(opID, message);
}

function toastTexts(): string[] {
  return [
    vi.mocked(toast.error),
    vi.mocked(toast.errorWithAction),
    vi.mocked(toast.notice),
    vi.mocked(toast.success),
  ].flatMap((fn) => fn.mock.calls.map((c) => String(c[0])));
}

function statusAnswer(state: string): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify({ state }), {
      status: 200,
      headers: { "content-type": "application/json" },
    }),
  );
}

beforeEach(() => {
  resetActionFramework();
  resetFailureNotices();
  vi.clearAllMocks();
  // Re-armed per test: configureApi REPLACES the library's fetch and exports no reset.
  configureApi({ fetchFn: mockFetch });
  mockFetch.mockImplementation(() => Promise.reject(new TypeError("Failed to fetch")));
  store.setSessions([
    {
      id: "c-tangent",
      name: "side quest",
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      usage: { context_pct: 0, context_size: 0, credits: 0, last_turn_ms: 0, has_real_data: false },
      turns: new Map(),
      turn_order: [],
      turn_count: 1,
      has_more: false,
      thinking: false,
      working_label: "Thinking",
      tangent: true,
    },
  ]);
});

describe("tangent merge notices", () => {
  it("says it could not confirm the merge started when every reply and status read was lost", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Failed to fetch", code: "network" });

    await mergeTangentChat("c-tangent");

    expect(mockSend).toHaveBeenCalledTimes(3);
    expect(mockFetch).toHaveBeenCalledTimes(3);
    expect(String(mockFetch.mock.calls[0]?.[0])).toContain("/api/chats/c-tangent/merges/op-test");
    expect(toastTexts()).toEqual(["side quest: Could not confirm whether the merge started"]);
  });

  it("says the merge did not start when the server holds no such op", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Failed to fetch", code: "network" });
    mockFetch.mockImplementation(() => statusAnswer("absent"));

    await mergeTangentChat("c-tangent");

    expect(toastTexts()).toEqual(["side quest: The merge did not start"]);
  });

  it("says nothing more when every attempt met the first one still starting and it then ran", async () => {
    mockSend.mockResolvedValue({
      ok: false,
      status: 409,
      error: "this merge is still starting",
      reason: "in_progress",
    });
    mockFetch.mockImplementation(() => statusAnswer("running"));

    await mergeTangentChat("c-tangent");

    expect(mockSend).toHaveBeenCalledTimes(3);
    expect(toastTexts()).toEqual([]);
  });

  it("reports a refusal sent before the merge was accepted as a failure, with the server's words", async () => {
    mockSend.mockResolvedValue({
      ok: false,
      status: 409,
      error: "this tangent is still working",
      reason: "busy",
    });

    await mergeTangentChat("c-tangent");

    expect(toastTexts()).toEqual(["side quest: Merge failed: this tangent is still working"]);
  });

  it("says nothing when the merge is accepted, leaving completion to its frame", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { ok: true, state: "running" } });

    await mergeTangentChat("c-tangent");

    expect(toastTexts()).toEqual([]);
  });

  it("says nothing more when the merge landed while its reply was still lost", async () => {
    const lost = { ok: false, status: 0, error: "Failed to fetch", code: "network" };
    let reply: (r: unknown) => void = () => undefined;
    mockSend.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          reply = resolve;
        }),
    );
    mockSend.mockResolvedValue(lost);

    const merging = mergeTangentChat("c-tangent");
    await vi.waitFor(() => {
      expect(mockSend).toHaveBeenCalledTimes(1);
    });
    noteTangentMerged("op-test", "c-parent");
    reply(lost);
    await merging;

    expect(toastTexts()).toEqual([]);
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it("reports a failed merge once when its status answer arrives before its frame", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "Failed to fetch", code: "network" });
    mockFetch.mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ state: "failed", message: "nothing was merged" }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      ),
    );

    await mergeTangentChat("c-tangent");
    frameFailed("op-test", "nothing was merged");

    expect(toastTexts()).toEqual(["side quest: nothing was merged"]);
  });

  it("reports a failed merge once when its frame arrives before its answer", async () => {
    let reply: (r: unknown) => void = () => undefined;
    mockSend.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          reply = resolve;
        }),
    );

    const merging = mergeTangentChat("c-tangent");
    await vi.waitFor(() => {
      expect(mockSend).toHaveBeenCalledTimes(1);
    });
    frameFailed("op-test", "nothing was merged");
    reply({
      ok: true,
      status: 200,
      body: { ok: true, state: "failed", message: "nothing was merged" },
    });
    await merging;

    expect(toastTexts()).toEqual(["side quest: nothing was merged"]);
  });

  it("reports only the frame's reason when the merge failed while its reply was still lost", async () => {
    const lost = { ok: false, status: 0, error: "Failed to fetch", code: "network" };
    let reply: (r: unknown) => void = () => undefined;
    mockSend.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          reply = resolve;
        }),
    );
    mockSend.mockResolvedValue(lost);

    const merging = mergeTangentChat("c-tangent");
    await vi.waitFor(() => {
      expect(mockSend).toHaveBeenCalledTimes(1);
    });
    frameFailed("op-test", "nothing was merged");
    reply(lost);
    await merging;

    expect(toastTexts()).toEqual(["side quest: nothing was merged"]);
    expect(mockFetch).not.toHaveBeenCalled();
  });
});
