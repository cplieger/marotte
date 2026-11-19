// open_external_url surfaces a clickable app-global banner (never auto-opens) with the paste box, only for safe URLs.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";

const mockShowBanner = vi.fn();
const mockClearBannerCodes = vi.fn();
vi.mock("../banner-stack.js", () => ({
  GLOBAL_BANNER: "*",
  showBanner: (...args: unknown[]) => mockShowBanner(...args),
  clearBannerCodes: (...args: unknown[]) => mockClearBannerCodes(...args),
}));
const mockRenderOAuthRelay = vi.fn((server: string) => {
  const box = document.createElement("details");
  box.dataset["server"] = server;
  return box;
});
vi.mock("../mcp-ui.js", () => ({
  renderOAuthRelay: (server: string) => mockRenderOAuthRelay(server),
}));
vi.mock("../bus.js", () => createBusMock());

// Import after mocks so the handler registers against the bus mock.
await import("./open-external-url.js");

const NOW = Date.parse("2026-11-01T12:00:00Z");

/** The payload KAS's 300 s wait gives a URL opened at sentAt. */
function signIn(url: string, sentAt = NOW): { url: string; expires_at: string } {
  return { url, expires_at: new Date(sentAt + 300_000).toISOString() };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("open_external_url handler", () => {
  it("shows a dismissible banner on every chat with the link and the server-less paste box", () => {
    fireSSE("open_external_url", "", signIn("https://auth.example.com/oauth?state=s"));
    expect(mockShowBanner).toHaveBeenCalledTimes(1);
    const call = mockShowBanner.mock.calls[0] ?? [];
    expect(call[0]).toBe("*"); // GLOBAL_BANNER
    expect(call[1]).toBe("open_external_url");
    expect(call[2]).toBe("An integration needs you to sign in.");
    expect(call[3]).toBe("info");
    expect(call[4]).toBe(true);
    expect(call[5]).toEqual({
      label: "Open sign-in page",
      href: "https://auth.example.com/oauth?state=s",
    });
    expect(mockRenderOAuthRelay).toHaveBeenCalledWith("");
    expect(call[6]).toBeInstanceOf(HTMLDetailsElement);
  });

  it("clears the last sign-in's banner and failure first, so a dismissed one does not hide the new one", () => {
    fireSSE("open_external_url", "", signIn("https://auth.example.com/oauth?state=s"));
    expect(mockClearBannerCodes).toHaveBeenCalledWith("*", [
      "open_external_url",
      "mcp_sign_in_failed",
    ]);
    const cleared = mockClearBannerCodes.mock.invocationCallOrder[0] ?? Infinity;
    const shown = mockShowBanner.mock.invocationCallOrder[0] ?? -Infinity;
    expect(cleared).toBeLessThan(shown);
  });

  it("takes the banner down when KAS's 300 s callback wait ends, and a newer URL restarts that wait", () => {
    fireSSE("open_external_url", "", signIn("https://auth.example.com/a?state=1"));
    vi.advanceTimersByTime(200_000);
    fireSSE("open_external_url", "", signIn("https://auth.example.com/b?state=2", NOW + 200_000));
    mockClearBannerCodes.mockClear();

    vi.advanceTimersByTime(299_999);
    expect(mockClearBannerCodes).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(mockClearBannerCodes).toHaveBeenCalledWith("*", ["open_external_url"]);
  });

  it("does not show a sign-in replayed after KAS stopped waiting for it", () => {
    fireSSE("open_external_url", "", signIn("https://auth.example.com/a?state=1", NOW - 360_000));
    expect(mockShowBanner).not.toHaveBeenCalled();
    expect(mockClearBannerCodes).not.toHaveBeenCalled();
  });

  it("shows a sign-in replayed partway through the wait only for the time left", () => {
    fireSSE("open_external_url", "", signIn("https://auth.example.com/a?state=1", NOW - 240_000));
    expect(mockShowBanner).toHaveBeenCalledTimes(1);
    mockClearBannerCodes.mockClear();

    vi.advanceTimersByTime(59_999);
    expect(mockClearBannerCodes).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(mockClearBannerCodes).toHaveBeenCalledWith("*", ["open_external_url"]);
  });

  it("ignores unsafe schemes (never surfaces javascript: etc.)", () => {
    fireSSE("open_external_url", "", { url: "javascript:alert(1)" });
    expect(mockShowBanner).not.toHaveBeenCalled();
    expect(mockClearBannerCodes).not.toHaveBeenCalled();
  });

  it("ignores a non-string url", () => {
    fireSSE("open_external_url", "", { url: 123 });
    expect(mockShowBanner).not.toHaveBeenCalled();
  });
});

describe("a sign-in KAS names its server and flow for", () => {
  function lazy(server: string, flow: string): Record<string, string> {
    return { ...signIn(`https://auth.example.com/oauth?state=${flow}`), server, flow_id: flow };
  }

  it("names the server, and hands it to the paste box", () => {
    fireSSE("open_external_url", "", lazy("linear", "f1"));
    expect(mockShowBanner.mock.calls[0]?.[2]).toBe("linear needs you to sign in.");
    expect(mockRenderOAuthRelay).toHaveBeenCalledWith("linear");
  });

  it("closes the banner when its flow succeeds, and stops the countdown", () => {
    fireSSE("open_external_url", "", lazy("linear", "f1"));
    mockClearBannerCodes.mockClear();
    mockShowBanner.mockClear();

    fireSSE("mcp_sign_in_closed", "", { server: "linear", flow_id: "f1", success: true });

    expect(mockClearBannerCodes).toHaveBeenCalledWith("*", ["open_external_url"]);
    expect(mockShowBanner).not.toHaveBeenCalled();
    mockClearBannerCodes.mockClear();
    vi.advanceTimersByTime(300_000);
    expect(mockClearBannerCodes).not.toHaveBeenCalled();
  });

  it("turns into a warning naming the server when its flow ends without a token", () => {
    fireSSE("open_external_url", "", lazy("linear", "f1"));
    mockShowBanner.mockClear();

    fireSSE("mcp_sign_in_closed", "", { server: "linear", flow_id: "f1", success: false });

    expect(mockShowBanner).toHaveBeenCalledWith(
      "*",
      "mcp_sign_in_failed",
      "linear: the sign-in did not complete. Its next tool call asks again.",
      "warning",
      true,
    );
  });

  it("leaves the banner up when another flow closes", () => {
    fireSSE("open_external_url", "", lazy("linear", "f1"));
    mockClearBannerCodes.mockClear();
    mockShowBanner.mockClear();

    fireSSE("mcp_sign_in_closed", "", { server: "github", flow_id: "f2", success: false });

    expect(mockClearBannerCodes).not.toHaveBeenCalled();
    expect(mockShowBanner).not.toHaveBeenCalled();
  });
});
