// Drives the REAL store and asserts its state ("the chat appears / disappears"); tabs.ts stays a
// mock, since closing a tab is outside this handler's contract.

import { vi, describe, it, expect, beforeEach } from "vitest";
import { setSessions, getSessions, get, setActive } from "../store.js";
import type { Session, ChatHeader } from "../types.js";

// tabs.ts: a separate subsystem; assert the close command, not store state.
const mockCloseTab = vi.fn();
const mockHasTab = vi.fn(() => false);
vi.mock("../tabs.js", () => ({
  // Undefined: present only so real-ESM linking succeeds (navigate.js's `openSpec` imports these).
  openTab: undefined,
  parentChatRef: undefined,
  setTabParent: undefined,
  activateTab: undefined,
  tabIdFor: undefined,
  getActiveTabId: undefined,
  getActiveTabRoute: undefined,
  openEditorView: undefined,
  setGitTab: undefined,
  setSettingsTab: undefined,
  setTabDirty: undefined,
  openGitView: undefined,
  openSettingsView: undefined,
  closeTab: mockCloseTab,
  hasTab: mockHasTab,
  refreshActiveView: undefined,
  activeChatRef: undefined,
}));

// Capture SSE handlers via shared helper.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
vi.mock("../bus.js", () => createBusMock());

// chat_deleted fires fire-and-forget dynamic imports; stub them so they
// resolve to no-ops instead of pulling in real DOM-touching modules.
vi.mock("../banner-stack.js", () => ({ clearBannersForChat: vi.fn() }));

// composer-state owns WHICH chat a remote change may touch (composer-state.test.ts); this file
// owns the ROUTING.
const mockAdoptComposer = vi.fn();
const mockDropComposer = vi.fn();
vi.mock("../composer-state.js", () => ({
  dropComposerState: mockDropComposer,
  adoptRemoteComposerState: mockAdoptComposer,
  // Present-but-inert so real-ESM linking succeeds: another module in this graph
  // imports the name.
  restoreFailedSend: vi.fn(),
}));

// Import after mocks so chat.ts registers its handlers against the bus mock.
await import("./chat.js");

function makeHeader(over: Partial<ChatHeader> = {}): ChatHeader {
  return {
    id: "c1",
    name: "Test Chat",
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
    ...over,
  };
}

function makeSession(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: "seeded",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
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
    ...over,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  setSessions([]);
});

describe("chat_created", () => {
  it("inserts the chat header into the store", () => {
    fireSSE("chat_created", "", makeHeader({ id: "c1", name: "Test Chat" }));
    expect(get("c1")?.name).toBe("Test Chat");
  });

  it("ignores an undefined payload (no chat created)", () => {
    fireSSE("chat_created", "", undefined);
    expect(getSessions()).toHaveLength(0);
  });
});

describe("chat_updated", () => {
  it("re-syncs the header fields of an existing chat", () => {
    setSessions([makeSession("c1", { name: "Old Name" })]);
    fireSSE("chat_updated", "", makeHeader({ id: "c1", name: "Renamed" }));
    expect(get("c1")?.name).toBe("Renamed");
  });

  it("ignores an undefined payload (existing chat unchanged)", () => {
    setSessions([makeSession("c1", { name: "Old Name" })]);
    fireSSE("chat_updated", "", undefined);
    expect(get("c1")?.name).toBe("Old Name");
  });
});

// Chat ids are minted server-side, so the ordinary openTab emit covers a new chat; what remains is
// the URL rewrite, asserted against the real router (the address bar is the observable fact).
describe("chat_created and the id-less chat URL", () => {
  it("rewrites the route to the created chat when it is this device's active one", () => {
    setSessions([makeSession("c1")]);
    setActive("c1");
    history.replaceState(null, "", "/");

    fireSSE("chat_created", "", makeHeader({ id: "c1", name: "Named by the server" }));

    expect(location.pathname).toBe("/chat/c1");
  });

  // Never hijack a reader who has navigated away meanwhile: the rewrite fires only
  // from the id-less chat route.
  it("leaves the route alone when the reader is not on the id-less chat route", () => {
    setSessions([makeSession("c1")]);
    setActive("c1");
    history.replaceState(null, "", "/settings");

    fireSSE("chat_created", "", makeHeader({ id: "c1" }));

    expect(location.pathname).toBe("/settings");
  });

  it("leaves the route alone for a chat created on another device", () => {
    setSessions([makeSession("c1"), makeSession("c2")]);
    setActive("c1");
    history.replaceState(null, "", "/");

    fireSSE("chat_created", "", makeHeader({ id: "c2" }));

    expect(location.pathname).toBe("/");
  });
});

describe("chat_deleted", () => {
  it("removes the chat from the store", () => {
    setSessions([makeSession("c1")]);
    fireSSE("chat_deleted", "", { id: "c1" });
    expect(get("c1")).toBeUndefined();
  });

  // The dock's queue is keyed by chat id and outlives the session row: without a drop, a chat
  // recreated under the same id inherits a stale ask (the tab dot's top state).
  it("drops the chat's unanswered asks with it", async () => {
    const { pushDecision, hasPendingDecision, _resetForTest } = await import("../decision-dock.js");
    _resetForTest();
    setSessions([makeSession("c1")]);
    pushDecision({
      kind: "permission",
      chatID: "c1",
      runID: "",
      requestID: 1,
      payload: { request_id: 1, title: "run a command", options: [] } as never,
      submit: vi.fn(),
    });
    expect(hasPendingDecision("c1")).toBe(true);

    fireSSE("chat_deleted", "", { id: "c1" });
    expect(hasPendingDecision("c1")).toBe(false);
  });

  // Load-bearing absence: the membership coordinator closes a deleted chat's tabs under the delete's
  // lock, so a `close_tab` here would double-close and race the deleting device.
  it("dispatches no tab close, even when a tab is open for the deleted chat", () => {
    setSessions([makeSession("c2")]);
    mockHasTab.mockReturnValue(true);
    fireSSE("chat_deleted", "", { id: "c2" });
    expect(mockCloseTab).not.toHaveBeenCalled();
    expect(get("c2")).toBeUndefined();
  });

  // The three per-chat cleanups keyed by CHAT id still run, because each outlives
  // the tab and the removal frame's own teardown may not have landed yet.
  it("still drops the chat's composer state, which is keyed by chat rather than by tab", () => {
    setSessions([makeSession("c4")]);
    fireSSE("chat_deleted", "", { id: "c4" });
    expect(mockDropComposer).toHaveBeenCalledWith("c4");
    expect(mockCloseTab).not.toHaveBeenCalled();
  });

  it("does not reach the tab store at all when nothing is open", () => {
    setSessions([makeSession("c3")]);
    mockHasTab.mockReturnValue(false);
    fireSSE("chat_deleted", "", { id: "c3" });
    expect(mockCloseTab).not.toHaveBeenCalled();
    expect(get("c3")).toBeUndefined();
  });

  it("ignores a payload with no id (chat left intact)", () => {
    setSessions([makeSession("c1")]);
    fireSSE("chat_deleted", "", {});
    expect(get("c1")).toBeDefined();
    expect(mockCloseTab).not.toHaveBeenCalled();
  });

  it("ignores an undefined payload (chat left intact)", () => {
    setSessions([makeSession("c1")]);
    fireSSE("chat_deleted", "", undefined);
    expect(get("c1")).toBeDefined();
  });
});

// draft_changed exists so an idle device converges on a draft it is not typing.
// The adoption RULE is composer-state's; this is the wire half.
describe("draft_changed", () => {
  it("hands the frame's composer state to the composer layer", () => {
    fireSSE("draft_changed", "c1", {
      text: "typed on the desktop",
      attachments: ["docs/spec.pdf"],
    });
    expect(mockAdoptComposer).toHaveBeenCalledWith("c1", "typed on the desktop", ["docs/spec.pdf"]);
  });

  // An empty draft is a VALUE — it is how a sent or abandoned message clears — so
  // it has to travel rather than read as a missing field.
  it("forwards an empty draft as the clear it is", () => {
    fireSSE("draft_changed", "c1", { text: "" });
    expect(mockAdoptComposer).toHaveBeenCalledWith("c1", "", []);
  });

  // Chat-scoped: with no chat id there is nothing to apply the state to, so the
  // frame is unroutable rather than ambiguous.
  it("drops a frame with no chat id", () => {
    fireSSE("draft_changed", "", { text: "nobody's" });
    expect(mockAdoptComposer).not.toHaveBeenCalled();
  });

  it("drops an undefined payload", () => {
    fireSSE("draft_changed", "c1", undefined);
    expect(mockAdoptComposer).not.toHaveBeenCalled();
  });
});
