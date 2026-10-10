import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as ChatActionsModule from "./actions/chat.js";
import type { MergeAnswer } from "./actions/chat.js";
import type * as Chat from "./chat.js";
import type * as NoticeSubject from "./notice-subject.js";

type ChatActions = typeof ChatActionsModule;

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
const { mockMerge, mockStatus, mockSwitch, mockChatNotice, mockActionNotice } = vi.hoisted(() => ({
  mockMerge: vi.fn(),
  mockStatus: vi.fn(),
  mockSwitch: vi.fn(async () => "activated"),
  mockChatNotice: vi.fn(),
  mockActionNotice: vi.fn(),
}));
vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<ChatActions>()),
  mergeTangent: { dispatch: mockMerge },
  readMergeStatus: { dispatch: mockStatus },
}));
vi.mock("./chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Chat>()),
  switchSession: mockSwitch,
}));
vi.mock("./notice-subject.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NoticeSubject>()),
  subjectName: (id: string) => id,
  chatNotice: mockChatNotice,
  actionNotice: mockActionNotice,
}));

const { mergeTangentChat, noteTangentMerged, noteTangentMergeFailed, reconcileMerges } =
  await import("./tangent-merge.js");
const store = await import("./store.js");
const { BUS_COMMAND_FAILED, onBus } = await import("./bus.js");

const failures: string[] = [];
onBus(BUS_COMMAND_FAILED, ({ chatID, message }) => {
  failures.push(`${chatID}: ${message}`);
});

function seed(active: string): void {
  store.setSessions(
    ["c-tangent", "c-parent", "c-other"].map((id) => ({
      id,
      name: id,
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
      tangent: id === "c-tangent",
    })),
  );
  store.setActive(active);
}

/** Long enough for the lazy chat.js import a switch would take. */
async function settle(): Promise<void> {
  await import("./chat.js");
  await new Promise((r) => setTimeout(r, 0));
}

function answer(value: MergeAnswer) {
  return { outcome: Promise.resolve({ status: "success", value }) };
}

const lostReply = {
  outcome: Promise.resolve({
    status: "error",
    error: { message: "Failed to fetch", status: 0, code: "network" },
  }),
};

const unreadable = {
  outcome: Promise.resolve({
    status: "error",
    error: { message: "Failed to fetch", status: 0, code: "network" },
  }),
};

beforeEach(() => {
  vi.clearAllMocks();
  failures.length = 0;
  mockMerge.mockReturnValue(answer({ state: "running" }));
  mockStatus.mockReturnValue(answer({ state: "running" }));
});

function notices(): string[] {
  return [...mockChatNotice.mock.calls, ...mockActionNotice.mock.calls].map((c) => String(c[1]));
}

function opIDOf(call: number): string {
  const args = mockMerge.mock.calls[call]?.[0] as { opID?: unknown } | undefined;
  if (typeof args?.opID !== "string" || args.opID === "") {
    throw new Error(`dispatch ${String(call)} carried no op_id`);
  }
  return args.opID;
}

describe("tangent merge", () => {
  it("switches to the parent when the merge lands with the tangent on screen", async () => {
    seed("c-tangent");
    await mergeTangentChat("c-tangent");
    noteTangentMerged(opIDOf(0), "c-parent");
    await vi.waitFor(() => {
      expect(mockSwitch).toHaveBeenCalledWith("c-parent");
    });
  });

  it("switches to the parent after the reader moved to another chat", async () => {
    seed("c-tangent");
    await mergeTangentChat("c-tangent");
    store.setActive("c-other");
    noteTangentMerged(opIDOf(0), "c-parent");
    await vi.waitFor(() => {
      expect(mockSwitch).toHaveBeenCalledWith("c-parent");
    });
  });

  it("still follows an accepted merge after a second attempt on the tangent was refused", async () => {
    seed("c-tangent");
    await mergeTangentChat("c-tangent");
    mockMerge.mockReturnValueOnce({
      outcome: Promise.resolve({
        status: "error",
        error: { message: "this tangent is still working", status: 409, code: "busy" },
      }),
    });
    await mergeTangentChat("c-tangent");
    expect(opIDOf(1)).not.toBe(opIDOf(0));
    noteTangentMerged(opIDOf(0), "c-parent");
    await vi.waitFor(() => {
      expect(mockSwitch).toHaveBeenCalledWith("c-parent");
    });
  });

  it("follows a merge the server accepted though its reply never arrived", async () => {
    seed("c-tangent");
    mockMerge.mockReturnValueOnce({
      outcome: Promise.resolve({
        status: "error",
        error: { message: "Failed to fetch", status: 0, code: "network" },
      }),
    });
    await mergeTangentChat("c-tangent");
    noteTangentMerged(opIDOf(0), "c-parent");
    await vi.waitFor(() => {
      expect(mockSwitch).toHaveBeenCalledWith("c-parent");
    });
  });

  it("follows a merge whose retry met the first attempt still in progress", async () => {
    seed("c-tangent");
    mockMerge.mockReturnValueOnce({
      outcome: Promise.resolve({
        status: "error",
        error: { message: "request already in progress", status: 409, code: "in_progress" },
      }),
    });
    await mergeTangentChat("c-tangent");
    noteTangentMerged(opIDOf(0), "c-parent");
    await vi.waitFor(() => {
      expect(mockSwitch).toHaveBeenCalledWith("c-parent");
    });
  });

  it("still follows an accepted merge after many later attempts went unanswered", async () => {
    seed("c-tangent");
    await mergeTangentChat("c-tangent");
    mockMerge.mockReturnValue({
      outcome: Promise.resolve({
        status: "error",
        error: { message: "Failed to fetch", status: 0, code: "network" },
      }),
    });
    mockStatus.mockReturnValue(answer({ state: "absent" }));
    for (let i = 0; i < 40; i++) {
      await mergeTangentChat("c-tangent");
    }
    noteTangentMerged(opIDOf(0), "c-parent");
    await vi.waitFor(() => {
      expect(mockSwitch).toHaveBeenCalledWith("c-parent");
    });
  });

  it("stops waiting on a merge the server refused before accepting it", async () => {
    seed("c-tangent");
    mockMerge.mockReturnValueOnce({
      outcome: Promise.resolve({
        status: "error",
        error: { message: "only a tangent can be merged", status: 409 },
      }),
    });
    await mergeTangentChat("c-tangent");
    noteTangentMerged(opIDOf(0), "c-parent");
    await settle();
    expect(mockSwitch).not.toHaveBeenCalled();
  });

  it("ignores a merge another device asked for, one with no op_id, or one that failed", async () => {
    seed("c-tangent");
    await mergeTangentChat("c-tangent");
    noteTangentMerged("op-another-device", "c-parent");
    noteTangentMerged(undefined, "c-parent");
    noteTangentMergeFailed(opIDOf(0), "nothing was merged");
    noteTangentMerged(opIDOf(0), "c-parent");
    await settle();
    expect(mockSwitch).not.toHaveBeenCalled();
  });

  it("never asks to merge an ordinary chat", async () => {
    seed("c-other");
    await mergeTangentChat("c-other");
    expect(mockMerge).not.toHaveBeenCalled();
  });

  it("settles a merge that never reached the server once the server says it holds no such op", async () => {
    seed("c-tangent");
    mockMerge.mockReturnValueOnce(lostReply);
    mockStatus.mockReturnValueOnce(answer({ state: "absent" }));
    await mergeTangentChat("c-tangent");

    expect(mockStatus).toHaveBeenCalledWith({ chatID: "c-tangent", opID: opIDOf(0) });
    expect(notices()).toEqual(["The merge did not start"]);
    noteTangentMerged(opIDOf(0), "c-parent");
    await settle();
    expect(mockSwitch).not.toHaveBeenCalled();
  });

  it("switches to the parent when the status read finds the lost merge succeeded", async () => {
    seed("c-tangent");
    mockMerge.mockReturnValueOnce(lostReply);
    mockStatus.mockReturnValueOnce(answer({ state: "succeeded", parentChatID: "c-parent" }));
    await mergeTangentChat("c-tangent");
    await vi.waitFor(() => {
      expect(mockSwitch).toHaveBeenCalledWith("c-parent");
    });

    noteTangentMerged(opIDOf(0), "c-parent");
    await settle();
    expect(mockSwitch).toHaveBeenCalledTimes(1);
    expect(notices()).toEqual([]);
  });

  it("reports the server's reason as a failure when the merge's answer says it failed", async () => {
    seed("c-tangent");
    mockMerge.mockReturnValueOnce(answer({ state: "failed", message: "nothing was merged" }));
    await mergeTangentChat("c-tangent");

    expect(failures).toEqual(["c-tangent: nothing was merged"]);
    expect(notices()).toEqual([]);
    noteTangentMerged(opIDOf(0), "c-parent");
    await settle();
    expect(mockSwitch).not.toHaveBeenCalled();
  });

  it("warns once when even the status read fails, then settles at the next connection", async () => {
    seed("c-tangent");
    mockMerge.mockReturnValueOnce(lostReply);
    mockStatus.mockReturnValueOnce(unreadable);
    await mergeTangentChat("c-tangent");
    expect(notices()).toEqual(["Could not confirm whether the merge started"]);

    mockStatus.mockReturnValueOnce(answer({ state: "succeeded", parentChatID: "c-parent" }));
    reconcileMerges();
    await vi.waitFor(() => {
      expect(mockSwitch).toHaveBeenCalledWith("c-parent");
    });
    mockStatus.mockClear();
    reconcileMerges();
    expect(mockStatus).not.toHaveBeenCalled();
  });

  it("says the outcome is unknown when an accepted merge expired before this device reconnected", async () => {
    seed("c-tangent");
    await mergeTangentChat("c-tangent");
    mockStatus.mockReturnValueOnce(answer({ state: "absent" }));
    reconcileMerges();
    await vi.waitFor(() => {
      expect(notices()).toEqual(["Could not confirm how the merge ended"]);
    });
    mockStatus.mockClear();
    reconcileMerges();
    expect(mockStatus).not.toHaveBeenCalled();
  });

  it("leaves a merge whose admission is still in flight to its own reply", async () => {
    seed("c-tangent");
    let reply: (v: unknown) => void = () => undefined;
    mockMerge.mockReturnValueOnce({
      outcome: new Promise((resolve) => {
        reply = resolve;
      }),
    });
    const merging = mergeTangentChat("c-tangent");
    reconcileMerges();
    expect(mockStatus).not.toHaveBeenCalled();
    reply({ status: "success", value: { state: "running" } });
    await merging;
    noteTangentMergeFailed(opIDOf(0), "nothing was merged");
  });
});
