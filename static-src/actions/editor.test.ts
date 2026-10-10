import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

// Every `api-client.ts` name: Browser Mode links ESM for real, and a broken link surfaces as a
// closed browser connection, not a missing export.
vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiGetTyped: vi.fn(),
  apiGetTypedOrError: vi.fn(),
  apiPostTyped: vi.fn(),
  apiGetOrError: vi.fn(),
}));
vi.mock("../transport.js", () => ({
  send: vi.fn(),
  newMessageID: () => "m-test",
  newRequestID: () => "r-test",
  newOpID: () => "op-test",
}));

vi.mock("../editor-types.js", () => ({
  routeForPath: (path: string) => {
    const q = `?path=${encodeURIComponent(path)}`;
    return { readURL: `/api/file${q}`, statURL: `/api/file/stat${q}`, writeURL: `/api/file${q}` };
  },
}));

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "../workspace.js";
import * as api from "../api-client.js";

const mockFetch = vi.fn();
const ID = `sha256:${"a".repeat(64)}`;

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.mocked(api.apiGetTypedOrError).mockReset();
  vi.stubGlobal("fetch", mockFetch);
  resetWorkspace();
});

describe("editor.save_file", () => {
  it("PUTs the content with the identity it was read under", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ ok: true, file_id: ID, size: 3 }), { status: 200 }),
    );
    const { saveFile } = await import("./editor.js");
    const r = await saveFile.dispatch({ path: "src/main.ts", content: "a\r\n", fileId: ID });
    expect(r).toEqual({ kind: "saved", result: { ok: true, file_id: ID, size: 3 } });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/file?path=src%2Fmain.ts");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body as string)).toEqual({ content: "a\r\n", file_id: ID });
  });

  it("sends no identity for an overwrite", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ ok: true, file_id: ID, size: 1 }), { status: 200 }),
    );
    const { saveFile } = await import("./editor.js");
    await saveFile.dispatch({ path: "x.ts", content: "x" });
    expect(JSON.parse(mockFetch.mock.calls[0]![1].body as string)).toEqual({ content: "x" });
  });

  it("answers a refused stale save with the decoded refusal", async () => {
    const body = {
      error: "file changed on disk since you opened it",
      code: "changed",
      content_kind: "binary",
      file_id: ID,
      size: 9,
    };
    mockFetch.mockResolvedValue(new Response(JSON.stringify(body), { status: 409 }));
    const { saveFile } = await import("./editor.js");
    const r = await saveFile.dispatch({ path: "x.ts", content: "mine", fileId: ID });
    expect(r).toEqual({ kind: "stale", refusal: body });
  });

  it("suppresses error toast (error: false)", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "disk full" }), { status: 500 }),
    );
    const { saveFile } = await import("./editor.js");
    const { error: toastError } = await import("../toast.js");
    await saveFile.dispatch({ path: "x.ts", content: "" });
    expect(toastError).not.toHaveBeenCalled();
  });
});

describe("editor.fetch_agent_lines", () => {
  it("GETs file changes with chat_id and path params", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ changes: [{ start_line: 1, end_line: 5 }] }), { status: 200 }),
    );
    const { fetchAgentLines } = await import("./editor.js");
    const r = await fetchAgentLines.dispatch({ chatID: "c1", path: "src/a.ts" });
    expect(r).toEqual({ changes: [{ start_line: 1, end_line: 5 }] });
    const [url] = mockFetch.mock.calls[0]!;
    expect(url).toContain("/api/file-changes");
    expect(url).toContain("chat_id=c1");
    expect(url).toContain("path=src%2Fa.ts");
  });
});

describe("editor.suggest_resolution", () => {
  it("POSTs to /api/utility/resolve-conflict", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ output: "resolved" }), { status: 200 }),
    );
    const { suggestResolution } = await import("./editor.js");
    const r = await suggestResolution.dispatch({ ours: "a", theirs: "b", context: "merge" });
    expect(r).toEqual({ output: "resolved" });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/utility/resolve-conflict");
    expect(opts.method).toBe("POST");
  });
});

/** A staged answer: the status and the JSON body the server sent. */
interface Staged {
  readonly status: number;
  readonly body?: unknown;
}

/** Stage answers by URL prefix through the real decoders, the way apiGetTypedOrError reads them:
 *  a 2xx is decoded (a rejection lands on the failure side), a failure keeps the body's error. */
function stage(routes: Record<string, Staged>): void {
  vi.mocked(api.apiGetTypedOrError).mockImplementation(
    (url: string, decoder: (v: unknown) => unknown) => {
      const prefix = Object.keys(routes).find((p) => url.startsWith(p));
      const s = prefix === undefined ? undefined : routes[prefix];
      if (s === undefined) {
        throw new Error(`unexpected GET ${url}`);
      }
      const error =
        s.body !== null &&
        typeof s.body === "object" &&
        typeof (s.body as { error?: unknown }).error === "string"
          ? (s.body as { error: string }).error
          : "";
      if (s.status >= 200 && s.status < 300) {
        try {
          return Promise.resolve({ ok: true, status: s.status, data: decoder(s.body), error: "" });
        } catch (e) {
          return Promise.resolve({ ok: false, status: s.status, data: null, error: String(e) });
        }
      }
      return Promise.resolve({ ok: false, status: s.status, data: null, error, body: s.body });
    },
  );
}

const read = (content: string): Staged => ({
  status: 200,
  body: {
    path: "/workspace/a.go",
    file_id: ID,
    modified: "2026-10-01T00:00:00Z",
    content,
    size: content.length,
    utf8: true,
    read_only: false,
  },
});
const show = (body: unknown, status = 200): Staged => ({ status, body });

const SHOW = "/api/git/show";
const FILE = "/api/file?";

describe("editor.load_diff", () => {
  function urls(): string[] {
    return vi.mocked(api.apiGetTypedOrError).mock.calls.map((c) => String(c[0]));
  }

  it("sends the workspace-relative path to git show and the absolute one to the file route", async () => {
    setWorkspaceRoot("/workspace");
    stage({ [SHOW]: show({ content: "base" }), [FILE]: read("work") });
    const { loadDiff } = await import("./editor.js");
    await loadDiff.dispatch({ path: "/workspace/sub/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(urls()).toContain("/api/git/show?path=sub%2Fa.go&ref=HEAD");
    expect(urls()).toContain("/api/file?path=%2Fworkspace%2Fsub%2Fa.go");
  });

  it("passes the path through untouched when the caller names a repo", async () => {
    setWorkspaceRoot("/workspace");
    stage({ [SHOW]: show({ content: "base" }), [FILE]: read("work") });
    const { loadDiff } = await import("./editor.js");
    await loadDiff.dispatch({ path: "static-src/a.ts", repo: "marotte", ref: "HEAD" }).outcome;
    expect(urls()).toContain("/api/git/show?path=static-src%2Fa.ts&ref=HEAD&repo=marotte");
  });

  it("captions both panes and carries the working read for an ordinary change", async () => {
    setWorkspaceRoot("/workspace");
    stage({ [SHOW]: show({ content: "base" }), [FILE]: read("work") });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o).toMatchObject({
      status: "success",
      value: {
        kind: "diff",
        baseLabel: "HEAD",
        workingLabel: "working tree",
        oldContent: "base",
        read: { file_id: ID, content: "work" },
      },
    });
  });

  it("captions a file outside every repo 'not in git' rather than HEAD", async () => {
    setWorkspaceRoot("/workspace");
    stage({ [SHOW]: show({ error: "not_in_repo" }), [FILE]: read("work") });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o).toMatchObject({
      status: "success",
      value: { baseLabel: "not in git", read: { content: "work" } },
    });
  });

  it("captions an untracked file 'not in <ref>'", async () => {
    setWorkspaceRoot("/workspace");
    stage({ [SHOW]: show({ content: "", absent: true }), [FILE]: read("brand new\n") });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "origin/main" })
      .outcome;
    expect(o).toMatchObject({
      status: "success",
      value: { baseLabel: "not in origin/main", oldContent: "", base: "text" },
    });
  });

  it("renders a deleted file as an all-deletions diff captioned 'deleted'", async () => {
    setWorkspaceRoot("/workspace");
    stage({
      [SHOW]: show({ content: "gone\n" }),
      [FILE]: { status: 404, body: { error: "not found" } },
    });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o).toMatchObject({
      status: "success",
      value: { oldContent: "gone\n", read: null, workingLabel: "deleted" },
    });
  });

  it("falls back to the file's own view when the working file is binary", async () => {
    setWorkspaceRoot("/workspace");
    stage({
      [SHOW]: show({ content: "x" }),
      [FILE]: { status: 415, body: { error: "binary file", code: "binary" } },
    });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/logo.png", repo: "", ref: "HEAD" })
      .outcome;
    expect(o).toMatchObject({ status: "success", value: { kind: "binary" } });
  });

  it.each([
    ["binary", { error: "binary revision", code: "binary" }],
    ["not_utf8", { error: "revision is not UTF-8 text", code: "not_utf8" }],
  ])("names a %s base and diffs nothing, keeping the working read", async (base, body) => {
    setWorkspaceRoot("/workspace");
    stage({ [SHOW]: show(body, 415), [FILE]: read("work") });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.dat", repo: "", ref: "HEAD" }).outcome;
    expect(o).toMatchObject({
      status: "success",
      value: { kind: "diff", oldContent: "", base, baseLabel: "HEAD", read: { content: "work" } },
    });
  });

  // A refusal is decoded before anything branches on it: a body that is not the shape its
  // status promises is a failed load, never a view.
  it.each([
    [
      "a base 415 with no code",
      { [SHOW]: show({ error: "binary revision" }, 415), [FILE]: read("work") },
    ],
    [
      "a base 415 naming the cap",
      { [SHOW]: show({ error: "x", code: "too_large" }, 415), [FILE]: read("work") },
    ],
    [
      "a base 413 naming binary",
      { [SHOW]: show({ error: "x", code: "binary" }, 413), [FILE]: read("work") },
    ],
    [
      "a working 413 with no size",
      {
        [SHOW]: show({ content: "base" }),
        [FILE]: { status: 413, body: { error: "too large", code: "too_large" } },
      },
    ],
    [
      "a working 415 with another code",
      {
        [SHOW]: show({ content: "base" }),
        [FILE]: { status: 415, body: { error: "x", code: "changed" } },
      },
    ],
  ])("fails on %s", async (_name, routes) => {
    setWorkspaceRoot("/workspace");
    stage(routes);
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("error");
  });

  it.each([
    [
      "the base",
      {
        [SHOW]: show({ error: "revision too large to diff", code: "too_large" }, 413),
        [FILE]: read("work"),
      },
    ],
    [
      "the working side",
      {
        [SHOW]: show({ content: "base" }),
        [FILE]: {
          status: 413,
          body: {
            error: "File is too large to display. Download it to view.",
            code: "too_large",
            size: 3 << 20,
          },
        },
      },
    ],
  ])("falls back to the file when %s is too large", async (_side, routes) => {
    setWorkspaceRoot("/workspace");
    stage(routes);
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o).toMatchObject({ status: "success", value: { kind: "too_large" } });
  });

  it("fails on a real git error rather than rendering an all-add diff", async () => {
    setWorkspaceRoot("/workspace");
    stage({
      [SHOW]: show({ error: "show_failed", detail: "fatal: bad object" }),
      [FILE]: read("work"),
    });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status === "error" ? o.error.message : "").toContain("fatal: bad object");
  });

  it("shows the server's reason when the working copy cannot be read", async () => {
    setWorkspaceRoot("/workspace");
    stage({
      [SHOW]: show({ content: "base" }),
      [FILE]: { status: 403, body: { error: "access denied: protected path" } },
    });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status === "error" ? o.error.message : "").toBe("access denied: protected path");
  });

  it("names the base side when git show is unreachable", async () => {
    setWorkspaceRoot("/workspace");
    stage({ [SHOW]: { status: 0 }, [FILE]: read("work") });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status === "error" ? o.error.message : "").toContain("HEAD");
  });
});

describe("editor.stat_file", () => {
  it("asks nothing for a tab that is not shown", async () => {
    stage({});
    const { statFile } = await import("./editor.js");
    expect(await statFile.dispatch({ path: "/w/a", shown: () => false })).toEqual({
      kind: "skipped",
    });
    expect(vi.mocked(api.apiGetTypedOrError)).not.toHaveBeenCalled();
  });

  it("answers a vanished file as gone, and a stat as its decoded facts", async () => {
    const { statFile } = await import("./editor.js");
    stage({ "/api/file/stat": { status: 404, body: { error: "not found" } } });
    expect(await statFile.dispatch({ path: "/w/a", shown: () => true })).toEqual({ kind: "gone" });
    const facts = {
      path: "/w/a",
      file_id: ID,
      modified: "x",
      size: 1,
      large: false,
      binary: false,
      utf8: true,
      read_only: false,
    };
    stage({ "/api/file/stat": { status: 200, body: facts } });
    expect(await statFile.dispatch({ path: "/w/a", shown: () => true })).toEqual({
      kind: "stat",
      stat: facts,
    });
  });

  // Only a file over the cap may answer without an identity; any other view is pinned to one.
  it.each([
    ["no identity", {}],
    ["a malformed identity", { file_id: `sha256:${"A".repeat(64)}` }],
  ])("refuses a stat under the cap with %s", async (_name, over) => {
    const { statFile } = await import("./editor.js");
    const body = {
      path: "/w/a.png",
      modified: "x",
      size: 1,
      large: false,
      binary: false,
      utf8: false,
      read_only: false,
      ...over,
    };
    stage({ "/api/file/stat": { status: 200, body } });
    const answer = await statFile.dispatch({ path: "/w/a.png", shown: () => true });
    expect(answer?.kind).toBe("refused");
  });

  it("answers a stat over the cap without an identity", async () => {
    const { statFile } = await import("./editor.js");
    const body = {
      path: "/w/big.log",
      modified: "x",
      size: 3 << 20,
      large: true,
      binary: false,
      utf8: false,
      read_only: false,
    };
    stage({ "/api/file/stat": { status: 200, body } });
    expect(await statFile.dispatch({ path: "/w/big.log", shown: () => true })).toEqual({
      kind: "stat",
      stat: body,
    });
  });
});
