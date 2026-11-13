import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as Toast from "../toast.js";

import { resetActionFramework, headerValue } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
}));

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("docs.delete", () => {
  it("POSTs a delete of the row's path to the file browser's action endpoint", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { deleteDoc } = await import("./docs.js");
    await deleteDoc.dispatch({ path: "/w/.kiro/steering/a.md", name: "a.md" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/files/action");
    expect(JSON.parse(opts.body as string)).toEqual({
      action: "delete",
      path: "/w/.kiro/steering/a.md",
    });
  });

  // A delete, a recreate and a second delete of one path within the server's replay window
  // (idempotency.go idempotencyTTL) must reach the handler twice, so each gesture carries its own key.
  it("deleting a recreated doc sends a fresh key, so the second delete is not a replay", async () => {
    mockFetch.mockImplementation(() =>
      Promise.resolve(new Response(JSON.stringify({}), { status: 200 })),
    );
    const { deleteDoc } = await import("./docs.js");
    const args = { path: "/w/.kiro/steering/a.md", name: "a.md" };
    await deleteDoc.dispatch(args);
    await deleteDoc.dispatch(args);
    const keys = mockFetch.mock.calls.map((c) =>
      headerValue(c[1] as RequestInit, "idempotency-key"),
    );
    expect(keys[0]).toEqual(expect.any(String));
    expect(keys[1]).toEqual(expect.any(String));
    expect(keys[1]).not.toBe(keys[0]);
  });
});
