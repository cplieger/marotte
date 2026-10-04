// Tests for actions/git-changes.ts: stage, discard, unstage, pull, push,
// commit, generateCommitMessage — including the HTTP-200 {error} envelope
// guard (18-F1). The git server reports subprocess failure as HTTP 200 +
// {"error": "<scrubbed git output>"} (internal/git/helpers.go writeCmdResult),
// so a non-empty error body must reject the action (its outcome carries git's
// words, and nothing toasts: the Changes tab says it in place) and an
// {output}/{ok:true} body must resolve.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () =>
  import("../__test-helpers__/toast-mock.js").then((m) => m.toastMock()),
);

import { resetActionFramework, headerValue } from "./__test-helpers__/action-test-setup.js";
import { getActionLog as recentLog } from "./index.js";
import * as toast from "../toast.js";

const mockFetch = vi.fn();

/** Queue a fresh Response per fetch call. A Response body is single-use,
 *  so mockResolvedValue(new Response(...)) would break on any second
 *  read (retries, repeated dispatches). */
function respondWith(body: unknown, status = 200): void {
  mockFetch.mockImplementation(() =>
    Promise.resolve(new Response(JSON.stringify(body), { status })),
  );
}

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
});

describe("git.stage", () => {
  it("POSTs to /api/git/stage with repo and files", async () => {
    respondWith({ ok: true });
    const { stage } = await import("./git-changes.js");
    await stage.dispatch({ repo: "myrepo", files: ["src/a.ts", "src/b.ts"] });
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/stage");
    expect(JSON.parse(opts.body as string)).toEqual({
      repo: "myrepo",
      files: ["src/a.ts", "src/b.ts"],
    });
  });

  it("answers a refusal in the server's words and toasts nothing", async () => {
    respondWith({ error: "no such file" }, 404);
    const { stage } = await import("./git-changes.js");
    const o = await stage.dispatch({ repo: "", files: ["README.md"] }).outcome;
    expect(o.status === "error" ? o.error.message : o.status).toBe("no such file");
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("git.discard", () => {
  it("is not retried (destructive)", async () => {
    respondWith({ error: "timeout" }, 500);
    const { discard } = await import("./git-changes.js");
    await discard.dispatch({ repo: "", files: ["x.ts"] });
    expect(recentLog()[0]?.status).toBe("error");
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("git.pull", () => {
  it("toasts success with repo name", async () => {
    respondWith({ output: "Already up to date." });
    const { pull } = await import("./git-changes.js");
    await pull.dispatch({ repo: "frontend" });
    expect(toast.success).toHaveBeenCalledWith("Pulled frontend");
  });
});

describe("git.push", () => {
  it("toasts success without repo name when empty", async () => {
    respondWith({ output: "" });
    const { push } = await import("./git-changes.js");
    await push.dispatch({ repo: "" });
    expect(toast.success).toHaveBeenCalledWith("Pushed");
  });

  it("is not retried (may have succeeded server-side)", async () => {
    respondWith({ error: "rejected" }, 500);
    const { push } = await import("./git-changes.js");
    const o = await push.dispatch({ repo: "" }).outcome;
    expect(o.status === "error" ? o.error.message : o.status).toBe("rejected");
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("git.commit", () => {
  it("toasts success on 200", async () => {
    respondWith({ output: "[main abc1234] fix: typo" });
    const { commit } = await import("./git-changes.js");
    await commit.dispatch({ repo: "", message: "fix: typo" });
    expect(toast.success).toHaveBeenCalledWith("Committed");
  });

  it("sends the Idempotency-Key header (server-side retry dedup)", async () => {
    respondWith({ output: "" });
    const { commit } = await import("./git-changes.js");
    await commit.dispatch({ repo: "r", message: "fix: x" });
    const [, init] = mockFetch.mock.calls[0]!;
    const key = headerValue(init as RequestInit, "Idempotency-Key");
    expect(key).toBeTypeOf("string");
    expect(key).not.toBe("");
  });
});

describe("git.generateCommitMessage", () => {
  it("POSTs to /api/git/commit-message and lifts the output field", async () => {
    // Real wire shape: {"output": "<message>"} (internal/git/handlers_ai.go).
    // An earlier version of this test encoded apiAction's raw-body
    // passthrough ({message: ...}); the runner now decodes the envelope.
    respondWith({ output: "feat: add X" });
    const { generateCommitMessage } = await import("./git-changes.js");
    const r = await generateCommitMessage.dispatch({ repo: "main" });
    expect(r).toEqual({ output: "feat: add X" });
    const [url] = mockFetch.mock.calls[0]!;
    expect(url).toBe("/api/git/commit-message");
  });

  it("rejects the 200 {error} envelope (no staged changes)", async () => {
    respondWith({ error: "no_staged_changes" });
    const { generateCommitMessage } = await import("./git-changes.js");
    const o = await generateCommitMessage.dispatch({ repo: "main" }).outcome;
    expect(o.status === "error" ? o.error.message : o.status).toBe("no_staged_changes");
    expect(toast.error).not.toHaveBeenCalled();
  });
});

// --- 18-F1: HTTP 200 + {"error": …} envelope guard ---
//
// Before the guard, these bodies resolved as success: the framework's
// success toast fired ("Pulled X") while git had actually failed —
// the "pull does nothing" bug. The guard turns a non-empty error field
// into a thrown ActionError inside run().

describe("error envelope (HTTP 200 + {error})", () => {
  it("pull: {error} body → git's message in the outcome, no toast, no retry", async () => {
    respondWith({ error: "fatal: Not possible to fast-forward, aborting." });
    const { pull } = await import("./git-changes.js");
    const o = await pull.dispatch({ repo: "subflux" }).outcome;
    expect(o.status === "error" ? o.error.message : o.status).toBe(
      "fatal: Not possible to fast-forward, aborting.",
    );
    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
    expect(recentLog()[0]?.status).toBe("error");
    // A "git" envelope error is not transient: pull's retryNetwork
    // classifier must not re-run the command.
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("pull: {output} body → success toast, result carries output", async () => {
    respondWith({ output: "Updating abc..def\nFast-forward" });
    const { pull } = await import("./git-changes.js");
    const r = await pull.dispatch({ repo: "subflux" });
    expect(r).toEqual({ output: "Updating abc..def\nFast-forward" });
    expect(toast.success).toHaveBeenCalledWith("Pulled subflux");
    expect(toast.error).not.toHaveBeenCalled();
    expect(recentLog()[0]?.status).toBe("success");
  });

  it("commit: {error} body → refused with the hook's words, so the caller keeps the draft", async () => {
    respondWith({ error: "pre-commit hook failed: lint" });
    const { commit } = await import("./git-changes.js");
    const o = await commit.dispatch({ repo: "r", message: "feat: y" }).outcome;
    expect(o.status === "error" ? o.error.message : o.status).toBe("pre-commit hook failed: lint");
    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("stage: {ok:true} body (staging success shape) → resolves non-null", async () => {
    respondWith({ ok: true });
    const { stage } = await import("./git-changes.js");
    const r = await stage.dispatch({ repo: "r", files: ["a.ts"] });
    expect(r).toEqual({});
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("empty error string is not a failure", async () => {
    respondWith({ output: "done", error: "" });
    const { stash } = await import("./git-changes.js");
    const r = await stash.dispatch({ repo: "r" });
    expect(r).toEqual({ output: "done" });
    expect(toast.error).not.toHaveBeenCalled();
  });
});
