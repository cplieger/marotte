import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetOrError: vi.fn(),
}));

import type * as ApiClient from "./api-client.js";
import { apiGetOrError } from "./api-client.js";
import { fetchDir } from "./files-fetch.js";

function holder(): { controllerHolder: { current: AbortController | null } } {
  return { controllerHolder: { current: null } };
}

function failure(status: number, error: string, code?: string): unknown {
  return code === undefined
    ? { ok: false, status, data: null, error }
    : { ok: false, status, data: null, error, code };
}

beforeEach(() => {
  vi.mocked(apiGetOrError).mockReset();
});

describe("fetchDir", () => {
  it("asks for the encoded path and answers a listing", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue({
      ok: true,
      status: 200,
      data: {
        files: [{ name: "a", isDir: true, size: 0, mode: "drwx", modTime: 0 }],
        writable: true,
      },
      error: "",
    });
    const d = await fetchDir("/w s/x", holder());
    expect(vi.mocked(apiGetOrError).mock.calls[0]?.[0]).toBe("/api/files?path=%2Fw%20s%2Fx");
    expect(d).toEqual({
      kind: "ok",
      files: [{ name: "a", isDir: true, size: 0, mode: "drwx", modTime: 0 }],
      writable: true,
    });
  });

  it("reads an absent writable flag and file list as a read-only empty folder", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue({ ok: true, status: 200, data: {}, error: "" });
    expect(await fetchDir("/a", holder())).toEqual({ kind: "ok", files: [], writable: false });
  });

  it("reports a file path as not-dir when the server tags it", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue(
      failure(400, "not a directory", "not_a_directory") as never,
    );
    expect(await fetchDir("/a/note.md", holder())).toEqual({ kind: "not-dir" });
  });

  it("does not guess the file arm from the message text alone", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue(failure(400, "not a directory") as never);
    expect(await fetchDir("/a/note.md", holder())).toEqual({
      kind: "error",
      message: "not a directory",
    });
  });

  it("shows a 404 in the server's own words", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue(failure(404, "not found") as never);
    expect(await fetchDir("/a/gone", holder())).toEqual({ kind: "error", message: "not found" });
  });

  it("shows a refusal in the server's own words", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue(failure(403, "path outside granted roots") as never);
    expect(await fetchDir("/etc", holder())).toEqual({
      kind: "error",
      message: "path outside granted roots",
    });
  });

  it("says the server was unreachable when no answer arrived", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue(failure(0, "Failed to fetch", "network") as never);
    expect(await fetchDir("/a", holder())).toEqual({
      kind: "error",
      message: "Could not reach the server",
    });
  });

  it("says the server was unreachable when the request timed out", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue(failure(0, "signal timed out", "timeout") as never);
    expect(await fetchDir("/a", holder())).toEqual({
      kind: "error",
      message: "Could not reach the server",
    });
  });

  it("keeps a request that never left in its own words", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue(
      failure(0, "Invalid header value", "invalid") as never,
    );
    expect(await fetchDir("/a", holder())).toEqual({
      kind: "error",
      message: "Invalid header value",
    });
  });

  it("calls an empty 2xx a broken answer rather than an empty folder", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue({ ok: true, status: 204, data: null, error: "" });
    expect(await fetchDir("/a", holder())).toEqual({
      kind: "error",
      message: "Empty answer from the server (HTTP 204)",
    });
  });

  it("stays silent for a cancelled request", async () => {
    vi.mocked(apiGetOrError).mockResolvedValue(failure(0, "aborted", "cancelled") as never);
    expect(await fetchDir("/a", holder())).toEqual({ kind: "stale" });
  });

  it("stays silent for a request a newer one on the same holder superseded", async () => {
    let release: (v: unknown) => void = () => undefined;
    vi.mocked(apiGetOrError)
      .mockImplementationOnce(
        () =>
          new Promise((r) => {
            release = r;
          }) as never,
      )
      .mockResolvedValueOnce(failure(404, "not found") as never);
    const h = holder();
    const first = fetchDir("/a", h);
    const second = fetchDir("/b", h);
    release({ ok: true, status: 200, data: { files: [], writable: true }, error: "" });
    expect(await first).toEqual({ kind: "stale" });
    expect(await second).toEqual({ kind: "error", message: "not found" });
  });
});
