import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import type * as BannerStack from "../banner-stack.js";
import type * as Toast from "../toast.js";
import type * as Bus from "../bus.js";
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";

const mockShowBanner = vi.fn();
const mockClearBannerCodes = vi.fn();
vi.mock("../banner-stack.js", async (importOriginal) => ({
  ...(await importOriginal<typeof BannerStack>()),
  showBanner: mockShowBanner,
  clearBannerCodes: mockClearBannerCodes,
}));
const mockNotice = vi.fn();
vi.mock("../toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
  notice: mockNotice,
}));

const busHandlers = new Map<string, () => void>();
vi.mock("../bus.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Bus>()),
  ...createBusMock({
    BUS_PAGE_RESUMED: "transport:resumed",
    BUS_RECONCILE: "transport:reconcile",
    onBus: vi.fn((name: string, fn: () => void) => {
      busHandlers.set(name, fn);
    }),
  }),
}));

const { INDEXING_GRACE_MS } = await import("./knowledge-indexing.js");

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
});

afterEach(() => {
  busHandlers.get("transport:reconcile")?.();
  vi.useRealTimers();
});

function started(chatID: string, name: string, files: number): void {
  fireSSE("knowledge_indexing", chatID, { name, phase: "started", file_count: files });
}

describe("knowledge_indexing handler", () => {
  it("raises nothing for an index that completes inside the grace", () => {
    started("c1", "docs", 12);
    vi.advanceTimersByTime(INDEXING_GRACE_MS - 1);
    fireSSE("knowledge_indexing", "c1", { name: "docs", phase: "completed", status: "success" });
    vi.advanceTimersByTime(INDEXING_GRACE_MS * 2);
    expect(mockShowBanner).not.toHaveBeenCalled();
    expect(mockNotice).not.toHaveBeenCalled();
  });

  it("raises one non-dismissible info banner once the grace has passed", () => {
    started("c1", "docs", 12);
    vi.advanceTimersByTime(INDEXING_GRACE_MS);
    expect(mockShowBanner).toHaveBeenCalledTimes(1);
    const [chatID, code, text, level, dismissible] = mockShowBanner.mock.calls[0] ?? [];
    expect(chatID).toBe("c1");
    expect(String(code)).toContain("docs");
    expect(String(text)).toContain("(12 files)");
    expect(String(text)).toContain("\u201Cdocs\u201D");
    expect(level).toBe("info");
    expect(dismissible).toBe(false);
  });

  it("names a single file in the singular", () => {
    started("c1", "one", 1);
    vi.advanceTimersByTime(INDEXING_GRACE_MS);
    expect(String(mockShowBanner.mock.calls[0]?.[2])).toContain("(1 file)");
  });

  it("clears the banner on a successful completion and says nothing more", () => {
    started("c1", "docs", 3);
    vi.advanceTimersByTime(INDEXING_GRACE_MS);
    const code = mockShowBanner.mock.calls[0]?.[1];
    fireSSE("knowledge_indexing", "c1", {
      name: "docs",
      phase: "completed",
      status: "success",
      item_count: 40,
    });
    expect(mockClearBannerCodes).toHaveBeenCalledWith("c1", [code]);
    expect(mockNotice).not.toHaveBeenCalled();
  });

  it("clears the banner and raises an error toast on a failed completion", () => {
    started("c1", "docs", 3);
    vi.advanceTimersByTime(INDEXING_GRACE_MS);
    fireSSE("knowledge_indexing", "c1", { name: "docs", phase: "completed", status: "failed" });
    expect(mockClearBannerCodes).toHaveBeenCalledTimes(1);
    expect(mockNotice).toHaveBeenCalledTimes(1);
    const [message, level] = mockNotice.mock.calls[0] ?? [];
    expect(String(message)).toContain("\u201Cdocs\u201D");
    expect(level).toBe("error");
  });

  it("clears a standing banner on a page resume, and a pending one never fires", () => {
    started("c1", "shown", 2);
    vi.advanceTimersByTime(INDEXING_GRACE_MS);
    started("c1", "pending", 2);
    busHandlers.get("transport:resumed")?.();
    expect(mockClearBannerCodes).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(INDEXING_GRACE_MS * 2);
    expect(mockShowBanner).toHaveBeenCalledTimes(1);
  });

  it("clears on a reconcile", () => {
    started("c1", "docs", 2);
    vi.advanceTimersByTime(INDEXING_GRACE_MS);
    busHandlers.get("transport:reconcile")?.();
    expect(mockClearBannerCodes).toHaveBeenCalledTimes(1);
  });

  it("keeps two chats' indexes of one base apart", () => {
    started("c1", "docs", 2);
    started("c2", "docs", 2);
    vi.advanceTimersByTime(INDEXING_GRACE_MS);
    expect(mockShowBanner).toHaveBeenCalledTimes(2);
    fireSSE("knowledge_indexing", "c1", { name: "docs", phase: "completed", status: "success" });
    expect(mockClearBannerCodes).toHaveBeenCalledTimes(1);
    expect(mockClearBannerCodes.mock.calls[0]?.[0]).toBe("c1");
  });

  it("drops a pending banner when the same index restarts", () => {
    started("c1", "docs", 2);
    vi.advanceTimersByTime(INDEXING_GRACE_MS - 1);
    started("c1", "docs", 2);
    fireSSE("knowledge_indexing", "c1", { name: "docs", phase: "completed", status: "success" });
    vi.advanceTimersByTime(INDEXING_GRACE_MS * 2);
    expect(mockShowBanner).not.toHaveBeenCalled();
  });

  it("raises no banner for a chat deleted inside the grace", () => {
    started("c1", "docs", 2);
    vi.advanceTimersByTime(INDEXING_GRACE_MS - 1);
    fireSSE("chat_deleted", "", { id: "c1" });
    vi.advanceTimersByTime(INDEXING_GRACE_MS * 2);
    expect(mockShowBanner).not.toHaveBeenCalled();
  });

  it("forgets a deleted chat's shown index and leaves other chats alone", () => {
    started("c1", "docs", 2);
    started("c2", "docs", 2);
    vi.advanceTimersByTime(INDEXING_GRACE_MS);
    fireSSE("chat_deleted", "", { id: "c1" });
    expect(mockClearBannerCodes).toHaveBeenCalledTimes(1);
    expect(mockClearBannerCodes.mock.calls[0]?.[0]).toBe("c1");
    busHandlers.get("transport:reconcile")?.();
    expect(mockClearBannerCodes).toHaveBeenCalledTimes(2);
    expect(mockClearBannerCodes.mock.calls[1]?.[0]).toBe("c2");
  });

  it("ignores a frame with no chat", () => {
    started("", "docs", 2);
    vi.advanceTimersByTime(INDEXING_GRACE_MS);
    expect(mockShowBanner).not.toHaveBeenCalled();
  });
});
