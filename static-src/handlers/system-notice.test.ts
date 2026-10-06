import { vi, describe, it, expect, beforeEach } from "vitest";
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
import type * as NoticeSubject from "../notice-subject.js";
import type * as Bus from "../bus.js";
import type * as Toast from "../toast.js";

vi.mock("../bus.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Bus>()),
  ...createBusMock(),
}));
const { mockNotice } = vi.hoisted(() => ({ mockNotice: vi.fn() }));
vi.mock("../toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
  notice: mockNotice,
}));
const { mockSubject } = vi.hoisted(() => ({ mockSubject: vi.fn() }));
vi.mock("../notice-subject.js", async (importOriginal) => ({
  ...(await importOriginal<typeof NoticeSubject>()),
  noticeSubject: mockSubject,
}));

await import("./system-notice.js");

const open = { label: "Open", onClick: () => undefined };

beforeEach(() => {
  vi.clearAllMocks();
  mockSubject.mockReturnValue({ name: "Fix the parser", open });
});

describe("system_notice", () => {
  it.each(["info", "warning", "error"] as const)("toasts a %s notice at that level", (level) => {
    fireSSE("system_notice", "c1", { level, message: "The model was switched" });
    expect(mockNotice).toHaveBeenCalledWith("Fix the parser: The model was switched", level, open);
  });

  it("resolves the subject from the frame's own chat id", () => {
    fireSSE("system_notice", "run:wf_1", { level: "info", message: "retrying" });
    expect(mockSubject).toHaveBeenCalledWith("run:wf_1", "");
  });

  it("names the chat by the name the server sent with the notice", () => {
    fireSSE("system_notice", "c1", {
      level: "warning",
      message: "High load",
      chat_name: "Fix the parser",
    });
    expect(mockSubject).toHaveBeenCalledWith("c1", "Fix the parser");
  });

  it("names nothing for the utility session's notice", () => {
    mockSubject.mockReturnValue({ name: "", open: undefined });
    fireSSE("system_notice", "", { level: "warning", message: "Rate limited" });
    expect(mockNotice).toHaveBeenCalledWith("Rate limited", "warning", undefined);
  });

  it("says nothing for an empty message", () => {
    fireSSE("system_notice", "c1", { level: "info", message: "  " });
    expect(mockNotice).not.toHaveBeenCalled();
  });
});
