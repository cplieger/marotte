// Native policy actions: the request wire shape, and that a refused write fails the dispatch.

import { describe, it, expect, vi, beforeEach } from "vitest";

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  // Inert: present only so real-ESM linking succeeds.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { editNativeRule, explainPolicy } from "./permissions.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

function requestBody(): Record<string, unknown> {
  const init = mockFetch.mock.calls[0]?.[1] as RequestInit | undefined;
  return JSON.parse(String(init?.body ?? "{}")) as Record<string, unknown>;
}

describe("editNativeRule wire shape", () => {
  it("POSTs the rule to /api/permissions/rules", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    const res = await editNativeRule.dispatch({
      op: "add",
      scope: "workspace",
      capability: "fs_write",
      effect: "ask",
      match: ["src/**"],
    });
    expect(res).not.toBeNull();
    expect(String(mockFetch.mock.calls[0]?.[0])).toContain("/api/permissions/rules");
    expect(requestBody()).toMatchObject({
      op: "add",
      scope: "workspace",
      capability: "fs_write",
      effect: "ask",
      match: ["src/**"],
    });
  });

  it("fails the dispatch when the server refuses the write (409)", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "existing policy file could not be parsed" }), {
        status: 409,
      }),
    );
    const res = await editNativeRule.dispatch({
      op: "add",
      scope: "workspace",
      capability: "shell",
      effect: "allow",
      match: ["rm *"],
    });
    expect(res).toBeNull();
  });
});

describe("explainPolicy wire shape", () => {
  it("POSTs the simulation request to /api/permissions/explain", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ capability: "shell", effect: "ask", is_explicit_ask: true }), {
        status: 200,
      }),
    );
    const res = await explainPolicy.dispatch({ capability: "shell", resource: "rm -rf /" });
    expect(String(mockFetch.mock.calls[0]?.[0])).toContain("/api/permissions/explain");
    expect(requestBody()).toMatchObject({ capability: "shell", resource: "rm -rf /" });
    expect(res).toMatchObject({ effect: "ask" });
  });
});
