import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { reconnectServer, relayOAuthCallback } from "./mcp.js";

const mockFetch = vi.fn();

beforeEach(() => {
  vi.useFakeTimers();
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.useRealTimers();
});

function lastCall(): { url: string; init: RequestInit } {
  const call = mockFetch.mock.calls[0] as [string, RequestInit];
  return { url: call[0], init: call[1] };
}

describe("reconnectServer", () => {
  it("POSTs the server name to /api/mcp/reconnect and returns the count", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ reconnected: 2 }), { status: 200 }));
    const res = await reconnectServer.dispatch({ server: "everything" });
    const { url, init } = lastCall();
    expect(url).toContain("/api/mcp/reconnect");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ server: "everything" });
    expect(res?.reconnected).toBe(2);
  });
});

describe("relayOAuthCallback", () => {
  it("POSTs the server and the pasted address to /api/mcp/oauth-relay", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ status: 200 }), { status: 200 }));
    const pasted = "http://localhost:41234/oauth/callback?code=abc&state=st";

    await relayOAuthCallback.dispatch({ server: "linear", redirect_url: pasted });

    const { url, init } = lastCall();
    expect(url).toContain("/api/mcp/oauth-relay");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ server: "linear", redirect_url: pasted });
  });

  // An authorization code is single-use, so a retried relay could spend it twice.
  it("never retries — a replayed callback would spend the code twice", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "the local sign-in listener did not answer" }), {
        status: 502,
      }),
    );

    await relayOAuthCallback
      .dispatch({ server: "linear", redirect_url: "http://localhost:1/x?code=a&state=s" })
      .catch(() => undefined);
    await vi.runAllTimersAsync();

    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  // The server's reason names the wrong part of the pasted address, shown beside the box.
  it("raises no error toast, so the panel can show the reason inline", async () => {
    const toast = await import("../toast.js");
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "that address belongs to a different sign-in" }), {
        status: 400,
      }),
    );

    await relayOAuthCallback
      .dispatch({ server: "linear", redirect_url: "http://localhost:1/x?code=a&state=s" })
      .catch(() => undefined);
    await vi.runAllTimersAsync();

    expect(toast.error).not.toHaveBeenCalled();
    expect(toast.showToast).not.toHaveBeenCalled();
  });
});
