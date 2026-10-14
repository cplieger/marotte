// The request, decode and dedupe of the one /api/forges action; the store is forge-store.test.ts.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
  // Inert: present only so real-ESM linking succeeds.
  apiGetTyped: vi.fn(),
}));
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { listForges } from "./forge-list.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const forgesResp = {
  forges: [
    {
      id: "github:github.com",
      kind: "github",
      host: "github.com",
      account: "acct-1",
      status: "connected",
    },
  ],
  kinds: ["github", "gitlab", "codeberg", "gitea"],
  oauth: { github: true },
};

describe("listForges", () => {
  it("has the expected action name", () => {
    expect(listForges.name).toBe("forges.list");
  });

  it("fetches /api/forges and does NOT fetch status-all", async () => {
    mockFetch.mockImplementation((url: string) => {
      if (url === "/api/forges") {
        return Promise.resolve(new Response(JSON.stringify(forgesResp), { status: 200 }));
      }
      return Promise.resolve(new Response("", { status: 404 }));
    });

    const result = await listForges.dispatch(undefined);
    expect(result?.forges).toHaveLength(1);
    expect(result?.oauth).toEqual({ github: true });
    const urls = mockFetch.mock.calls.map((c) => c[0]);
    expect(urls).toContain("/api/forges");
    // Git status belongs to the other shared store; fetching it here is the duplication they remove.
    expect(urls).not.toContain("/api/git/status-all");
  });

  // A read joined to one issued before a sign-in would answer the rows from before it.
  it("sends every dispatch, one in flight or not", async () => {
    mockFetch.mockImplementation(() =>
      Promise.resolve(new Response(JSON.stringify(forgesResp), { status: 200 })),
    );

    await Promise.all([listForges.dispatch(undefined), listForges.dispatch(undefined)]);

    expect(mockFetch).toHaveBeenCalledTimes(2);
  });

  it("resolves null on a failed fetch rather than throwing", async () => {
    mockFetch.mockResolvedValue(new Response("", { status: 500 }));
    await expect(listForges.dispatch(undefined)).resolves.toBeNull();
  });

  it("resolves null on a malformed payload rather than handing it on", async () => {
    // A missing `forges` (an early empty body) must not read as "no connected forges".
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ kinds: [] }), { status: 200 }));
    await expect(listForges.dispatch(undefined)).resolves.toBeNull();
  });
});
