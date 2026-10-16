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
  it("POSTs the server name to /api/mcp/reconnect and takes a 204 as done", async () => {
    const toast = await import("../toast.js");
    mockFetch.mockResolvedValue(new Response(null, { status: 204 }));
    const done = vi.fn();
    await reconnectServer.dispatch({ server: "everything" }, { onSuccess: done });
    const { url, init } = lastCall();
    expect(new URL(url, location.href).pathname).toBe("/api/mcp/reconnect");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ server: "everything" });
    expect(done).toHaveBeenCalledTimes(1);
    expect(toast.error).not.toHaveBeenCalled();
  });

  it.each([
    [503, "The agent engine is not running. Try again once it is back."],
    [502, "kiro-cli did not reset the server."],
  ])("a %i shows the server's sentence and never reports success", async (status, sentence) => {
    const toast = await import("../toast.js");
    vi.mocked(toast.error).mockClear();
    mockFetch.mockImplementation(() =>
      Promise.resolve(new Response(JSON.stringify({ error: sentence }), { status })),
    );
    const done = vi.fn();
    const settled = reconnectServer
      .dispatch({ server: "everything" }, { onSuccess: done })
      .catch(() => undefined);
    // Each retry's delay is armed only after the previous response settles, so step past each one.
    for (let i = 0; i < 5; i++) {
      await vi.advanceTimersByTimeAsync(5_000);
    }
    await settled;
    expect(done).not.toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalledWith(
      `Could not reconnect integration: ${sentence}`,
      expect.anything(),
    );
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
