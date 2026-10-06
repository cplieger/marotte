import { describe, it, expect, beforeEach, vi } from "vitest";
import type * as RunStore from "./run-store.js";
import type * as Tabs from "./tabs.js";
import type * as Toast from "./toast.js";

const { mockActiveTabId, mockTabIdFor, mockActivateTab, mockOpenTab, mockRunLabel, mockNotice } =
  vi.hoisted(() => ({
    mockActiveTabId: vi.fn(() => ""),
    // The chat id doubles as its own tab id, so assertions read without opaque ids.
    mockTabIdFor: vi.fn((_kind: string, ref = "") => ref),
    mockActivateTab: vi.fn(),
    mockOpenTab: vi.fn(),
    mockRunLabel: vi.fn((_id: string) => ""),
    mockNotice: vi.fn(),
  }));
vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  activateTab: mockActivateTab,
  openTab: mockOpenTab,
  getActiveTabId: mockActiveTabId,
  tabIdFor: mockTabIdFor,
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
  notice: mockNotice,
}));
vi.mock("./run-store.js", async (importOriginal) => ({
  ...(await importOriginal<typeof RunStore>()),
  runLabelOf: mockRunLabel,
}));

import { chatNotice, named, noticeSubject } from "./notice-subject.js";
import { defaultUsage, setSessions } from "./store.js";
import type { Session } from "./types.js";

function session(id: string, name: string): Session {
  return {
    id,
    name,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  mockActiveTabId.mockReturnValue("c1");
  mockTabIdFor.mockImplementation((_kind: string, ref = "") => ref);
  setSessions([session("c1", "Fix the parser"), session("c2", "Write the docs")]);
});

describe("noticeSubject", () => {
  it("names the chat on screen and offers no jump to it", () => {
    const s = noticeSubject("c1");
    expect(named(s, "rate limited")).toBe("Fix the parser: rate limited");
    expect(s.open).toBeUndefined();
  });

  it("names a background chat and offers Open onto its tab", () => {
    const s = noticeSubject("c2");
    expect(named(s, "rate limited")).toBe("Write the docs: rate limited");
    expect(s.open?.label).toBe("Open");
    s.open?.onClick();
    expect(mockActivateTab).toHaveBeenCalledWith("c2");
  });

  it("offers Open onto a retained chat that has no tab", () => {
    mockTabIdFor.mockReturnValue("");
    const s = noticeSubject("c2");
    expect(s.open?.label).toBe("Open");
    s.open?.onClick();
    expect(mockOpenTab).toHaveBeenCalledWith({ kind: "chat", ref: "c2", name: "Write the docs" });
    expect(mockActivateTab).not.toHaveBeenCalled();
  });

  it("offers no jump for a chat the store no longer holds", () => {
    mockTabIdFor.mockReturnValue("");
    expect(noticeSubject("c-gone", "Old chat").open).toBeUndefined();
  });

  it("names a run by its label and offers nothing to open", () => {
    mockRunLabel.mockReturnValue("nightly review · scheduled");
    const s = noticeSubject("run:wf_1");
    expect(mockRunLabel).toHaveBeenCalledWith("wf_1");
    expect(named(s, "retrying")).toBe("nightly review · scheduled: retrying");
    expect(s.open).toBeUndefined();
  });

  it("names a run whose state has not arrived yet as a workflow run", () => {
    mockRunLabel.mockReturnValue("");
    expect(named(noticeSubject("run:wf_2"), "retrying")).toBe("Workflow run: retrying");
  });

  it("names a chat whose row is gone by the name the notice carried", () => {
    setSessions([]);
    expect(named(noticeSubject("c1", "Fix the parser"), "rate limited")).toBe(
      "Fix the parser: rate limited",
    );
  });

  it("names a run by the label the notice carried", () => {
    mockRunLabel.mockReturnValue("Renamed since");
    expect(named(noticeSubject("run:wf_3", "Nightly build"), "retrying")).toBe(
      "Nightly build: retrying",
    );
  });

  it("names nothing for a workspace-global notice", () => {
    const s = noticeSubject("");
    expect(named(s, "catalog refreshed")).toBe("catalog refreshed");
    expect(s.open).toBeUndefined();
  });
});

describe("chatNotice", () => {
  it("raises the toast named for its chat with that chat's Open action", () => {
    chatNotice("c2", "Type a name first", "error");
    expect(mockNotice).toHaveBeenCalledTimes(1);
    const [message, level, action] = mockNotice.mock.calls[0] as [string, string, unknown];
    expect(message).toBe("Write the docs: Type a name first");
    expect(level).toBe("error");
    expect((action as { label: string } | undefined)?.label).toBe("Open");
  });

  it("defaults to the info level and offers nothing for the chat on screen", () => {
    chatNotice("c1", "No turn is running on this chat.");
    expect(mockNotice).toHaveBeenCalledWith(
      "Fix the parser: No turn is running on this chat.",
      "info",
      undefined,
    );
  });
});
