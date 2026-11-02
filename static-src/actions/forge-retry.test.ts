// Retry classification of the forge actions:
// - signOut: retryable with retry config; a repeated DELETE answers as the first did
// - startDeviceFlow: retryable, no auto-retry, no toast
// - cloneRepo: NOT retryable (a partial destination makes a retry report "already exists")
// - connectPAT: retryable with retry config; the idempotency key is reused
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

import * as toast from "../toast.js";
const IDEMPOTENCY_HEADER = "idempotency-key";
import { resetActionFramework, headerValue } from "./__test-helpers__/action-test-setup.js";
import { signOut, startDeviceFlow, cloneRepo, connectPAT } from "./forge.js";

const GITHUB_START = { kind: "github", host: "github.com", clientId: "", options: {} } as const;

beforeEach(() => {
  resetActionFramework();
  vi.clearAllMocks();
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function networkError(): never {
  throw new TypeError("Failed to fetch");
}

describe("forge.signOut retry", () => {
  it("retries a network error and a cleanup-pending 503, then gives the row the last refusal", async () => {
    let attempt = 0;
    const fetchSpy = vi.fn<typeof fetch>(() => {
      attempt++;
      if (attempt === 1) {
        return Promise.reject(new TypeError("Failed to fetch"));
      }
      return Promise.resolve(
        new Response(
          '{"error":"signed out, but a stored credential could not be deleted","code":"sign_out_cleanup_pending"}',
          {
            status: 503,
          },
        ),
      );
    });
    vi.stubGlobal("fetch", fetchSpy);

    const h = signOut.dispatch({ forgeId: "gh:user" });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await h;
    const o = await h.outcome;

    expect(fetchSpy).toHaveBeenCalledTimes(3);
    expect(o).toMatchObject({ status: "error", error: { code: "sign_out_cleanup_pending" } });
    // No toast and so no Retry button: the account row renders the refusal.
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("does NOT auto-retry or toast on HTTP 403", async () => {
    const fetchSpy = vi.fn<typeof fetch>(() =>
      Promise.resolve(new Response('{"error":"forbidden"}', { status: 403 })),
    );
    vi.stubGlobal("fetch", fetchSpy);

    await signOut.dispatch({ forgeId: "gh:user" });

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("forge.cloneRepo retry", () => {
  it("does NOT auto-retry on network error", async () => {
    const fetchSpy = vi.fn<typeof fetch>(networkError);
    vi.stubGlobal("fetch", fetchSpy);

    const p = cloneRepo.dispatch({ url: "https://github.com/org/repo" });
    // Give any (wrong) retry schedule room to fire.
    await vi.advanceTimersByTimeAsync(1000);
    const result = await p;

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(result).toBeNull();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("gives up only on a STALLED stream, never on elapsed time", async () => {
    // One chunk, then silence: the stall detector must abort. Liveness is measured from chunks (the
    // slow-but-streaming inverse is in forge-actions.test.ts).
    let controller!: ReadableStreamDefaultController<Uint8Array>;
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        controller = c;
      },
    });
    const fetchSpy = vi.fn<typeof fetch>(() => Promise.resolve(new Response(body)));
    vi.stubGlobal("fetch", fetchSpy);

    const p = cloneRepo.dispatch({ url: "https://github.com/org/repo" });
    // Let the fetch resolve and the reader attach before feeding a chunk.
    await vi.advanceTimersByTimeAsync(0);
    controller.enqueue(new TextEncoder().encode('{"progress":"Receiving objects: 1%"}\n'));
    // Past the 3-minute stall window with nothing arriving: abort.
    await vi.advanceTimersByTimeAsync(4 * 60_000);
    const result = await p;
    expect(result).toBeNull();
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("forge.connectPAT retry", () => {
  it("auto-retries on network error with idempotency key reused", async () => {
    let attempt = 0;
    const fetchSpy = vi.fn<typeof fetch>(() => {
      attempt++;
      if (attempt < 3) {
        return Promise.reject(new TypeError("Failed to fetch"));
      }
      return Promise.resolve(new Response('{"forge_id":"minted"}', { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchSpy);

    const p = connectPAT.dispatch({
      kind: "github",
      host: "github.com",
      token: "ghp_xxx",
      options: {},
    });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    const result = await p;

    expect(fetchSpy).toHaveBeenCalledTimes(3);
    expect(result).toEqual({ forge_id: "minted" });

    const key1 = headerValue(fetchSpy.mock.calls[0]![1], IDEMPOTENCY_HEADER);
    const key2 = headerValue(fetchSpy.mock.calls[1]![1], IDEMPOTENCY_HEADER);
    const key3 = headerValue(fetchSpy.mock.calls[2]![1], IDEMPOTENCY_HEADER);
    expect(key1).toBeDefined();
    expect(key1).toBe(key2);
    expect(key2).toBe(key3);
  });

  // Its meaning is that the same 25-second wait already ran out once.
  it("does not retry a sign-in that ran out of time", async () => {
    const fetchSpy = vi.fn<typeof fetch>(() =>
      Promise.resolve(
        new Response('{"error":"ran out of time","code":"sign_in_timed_out"}', { status: 504 }),
      ),
    );
    vi.stubGlobal("fetch", fetchSpy);

    const p = connectPAT.dispatch({ kind: "github", host: "github.com", token: "t", options: {} });
    await vi.advanceTimersByTimeAsync(5000);
    await p;

    expect(fetchSpy).toHaveBeenCalledTimes(1);
  });

  it("no toast emitted (error: false)", async () => {
    const fetchSpy = vi.fn<typeof fetch>(networkError);
    vi.stubGlobal("fetch", fetchSpy);

    const p = connectPAT.dispatch({
      kind: "github",
      host: "github.com",
      token: "ghp_xxx",
      options: {},
    });
    await vi.advanceTimersByTimeAsync(300);
    await vi.advanceTimersByTimeAsync(600);
    await p;

    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("forge.startDeviceFlow retry", () => {
  it("no auto-retry (no retry config), returns null on network error", async () => {
    const fetchSpy = vi.fn<typeof fetch>(networkError);
    vi.stubGlobal("fetch", fetchSpy);

    const result = await startDeviceFlow.dispatch(GITHUB_START);

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(result).toBeNull();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("retryable flag enables manual retry via onError callback", async () => {
    // `error: false` drops the toast's Retry, but the retryable classification still reaches onError.
    let attempt = 0;
    const fetchSpy = vi.fn<typeof fetch>(() => {
      attempt++;
      if (attempt === 1) {
        return Promise.reject(new TypeError("Failed to fetch"));
      }
      return Promise.resolve(new Response('{"grant_id":"abc"}', { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchSpy);

    const onError = vi.fn();
    await startDeviceFlow.dispatch(GITHUB_START, { onError });

    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError.mock.calls[0]![0]).toMatchObject({ code: "network" });
  });
});
