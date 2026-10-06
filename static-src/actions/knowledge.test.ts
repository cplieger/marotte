import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as Toast from "../toast.js";
import type * as ApiClient from "../api-client.js";

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

vi.mock("../toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
}));

vi.mock("../api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { cancelKnowledgeIndexing, clearKnowledge } from "./knowledge.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

function request(): { url: string; method: string | undefined } {
  const call = mockFetch.mock.calls[0] as [unknown, RequestInit | undefined] | undefined;
  return { url: String(call?.[0]), method: call?.[1]?.method };
}

describe("knowledge.cancel wire shape", () => {
  it("POSTs the cancel for the encoded base name", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await cancelKnowledgeIndexing.dispatch({ name: "my docs" });
    const { url, method } = request();
    expect(method).toBe("POST");
    expect(url).toContain("/api/knowledge/my%20docs/cancel");
  });
});

describe("knowledge.clear wire shape", () => {
  it("DELETEs the whole knowledge collection", async () => {
    mockFetch.mockResolvedValue(new Response(null, { status: 204 }));
    await clearKnowledge.dispatch(undefined);
    const { url, method } = request();
    expect(method).toBe("DELETE");
    expect(new URL(url, "http://x").pathname).toBe("/api/knowledge");
  });
});
