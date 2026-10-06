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
import {
  _resetForTest,
  applyInventoryList,
  bindPRPaint,
  getPRGroups,
  setPRForges,
} from "../git-prs-state.js";
import type { PR } from "../wire/types.gen.js";
import * as toast from "../toast.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import {
  mergePR,
  closePR,
  createPR,
  armAutoMerge,
  reopenPR,
  rerunChecks,
  readCapabilities,
  readAffordances,
  readMergeStatus,
  sendCloseOnUnload,
} from "./git-prs.js";

const mockFetch = vi.fn();

/** org/repo's id, the segment every route of this suite addresses. */
const REPO_ID = "v1.6f72672f7265706f";
const REPO_PATH = `/api/forges/gh1/repos/${REPO_ID}`;

function row(n: number): PR {
  return {
    repo_id: REPO_ID,
    repo: "org/repo",
    number: n,
    title: `PR ${String(n)}`,
    state: "open",
    source_branch: `f${String(n)}`,
    target_branch: "main",
    action: {
      mergeable: "yes",
      checks: "unknown",
      checks_passing: 0,
      checks_failing: 0,
      checks_pending: 0,
      checks_neutral: 0,
      checks_unknown: 0,
      checks_total: 0,
      auto_merge_armed: "no",
      queue_state: "none",
      queue_position: -1,
      merge_blocked: "none",
    },
  };
}

/** One connection listing org/repo's #10, #5 and #3. */
function seed(): void {
  _resetForTest();
  setPRForges([
    { id: "gh1", kind: "github", host: "github.com", connected: true, reconnect_required: false },
  ]);
  applyInventoryList({
    entries: [
      {
        forge_id: "gh1",
        state: "ready",
        cycle_id: "1",
        credential: "valid",
        scopes: [{ scope: "owner", owner: "org", rows: [row(10), row(5), row(3)] }],
        clones: [],
        fetched_at: 1,
      },
    ],
    subject: [],
    viewing: false,
  });
}

/** The listed numbers of org/repo. */
function listed(): number[] {
  return getPRGroups()[0]?.prs.map((p) => p.number) ?? [];
}

const paint = vi.fn();

beforeEach(() => {
  resetActionFramework();
  mockFetch.mockReset();
  paint.mockReset();
  vi.stubGlobal("fetch", mockFetch);
  bindPRPaint(paint);
  seed();
});

const prArgs = { forge_id: "gh1", repo_id: REPO_ID, owner: "org", name: "repo", pr_number: 5 };
const HEAD = "aaaa1111bbbb2222cccc3333dddd4444eeee5555";
const mergeArgs = {
  ...prArgs,
  forge_kind: "github" as const,
  head_sha: HEAD,
  strategy: "rebase",
};
const rerunArgs = { ...prArgs, head_sha: "" };

/** The request URL of the Nth fetch the action framework issued. */
function requestURL(call = 0): string {
  return mockFetch.mock.calls[call]![0] as string;
}

/** The JSON body of the Nth fetch the action framework issued. */
function requestBody(call = 0): unknown {
  return JSON.parse(mockFetch.mock.calls[call]![1].body as string);
}

/** A merge answer whose outcome is `state`. */
function outcome(state: string): Response {
  return new Response(
    JSON.stringify({ outcome: { state, queue_state: "none", queue_position: -1 }, cycle_id: "4" }),
    { status: 200 },
  );
}

describe("mergePR leaves the rows to its outcome", () => {
  // An accepted merge is still open, so nothing is removed before the outcome is read.
  it("removes no row before the answer, whatever the answer", async () => {
    for (const res of [
      outcome("merged"),
      outcome("accepted"),
      new Response("{}", { status: 500 }),
    ]) {
      seed();
      mockFetch.mockResolvedValueOnce(res);
      await mergePR.dispatch(mergeArgs);
      expect(listed()).toEqual([10, 5, 3]);
    }
  });

  it("answers the decoded outcome", async () => {
    mockFetch.mockResolvedValue(outcome("accepted"));
    const res = await mergePR.dispatch(mergeArgs);
    expect(res).toEqual({
      outcome: { state: "accepted", queue_state: "none", queue_position: -1 },
      cycle_id: "4",
    });
  });
});

describe("closePR", () => {
  // The tab owns the close's undo window and the row's hide, so the action only asks the forge.
  it("posts to the pull request's close route", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await closePR.dispatch(prArgs);
    expect(requestURL()).toBe(`${REPO_PATH}/prs/5/close`);
    expect(mockFetch.mock.calls[0]![1].method).toBe("POST");
  });

  it("leaves the list to the tab, whatever the answer", async () => {
    for (const res of [
      new Response("{}", { status: 200 }),
      new Response(JSON.stringify({ error: "fail" }), { status: 500 }),
    ]) {
      seed();
      paint.mockReset();
      mockFetch.mockResolvedValueOnce(res);
      await closePR.dispatch(prArgs);
      expect(listed()).toEqual([10, 5, 3]);
      expect(paint).not.toHaveBeenCalled();
    }
  });
});

describe("sendCloseOnUnload", () => {
  // The unload cancels an ordinary request; a keepalive one outlives the page.
  it("posts the close to the pull request's close route with keepalive", () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    sendCloseOnUnload(prArgs);
    expect(requestURL()).toBe(`${REPO_PATH}/prs/5/close`);
    const init = mockFetch.mock.calls[0]![1] as RequestInit;
    expect(init.method).toBe("POST");
    expect(init.keepalive).toBe(true);
    expect(new Headers(init.headers).get("Content-Type")).toBe("application/json");
    expect(requestBody()).toEqual({});
  });

  it("swallows a refused send, which no page is left to show", async () => {
    mockFetch.mockRejectedValue(new TypeError("Failed to fetch"));
    expect(() => {
      sendCloseOnUnload(prArgs);
    }).not.toThrow();
    await Promise.resolve();
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });
});

describe("merge request body", () => {
  // GitHub and the Gitea family name the strategy the repository listed; GitLab takes squash as its
  // intent and refuses any strategy, so its other choice is the project's method without squash.
  const cases = [
    { kind: "github", strategy: "merge", body: { intent: "default", strategy: "merge" } },
    { kind: "github", strategy: "squash", body: { intent: "default", strategy: "squash" } },
    { kind: "github", strategy: "rebase", body: { intent: "default", strategy: "rebase" } },
    {
      kind: "gitea",
      strategy: "rebase-merge",
      body: { intent: "default", strategy: "rebase-merge" },
    },
    { kind: "gitea", strategy: "squash", body: { intent: "default", strategy: "squash" } },
    {
      kind: "codeberg",
      strategy: "fast-forward-only",
      body: { intent: "default", strategy: "fast-forward-only" },
    },
    { kind: "gitlab", strategy: "squash", body: { intent: "squash" } },
    { kind: "gitlab", strategy: "merge", body: { intent: "no_squash" } },
    { kind: "gitlab", strategy: "ff", body: { intent: "no_squash" } },
  ] as const;

  for (const c of cases) {
    it(`sends ${c.strategy} on ${c.kind} as ${JSON.stringify(c.body)} with the pin`, async () => {
      mockFetch.mockResolvedValue(outcome("merged"));
      await mergePR.dispatch({ ...mergeArgs, forge_kind: c.kind, strategy: c.strategy });
      expect(requestURL()).toBe(`${REPO_PATH}/prs/5/merge`);
      expect(mockFetch.mock.calls[0]![1].method).toBe("POST");
      expect(requestBody()).toEqual({ ...c.body, head_sha: HEAD });
    });
  }

  it("sends the empty pin as it is, for the server to refuse", async () => {
    mockFetch.mockResolvedValue(outcome("merged"));
    await mergePR.dispatch({ ...mergeArgs, head_sha: "" });
    expect(requestBody()).toEqual({ intent: "default", strategy: "rebase", head_sha: "" });
  });
});

describe("armAutoMerge", () => {
  it("arms on the merge route with auto and the same body", async () => {
    mockFetch.mockResolvedValue(outcome("accepted"));
    await armAutoMerge.dispatch({ ...mergeArgs, forge_kind: "gitlab", strategy: "squash" });
    expect(requestURL()).toBe(`${REPO_PATH}/prs/5/merge`);
    expect(mockFetch.mock.calls[0]![1].method).toBe("POST");
    expect(requestBody()).toEqual({ intent: "squash", head_sha: HEAD, auto: true });
  });

  // Arming does not merge: an optimistic remove would show the PR gone while the forge holds it.
  it("does not optimistically remove the PR", async () => {
    mockFetch.mockResolvedValue(outcome("accepted"));
    await armAutoMerge.dispatch({ ...mergeArgs, forge_kind: "gitlab" });
    expect(listed()).toEqual([10, 5, 3]);
  });
});

describe("reopenPR", () => {
  it("POSTs the reopen route", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await reopenPR.dispatch(prArgs);
    expect(requestURL()).toBe(`${REPO_PATH}/prs/5/reopen`);
    expect(mockFetch.mock.calls[0]![1].method).toBe("POST");
  });

  it("leaves the groups alone (a reopened PR is not in the open list)", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await reopenPR.dispatch(prArgs);
    expect(listed()).toEqual([10, 5, 3]);
  });
});

describe("rerunChecks", () => {
  // Without the SHA the server resolves the run from the mutable branch and could re-run an older
  // commit's CI, deployment side effects included.
  it("sends the head SHA the row was rendered from", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await rerunChecks.dispatch({ ...prArgs, head_sha: "aaaaaaa1111" });
    expect(requestURL()).toBe(`${REPO_PATH}/prs/5/rerun?head_sha=aaaaaaa1111`);
    expect(mockFetch.mock.calls[0]![1].method).toBe("POST");
  });

  it("omits the pin when the forge reported no head SHA", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await rerunChecks.dispatch(rerunArgs);
    expect(requestURL()).toBe(`${REPO_PATH}/prs/5/rerun`);
  });

  it("never asks for auto-merge on the rerun route", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await rerunChecks.dispatch({ ...prArgs, head_sha: "aaaaaaa1111" });
    expect(requestURL()).not.toContain("auto=");
  });

  it("does not touch the groups", async () => {
    mockFetch.mockResolvedValue(new Response("{}", { status: 200 }));
    await rerunChecks.dispatch(rerunArgs);
    expect(listed()).toEqual([10, 5, 3]);
  });

  // The row renders the refusal (the capability's evidence), so no toast repeats it.
  it("leaves a refusal to the row, quoting the capability's evidence", async () => {
    mockFetch.mockResolvedValue(
      new Response(
        JSON.stringify({
          error:
            "RerunFailedChecks: capability_unsupported: this instance carries no rerun_checks capability",
          code: "capability_unsupported",
          kind: "forbidden",
          capability: {
            name: "rerun_checks",
            support: "no",
            source: "swagger",
            detail: "swagger declares no rerun verb",
          },
        }),
        { status: 501 },
      ),
    );
    const o = await rerunChecks.dispatch(rerunArgs).outcome;
    expect(o.status === "error" && o.error).toMatchObject({
      code: "capability_unsupported",
      message: "swagger declares no rerun verb",
    });
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("keeps the server's words for a refusal with no capability", async () => {
    mockFetch.mockResolvedValue(
      new Response(
        JSON.stringify({ error: "needs the workflow scope", code: "scope_insufficient" }),
        { status: 403 },
      ),
    );
    const o = await rerunChecks.dispatch(rerunArgs).outcome;
    expect(o.status === "error" && o.error).toMatchObject({
      code: "scope_insufficient",
      message: "needs the workflow scope",
    });
  });
});

describe("a refusal naming a moved repository", () => {
  const body = {
    error: "repo_ref_stale: the repository moved",
    code: "repo_ref_stale",
    kind: "not_found",
    successor: { repo_id: "v1.6e65772f7265706f", display_path: "new/repo" },
  };

  // The successor is in the body alone, and the row offers the re-point from it.
  it("keeps the body that says where the repository went, on every row action", async () => {
    for (const run of [
      () => mergePR.dispatch(mergeArgs).outcome,
      () => armAutoMerge.dispatch({ ...mergeArgs, forge_kind: "gitlab" }).outcome,
      () => reopenPR.dispatch(prArgs).outcome,
      () => closePR.dispatch(prArgs).outcome,
      () => rerunChecks.dispatch(rerunArgs).outcome,
    ]) {
      mockFetch.mockResolvedValueOnce(new Response(JSON.stringify(body), { status: 404 }));
      const o = await run();
      expect(o.status === "error" && o.error).toMatchObject({
        code: "repo_ref_stale",
        status: 404,
        message: body.error,
        cause: body,
      });
    }
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("the row actions leave their refusal to the row", () => {
  const refusal = (): Response =>
    new Response(JSON.stringify({ error: "the forge said no", code: "conflict" }), { status: 409 });

  it("toasts none of merge, arm, reopen and close", async () => {
    for (const run of [
      () => mergePR.dispatch(mergeArgs).outcome,
      () => armAutoMerge.dispatch({ ...mergeArgs, forge_kind: "gitlab" }).outcome,
      () => reopenPR.dispatch(prArgs).outcome,
      () => closePR.dispatch(prArgs).outcome,
    ]) {
      mockFetch.mockResolvedValueOnce(refusal());
      const o = await run();
      expect(o.status === "error" && o.error.message).toBe("the forge said no");
    }
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("readCapabilities", () => {
  it("reads the connection's capabilities", async () => {
    const caps = {
      connection: {
        rerun_checks: { support: "unknown", source: "version", detail: "per repository" },
      },
      grant: { read_merge_state: { support: "yes", source: "probe", detail: "" } },
    };
    mockFetch.mockResolvedValue(new Response(JSON.stringify(caps), { status: 200 }));
    expect(await readCapabilities.dispatch({ forge_id: "gh1" })).toEqual(caps);
    expect(requestURL()).toBe("/api/forges/gh1/capabilities");
    expect(mockFetch.mock.calls[0]![1].method).toBe("GET");
  });

  it("answers nothing, and toasts nothing, when the read fails", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "down" }), { status: 503 }));
    expect(await readCapabilities.dispatch({ forge_id: "gh1" })).toBeNull();
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("readAffordances", () => {
  it("reads what the repository allows from its affordances route", async () => {
    const affordances = {
      merge_strategies: ["merge", "squash"],
      has_issues: { support: "yes", source: "response_body", detail: "" },
      can_push: { support: "yes", source: "response_body", detail: "" },
      merge_train: { support: "unknown", source: "default", detail: "" },
      default_branch: "main",
    };
    mockFetch.mockResolvedValue(new Response(JSON.stringify(affordances), { status: 200 }));
    expect(await readAffordances.dispatch({ forge_id: "gh1", repo_id: REPO_ID })).toEqual(
      affordances,
    );
    expect(requestURL()).toBe(`${REPO_PATH}/affordances`);
    expect(mockFetch.mock.calls[0]![1].method).toBe("GET");
  });

  it("answers the refusal, and toasts nothing, when the read fails", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "the forge is down", code: "" }), { status: 503 }),
    );
    const o = await readAffordances.dispatch({ forge_id: "gh1", repo_id: REPO_ID }).outcome;
    expect(o.status === "error" && o.error.message).toBe("the forge is down");
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("readMergeStatus", () => {
  it("reads the pull request's merge state back from the merge route", async () => {
    const status = { merged: "no", queue_state: "queued", web_url: "https://example.test/pr/5" };
    mockFetch.mockResolvedValue(new Response(JSON.stringify(status), { status: 200 }));
    expect(await readMergeStatus.dispatch(prArgs)).toEqual(status);
    expect(requestURL()).toBe(`${REPO_PATH}/prs/5/merge`);
    expect(mockFetch.mock.calls[0]![1].method).toBe("GET");
  });

  it("toasts nothing when the read fails", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "down" }), { status: 502 }));
    expect(await readMergeStatus.dispatch(prArgs)).toBeNull();
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("createPR", () => {
  const createArgs = {
    forge_id: "gh1",
    repo_id: REPO_ID,
    owner: "org",
    name: "repo",
    source_branch: "feature",
    target_branch: "main",
    title: "Add feature",
    body: "Closes #1",
    draft: false,
  };

  it("POSTs to the repo prs endpoint with the PR body and answers the row it opened", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify(row(42)), { status: 200 }));
    const r = await createPR.dispatch(createArgs);
    expect(r?.number).toBe(42);
    expect(r?.repo_id).toBe(REPO_ID);
    const [url, opts] = mockFetch.mock.calls[0]!;
    expect(url).toBe(`${REPO_PATH}/prs`);
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body as string)).toEqual({
      source_branch: "feature",
      target_branch: "main",
      title: "Add feature",
      body: "Closes #1",
      draft: false,
    });
  });

  it("refuses an answer that is not a pull request row rather than reporting it opened", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ number: 42 }), { status: 200 }));
    const o = await createPR.dispatch(createArgs).outcome;
    expect(o.status).toBe("error");
  });

  it("does not toast on failure (error: false — the create dialog shows inline status)", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "branch exists" }), { status: 500 }),
    );
    await createPR.dispatch(createArgs);
    expect(toast.error).not.toHaveBeenCalled();
  });
});
