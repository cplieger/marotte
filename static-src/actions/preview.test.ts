import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as Toast from "../toast.js";

vi.mock("../toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
}));

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { grantPreview, previewStamp } from "./preview.js";
import { error as toastError } from "../toast.js";

const mockFetch = vi.fn();

const GRANT = {
  url: "/preview/tok/index.html",
  base: "/preview/tok/",
  expires_at: "2026-10-03T12:00:00Z",
  epoch: "e1",
  stamp: "s1",
  hint: { preset: "phone", source: "meta" },
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("preview.grant", () => {
  it("POSTs the page path and decodes the grant", async () => {
    mockFetch.mockResolvedValue(json(GRANT));
    const out = await grantPreview.dispatch("/workspace/demo/index.html");
    expect(out).toEqual({ kind: "granted", grant: GRANT });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/preview/grant");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({ path: "/workspace/demo/index.html" });
  });

  it("answers a refusal with the server's own sentence, without a toast", async () => {
    mockFetch.mockResolvedValue(json({ error: "Put the page in its own folder" }, 400));
    const out = await grantPreview.dispatch("/workspace/root.html");
    expect(out).toEqual({ kind: "refused", message: "Put the page in its own folder" });
    expect(toastError).not.toHaveBeenCalled();
  });

  it("answers an unavailable preview as a refusal too", async () => {
    mockFetch.mockResolvedValue(json({ error: "preview unavailable" }, 503));
    const out = await grantPreview.dispatch("/workspace/demo/index.html");
    expect(out).toEqual({ kind: "refused", message: "preview unavailable" });
  });

  it("mints a separate grant for each of two loads in flight for one page", async () => {
    const later = {
      ...GRANT,
      url: "/preview/tok2/index.html",
      base: "/preview/tok2/",
      stamp: "s2",
    };
    let releaseFirst: (r: Response) => void = () => undefined;
    mockFetch
      .mockReturnValueOnce(
        new Promise<Response>((resolve) => {
          releaseFirst = resolve;
        }),
      )
      .mockResolvedValueOnce(json({ ...later, hint: { preset: "desktop", source: "meta" } }));
    const first = grantPreview.dispatch("/workspace/demo/index.html");
    const second = grantPreview.dispatch("/workspace/demo/index.html");
    releaseFirst(json(GRANT));
    const [a, b] = await Promise.all([first, second]);
    expect(mockFetch).toHaveBeenCalledTimes(2);
    expect(b).toEqual({
      kind: "granted",
      grant: { ...later, hint: { preset: "desktop", source: "meta" } },
    });
    expect(a).toEqual({ kind: "granted", grant: GRANT });
  });
});

describe("preview.stamp", () => {
  it("GETs the stamp for the page path, escaped", async () => {
    mockFetch.mockResolvedValue(json({ stamp: "ab", epoch: "e1", entries: 3, truncated: false }));
    const out = await previewStamp.dispatch("/workspace/my demo/index.html");
    expect(out).toEqual({ stamp: "ab", epoch: "e1", entries: 3, truncated: false });
    const [url] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/preview/stamp?path=%2Fworkspace%2Fmy%20demo%2Findex.html");
  });

  it("resolves null on a failure, so the poll reads it as no news", async () => {
    mockFetch.mockResolvedValue(json({ error: "not found" }, 404));
    expect(await previewStamp.dispatch("/workspace/gone/index.html")).toBeNull();
    expect(toastError).not.toHaveBeenCalled();
  });
});
