import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { fetchDir } from "./files-fetch.js";

function holder(): { controllerHolder: { current: AbortController | null } } {
  return { controllerHolder: { current: null } };
}

function answer(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("fetchDir over the real api client", () => {
  let fetchSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchSpy = vi.fn();
    vi.stubGlobal("fetch", fetchSpy);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads the server's not_a_directory code as a file path", async () => {
    fetchSpy.mockResolvedValue(answer(400, { error: "not a directory", code: "not_a_directory" }));
    expect(await fetchDir("/a/note.md", holder())).toEqual({ kind: "not-dir" });
  });

  it("shows the server's error text for a coded refusal it does not recognise", async () => {
    fetchSpy.mockResolvedValue(answer(400, { error: "bad path", code: "bad_path" }));
    expect(await fetchDir("/a", holder())).toEqual({ kind: "error", message: "bad path" });
  });

  it("says the server was unreachable when the request never got an answer", async () => {
    fetchSpy.mockRejectedValue(new TypeError("Failed to fetch"));
    expect(await fetchDir("/a", holder())).toEqual({
      kind: "error",
      message: "Could not reach the server",
    });
  });
});
