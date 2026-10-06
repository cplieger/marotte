// The failure toast: dedupes its two channels, names its chat, links a background one, carries a remedy.
import { describe, it, expect, beforeEach, vi } from "vitest";

const { mockDismiss, mockToastError, mockErrorWithAction, mockActivateTab } = vi.hoisted(() => {
  const dismiss = vi.fn();
  return {
    mockDismiss: dismiss,
    mockToastError: vi.fn(
      (_message: string, _retry?: { label?: string; onClick: () => void }): (() => void) => dismiss,
    ),
    mockErrorWithAction: vi.fn(
      (_message: string, _action: { label?: string; onClick: () => void }): (() => void) => dismiss,
    ),
    mockActivateTab: vi.fn(),
  };
});
vi.mock("./toast.js", async () => ({
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
  error: mockToastError,
  errorWithAction: mockErrorWithAction,
}));

// The tab strip decides "is this chat on screen", so it is mocked; `tabIdFor` maps to an opaque server-minted id.
const { mockActiveTabId, mockTabIdFor } = vi.hoisted(() => ({
  mockActiveTabId: vi.fn(() => ""),
  mockTabIdFor: vi.fn((_kind: string, ref = "") => ref),
}));
// The complete tab-store mock: Browser Mode links ESM for real, so every name the graph reaches must exist.
vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  activateTab: mockActivateTab,
  getActiveTabId: mockActiveTabId,
  tabIdFor: mockTabIdFor,
}));

import { reportFailure, clearFailure, _resetForTest } from "./failure-notice.js";
import { BUS_COMMAND_FAILED, emitBus } from "./bus.js";
import { setSessions, setActive } from "./store.js";
import type { Session } from "./types.js";
function makeSession(id: string, name: string): Session {
  return {
    id,
    name,
    model: "",
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
  };
}

function lastToast(): string {
  const plain = mockToastError.mock.calls.at(-1)?.[0];
  const acted = mockErrorWithAction.mock.calls.at(-1)?.[0];
  return acted ?? plain ?? "";
}

function lastAction(): { label?: string; onClick: () => void } | undefined {
  return mockErrorWithAction.mock.calls.at(-1)?.[1];
}

function lastStickyAction(): { label?: string; onClick: () => void } | undefined {
  return mockToastError.mock.calls.at(-1)?.[1];
}

function toastCount(): number {
  return mockToastError.mock.calls.length + mockErrorWithAction.mock.calls.length;
}

beforeEach(() => {
  _resetForTest();
  mockToastError.mockClear();
  mockErrorWithAction.mockClear();
  mockDismiss.mockClear();
  mockActivateTab.mockClear();
  // The tab decides "on screen", not the store's active chat.
  mockActiveTabId.mockReturnValue("c1");
  setSessions([makeSession("c1", "Fix the parser"), makeSession("c2", "Write the docs")]);
  setActive("c1");
});

describe("failure-notice reports the server's own prose", () => {
  it("shows the reason verbatim after the chat's name", () => {
    reportFailure("c1", "Too many requests, please wait before trying again.");
    expect(toastCount()).toBe(1);
    expect(lastToast()).toBe("Fix the parser: Too many requests, please wait before trying again.");
  });

  // A toast names its chat every time; the chat on screen is named but not linked.
  it("names but does not link the chat whose transcript is on screen", () => {
    reportFailure("c1", "at capacity");
    expect(lastToast()).toBe("Fix the parser: at capacity");
    expect(lastAction()).toBeUndefined();
  });

  it("substitutes a pointer to the log when the server sent no message", () => {
    reportFailure("c1", "   ");
    expect(lastToast()).toContain("server log");
  });

  // The untruncated reason is on the turn's own divider.
  it("truncates a very long reason", () => {
    reportFailure("c1", "x".repeat(4000));
    expect(lastToast().length).toBeLessThan(300);
    expect(lastToast().endsWith("\u2026")).toBe(true);
  });
});

describe("failure-notice says which chat it is about", () => {
  it("names and links a chat that is not on screen", () => {
    reportFailure("c2", "at capacity");
    expect(lastToast()).toBe("Write the docs: at capacity");
    expect(lastAction()?.label).toBe("Open");
  });

  it("the link activates that chat's tab", () => {
    reportFailure("c2", "at capacity");
    lastAction()?.onClick();
    expect(mockActivateTab).toHaveBeenCalledWith("c2");
  });

  // `store.getActiveId()` keeps the last opened chat while the reader is on another tab, so "active chat" is the wrong test.
  it("names the chat even when it is the active chat but not the active TAB", () => {
    mockActiveTabId.mockReturnValue("__settings__");
    reportFailure("c1", "at capacity");
    expect(lastToast()).toBe("Fix the parser: at capacity");
    expect(lastAction()?.label).toBe("Open");
  });

  it("names the chat while the reader is in an editor tab", () => {
    mockActiveTabId.mockReturnValue("editor:/workspace/main.go");
    reportFailure("c1", "at capacity");
    expect(lastToast()).toBe("Fix the parser: at capacity");
  });

  // A chat name is its first prompt truncated to 80 chars server-side.
  it("truncates a long chat name in the prefix", () => {
    setSessions([makeSession("c3", "y".repeat(80))]);
    mockActiveTabId.mockReturnValue("c1");
    reportFailure("c3", "at capacity");
    expect(lastToast().indexOf(": at capacity")).toBeLessThan(45);
    expect(lastToast().endsWith("at capacity")).toBe(true);
  });

  // A failure racing chat_created still reports and links.
  it("links an unnamed chat without a prefix", () => {
    reportFailure("c-unknown", "at capacity");
    expect(lastToast()).toBe("at capacity");
    expect(lastAction()?.label).toBe("Open");
  });

  // activateTab no-ops on an unheld id; "no tab" is `tabIdFor` answering "".
  it("offers Open onto a retained chat that has no tab", () => {
    mockTabIdFor.mockReturnValue("");
    reportFailure("c2", "at capacity");
    expect(lastToast()).toBe("Write the docs: at capacity");
    expect(lastAction()?.label).toBe("Open");
  });

  it("neither names nor links a chatless failure", () => {
    reportFailure("", "the tools engine is unreachable");
    expect(lastToast()).toBe("the tools engine is unreachable");
    expect(lastAction()).toBeUndefined();
  });
});

describe("failure-notice dedupes the two channels one failure arrives on", () => {
  // Reported twice by design (POST body and SSE error frame), and both channels must keep reporting.
  it("shows one toast when both channels report the same reason", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c1", "at capacity");
    expect(toastCount()).toBe(1);
  });

  it("shows both when the same chat fails two different ways", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c1", "too many requests");
    expect(toastCount()).toBe(2);
  });

  // Two chats failing identically are two failures.
  it("shows both when two chats fail with the same reason", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c2", "at capacity");
    expect(toastCount()).toBe(2);
  });

  it("shows a repeat once the window has passed", () => {
    vi.useFakeTimers();
    try {
      reportFailure("c1", "at capacity");
      vi.advanceTimersByTime(6_000);
      reportFailure("c1", "at capacity");
      expect(toastCount()).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  // The latch must survive the retraction preceding every raise, or only a chat's first failure is deduped.
  it("dedupes both channels on a chat's SECOND distinct failure", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c1", "too many requests");
    reportFailure("c1", "too many requests");
    expect(toastCount()).toBe(2);
  });

  // Per-chat latch: one shared slot is un-latched by an unrelated chat's failure.
  it("dedupes both channels when another chat fails in between", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c2", "too many requests");
    reportFailure("c1", "at capacity");
    expect(toastCount()).toBe(2);
  });
});

describe("failure-notice carries the route's own remedy", () => {
  const remedy = { label: "Open custom instructions", onClick: () => undefined };

  // A remedy takes the action slot and goes sticky; which mock fires is the assertion.
  it("names a background chat and carries the remedy, stickily", () => {
    reportFailure("c2", "bad agent front matter", remedy);
    expect(mockErrorWithAction).not.toHaveBeenCalled();
    expect(lastToast()).toBe("Write the docs: bad agent front matter");
    expect(lastStickyAction()?.label).toBe("Open custom instructions");
  });

  it("carries it stickily for the chat that IS on screen, still named", () => {
    reportFailure("c1", "bad agent front matter", remedy);
    expect(mockErrorWithAction).not.toHaveBeenCalled();
    expect(lastToast()).toBe("Fix the parser: bad agent front matter");
    expect(lastStickyAction()?.label).toBe("Open custom instructions");
  });

  // A sticky remedy must not be retracted by a later failure on the same chat.
  it("is not retracted by a later failure on the same chat", () => {
    reportFailure("c1", "bad agent front matter", remedy);
    reportFailure("c1", "at capacity");
    expect(mockDismiss).not.toHaveBeenCalled();
    expect(toastCount()).toBe(2);
  });

  // A repeat past the dedupe window retracts its copy, or identical remedies stack.
  it("replaces its own repeat rather than standing beside it", () => {
    vi.useFakeTimers();
    try {
      reportFailure("c1", "bad agent front matter", remedy);
      vi.advanceTimersByTime(6_000);
      reportFailure("c1", "bad agent front matter", remedy);
      expect(toastCount()).toBe(2);
      expect(mockDismiss).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  // Each chat's remedy names its own chat, so none retracts another.
  it("leaves another chat's remedy standing", () => {
    reportFailure("c1", "bad agent front matter", remedy);
    reportFailure("c2", "bad agent front matter", remedy);
    expect(toastCount()).toBe(2);
    expect(mockDismiss).not.toHaveBeenCalled();
  });

  it("replaces its own repeat after another chat reported the same failure", () => {
    vi.useFakeTimers();
    try {
      reportFailure("c1", "bad agent front matter", remedy);
      reportFailure("c2", "bad agent front matter", remedy);
      vi.advanceTimersByTime(6_000);
      reportFailure("c1", "bad agent front matter", remedy);
      expect(toastCount()).toBe(3);
      expect(mockDismiss).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  // Held per failure, not per chat: two remedies on one chat are two problems.
  it("replaces its own repeat after a different remedy came in between", () => {
    reportFailure("c1", "bad agent front matter", remedy);
    reportFailure("c1", "refresh token expired", { label: "Sign in", onClick: () => undefined });
    reportFailure("c1", "bad agent front matter", remedy);
    expect(toastCount()).toBe(3);
    expect(mockDismiss).toHaveBeenCalledTimes(1);
  });
});

describe("failure-notice retracts a notice that turned out to be wrong", () => {
  // The prompt POST can die while its turn runs on, discovered up to two seconds later.
  it("dismisses the chat's live toast", () => {
    reportFailure("c1", "connection reset");
    clearFailure("c1");
    expect(mockDismiss).toHaveBeenCalledTimes(1);
  });

  it("is a no-op for a chat with no live toast", () => {
    clearFailure("c1");
    expect(mockDismiss).not.toHaveBeenCalled();
  });

  it("leaves another chat's toast standing", () => {
    reportFailure("c2", "at capacity");
    clearFailure("c1");
    expect(mockDismiss).not.toHaveBeenCalled();
  });

  // A retraction drops the latch too.
  it("lets the same reason report again after a retraction", () => {
    reportFailure("c1", "connection reset");
    clearFailure("c1");
    reportFailure("c1", "connection reset");
    expect(toastCount()).toBe(2);
  });

  it("replaces its own live toast rather than stacking a second", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c1", "too many requests");
    expect(mockDismiss).toHaveBeenCalledTimes(1);
    expect(toastCount()).toBe(2);
  });
});

// The chat on screen carries the failure durably (`turn_failure_reason` → `.turn-notice`), so the toast stands down.

describe("a turn-scoped failure on the chat in front of you", () => {
  function watching(): void {
    mockActiveTabId.mockReturnValue("c1");
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  }

  it("raises no toast at all", () => {
    watching();
    reportFailure("c1", "at capacity", undefined, true);
    expect(toastCount()).toBe(0);
  });

  it("still raises one for a DIFFERENT chat, named and linked", () => {
    // A background chat's failure has no surface on screen.
    watching();
    reportFailure("c2", "at capacity", undefined, true);
    expect(toastCount()).toBe(1);
    expect(lastToast()).toContain("Write the docs");
    expect(lastAction()?.label).toBe("Open");
  });

  it("still raises one when the window is hidden", () => {
    // An active tab in an unseen window is not being read.
    mockActiveTabId.mockReturnValue("c1");
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    reportFailure("c1", "at capacity", undefined, true);
    expect(toastCount()).toBe(1);
  });

  it("still raises one for a failure that ends no turn", () => {
    // A failure with no transcript row keeps its toast (the default flag).
    watching();
    reportFailure("c1", "the agent could not be started", undefined, false);
    expect(toastCount()).toBe(1);
  });

  it("still raises one when the route carries its own remedy", () => {
    // `auth_token_unavailable` is turn-scoped and action-bearing: the row cannot offer Sign in.
    watching();
    const action = { label: "Sign in", onClick: vi.fn() };
    reportFailure("c1", "no access token", action, true);
    expect(toastCount()).toBe(1);
    // On screen the remedy takes the sticky entry point.
    expect(lastStickyAction()?.label).toBe("Sign in");
  });

  it("still raises one for a workspace-global failure that names no chat", () => {
    // An empty chat id has no inline home.
    watching();
    reportFailure("", "the tools engine is unreachable", undefined, true);
    expect(toastCount()).toBe(1);
  });

  it("suppresses both channels, not just the first", () => {
    // Suppression happens before the latch is touched, so the latch cannot be what silences this.
    watching();
    reportFailure("c1", "at capacity", undefined, true);
    reportFailure("c1", "at capacity", undefined, true);
    expect(toastCount()).toBe(0);
    // The latch is untouched: another tab lets the same reason report.
    mockActiveTabId.mockReturnValue("c2");
    reportFailure("c1", "at capacity", undefined, true);
    expect(toastCount()).toBe(1);
  });
});

// Over the bus, not an import: `raise` reads the tab store, which dispatches through the transport (bus.ts's
// BUS_COMMAND_FAILED). Driven through the real bus.
describe("failure-notice subscribes to the transport's failure event", () => {
  it("raises the notice for a failure emitted on the bus", () => {
    emitBus(BUS_COMMAND_FAILED, { chatID: "c1", chatName: "", message: "at capacity" });
    expect(toastCount()).toBe(1);
    expect(lastToast()).toBe("Fix the parser: at capacity");
  });

  it("names and links a chat that is not the one on screen", () => {
    // The payload's chat id is the argument, not the active chat.
    emitBus(BUS_COMMAND_FAILED, { chatID: "c2", chatName: "", message: "at capacity" });
    expect(lastToast()).toBe("Write the docs: at capacity");
    expect(lastAction()?.label).toBe("Open");
  });

  it("carries no remedy and no turn scope, so it is never inline-suppressed", () => {
    // A transport failure has no inline home, so it toasts even on the chat in view (`reportFailure`'s default).
    mockActiveTabId.mockReturnValue("c1");
    emitBus(BUS_COMMAND_FAILED, { chatID: "c1", chatName: "", message: "Request timed out" });
    expect(toastCount()).toBe(1);
    expect(lastStickyAction()).toBeUndefined();
  });
  it("names a chat removed before its command failed by the name the send captured", () => {
    setSessions([]);
    emitBus(BUS_COMMAND_FAILED, {
      chatID: "c1",
      chatName: "Fix the parser",
      message: "at capacity",
    });
    expect(lastToast()).toBe("Fix the parser: at capacity");
  });
});
