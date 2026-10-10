// The error copy for `runs.answer_input`. The real refusal is a 409 whose server sentence says
// the answer was not needed. A static `error` string PREFIXES the server message
// (`emitErrorToast`: `${spec}: ${err.message}`), contradicting it; and `@cplieger/fetch` fills
// `message` with `HTTP <status>` or a browser sentence (status 0) when there is none, so both
// empty cases must be recognised.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

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
  apiGet: vi.fn(),
  apiGetTyped: vi.fn().mockResolvedValue(null),
}));

import { error as toastError } from "../toast.js";
import { answerRunInput } from "./runs.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";

const mockFetch = vi.fn();
const mockToastError = vi.mocked(toastError);

const FALLBACK = "Could not send your answer to the step";
const CONFLICT = "that question has already been answered, or the step it belonged to has moved on";

beforeEach(() => {
  resetActionFramework();
  mockToastError.mockClear();
  vi.stubGlobal("fetch", mockFetch);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function answerAgainst(res: () => Response | Promise<Response>): Promise<string> {
  mockFetch.mockImplementation(res);
  await answerRunInput.dispatch({ workflowID: "wf_1", ask_id: "a1", text: "the main branch" });
  return (mockToastError.mock.calls.at(-1)?.[0] ?? "") as string;
}

describe("runs.answer_input error copy", () => {
  it("shows the server's sentence on the 409 that actually happens", async () => {
    const msg = await answerAgainst(
      () => new Response(JSON.stringify({ error: CONFLICT }), { status: 409 }),
    );
    expect(msg).toBe(CONFLICT);
  });

  it("shows the server's sentence on a 400 too", async () => {
    const msg = await answerAgainst(
      () =>
        new Response(JSON.stringify({ error: "the step that asked cannot be addressed" }), {
          status: 400,
        }),
    );
    expect(msg).toBe("the step that asked cannot be addressed");
  });

  it("falls back when the body carried no sentence", async () => {
    // fetch's placeholder, not a server message: `HTTP 500` reads like a value the reader can act on.
    const msg = await answerAgainst(() => new Response("", { status: 500 }));
    expect(msg).toBe(FALLBACK);
  });

  it("falls back on a transport failure, whose message is about a fetch", async () => {
    const msg = await answerAgainst(() => Promise.reject(new Error("Failed to fetch")));
    expect(msg).toBe(FALLBACK);
  });
});
