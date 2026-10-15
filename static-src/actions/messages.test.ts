import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

vi.mock("../api-client.js", () => ({
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,

  apiGet: vi.fn(),
  apiPost: vi.fn(),
  // Inert: present only so real-ESM linking succeeds.
  apiGetTyped: vi.fn(),
}));

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { getActionLog as recentLog } from "@cplieger/actions";
import * as toast from "../toast.js";

beforeEach(() => {
  resetActionFramework();
});

describe("ui.copy_clipboard", () => {
  it("copies text and toasts success", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    const { copyClipboard } = await import("./messages.js");
    await copyClipboard.dispatch("hello world");
    expect(writeText).toHaveBeenCalledWith("hello world");
    expect(toast.success).toHaveBeenCalledWith("Copied");
  });

  it("toasts error when clipboard unavailable", async () => {
    vi.stubGlobal("navigator", {
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error("denied")) },
    });
    const { copyClipboard } = await import("./messages.js");
    const r = await copyClipboard.dispatch("x");
    expect(r).toBeNull();
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Could not copy"), undefined);
    expect(recentLog()[0]?.error?.code).toBe("clipboard");
  });
});
