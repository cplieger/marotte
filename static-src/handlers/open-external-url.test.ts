// open_external_url surfaces a clickable banner (never auto-opens), only for safe URLs.

import { vi, describe, it, expect, beforeEach } from "vitest";
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";

const mockShowBanner = vi.fn();
vi.mock("../banner-stack.js", () => ({
  showBanner: (...args: unknown[]) => mockShowBanner(...args),
}));
vi.mock("../bus.js", () => createBusMock());

// Import after mocks so the handler registers against the bus mock.
await import("./open-external-url.js");

beforeEach(() => {
  vi.clearAllMocks();
});

describe("open_external_url handler", () => {
  it("shows a dismissible banner with a clickable link for a safe URL", () => {
    fireSSE("open_external_url", "c1", { url: "https://auth.example.com/oauth" });
    expect(mockShowBanner).toHaveBeenCalledTimes(1);
    const call = mockShowBanner.mock.calls[0] ?? [];
    expect(call[0]).toBe("c1"); // chatID
    expect(call[1]).toBe("open_external_url"); // code
    expect(call[3]).toBe("info"); // level
    expect(call[4]).toBe(true); // dismissible
    expect(call[5]).toEqual({ label: expect.any(String), href: "https://auth.example.com/oauth" });
  });

  it("ignores unsafe schemes (never surfaces javascript: etc.)", () => {
    fireSSE("open_external_url", "c1", { url: "javascript:alert(1)" });
    expect(mockShowBanner).not.toHaveBeenCalled();
  });

  it("ignores an event with an empty chat id", () => {
    fireSSE("open_external_url", "", { url: "https://x.example" });
    expect(mockShowBanner).not.toHaveBeenCalled();
  });

  it("ignores a non-string url", () => {
    fireSSE("open_external_url", "c1", { url: 123 });
    expect(mockShowBanner).not.toHaveBeenCalled();
  });
});
