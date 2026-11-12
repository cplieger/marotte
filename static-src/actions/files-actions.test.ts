import { describe, it, expect, vi, beforeEach } from "vitest";

import { resetActionFramework, headerValue } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  apiBlobOrError: vi.fn(),
  apiGetOrError: vi.fn(),
  // Inert: present only so real-ESM linking succeeds.
  apiGet: undefined,
  // Inert: present only so real-ESM linking succeeds.
  apiGetTyped: undefined,
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

vi.mock("../upload.js", () => ({
  uploadFiles: vi.fn(),
}));

import { getActionLog as recentLog } from "@cplieger/reactive/actions";
import * as toast from "../toast.js";
import { uploadFiles } from "../upload.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("files.create_file", () => {
  it("POSTs touch action to /api/files/action", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { createFile } = await import("./files.js");
    await createFile.dispatch({ dir: "/src", name: "index.ts" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/files/action");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ action: "touch", path: "/src/index.ts" });
  });

  it("retries on network error", async () => {
    vi.useFakeTimers();
    mockFetch
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(new Response(JSON.stringify({}), { status: 200 }));
    const { createFile } = await import("./files.js");
    const p = createFile.dispatch({ dir: "/", name: "a.ts" });
    await vi.advanceTimersByTimeAsync(300);
    await p;
    expect(mockFetch).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });

  it("toasts error on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "exists" }), { status: 409 }));
    const { createFile } = await import("./files.js");
    await createFile.dispatch({ dir: "/", name: "x" });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("create file"), undefined);
  });
});

describe("files.create_folder", () => {
  it("POSTs mkdir action to /api/files/action", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { createFolder } = await import("./files.js");
    await createFolder.dispatch({ dir: "/project", name: "utils" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/files/action");
    expect(JSON.parse(opts.body as string)).toEqual({ action: "mkdir", path: "/project/utils" });
  });
});

describe("files.rename", () => {
  it("POSTs rename action with original and new name", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { renameFile } = await import("./files.js");
    await renameFile.dispatch({ dir: "/src", original: "old.ts", newName: "new.ts" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/files/action");
    expect(JSON.parse(opts.body as string)).toEqual({
      action: "rename",
      path: "/src/old.ts",
      name: "new.ts",
    });
  });

  it("includes Idempotency-Key header", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { renameFile } = await import("./files.js");
    await renameFile.dispatch({ dir: "/", original: "a", newName: "b" });
    expect(headerValue(mockFetch.mock.calls[0]![1], "idempotency-key")).toEqual(expect.any(String));
  });

  it("toasts error with truncated filename on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "perm" }), { status: 403 }));
    const { renameFile } = await import("./files.js");
    await renameFile.dispatch({ dir: "/", original: "my-file.ts", newName: "x" });
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("my-file.ts"), undefined);
  });
});

describe("files.delete (batch)", () => {
  it("sends parallel delete requests for each file", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const listEl = document.createElement("div");
    const { deleteFilesBatch } = await import("./files.js");
    await deleteFilesBatch.dispatch({ dir: "/src", names: ["a.ts", "b.ts"], listEl });
    expect(mockFetch).toHaveBeenCalledTimes(2);
    const bodies = mockFetch.mock.calls.map((c) => JSON.parse(c[1].body as string));
    expect(bodies).toContainEqual({ action: "delete", path: "/src/a.ts" });
    expect(bodies).toContainEqual({ action: "delete", path: "/src/b.ts" });
  });

  it("applies optimistic exit class and rolls back on failure", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const listEl = document.createElement("div");
    const row = document.createElement("div");
    row.dataset["name"] = "target.ts";
    listEl.appendChild(row);

    const { deleteFilesBatch } = await import("./files.js");
    await deleteFilesBatch.dispatch({ dir: "/", names: ["target.ts"], listEl });
    expect(row.classList.contains("fb-row-exiting")).toBe(false);
  });

  it("is not retryable", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "fail" }), { status: 500 }));
    const listEl = document.createElement("div");
    const { deleteFilesBatch } = await import("./files.js");
    await deleteFilesBatch.dispatch({ dir: "/", names: ["x"], listEl });
    const log = recentLog();
    expect(log[0]?.status).toBe("error");
  });
});

describe("files.upload", () => {
  it("calls uploadFiles and toasts success with count", async () => {
    vi.mocked(uploadFiles).mockImplementation(({ onComplete }) => {
      onComplete!(["a.ts", "b.ts"]);
    });
    const { upload } = await import("./files.js");
    const files = {
      length: 2,
      item: () => null,
      0: new File([""], "a.ts"),
      1: new File([""], "b.ts"),
    } as unknown as FileList;
    const r = await upload.dispatch({ files, targetDir: "/uploads" });
    expect(r).toEqual(["a.ts", "b.ts"]);
    expect(toast.success).toHaveBeenCalledWith("Uploaded 2 files");
  });

  it("toasts error on upload failure", async () => {
    vi.mocked(uploadFiles).mockImplementation(({ onError }) => {
      onError!("Network error", []);
    });
    const { upload } = await import("./files.js");
    const files = { length: 1, item: () => null, 0: new File([""], "x") } as unknown as FileList;
    const r = await upload.dispatch({ files, targetDir: "/" });
    expect(r).toBeNull();
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Upload failed"), undefined);
  });

  it("carries the partial batch to the caller, which is not rolled back", async () => {
    vi.mocked(uploadFiles).mockImplementation(({ onError }) => {
      onError!("3 of 5 uploaded, then big.zip failed: upload too large", [
        "/uploads/a.txt",
        "/uploads/b.txt",
        "/uploads/c.txt",
      ]);
    });
    const { upload, partialUploadOf } = await import("./files.js");
    const files = { length: 5, item: () => null } as unknown as FileList;
    let seen: string[] = [];
    await upload.dispatch(
      { files, targetDir: "/uploads" },
      {
        onError: (err) => {
          seen = partialUploadOf(err.cause);
        },
      },
    );
    expect(seen).toEqual(["/uploads/a.txt", "/uploads/b.txt", "/uploads/c.txt"]);
    expect(toast.error).toHaveBeenCalledWith(
      expect.stringContaining("then big.zip failed"),
      undefined,
    );
  });
});

describe("partialUploadOf", () => {
  it("returns nothing for every other failure shape, so callers need no branch", async () => {
    const { partialUploadOf } = await import("./files.js");
    expect(partialUploadOf(undefined)).toEqual([]);
    expect(partialUploadOf(null)).toEqual([]);
    expect(partialUploadOf(new Error("boom"))).toEqual([]);
    expect(partialUploadOf({ uploaded: "not-a-list" })).toEqual([]);
    expect(partialUploadOf({ uploaded: ["a", 7, "b"] })).toEqual(["a", "b"]);
  });
});

// The server replays any answer it cached under a key for five minutes (internal/server/idempotency.go),
// so a key must name one user gesture: a retry of that gesture reuses it, a repeat of it must not.
describe("idempotency keys name one gesture, not its arguments", () => {
  const ok = (): Response => new Response(JSON.stringify({}), { status: 200 });

  async function keysOf(dispatches: (() => Promise<unknown>)[]): Promise<(string | undefined)[]> {
    mockFetch.mockImplementation(() => Promise.resolve(ok()));
    for (const d of dispatches) {
      await d();
    }
    return mockFetch.mock.calls.map((c) => headerValue(c[1] as RequestInit, "idempotency-key"));
  }

  it("New file twice in one folder sends two keys, so the second create is not a replay", async () => {
    const { createFile } = await import("./files.js");
    const args = { dir: "/w", name: "new file" };
    const [first, second] = await keysOf([
      () => createFile.dispatch(args),
      () => createFile.dispatch(args),
    ]);
    expect(first).toEqual(expect.any(String));
    expect(second).toEqual(expect.any(String));
    expect(second).not.toBe(first);
  });

  it("New folder twice in one folder sends two keys", async () => {
    const { createFolder } = await import("./files.js");
    const args = { dir: "/w", name: "new folder" };
    const [first, second] = await keysOf([
      () => createFolder.dispatch(args),
      () => createFolder.dispatch(args),
    ]);
    expect(first).toEqual(expect.any(String));
    expect(second).not.toBe(first);
  });

  it("renaming A to B, back to A, then to B again sends a fresh key for the third rename", async () => {
    const { renameFile } = await import("./files.js");
    const ab = { dir: "/w", original: "a.txt", newName: "b.txt" };
    const ba = { dir: "/w", original: "b.txt", newName: "a.txt" };
    const keys = await keysOf([
      () => renameFile.dispatch(ab),
      () => renameFile.dispatch(ba),
      () => renameFile.dispatch(ab),
    ]);
    expect(keys[0]).toEqual(expect.any(String));
    expect(keys[2]).not.toBe(keys[0]);
  });

  it("a network retry of one create reuses its key, so the server can replay it", async () => {
    vi.useFakeTimers();
    mockFetch.mockRejectedValueOnce(new TypeError("Failed to fetch")).mockResolvedValueOnce(ok());
    const { createFile } = await import("./files.js");
    const p = createFile.dispatch({ dir: "/w", name: "new file" });
    await vi.advanceTimersByTimeAsync(300);
    await p;
    vi.useRealTimers();
    const keys = mockFetch.mock.calls.map((c) =>
      headerValue(c[1] as RequestInit, "idempotency-key"),
    );
    expect(keys).toHaveLength(2);
    expect(keys[0]).toEqual(expect.any(String));
    expect(keys[1]).toBe(keys[0]);
  });
});

describe("files.delete dedupe key", () => {
  /** Dispatch a batch delete and report how many fetches ran (a deduped dispatch never fetches). */
  async function dispatchBoth(first: string[], second: string[]): Promise<number> {
    mockFetch.mockImplementation(
      () =>
        new Promise((resolve) => {
          setTimeout(() => {
            resolve(new Response(JSON.stringify({}), { status: 200 }));
          }, 0);
        }),
    );
    const { deleteFilesBatch } = await import("./files.js");
    const listEl = document.createElement("div");
    const a = deleteFilesBatch.dispatch({ dir: "/w", names: first, listEl });
    const b = deleteFilesBatch.dispatch({ dir: "/w", names: second, listEl });
    await Promise.all([a, b]);
    return mockFetch.mock.calls.length;
  }

  it("does not collapse the batch [\u201ca,b\u201d] into the batch [\u201ca\u201d,\u201cb\u201d]", async () => {
    // A ","-joined list cannot tell one comma-bearing name from two; the nested join can, so both
    // dispatches run (3 fetches: 1 + 2).
    const oldKey = (names: string[]): string => `files.delete:${names.slice().sort().join(",")}`;
    expect(oldKey(["a,b"])).toBe(oldKey(["a", "b"]));

    expect(await dispatchBoth(["a,b"], ["a", "b"])).toBe(3);
  });

  it("still dedupes an identical batch regardless of order", async () => {
    expect(await dispatchBoth(["a", "b"], ["b", "a"])).toBe(2);
  });
});
