import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../api-client.js", () => ({
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  // Inert: present only so real-ESM linking succeeds.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { resetActionFramework, headerValue } from "./__test-helpers__/action-test-setup.js";
import { getActionLog as recentLog } from "./index.js";
import * as toast from "../toast.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

const GITHUB_START = { kind: "github", host: "github.com", clientId: "", options: {} } as const;

describe("forge.start_device_flow", () => {
  it("POSTs to /api/forges/oauth/github/start", async () => {
    const resp = {
      grant_id: "abc",
      user_code: "1234",
      verification_uri: "https://github.com/login/device",
    };
    mockFetch.mockResolvedValue(new Response(JSON.stringify(resp), { status: 200 }));
    const { startDeviceFlow } = await import("./forge.js");
    const r = await startDeviceFlow.dispatch(GITHUB_START);
    expect(r).toEqual(resp);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/oauth/github/start");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ host: "github.com" });
  });

  it("starts a self-managed GitLab grant with its client id and connection fields", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { startDeviceFlow } = await import("./forge.js");
    await startDeviceFlow.dispatch({
      kind: "gitlab",
      host: "gitlab.internal",
      clientId: "app-id",
      options: { private_addresses: true },
    });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/oauth/gitlab/start");
    expect(JSON.parse(opts.body as string)).toEqual({
      host: "gitlab.internal",
      client_id: "app-id",
      private_addresses: true,
    });
  });

  it("dedupes concurrent dispatches", async () => {
    vi.useFakeTimers();
    mockFetch.mockImplementation(
      () =>
        new Promise((r) =>
          setTimeout(() => {
            r(new Response(JSON.stringify({}), { status: 200 }));
          }, 50),
        ),
    );
    const { startDeviceFlow } = await import("./forge.js");
    const p1 = startDeviceFlow.dispatch(GITHUB_START);
    const p2 = startDeviceFlow.dispatch(GITHUB_START);
    await vi.advanceTimersByTimeAsync(50);
    await Promise.all([p1, p2]);
    expect(mockFetch).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it("suppresses error toast on failure", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "rate limited" }), { status: 429 }),
    );
    const { startDeviceFlow } = await import("./forge.js");
    await startDeviceFlow.dispatch(GITHUB_START);
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("forge.cancel_device_flow", () => {
  it("POSTs the grant id to the kind's cancel route", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 204 }));
    const { cancelDeviceFlow } = await import("./forge.js");
    await cancelDeviceFlow.dispatch({ kind: "gitlab", grantId: "g-1" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/oauth/gitlab/cancel");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ grant_id: "g-1" });
  });
});

describe("forge.sign_out", () => {
  it("DELETEs /api/forges/:id", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 204 }));
    const { signOut } = await import("./forge.js");
    await signOut.dispatch({ forgeId: "github:user" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/github%3Auser");
    expect(opts.method).toBe("DELETE");
  });

  it("is not retryable, and leaves the refusal to the account row", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const { signOut } = await import("./forge.js");
    const o = await signOut.dispatch({ forgeId: "gh:1" }).outcome;
    expect(o.status === "error" && o.error.message).toBe("fail");
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(toast.error).not.toHaveBeenCalled();
    expect(recentLog()[0]?.status).toBe("error");
  });
});

describe("forge.clone_repo", () => {
  it("POSTs to /api/git/clone with url", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ output: "done" }), { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    const r = await cloneRepo.dispatch({ url: "https://github.com/user/repo.git" });
    expect(r).toEqual({ output: "done" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/clone");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ url: "https://github.com/user/repo.git" });
  });

  // No retry, hence no key: an interrupted clone can leave a partial destination, so a retry would
  // report a false "already exists".
  it("sends no Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    await cloneRepo.dispatch({ url: "https://x.com/r.git" });
    expect(headerValue(mockFetch.mock.calls[0]![1], "idempotency-key")).toBeUndefined();
  });

  it("suppresses error toast on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "exists" }), { status: 409 }));
    const { cloneRepo } = await import("./forge.js");
    await cloneRepo.dispatch({ url: "x" });
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("does NOT retry on network error", async () => {
    vi.useFakeTimers();
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response(JSON.stringify({}), { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    const p = cloneRepo.dispatch({ url: "x" });
    await vi.advanceTimersByTimeAsync(1000);
    const r = await p;
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(r).toBeNull();
    vi.useRealTimers();
  });

  // NDJSON: progress lines feed onProgress (re-arming the stall detector); the final line is the
  // output/error envelope.
  it("forwards streamed progress and returns the final envelope", async () => {
    const enc = new TextEncoder();
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(enc.encode('{"progress":"Receiving objects: 42% (1/2)"}\n'));
        c.enqueue(enc.encode('{"progress":"Receiving objects: 100% (2/2)"}\n'));
        c.enqueue(enc.encode('{"output":"done"}\n'));
        c.close();
      },
    });
    mockFetch.mockResolvedValue(new Response(body, { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    const seen: string[] = [];
    const r = await cloneRepo.dispatch({
      url: "https://x.com/r.git",
      onProgress: (line) => seen.push(line),
    });
    expect(seen).toEqual(["Receiving objects: 42% (1/2)", "Receiving objects: 100% (2/2)"]);
    expect(r).toEqual({ output: "done" });
  });

  it("returns the streamed error envelope on a failed clone", async () => {
    const enc = new TextEncoder();
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(enc.encode('{"progress":"Receiving objects: 42%"}\n'));
        c.enqueue(enc.encode('{"error":"the transfer stalled: no progress from git for 1m30s"}\n'));
        c.close();
      },
    });
    mockFetch.mockResolvedValue(new Response(body, { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    const r = await cloneRepo.dispatch({ url: "https://x.com/r.git" });
    expect(r?.error).toContain("stalled");
  });

  // No final envelope means the server died mid-clone: a failure, not an empty success.
  it("fails when the stream ends without a verdict", async () => {
    const enc = new TextEncoder();
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(enc.encode('{"progress":"Receiving objects: 42%"}\n'));
        c.close();
      },
    });
    mockFetch.mockResolvedValue(new Response(body, { status: 200 }));
    const { cloneRepo } = await import("./forge.js");
    const r = await cloneRepo.dispatch({ url: "https://x.com/r.git" });
    expect(r).toBeNull();
  });
});

describe("forge.connect_pat", () => {
  it("POSTs to /api/forges/:id/login/pat with token", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ status: "ok" }), { status: 200 }));
    const { connectPAT } = await import("./forge.js");
    await connectPAT.dispatch({
      kind: "github",
      host: "github.com",
      token: "ghp_abc",
      options: {},
    });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/github%3Agithub.com/login/pat");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ token: "ghp_abc" });
  });

  it("sends the connection fields beside the token", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ status: "complete" }), { status: 200 }),
    );
    const { connectPAT } = await import("./forge.js");
    await connectPAT.dispatch({
      kind: "gitea",
      host: "127.0.0.1:3000",
      token: "tok",
      options: { web_base_url: "http://127.0.0.1:3000", plaintext_http: true },
    });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/forges/gitea%3A127.0.0.1%3A3000/login/pat");
    expect(JSON.parse(opts.body as string)).toEqual({
      token: "tok",
      web_base_url: "http://127.0.0.1:3000",
      plaintext_http: true,
    });
  });

  it("includes Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { connectPAT } = await import("./forge.js");
    await connectPAT.dispatch({ kind: "gitlab", host: "gitlab.com", token: "tok", options: {} });
    expect(headerValue(mockFetch.mock.calls[0]![1], "idempotency-key")).toEqual(expect.any(String));
  });

  it("suppresses error toast on failure", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "invalid" }), { status: 401 }),
    );
    const { connectPAT } = await import("./forge.js");
    await connectPAT.dispatch({ kind: "github", host: "github.com", token: "bad", options: {} });
    expect(toast.error).not.toHaveBeenCalled();
  });
});
