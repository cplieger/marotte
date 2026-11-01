import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { getActionLog as recentLog } from "@cplieger/actions";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { downloadKiroSession } from "./chat.js";
import { exportFilename } from "../export-filename.js";

beforeEach(() => {
  resetActionFramework();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("downloadKiroSession", () => {
  it("reads the chain's zip and saves it under the chat's export name", async () => {
    const zip = new Blob(["zip"], { type: "application/zip" });
    const mockFetch = vi.fn((_input: string, _init?: RequestInit) =>
      Promise.resolve(new Response(zip)),
    );
    vi.stubGlobal("fetch", mockFetch);
    const createURL = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:kiro");
    const revokeURL = vi.spyOn(URL, "revokeObjectURL").mockReturnValue(undefined);
    const saved: { href: string; download: string }[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      saved.push({ href: this.href, download: this.download });
    });

    const result = await downloadKiroSession.dispatch({ chatID: "c/1", name: "My chat" });

    expect(result).toBeUndefined();
    const [url, init] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/chats/c%2F1/kiro-session");
    expect(init?.method).toBe("GET");
    const read: unknown = createURL.mock.calls[0]?.[0];
    expect(read).toBeInstanceOf(Blob);
    expect(await (read as Blob).text()).toBe("zip");
    expect(saved).toEqual([
      { href: "blob:kiro", download: exportFilename("My chat", "c/1", "kiro-session") },
    ]);
    expect(revokeURL).toHaveBeenCalledWith("blob:kiro");
  });

  it("reads a refused download as 'Download failed' with its status", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(new Response(JSON.stringify({ error: "chat not found" }), { status: 404 })),
      ),
    );
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click");

    const result = await downloadKiroSession.dispatch({ chatID: "c1", name: "x" });

    expect(result).toBeNull();
    const err = recentLog()[0]?.error;
    expect(err?.message).toBe("Download failed");
    expect(err?.status).toBe(404);
    expect(click).not.toHaveBeenCalled();
  });
});
