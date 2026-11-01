import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
}));

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { getActionLog as recentLog } from "@cplieger/actions";
import { downloadFiles } from "./files.js";

beforeEach(() => {
  resetActionFramework();
  vi.clearAllMocks();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("downloadFiles action", () => {
  it("makes a single fetch attempt on network error (no retry config, no auto-retry)", async () => {
    // `retryable: retryNetwork` with NO `retry` config: one attempt, then null.
    const fetchSpy = vi.fn(() => Promise.reject(new TypeError("Failed to fetch")));
    vi.stubGlobal("fetch", fetchSpy);
    const result = await downloadFiles.dispatch({ paths: ["a.txt"] });
    expect(result).toBeNull();
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(recentLog()[0]?.status).toBe("error");
  });

  it("dispatches a POST to /api/files/download and triggers blob download", async () => {
    const fakeBlob = new Blob(["zip-content"], { type: "application/zip" });
    const mockFetch = vi.fn((_input: string, _init?: RequestInit) =>
      Promise.resolve(new Response(fakeBlob)),
    );
    vi.stubGlobal("fetch", mockFetch);

    // Spy on the statics; never replace the global URL. `{ ...URL }` is non-constructible and hangs
    // `@vitest/coverage-v8` in Browser Mode after the tests report green.
    const createURL = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:fake-url");
    const revokeURL = vi.spyOn(URL, "revokeObjectURL").mockReturnValue(undefined);

    const clickSpy = vi.fn();
    const removeSpy = vi.fn();
    vi.spyOn(document, "createElement").mockImplementation((tag: string) => {
      if (tag === "a") {
        return {
          href: "",
          download: "",
          click: clickSpy,
          remove: removeSpy,
        } as unknown as HTMLAnchorElement;
      }
      return document.createElement(tag);
    });
    vi.spyOn(document.body, "appendChild").mockImplementation((el) => el);

    const result = await downloadFiles.dispatch({ paths: ["dir/a.txt", "dir/b.txt"] });

    expect(result).toBeUndefined();
    expect(mockFetch).toHaveBeenCalledOnce();
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/files/download");
    expect(opts?.method).toBe("POST");
    expect(JSON.parse(opts?.body as string)).toEqual({ paths: ["dir/a.txt", "dir/b.txt"] });
    expect(clickSpy).toHaveBeenCalledOnce();
    expect(removeSpy).toHaveBeenCalledOnce();
    expect(createURL).toHaveBeenCalledOnce();
    expect(revokeURL).toHaveBeenCalledWith("blob:fake-url");
  });

  it("reads a refused download as 'Download failed' with its status", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(new Response(JSON.stringify({ error: "disk full" }), { status: 500 })),
      ),
    );

    const result = await downloadFiles.dispatch({ paths: ["x.txt"] });
    expect(result).toBeNull();
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
    expect(log[0]?.error?.message).toBe("Download failed");
    expect(log[0]?.error?.status).toBe(500);
  });

  it("reads a connection dropped mid-body as a network failure", async () => {
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(new Uint8Array([1, 2, 3]));
        c.error(new TypeError("network error"));
      },
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response(body))),
    );
    const createURL = vi.spyOn(URL, "createObjectURL");

    const result = await downloadFiles.dispatch({ paths: ["a.txt"] });
    expect(result).toBeNull();
    const err = recentLog()[0]?.error;
    expect(err?.code).toBe("network");
    expect(err?.status).toBe(0);
    expect(err?.message).toBe("network error");
    expect(createURL).not.toHaveBeenCalled();
  });

  it("does not trigger download if cancelled before the body is read", async () => {
    const fakeBlob = new Blob(["zip-content"], { type: "application/zip" });
    const mockFetch = vi.fn(() => Promise.resolve(new Response(fakeBlob)));
    vi.stubGlobal("fetch", mockFetch);

    const createURL = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:fake-url");
    const revokeURL = vi.spyOn(URL, "revokeObjectURL").mockReturnValue(undefined);

    const clickSpy = vi.fn();
    vi.spyOn(document, "createElement").mockImplementation((tag: string) => {
      if (tag === "a") {
        return {
          href: "",
          download: "",
          click: clickSpy,
          remove: vi.fn(),
        } as unknown as HTMLAnchorElement;
      }
      return document.createElement(tag);
    });
    vi.spyOn(document.body, "appendChild").mockImplementation((el) => el);

    // Cancel immediately: the response resolves, but the signal is aborted before the anchor click.
    const promise = downloadFiles.dispatch({ paths: ["a.txt"] });
    downloadFiles.cancel();
    const result = await promise;

    expect(result).toBeNull();
    expect(clickSpy).not.toHaveBeenCalled();
    if (createURL.mock.calls.length > 0) {
      expect(revokeURL).toHaveBeenCalledWith("blob:fake-url");
    }
  });

  it("records pending then success in registry", async () => {
    const fakeBlob = new Blob(["z"]);
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response(fakeBlob))),
    );
    vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:x");
    vi.spyOn(URL, "revokeObjectURL").mockReturnValue(undefined);
    vi.spyOn(document, "createElement").mockReturnValue({
      href: "",
      download: "",
      click: vi.fn(),
      remove: vi.fn(),
    } as unknown as HTMLAnchorElement);
    vi.spyOn(document.body, "appendChild").mockImplementation((el) => el);

    await downloadFiles.dispatch({ paths: ["a.txt"] });
    const log = recentLog();
    expect(log.length).toBe(1);
    expect(log[0]?.name).toBe("files.download");
    expect(log[0]?.status).toBe("success");
  });
});
