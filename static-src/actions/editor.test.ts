import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

// Every `api-client.ts` name: Browser Mode links ESM for real, and a broken link surfaces as a
// closed browser connection, not a missing export. Listed rather than `vi.importActual`, whose
// graph reaches the also-mocked `transport.js` and dies the same opaque way.
vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiDelete: vi.fn(),
  apiGetTyped: vi.fn(),
  apiPostTyped: vi.fn(),
  apiPutOrError: vi.fn(),
  apiGetOrError: vi.fn(),
}));
// Every `transport.ts` name, listed: an `importOriginal()` that throws on a broken link kills the
// page rather than naming the export. The id minters are real so requests carry real ids.
vi.mock("../transport.js", () => ({
  send: vi.fn(),
  newMessageID: () => "m-test",
  newRequestID: () => "r-test",
  newOpID: () => "op-test",
}));

vi.mock("../editor-types.js", () => ({
  routeForPath: (path: string) => ({ writeURL: `/api/file?path=${encodeURIComponent(path)}` }),
}));

import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "../workspace.js";
import * as api from "../api-client.js";

const mockFetch = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
  resetWorkspace();
});

describe("editor.save_file", () => {
  it("PUTs file content to the write URL", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const { saveFile } = await import("./editor.js");
    const r = await saveFile.dispatch({ path: "src/main.ts", content: "console.log('hi')" });
    expect(r).toEqual({ ok: true });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/file?path=src%2Fmain.ts");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body as string)).toEqual({ content: "console.log('hi')" });
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

// KAS decides per ACTION (a multi-file rename shares one toolCallId), so per-hunk resolution has
// no addressable target.
describe("editor.resolve_partial", () => {
  it("no longer exists", async () => {
    const mod = await import("./editor.js");
    expect(mod).not.toHaveProperty("resolvePendingPartial");
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

// load_diff: the two sides speak different path languages.
//   /api/file      container-ABSOLUTE (granted-roots allow-list); relative is a 403.
//   /api/git/show  workspace- or repo-relative; a leading "/" is a 400.
// They use different helpers: the working copy needs `apiGetOrError` because 404 (deleted) and
// 415 (binary) are answers about a changed file.
describe("editor.load_diff", () => {
  const SHOW = "/api/git/show";
  const FILE = "/api/file?";

  /** Stage both sides: git's answer body (null = unreachable), and the working copy's status and
   *  body, since the status is what the action branches on. */
  function answer(show: unknown, file: { status: number; data?: unknown }): void {
    vi.mocked(api.apiGet).mockImplementation((url: string) => {
      if (!url.startsWith(SHOW)) {
        throw new Error(`unexpected GET ${url}`);
      }
      return Promise.resolve(show);
    });
    vi.mocked(api.apiGetOrError).mockImplementation((url: string) => {
      if (!url.startsWith(FILE)) {
        throw new Error(`unexpected GET ${url}`);
      }
      return Promise.resolve({
        ok: file.status >= 200 && file.status < 300,
        status: file.status,
        data: file.data ?? null,
        error: "",
      });
    });
  }

  const ok = (content: string): { status: number; data: unknown } => ({
    status: 200,
    data: { content },
  });

  /** Every URL either side was asked for. */
  function urls(): string[] {
    return [
      ...vi.mocked(api.apiGet).mock.calls.map((c) => String(c[0])),
      ...vi.mocked(api.apiGetOrError).mock.calls.map((c) => String(c[0])),
    ];
  }

  it("sends the workspace-relative path to git show and the absolute one to the file route", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ content: "base" }, ok("work"));
    const { loadDiff } = await import("./editor.js");
    await loadDiff.dispatch({ path: "/workspace/sub/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(urls()).toContain("/api/git/show?path=sub%2Fa.go&ref=HEAD");
    expect(urls()).toContain("/api/file?path=%2Fworkspace%2Fsub%2Fa.go");
  });

  it("passes the path through untouched when the caller names a repo", async () => {
    // With an explicit repo the caller already holds the repo-relative path.
    expect.assertions(1);
    setWorkspaceRoot("/workspace");
    answer({ content: "base" }, ok("work"));
    const { loadDiff } = await import("./editor.js");
    await loadDiff.dispatch({ path: "static-src/a.ts", repo: "marotte", ref: "HEAD" }).outcome;
    expect(urls()).toContain("/api/git/show?path=static-src%2Fa.ts&ref=HEAD&repo=marotte");
  });

  it("captions both panes for an ordinary change", async () => {
    expect.assertions(4);
    setWorkspaceRoot("/workspace");
    answer({ content: "base" }, ok("work"));
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.baseLabel).toBe("HEAD");
    expect(o.value.workingLabel).toBe("working tree");
    expect(o.value.oldContent).toBe("base");
  });

  it("captions a file outside every repo 'not in git' rather than HEAD", async () => {
    // An empty pane captioned HEAD would claim HEAD holds the file empty (internal/git KindNotInRepo).
    expect.assertions(3);
    setWorkspaceRoot("/workspace");
    answer({ error: "not_in_repo" }, ok("work"));
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.baseLabel).toBe("not in git");
    expect(o.value.newContent).toBe("work");
  });

  it("captions an untracked file 'not in HEAD' rather than HEAD", async () => {
    // A repo owns the directory but the ref holds no revision (untracked or staged-new): the pane
    // must not claim the ref holds the file empty. Distinct from 'not in git' above.
    expect.assertions(4);
    setWorkspaceRoot("/workspace");
    answer({ content: "", absent: true }, ok("brand new\n"));
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.baseLabel).toBe("not in HEAD");
    expect(o.value.oldContent).toBe("");
    // An all-add diff is a correct rendering, not an error state.
    expect(o.value.error).toBe("");
  });

  it("names the ref the file is absent from, not a hardcoded HEAD", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ content: "", absent: true }, ok("brand new\n"));
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "origin/main" })
      .outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.baseLabel).toBe("not in origin/main");
  });

  it("renders a deleted file as an all-deletions diff captioned 'deleted'", async () => {
    // A 404 from the file route is the CHANGE (working copy gone): every base line removed.
    expect.assertions(4);
    setWorkspaceRoot("/workspace");
    answer({ content: "gone\n" }, { status: 404 });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.oldContent).toBe("gone\n");
    expect(o.value.newContent).toBe("");
    expect(o.value.workingLabel).toBe("deleted");
  });

  it("says a binary file has no text diff instead of rendering the blob", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ content: "\u0000\u0001" }, { status: 415 });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/logo.png", repo: "", ref: "HEAD" })
      .outcome;
    expect(o.status).toBe("success");
    if (o.status !== "success") {
      return;
    }
    expect(o.value.error).toContain("binary");
  });

  it("fails on a real git error rather than rendering an all-add diff", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ error: "show_failed", detail: "fatal: bad object" }, ok("work"));
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("error");
    if (o.status !== "error") {
      return;
    }
    expect(o.error.message).toContain("fatal: bad object");
  });

  it("names the side that died, not 'base/new'", async () => {
    // A 500 is a genuine read failure, unlike the 404 and 415 above.
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer({ content: "base" }, { status: 500 });
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("error");
    if (o.status !== "error") {
      return;
    }
    expect(o.error.message).toContain("working copy");
  });

  it("names the base side when git show is unreachable", async () => {
    expect.assertions(2);
    setWorkspaceRoot("/workspace");
    answer(null, ok("work"));
    const { loadDiff } = await import("./editor.js");
    const o = await loadDiff.dispatch({ path: "/workspace/a.go", repo: "", ref: "HEAD" }).outcome;
    expect(o.status).toBe("error");
    if (o.status !== "error") {
      return;
    }
    expect(o.error.message).toContain("HEAD");
  });
});
